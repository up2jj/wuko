package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const defaultExecutorWorkspace = "/workspace"

// defaultServiceGracePeriod matches the process package default for a service that does not
// configure shutdown.timeout.
const defaultServiceGracePeriod = 10 * time.Second

type ExecutorConfig struct {
	Image     string           `yaml:"image"`
	Pull      string           `yaml:"pull,omitempty"`
	Platform  string           `yaml:"platform,omitempty"`
	Network   string           `yaml:"network,omitempty"`
	User      string           `yaml:"user,omitempty"`
	Workspace *WorkspaceConfig `yaml:"workspace,omitempty"`
	Mounts    []Mount          `yaml:"mounts,omitempty"`
	Init      *InitConfig      `yaml:"init,omitempty"`
}

type WorkspaceConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Target   string `yaml:"target,omitempty"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

func (workspace *WorkspaceConfig) UnmarshalYAML(unmarshal func(any) error) error {
	type plain WorkspaceConfig
	decoded := plain{Enabled: true, Target: defaultExecutorWorkspace}
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*workspace = WorkspaceConfig(decoded)
	return nil
}

type InitConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args,omitempty"`
}

type ExecutorProvider struct {
	config    ExecutorConfig
	newClient func() (dockerClient, error)
}

func RegisterExecutor(registry *executor.Registry) error {
	return registry.Register("docker", NewExecutor)
}

func NewExecutor(raw map[string]any) (executor.Provider, error) {
	var config ExecutorConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	provider := &ExecutorProvider{
		config: config,
		newClient: func() (dockerClient, error) {
			return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		},
	}
	if err := validateExecutorConfig(config); err != nil {
		return nil, err
	}
	return provider, nil
}

func validateExecutorConfig(config ExecutorConfig) error {
	if strings.TrimSpace(config.Image) == "" {
		return fmt.Errorf("image is required")
	}
	if !templated(config.Pull) {
		switch normalizePullPolicy(config.Pull) {
		case "never", "if-missing", "always":
		default:
			return fmt.Errorf("pull must be never, if-missing, or always")
		}
	}
	if err := validatePlatformValue(config.Platform); err != nil {
		return err
	}
	workspace := executorWorkspace(config)
	if workspace.Enabled {
		if strings.TrimSpace(workspace.Target) == "" {
			return fmt.Errorf("workspace target is required when workspace is enabled")
		}
		if !templated(workspace.Target) && !filepath.IsAbs(workspace.Target) {
			return fmt.Errorf("workspace target must be absolute")
		}
	}
	seenTargets := make(map[string]struct{}, len(config.Mounts)+1)
	if workspace.Enabled && !templated(workspace.Target) {
		seenTargets[filepath.Clean(workspace.Target)] = struct{}{}
	}
	for i, configured := range config.Mounts {
		if configured.Type != "" && !templated(configured.Type) && configured.Type != "bind" && configured.Type != "volume" {
			return fmt.Errorf("mount %d type must be bind or volume", i+1)
		}
		if strings.TrimSpace(configured.Source) == "" {
			return fmt.Errorf("mount %d source is required", i+1)
		}
		if strings.TrimSpace(configured.Target) == "" {
			return fmt.Errorf("mount %d target is required", i+1)
		}
		if templated(configured.Target) {
			continue
		}
		if !filepath.IsAbs(configured.Target) {
			return fmt.Errorf("mount %d target must be absolute", i+1)
		}
		target := filepath.Clean(configured.Target)
		if _, exists := seenTargets[target]; exists {
			return fmt.Errorf("mount target %q is configured more than once", target)
		}
		seenTargets[target] = struct{}{}
	}
	if config.Init != nil && strings.TrimSpace(config.Init.Command) == "" {
		return fmt.Errorf("init command is required")
	}
	return nil
}

func executorWorkspace(config ExecutorConfig) WorkspaceConfig {
	if config.Workspace == nil {
		return WorkspaceConfig{Enabled: true, Target: defaultExecutorWorkspace}
	}
	workspace := *config.Workspace
	if workspace.Enabled && workspace.Target == "" {
		workspace.Target = defaultExecutorWorkspace
	}
	return workspace
}

