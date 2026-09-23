//go:build darwin || linux

// Package elevation runs explicitly trusted local commands through a one-shot sudo helper.
package elevation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/ptyinteract"
)

const (
	helperArgument  = "__wuko_elevated_v1"
	protocolVersion = 1
	socketName      = "control.sock"
	maxRequestSize  = 8 << 20
	defaultPath     = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	// ticketReuse bounds how long a non-interactive validation is trusted without re-running
	// sudo. Every elevated command refreshes the real ticket, so a liveness probe ticking every
	// few seconds no longer forks a redundant sudo -n -v alongside each helper launch.
	ticketReuse = time.Minute
	// promptGrace lets sudo restore the terminal after a cancelled password prompt. Killing it
	// outright leaves the caller's shell with echo disabled.
	promptGrace = 2 * time.Second
)

// Executor implements process.Executor by authenticating with sudo and re-executing Wuko in a
// private helper mode. The zero value is not usable; construct one with NewExecutor.
type Executor struct {
	executable func() (string, error)
	lookPath   func(string) (string, error)
	getenv     func(string) string
	geteuid    func() int
	tempRoot   string
	local      process.Executor
	authMu     sync.Mutex
	authAt     time.Time
}

// NewExecutor returns the default host elevation executor.
func NewExecutor() *Executor {
	return &Executor{
		executable: os.Executable,
		lookPath:   exec.LookPath,
		getenv:     os.Getenv,
		geteuid:    os.Geteuid,
		tempRoot:   "/tmp",
		local:      process.LocalExecutor{},
	}
}

type request struct {
	Version                  int                         `json:"version"`
	Nonce                    string                      `json:"nonce"`
	Command                  string                      `json:"command"`
	Args                     []string                    `json:"args,omitempty"`
	Dir                      string                      `json:"dir"`
	Env                      map[string]string           `json:"env"`
	TTY                      bool                        `json:"tty,omitempty"`
	Interactions             []ptyinteract.Spec          `json:"interactions,omitempty"`
	Interact                 bool                        `json:"interact,omitempty"`
	Terminal                 *process.TerminalAppearance `json:"terminal,omitempty"`
	CaptureLimit             int64                       `json:"capture_limit,omitempty"`
	StdoutPolicy             process.OutputPolicy        `json:"stdout_policy"`
	StderrPolicy             process.OutputPolicy        `json:"stderr_policy"`
	StdinOutlivesProcess     bool                        `json:"stdin_outlives_process,omitempty"`
	TerminationSignal        syscall.Signal              `json:"termination_signal,omitempty"`
	TerminationParentOnly    bool                        `json:"termination_parent_only,omitempty"`
	TerminationGracePeriodNS int64                       `json:"termination_grace_period_ns,omitempty"`
}

type message struct {
	Version int            `json:"version"`
	Nonce   string         `json:"nonce"`
	Type    string         `json:"type"`
	Result  process.Result `json:"result,omitempty"`
	Error   string         `json:"error,omitempty"`
	Kind    string         `json:"kind,omitempty"`
	Code    int            `json:"code,omitempty"`
}

type wrapperOutcome struct {
	err error
}

// Run executes one command as root. It deliberately returns target exit errors as
// *process.ExitError while keeping sudo and protocol failures as infrastructure errors.
func (executor *Executor) Run(ctx context.Context, options process.Options) (process.Result, error) {
	if options.User != "" {
		return process.Result{}, fmt.Errorf("elevated execution cannot select user %q", options.User)
	}
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	environment := targetEnvironment(options.Env, executor.getenv)
	command, dir, err := resolveExecutable(options.Command, options.Dir, environment["PATH"])
	if err != nil {
		return process.Result{}, err
	}
	options.Command, options.Dir, options.Env = command, dir, environment
	if executor.geteuid() == 0 {
		return executor.local.Run(ctx, options)
	}
	if err := executor.authorize(ctx, options); err != nil {
		return process.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}

	executable, err := executor.executable()
	if err != nil {
		return process.Result{}, fmt.Errorf("locating the Wuko executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return process.Result{}, fmt.Errorf("resolving the Wuko executable: %w", err)
	}
	sudo, err := executor.lookPath("sudo")
	if err != nil {
		return process.Result{}, fmt.Errorf("sudo is required for elevated execution: %w", err)
	}

	dirPath, listener, nonce, err := prepareTransport(executor.tempRoot)
	if err != nil {
		return process.Result{}, err
	}
	defer os.RemoveAll(dirPath)
	defer listener.Close()

	events := make(chan message)
	protocolDone := make(chan error, 1)
	// The request travels over the authenticated socket rather than a file in the transport
	// directory: it carries PTY interaction specs, including ones marked sensitive, and a crash
	// between writing and cleanup would otherwise leave those secrets on disk.
	go serveHelper(listener, buildRequest(nonce, options), nonce, events, protocolDone)

	outerGrace := options.TerminationGracePeriod
	if outerGrace == 0 {
		outerGrace = 2 * time.Second
	}
	wrapperDone := make(chan wrapperOutcome, 1)
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go func() {
		runErr := runSudoWrapper(runCtx, sudo, executable, dirPath, options, environmentList(sudoEnvironment(executor.getenv)), outerGrace+2*time.Second)
		wrapperDone <- wrapperOutcome{err: runErr}
	}()

	var final *message
	started := false
	var outcome wrapperOutcome
	var protocolErr error
	for wrapperDone != nil || events != nil || protocolDone != nil {
		select {
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			switch event.Type {
			case "started":
				if !started {
					started = true
					if options.Started != nil {
						options.Started()
					}
				}
			case "final":
				copy := event
				final = &copy
			}
		case outcome = <-wrapperDone:
			wrapperDone = nil
			_ = listener.Close()
		case protocolErr = <-protocolDone:
			protocolDone = nil
			if protocolErr != nil {
				cancelRun()
			}
		}
	}
	if final == nil {
		if err := ctx.Err(); err != nil {
			return process.Result{}, err
		}
		// Closing the listener after sudo exits is what usually breaks the protocol, so the
		// wrapper failure is reported first and stays the actionable diagnosis.
		if outcome.err != nil {
			return process.Result{}, fmt.Errorf("sudo failed before the elevated command completed: %v", outcome.err)
		}
		if protocolErr != nil {
			return process.Result{}, fmt.Errorf("elevated helper protocol failed: %v", protocolErr)
		}
		return process.Result{}, fmt.Errorf("elevated helper exited without reporting a result")
	}
	// A reported result carries the captured output even on cancellation, matching LocalExecutor.
	if err := ctx.Err(); err != nil {
		return final.Result, err
	}
	switch final.Kind {
	case "":
		return final.Result, nil
	case "exit":
		return final.Result, &process.ExitError{Command: options.Command, Code: final.Code, Err: errors.New(final.Error)}
	default:
		return final.Result, errors.New(final.Error)
	}
}

// runSudoWrapper intentionally inherits Wuko's process group and controlling terminal. Sudo's
// default per-terminal credential cache therefore matches authorize, and the root helper can hand
// the real terminal to a target PTY without becoming a background terminal reader.
func runSudoWrapper(ctx context.Context, sudo, executable, transportDir string, options process.Options, environment []string, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.StdinOutlivesProcess {
		if _, ok := options.Stdin.(*os.File); !ok {
			return fmt.Errorf("stdin that outlives the elevated process must be an *os.File")
		}
	}
	command := exec.Command(sudo, "-n", "--", executable, helperArgument, transportDir)
	command.Dir = "/"
	command.Env = environment
	command.Stdin = options.Stdin
	command.Stdout = writerOrDiscard(options.Stdout)
	command.Stderr = writerOrDiscard(options.Stderr)
	command.WaitDelay = grace + time.Second
	if err := command.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
	}
	termErr := command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-wait:
		return errors.Join(ctx.Err(), termErr)
	case <-timer.C:
		killErr := command.Process.Kill()
		<-wait
		return errors.Join(ctx.Err(), termErr, killErr)
	}
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func (executor *Executor) authorize(ctx context.Context, options process.Options) error {
	sudo, err := executor.lookPath("sudo")
	if err != nil {
		return fmt.Errorf("sudo is required for elevated execution: %w", err)
	}
	if executor.ticketFresh() {
		return nil
	}
	if executor.validate(ctx, sudo) == nil {
		return nil
	}
	if !options.Interactive {
		return fmt.Errorf("sudo credentials are unavailable in non-interactive mode; authenticate first or configure an appropriate NOPASSWD rule")
	}
	executor.authMu.Lock()
	defer executor.authMu.Unlock()
	if executor.validate(ctx, sudo) == nil {
		return nil
	}
	writer := options.PromptStderr
	if writer == nil {
		writer = options.Stderr
	}
	if writer == nil {
		writer = io.Discard
	}
	_, _ = fmt.Fprintln(writer, "wuko: administrator privileges are required")
	if err := runSudoPrompt(ctx, sudo, options.PromptStdin, writer, environmentList(sudoEnvironment(executor.getenv))); err != nil {
		return fmt.Errorf("authenticating with sudo: %w", err)
	}
	return nil
}