func executorInit(config ExecutorConfig) InitConfig {
	if config.Init != nil {
		return *config.Init
	}
	return InitConfig{Command: "/bin/sh", Args: []string{"-c", "trap 'exit 0' TERM INT; while :; do sleep 86400; done"}}
}

// serviceShell reports the shell this session can use to supervise a managed service. The Docker
// API can detach from an exec but never signal it, so a service is launched through a shell that
// records its container PID and stopped by a second exec that signals that PID. The session's
// init command is that shell: keeping the container alive already depends on it, so an image
// that works as an executor at all has it. A session configured with a non-shell init keeps the
// older behavior, where only an explicit shutdown command can stop a service.
func serviceShell(config ExecutorConfig) (string, bool) {
	shell := executorInit(config).Command
	switch filepath.Base(shell) {
	case "sh", "bash", "ash", "dash", "ksh", "zsh":
		return shell, true
	}
	return "", false
}

// serviceExec is the state needed to stop one supervised service exec.
type serviceExec struct {
	shell   string
	pidPath string
	user    string
}

func (service serviceExec) launchScript() string {
	// exec replaces the shell, so the recorded PID is the service itself and its argv is
	// unchanged. A read-only /tmp leaves no PID file, which the stop script tolerates.
	return "echo $$ > '" + service.pidPath + `' 2>/dev/null; exec "$@"`
}

func (service serviceExec) stopScript(options process.Options) string {
	signal := int(options.TerminationSignal)
	if signal == 0 {
		signal = int(syscall.SIGTERM)
	}
	grace := options.TerminationGracePeriod
	if grace <= 0 {
		grace = defaultServiceGracePeriod
	}
	seconds := int(grace / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	quoted := "'" + service.pidPath + "'"
	// The exec is usually its own process group leader, so the group is signaled first and the
	// bare PID is the fallback. parent_only asks for the direct process only.
	send := func(number int) string {
		if options.TerminationParentOnly {
			return fmt.Sprintf(`kill -%d "$pid" 2>/dev/null`, number)
		}
		return fmt.Sprintf(`{ kill -%d "-$pid" 2>/dev/null || kill -%d "$pid" 2>/dev/null; }`, number, number)
	}
	return strings.Join([]string{
		// read is a shell builtin, so stopping needs nothing from the image that keeping the
		// container alive does not already need.
		"pid=",
		"[ -r " + quoted + " ] && read pid < " + quoted,
		`[ -n "$pid" ] || exit 0`,
		send(signal) + " || { rm -f " + quoted + "; exit 0; }",
		"n=0",
		fmt.Sprintf(`while [ "$n" -lt %d ] && kill -0 "$pid" 2>/dev/null; do sleep 1; n=$((n+1)); done`, seconds),
		send(int(syscall.SIGKILL)),
		"rm -f " + quoted,
		"exit 0",
	}, "\n")
}

func (provider *ExecutorProvider) Open(ctx context.Context, request executor.Request) (executor.Session, error) {
	if err := validateExecutorConfig(provider.config); err != nil {
		return nil, err
	}
	ownerHost, err := clientHostIdentity()
	if err != nil {
		return nil, err
	}
	dockerClient, err := provider.newClient()
	if err != nil {
		return nil, fmt.Errorf("connecting to Docker: %w", err)
	}
	session := &dockerExecutorSession{config: provider.config, request: request, client: dockerClient, ownerHost: ownerHost}
	if err := recoverOrphans(ctx, dockerClient, ownerHost); err != nil {
		_ = dockerClient.Close()
		return nil, err
	}
	if err := session.startLocked(ctx); err != nil {
		_ = dockerClient.Close()
		return nil, err
	}
	return session, nil
}

type pathMapping struct {
	source string
	target string
}

type dockerExecutorSession struct {
	mu          sync.Mutex
	config      ExecutorConfig
	request     executor.Request
	client      dockerClient
	ownerHost   string
	containerID string
	mappings    []pathMapping
	closed      bool
	services    int
}

func (session *dockerExecutorSession) startLocked(ctx context.Context) error {
	if session.closed {
		return fmt.Errorf("Docker executor session is closed")
	}
	if session.containerID != "" {
		return nil
	}
	platform, err := parsePlatform(session.config.Platform)
	if err != nil {
		return err
	}
	if err := ensureImage(ctx, session.client, session.config.Image, session.config.Pull, platform); err != nil {
		return err
	}
	mounts, mappings, err := session.mounts()
	if err != nil {
		return err
	}
	init := executorInit(session.config)
	workspace := executorWorkspace(session.config)
	workingDir := "/"
	if workspace.Enabled {
		workingDir = workspace.Target
	}
	environment := environmentList(session.request.Env)
	created, err := session.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: session.config.Image, Entrypoint: []string{init.Command}, Cmd: append([]string(nil), init.Args...),
			WorkingDir: workingDir, User: session.config.User, Env: environment,
			Labels: map[string]string{
				managedLabel: "true", ownerHostLabel: session.ownerHost, ownerPIDLabel: strconv.Itoa(os.Getpid()),
				workflowLabel: session.request.WorkflowName, stepLabel: "executor:docker",
			},
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(session.config.Network), Mounts: mounts},
		Platform:   platform,
	})
	if err != nil {
		return fmt.Errorf("creating Docker executor container: %w", err)
	}
	if _, err := session.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanupCtx, cancel := detachedCleanupContext(ctx)
		defer cancel()
		_, removeErr := session.client.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if removeErr != nil {
			removeErr = fmt.Errorf("removing Docker executor container after start failure: %w", removeErr)
		}
		return errors.Join(fmt.Errorf("starting Docker executor container: %w", err), removeErr)
	}
	session.containerID = created.ID
	session.mappings = mappings
	return nil
}

func (session *dockerExecutorSession) mounts() ([]mount.Mount, []pathMapping, error) {
	workspace := executorWorkspace(session.config)
	configured := append([]Mount(nil), session.config.Mounts...)
	if workspace.Enabled {
		configured = append([]Mount{{Type: "bind", Source: session.request.RunDir, Target: workspace.Target, ReadOnly: workspace.ReadOnly}}, configured...)
	}
	mounts := make([]mount.Mount, 0, len(configured))
	mappings := make([]pathMapping, 0, len(configured))
	for _, item := range configured {
		mountType := mount.TypeBind
		source := item.Source
		if item.Type == "volume" {
			mountType = mount.TypeVolume
		} else {
			if !filepath.IsAbs(source) {
				source = filepath.Join(session.request.RunDir, source)
			}
			absolute, err := filepath.Abs(source)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving Docker executor mount %q: %w", item.Source, err)
			}
			source = filepath.Clean(absolute)
			mappings = append(mappings, pathMapping{source: source, target: filepath.Clean(item.Target)})
		}
		mounts = append(mounts, mount.Mount{Type: mountType, Source: source, Target: item.Target, ReadOnly: item.ReadOnly})
	}
	slices.SortFunc(mappings, func(left, right pathMapping) int { return len(right.source) - len(left.source) })
	return mounts, mappings, nil
}