// ticketFresh reports whether a non-interactive validation succeeded recently enough that the
// real sudo invocation can be trusted to refresh the ticket by itself. Only non-interactive
// success records a timestamp, so a host configured with timestamp_timeout=0 never takes this
// path and still prompts for every command.
func (executor *Executor) ticketFresh() bool {
	executor.authMu.Lock()
	defer executor.authMu.Unlock()
	return !executor.authAt.IsZero() && time.Since(executor.authAt) < ticketReuse
}

func (executor *Executor) validate(ctx context.Context, sudo string) error {
	err := runSudoValidation(ctx, sudo, nil, io.Discard, environmentList(sudoEnvironment(executor.getenv)))
	if err != nil {
		return err
	}
	executor.authMu.Lock()
	executor.authAt = time.Now()
	executor.authMu.Unlock()
	return nil
}

func runSudoValidation(ctx context.Context, sudo string, stdin io.Reader, stderr io.Writer, environment []string) error {
	command := exec.CommandContext(ctx, sudo, "-n", "-v")
	command.Env = environment
	command.Stdin = stdin
	command.Stdout = io.Discard
	command.Stderr = stderr
	return command.Run()
}

// runSudoPrompt runs an interactive validation without exec.CommandContext's SIGKILL. Sudo turns
// off terminal echo to read a password and only restores it while handling a catchable signal, so
// a probe deadline or Ctrl-C must terminate it politely or the caller's shell is left unusable.
func runSudoPrompt(ctx context.Context, sudo string, stdin io.Reader, stderr io.Writer, environment []string) error {
	command := exec.Command(sudo, "-v")
	command.Env = environment
	command.Stdin = stdin
	command.Stdout = io.Discard
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
	}
	termErr := command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(promptGrace)
	defer timer.Stop()
	select {
	case <-wait:
		return errors.Join(ctx.Err(), termErr)
	case <-timer.C:
		killErr := command.Process.Kill()
		<-wait
		return errors.Join(ctx.Err(), termErr, killErr)
	}
}

func buildRequest(nonce string, options process.Options) request {
	return request{
		Version: protocolVersion, Nonce: nonce, Command: options.Command, Args: append([]string(nil), options.Args...), Dir: options.Dir,
		Env: options.Env, TTY: options.TTY, Interactions: options.Interactions.Specs(), Interact: options.Interact, Terminal: options.Terminal,
		CaptureLimit: options.CaptureLimit, StdoutPolicy: options.StdoutPolicy, StderrPolicy: options.StderrPolicy,
		StdinOutlivesProcess: options.StdinOutlivesProcess, TerminationSignal: options.TerminationSignal,
		TerminationParentOnly: options.TerminationParentOnly, TerminationGracePeriodNS: int64(options.TerminationGracePeriod),
	}
}

func prepareTransport(root string) (string, net.Listener, string, error) {
	dir, err := os.MkdirTemp(root, "wuko-elevated-")
	if err != nil {
		return "", nil, "", fmt.Errorf("creating elevation transport: %w", err)
	}
	fail := func(err error) (string, net.Listener, string, error) {
		_ = os.RemoveAll(dir)
		return "", nil, "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fail(fmt.Errorf("securing elevation transport: %w", err))
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fail(fmt.Errorf("creating elevation nonce: %w", err))
	}
	nonce := hex.EncodeToString(nonceBytes)
	listener, err := net.Listen("unix", filepath.Join(dir, socketName))
	if err != nil {
		return fail(fmt.Errorf("opening elevation control socket: %w", err))
	}
	if err := os.Chmod(filepath.Join(dir, socketName), 0o600); err != nil {
		listener.Close()
		return fail(fmt.Errorf("securing elevation control socket: %w", err))
	}
	return dir, listener, nonce, nil
}

func serveHelper(listener net.Listener, payload request, nonce string, events chan<- message, result chan<- error) {
	defer close(events)
	connection, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer connection.Close()
	if err := json.NewEncoder(connection).Encode(payload); err != nil {
		result <- fmt.Errorf("sending elevation request: %w", err)
		return
	}
	decoder := json.NewDecoder(connection)
	for {
		var event message
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				result <- nil
			} else {
				result <- err
			}
			return
		}
		if event.Version != protocolVersion || event.Nonce != nonce {
			result <- fmt.Errorf("invalid elevation helper response")
			return
		}
		events <- event
	}
}

// rootHome resolves root's home directory once per process. The lookup can reach NSS or LDAP, and
// an elevated service with a liveness probe would otherwise repeat it on every tick.
var rootHome = sync.OnceValue(func() string {
	root, err := user.LookupId("0")
	if err == nil && root.HomeDir != "" {
		return root.HomeDir
	}
	return "/root"
})

func targetEnvironment(explicit map[string]string, getenv func(string) string) map[string]string {
	result := map[string]string{"HOME": rootHome(), "USER": "root", "LOGNAME": "root", "SHELL": "/bin/sh", "PATH": defaultPath}
	for _, key := range []string{"TERM", "COLORTERM", "NO_COLOR", "LANG", "LC_ALL", "LC_CTYPE"} {
		if value := getenv(key); value != "" {
			result[key] = value
		}
	}
	for key, value := range explicit {
		result[key] = value
	}
	return result
}

func sudoEnvironment(getenv func(string) string) map[string]string {
	values := map[string]string{"PATH": defaultPath}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "SHELL", "TERM", "COLORTERM", "NO_COLOR", "LANG", "LC_ALL", "LC_CTYPE"} {
		if value := getenv(key); value != "" {
			values[key] = value
		}
	}
	return values
}

func environmentList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	// Stable ordering keeps tests and process diagnostics deterministic.
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func resolveExecutable(command, dir, pathValue string) (string, string, error) {
	if command == "" {
		return "", "", fmt.Errorf("command is required")
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("resolving working directory: %w", err)
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("resolving working directory: %w", err)
	}
	candidates := []string{command}
	if !strings.ContainsRune(command, filepath.Separator) {
		candidates = candidates[:0]
		for _, entry := range filepath.SplitList(pathValue) {
			// A plain exec treats an empty or relative PATH element as the working directory,
			// but promoting a run-directory binary into root execution is not worth the
			// POSIX fidelity: an elevated lookup only trusts absolute directories.
			if !filepath.IsAbs(entry) {
				continue
			}
			candidates = append(candidates, filepath.Join(entry, command))
		}
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}
		candidate = filepath.Clean(candidate)
		info, statErr := os.Stat(candidate)
		if statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, dir, nil
	}
	return "", "", fmt.Errorf("resolving elevated executable %q using PATH %q", command, pathValue)
}

// HandleHelper recognizes and runs the private root helper mode. It returns false for ordinary
// invocations so main can continue into Cobra.
func HandleHelper(args []string) (int, bool) {
	if len(args) != 3 || args[1] != helperArgument {
		return 0, false
	}
	return runHelper(args[2]), true
}