func (session *dockerExecutorSession) Run(ctx context.Context, options process.Options) (process.Result, error) {
	if options.TTY {
		return process.Result{}, fmt.Errorf("tty is not supported by the Docker executor")
	}
	if !options.StdoutPolicy.Valid() || !options.StderrPolicy.Valid() {
		return process.Result{}, fmt.Errorf("invalid output policy")
	}
	session.mu.Lock()
	locked := true
	defer func() {
		if locked {
			session.mu.Unlock()
		}
	}()
	if options.Command == "" {
		return process.Result{}, fmt.Errorf("command is required")
	}
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	if err := session.startLocked(ctx); err != nil {
		return process.Result{}, err
	}
	workingDir, err := session.translatePath(options.Dir)
	if err != nil {
		return process.Result{}, err
	}
	user := options.User
	if user == "" {
		user = session.config.User
	}
	command := append([]string{options.Command}, options.Args...)
	var service *serviceExec
	if options.Started != nil {
		if shell, ok := serviceShell(session.config); ok {
			session.services++
			service = &serviceExec{shell: shell, pidPath: fmt.Sprintf("/tmp/wuko-service-%d.pid", session.services), user: user}
			command = append([]string{shell, "-c", service.launchScript(), "wuko-service"}, command...)
		}
	}
	created, err := session.client.ExecCreate(ctx, session.containerID, client.ExecCreateOptions{
		User: user, AttachStdin: options.Stdin != nil, AttachStdout: true, AttachStderr: true,
		Env: environmentList(options.Env), WorkingDir: workingDir, Cmd: command,
	})
	if err != nil {
		return process.Result{}, fmt.Errorf("creating Docker exec for %s: %w", options.Command, err)
	}
	attached, err := session.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return process.Result{}, fmt.Errorf("attaching Docker exec for %s: %w", options.Command, err)
	}
	defer attached.Close()
	session.mu.Unlock()
	locked = false
	if options.Started != nil {
		options.Started()
	}

	stdout := newExecutorCapture(options.CaptureLimit)
	stderr := newExecutorCapture(options.CaptureLimit)
	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := stdcopy.StdCopy(
			executorOutputWriter(options.StdoutPolicy, options.Stdout, stdout),
			executorOutputWriter(options.StderrPolicy, options.Stderr, stderr),
			attached.Reader,
		)
		copyDone <- copyErr
	}()
	inputDone := make(chan error, 1)
	if options.Stdin != nil {
		go func() {
			_, inputErr := io.Copy(attached.Conn, options.Stdin)
			if closeErr := attached.CloseWrite(); inputErr == nil {
				inputErr = closeErr
			}
			inputDone <- inputErr
		}()
	}

	var copyErr error
	select {
	case copyErr = <-copyDone:
	case <-ctx.Done():
		// Closing the stream only detaches, so the exec must be signaled through the container
		// before the stream goes away. A one-shot exec is instead reaped by removing the
		// container, which a shared service session must keep open for its siblings.
		var stopErr error
		if service != nil {
			stopErr = session.stopService(ctx, *service, options)
		}
		attached.Close()
		var removeErr error
		if options.Started == nil {
			cleanupCtx, cancel := detachedCleanupContext(ctx)
			session.mu.Lock()
			removeErr = session.removeLocked(cleanupCtx)
			session.mu.Unlock()
			cancel()
		}
		// Join both stream pumps before returning: attached.Close above unblocks
		// them, and options.Stdin must not still be read after Run returns. A stdin that
		// outlives the process is the exception, because only the caller can end it.
		<-copyDone
		if options.Stdin != nil && !options.StdinOutlivesProcess {
			<-inputDone
		}
		result := executorResult(stdout, stderr, -1)
		return result, errors.Join(ctx.Err(), removeErr, stopErr)
	}
	var inputErr error
	if options.Stdin != nil && !options.StdinOutlivesProcess {
		inputErr = <-inputDone
	}
	if copyErr != nil && !expectedStreamError(copyErr) {
		return executorResult(stdout, stderr, -1), fmt.Errorf("reading Docker exec output: %w", copyErr)
	}
	if inputErr != nil && !expectedStreamError(inputErr) {
		return executorResult(stdout, stderr, -1), fmt.Errorf("writing Docker exec input: %w", inputErr)
	}
	inspected, err := session.client.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return executorResult(stdout, stderr, -1), fmt.Errorf("inspecting Docker exec: %w", err)
	}
	result := executorResult(stdout, stderr, inspected.ExitCode)
	if inspected.ExitCode != 0 {
		return result, &process.ExitError{Command: options.Command, Code: inspected.ExitCode}
	}
	return result, nil
}

// CancelStopsProcess reports whether this session can stop a canceled service exec. It can when
// the container has the shell that wraps a managed service, because stopping then signals the
// recorded PID from inside the container. A non-shell init leaves the older behavior, where the
// exec keeps running until the session container is removed.
func (session *dockerExecutorSession) CancelStopsProcess() bool {
	_, ok := serviceShell(session.config)
	return ok
}