func runHelper(dir string) int {
	if os.Geteuid() != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper must run as root through sudo")
		return 125
	}
	uid, uidErr := strconv.ParseUint(os.Getenv("SUDO_UID"), 10, 32)
	gid, gidErr := strconv.ParseUint(os.Getenv("SUDO_GID"), 10, 32)
	if uidErr != nil || gidErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper requires SUDO_UID and SUDO_GID")
		return 125
	}
	if err := validateTransportDir(dir, uint32(uid), uint32(gid)); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper:", err)
		return 125
	}
	socketPath := filepath.Join(dir, socketName)
	if err := validateSocket(socketPath, uint32(uid), uint32(gid)); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper:", err)
		return 125
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper: connecting control socket:", err)
		return 125
	}
	defer connection.Close()
	request, err := readRequest(connection)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper:", err)
		return 125
	}
	encoder := json.NewEncoder(connection)
	interactions, err := ptyinteract.Compile(request.Interactions)
	if err != nil {
		_ = encoder.Encode(message{Version: protocolVersion, Nonce: request.Nonce, Type: "final", Kind: "target", Error: err.Error()})
		return 0
	}
	if len(request.Interactions) == 0 {
		interactions = nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	options := process.Options{
		Command: request.Command, Args: request.Args, Dir: request.Dir, Env: request.Env,
		Stdin: os.Stdin, StdinOutlivesProcess: request.StdinOutlivesProcess, Stdout: os.Stdout, Stderr: os.Stderr,
		TTY: request.TTY, Interactions: interactions, Interact: request.Interact, Terminal: request.Terminal,
		CaptureLimit: request.CaptureLimit, StdoutPolicy: request.StdoutPolicy, StderrPolicy: request.StderrPolicy,
		TerminationSignal: request.TerminationSignal, TerminationParentOnly: request.TerminationParentOnly,
		TerminationGracePeriod: time.Duration(request.TerminationGracePeriodNS),
		Started: func() {
			if err := encoder.Encode(message{Version: protocolVersion, Nonce: request.Nonce, Type: "started"}); err != nil {
				cancel()
			}
		},
	}
	result, runErr := (process.LocalExecutor{}).Run(runCtx, options)
	final := message{Version: protocolVersion, Nonce: request.Nonce, Type: "final", Result: result}
	if runErr != nil {
		var exitErr *process.ExitError
		if errors.As(runErr, &exitErr) {
			final.Kind, final.Code, final.Error = "exit", exitErr.Code, runErr.Error()
		} else {
			final.Kind, final.Error = "target", runErr.Error()
		}
	}
	if err := encoder.Encode(final); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "wuko: elevated helper: reporting result:", err)
		return 125
	}
	return 0
}

func validateTransportDir(dir string, uid, gid uint32) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("transport directory must be absolute")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("checking transport directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("transport directory must be a mode 0700 directory")
	}
	if err := requireOwner(info, uid, gid); err != nil {
		return fmt.Errorf("transport directory: %w", err)
	}
	return nil
}

// readRequest consumes the single request object the parent writes once the helper connects. The
// socket has already been checked for ownership and mode, so the connection is the authenticated
// channel and the payload never touches the filesystem.
func readRequest(connection io.Reader) (request, error) {
	var payload request
	decoder := json.NewDecoder(io.LimitReader(connection, maxRequestSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("decoding request: %w", err)
	}
	if payload.Version != protocolVersion || payload.Nonce == "" || payload.Command == "" || !filepath.IsAbs(payload.Command) || !filepath.IsAbs(payload.Dir) {
		return payload, fmt.Errorf("invalid request fields")
	}
	if !payload.StdoutPolicy.Valid() || !payload.StderrPolicy.Valid() {
		return payload, fmt.Errorf("invalid output policy")
	}
	return payload, nil
}

func validateSocket(path string, uid, gid uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("checking control socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("control socket must be a mode 0600 Unix socket")
	}
	if err := requireOwner(info, uid, gid); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	return nil
}

func requireOwner(info os.FileInfo, uid, gid uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Gid != gid {
		return fmt.Errorf("owner must match the invoking sudo user")
	}
	return nil
}