// stopService signals a supervised service inside the container. The Docker API offers no way to
// signal an exec, so this runs a second exec that reads the PID recorded at launch, sends the
// configured signal, and escalates to SIGKILL once the grace period expires.
func (session *dockerExecutorSession) stopService(ctx context.Context, service serviceExec, options process.Options) error {
	grace := options.TerminationGracePeriod
	if grace <= 0 {
		grace = defaultServiceGracePeriod
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace+cleanupTimeout)
	defer cancel()
	session.mu.Lock()
	containerID, closed := session.containerID, session.closed
	session.mu.Unlock()
	if closed || containerID == "" {
		return nil
	}
	created, err := session.client.ExecCreate(stopCtx, containerID, client.ExecCreateOptions{
		User: service.user, AttachStdout: true, AttachStderr: true,
		Cmd: []string{service.shell, "-c", service.stopScript(options)},
	})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("creating Docker exec to stop %s: %w", options.Command, err)
	}
	attached, err := session.client.ExecAttach(stopCtx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("attaching Docker exec to stop %s: %w", options.Command, err)
	}
	defer attached.Close()
	// Draining the stream is how the API reports that the stop exec finished.
	if _, err := io.Copy(io.Discard, attached.Reader); err != nil && !expectedStreamError(err) {
		return fmt.Errorf("stopping Docker service %s: %w", options.Command, err)
	}
	return nil
}

func (session *dockerExecutorSession) translatePath(value string) (string, error) {
	if value == "" {
		workspace := executorWorkspace(session.config)
		if workspace.Enabled {
			return workspace.Target, nil
		}
		return "/", nil
	}
	cleaned := filepath.Clean(value)
	workspace := executorWorkspace(session.config)
	if !workspace.Enabled && cleaned == filepath.Clean(session.request.RunDir) {
		return "/", nil
	}
	for _, mapping := range session.mappings {
		relative, err := filepath.Rel(mapping.source, cleaned)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if relative == "." {
			return filepath.ToSlash(mapping.target), nil
		}
		return filepath.ToSlash(filepath.Join(mapping.target, relative)), nil
	}
	for _, mapping := range session.mappings {
		relative, err := filepath.Rel(mapping.target, cleaned)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(cleaned), nil
		}
	}
	if pathWithin(session.request.RunDir, cleaned) {
		return "", fmt.Errorf("Docker executor working directory %q is not covered by a bind mount", value)
	}
	if filepath.IsAbs(cleaned) {
		return filepath.ToSlash(cleaned), nil
	}
	return "", fmt.Errorf("Docker executor working directory %q is not covered by a bind mount", value)
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (session *dockerExecutorSession) Close(ctx context.Context) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	removeErr := session.removeLocked(ctx)
	closeErr := session.client.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("closing Docker client: %w", closeErr)
	}
	return errors.Join(removeErr, closeErr)
}

func (session *dockerExecutorSession) removeLocked(ctx context.Context) error {
	if session.containerID == "" {
		return nil
	}
	id := session.containerID
	_, err := session.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("removing Docker executor container %q: %w", id, err)
	}
	session.containerID = ""
	session.mappings = nil
	return nil
}

func environmentList(environment map[string]string) []string {
	keys := slices.Sorted(maps.Keys(environment))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

type executorCapture struct {
	buffer    bytes.Buffer
	limit     int64
	truncated bool
}

func newExecutorCapture(limit int64) *executorCapture { return &executorCapture{limit: limit} }

func executorOutputWriter(policy process.OutputPolicy, stream io.Writer, capture io.Writer) io.Writer {
	switch {
	case policy.Streams() && policy.Captures():
		return io.MultiWriter(writerOrDiscard(stream), capture)
	case policy.Streams():
		return writerOrDiscard(stream)
	case policy.Captures():
		return capture
	default:
		return io.Discard
	}
}

func (capture *executorCapture) Write(data []byte) (int, error) {
	if capture.limit <= 0 {
		return capture.buffer.Write(data)
	}
	remaining := capture.limit - int64(capture.buffer.Len())
	if remaining <= 0 {
		capture.truncated = true
		return len(data), nil
	}
	write := data
	if int64(len(write)) > remaining {
		write = write[:remaining]
		capture.truncated = true
	}
	_, _ = capture.buffer.Write(write)
	return len(data), nil
}

func executorResult(stdout, stderr *executorCapture, exitCode int) process.Result {
	return process.Result{
		Stdout: stdout.buffer.String(), Stderr: stderr.buffer.String(), ExitCode: exitCode,
		StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
	}
}
