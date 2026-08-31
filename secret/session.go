package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// minRedactableLength is the shortest resolved value the session substitutes in diagnostic text.
// Redaction is a blind textual replacement, so a short value such as a port, a boolean or a small
// number would rewrite unrelated output and make failures unreadable. Values below this length are
// kept out of the redaction set.
const minRedactableLength = 6

// Runner executes provider commands. It is exported to make provider behavior testable without
// installing password-manager CLIs.
type Runner interface {
	Run(context.Context, Command) (string, error)
}

// Command describes one provider CLI invocation.
type Command struct {
	Name        string
	Args        []string
	Env         map[string]string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Capture     bool
	Interactive bool
}

// Options configure one workflow-occurrence session.
type Options struct {
	BaseEnv     map[string]string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Interactive bool
	Runner      Runner
}

type cacheEntry struct {
	done  chan struct{}
	value string
	err   error
}

// Session owns provider authentication state and a successful-result cache for one workflow
// occurrence. It is safe for concurrent workflow branches.
type Session struct {
	ctx         context.Context
	runner      Runner
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	interactive bool

	mu       sync.Mutex
	env      map[string]string
	cache    map[string]*cacheEntry
	resolved []string
}

// NewSession constructs a provider session without performing authentication.
//
// The session detaches from the caller's cancellation and deadline. A session is created once per
// workflow occurrence and outlives the run context: the engine deliberately runs cleanup and
// finally steps with a cancelled parent context (context.WithoutCancel), and a secret lookup made
// from there must still reach the provider CLI.
func NewSession(ctx context.Context, options Options) *Session {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)
	runner := options.Runner
	if runner == nil {
		runner = execRunner{}
	}
	environment := options.BaseEnv
	if environment == nil {
		environment = processEnvironment()
	} else {
		environment = maps.Clone(environment)
	}
	return &Session{
		ctx: ctx, runner: runner, stdin: options.Stdin, stdout: options.Stdout, stderr: options.Stderr,
		interactive: options.Interactive, env: environment, cache: make(map[string]*cacheEntry),
	}
}

// EnsureAuth checks configured providers in declaration order, optionally authenticates, and
// verifies each provider once more after a login attempt.
func (session *Session) EnsureAuth(config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	for _, auth := range config.EnsureAuth {
		if err := session.ensureProvider(auth); err != nil {
			return fmt.Errorf("authenticating secret provider %s: %w", auth.Provider, err)
		}
	}
	return nil
}

func (session *Session) ensureProvider(auth AuthConfig) error {
	authenticated, state, err := session.check(auth.Provider)
	if err != nil {
		return err
	}
	if authenticated {
		return nil
	}
	attempted := false
	if auth.Login.Native && session.interactive {
		attempted = true
		if err := session.nativeLogin(auth.Provider, state); err == nil {
			authenticated, _, err = session.check(auth.Provider)
			if err != nil {
				return err
			}
			if authenticated {
				return nil
			}
		} else if auth.Login.Fallback == nil {
			return err
		}
	}
	if auth.Login.Fallback != nil {
		attempted = true
		if err := session.fallback(*auth.Login.Fallback); err != nil {
			return err
		}
		authenticated, _, err = session.check(auth.Provider)
		if err != nil {
			return err
		}
		if authenticated {
			return nil
		}
	}
	if !attempted && auth.Login.Native && !session.interactive {
		return fmt.Errorf("not authenticated; native login requires an interactive terminal and no fallback is configured")
	}
	if attempted {
		return fmt.Errorf("authentication check still fails after login")
	}
	return fmt.Errorf("not authenticated and no login method is configured")
}

func (session *Session) check(provider string) (bool, string, error) {
	switch provider {
	case "op":
		_, err := session.run(Command{Name: "op", Args: []string{"whoami"}, Capture: true})
		if err == nil {
			return true, "", nil
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return false, "", fmt.Errorf("checking status: %w", err)
		}
		return false, "unauthenticated", nil
	case "bw":
		output, err := session.run(Command{Name: "bw", Args: []string{"status"}, Capture: true})
		if err != nil {
			var executableError *exec.Error
			if errors.As(err, &executableError) {
				return false, "", fmt.Errorf("checking status: %w", err)
			}
			// A failing status command means bw could not report a state, not that the
			// configured login is unusable. Report it as unauthenticated so the fallback
			// still gets its attempt, exactly as the op branch does.
			return false, "unauthenticated", nil
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(output), &status); err != nil {
			return false, "", fmt.Errorf("decoding bw status: %w", err)
		}
		return status.Status == "unlocked", status.Status, nil
	default:
		return false, "", fmt.Errorf("unsupported provider %q", provider)
	}
}

func (session *Session) nativeLogin(provider, state string) error {
	command := Command{Capture: true, Interactive: true, Stdin: session.stdin, Stderr: session.stderr}
	switch provider {
	case "op":
		command.Name, command.Args = "op", []string{"signin", "--raw"}
	case "bw":
		command.Name = "bw"
		if state == "locked" {
			command.Args = []string{"unlock", "--raw"}
		} else {
			command.Args = []string{"login", "--raw"}
		}
	}
	output, err := session.run(command)
	if err != nil {
		return fmt.Errorf("native login: %w", err)
	}
	value := strings.TrimSpace(output)
	switch provider {
	case "op":
		// The desktop app integration signs in without handing out a token; the account is
		// unlocked and the caller's re-check confirms it.
		if value == "" {
			return nil
		}
		// op reads OP_SESSION_<account>, never a bare OP_SESSION.
		names := session.opSessionNames()
		if len(names) == 0 {
			return fmt.Errorf("native login returned a session token but the OP_SESSION_<account> variable could not be determined; configure login.fallback with session_env")
		}
		session.storeSession(names, value)
	case "bw":
		if value == "" {
			return fmt.Errorf("login returned an empty session value for BW_SESSION")
		}
		session.storeSession([]string{"BW_SESSION"}, value)
	}
	return nil
}

// opSessionNames reports the environment variables op accepts for the signed-in account. The
// account is only unambiguous when exactly one is configured; with several, op requires an
// explicit --account and a token cannot be attributed to one of them.
func (session *Session) opSessionNames() []string {
	output, err := session.run(Command{Name: "op", Args: []string{"account", "list", "--format=json"}, Capture: true})
	if err != nil {
		return nil
	}
	var accounts []struct {
		URL         string `json:"url"`
		Shorthand   string `json:"shorthand"`
		UserUUID    string `json:"user_uuid"`
		AccountUUID string `json:"account_uuid"`
	}
	if err := json.Unmarshal([]byte(output), &accounts); err != nil || len(accounts) != 1 {
		return nil
	}
	account := accounts[0]
	var names []string
	for _, candidate := range []string{account.Shorthand, signInSubdomain(account.URL), account.UserUUID, account.AccountUUID} {
		name := "OP_SESSION_" + candidate
		if candidate != "" && environmentNamePattern.MatchString(name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// signInSubdomain extracts the account shorthand op derives from a sign-in address, so
// my.1password.com becomes my.
func signInSubdomain(address string) string {
	host := address
	if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	host, _, _ = strings.Cut(host, ":")
	shorthand, _, found := strings.Cut(host, ".")
	if !found {
		return ""
	}
	return shorthand
}

func (session *Session) fallback(config FallbackConfig) error {
	command := Command{Args: config.Args, Stdin: session.stdin, Stderr: session.stderr, Interactive: session.interactive}
	if config.Command != "" {
		command.Name = config.Command
	} else {
		command.Name = config.Shell
		if command.Name == "" {
			command.Name = "/bin/sh"
		}
		// sh -c binds the first operand to $0, so pass the shell name as a placeholder and
		// keep the configured arguments at $1 upwards.
		command.Args = append([]string{"-c", config.Script, command.Name}, config.Args...)
	}
	command.Capture = config.SessionEnv != ""
	if !command.Capture {
		command.Stdout = session.stdout
	}
	output, err := session.run(command)
	if err != nil {
		return fmt.Errorf("fallback login: %w", err)
	}
	if config.SessionEnv != "" {
		return session.captureSession(config.SessionEnv, output)
	}
	return nil
}

func (session *Session) captureSession(name, output string) error {
	value := strings.TrimSpace(output)
	if value == "" {
		return fmt.Errorf("login returned an empty session value for %s", name)
	}
	session.storeSession([]string{name}, value)
	return nil
}

// storeSession keeps one login token in the provider's private environment under every name the
// provider CLI may read it from.
func (session *Session) storeSession(names []string, value string) {
	session.mu.Lock()
	for _, name := range names {
		session.env[name] = value
	}
	session.recordLocked(value)
	session.mu.Unlock()
}

// Resolve returns one secret value, coalescing concurrent identical lookups and caching only
// successful results.
func (session *Session) Resolve(reference string) (string, error) {
	if session == nil {
		return "", errors.New("secret resolver is unavailable")
	}
	session.mu.Lock()
	if existing := session.cache[reference]; existing != nil {
		session.mu.Unlock()
		<-existing.done
		return existing.value, existing.err
	}
	entry := &cacheEntry{done: make(chan struct{})}
	session.cache[reference] = entry
	session.mu.Unlock()

	entry.value, entry.err = session.resolve(reference)
	session.mu.Lock()
	if entry.err != nil {
		delete(session.cache, reference)
	} else {
		session.recordLocked(entry.value)
	}
	close(entry.done)
	session.mu.Unlock()
	return entry.value, entry.err
}

func (session *Session) resolve(reference string) (string, error) {
	parsed, err := url.Parse(reference)
	if err != nil {
		return "", fmt.Errorf("parsing secret reference: %w", err)
	}
	var command Command
	switch parsed.Scheme {
	case "op":
		if parsed.Host == "" || parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return "", fmt.Errorf("invalid 1Password reference %q", reference)
		}
		command = Command{Name: "op", Args: []string{"read", "--no-newline", reference}, Capture: true}
	case "bw":
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("invalid Bitwarden reference %q", reference)
		}
		selector := parsed.Host
		allowed := map[string]bool{"password": true, "username": true, "uri": true, "totp": true, "notes": true}
		if !allowed[selector] {
			return "", fmt.Errorf("unsupported Bitwarden selector %q", selector)
		}
		item, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
		if err != nil || item == "" {
			return "", fmt.Errorf("invalid Bitwarden item in %q", reference)
		}
		command = Command{Name: "bw", Args: []string{"get", selector, item}, Capture: true}
	default:
		return "", fmt.Errorf("unsupported secret provider scheme %q", parsed.Scheme)
	}
	output, err := session.run(command)
	if err != nil {
		return "", fmt.Errorf("resolving %s secret: %w", parsed.Scheme, err)
	}
	return strings.TrimSuffix(output, "\n"), nil
}

func (session *Session) run(command Command) (string, error) {
	session.mu.Lock()
	command.Env = maps.Clone(session.env)
	session.mu.Unlock()
	return session.runner.Run(session.ctx, command)
}

// recordLocked adds one learned value to the redaction set. The caller holds the session mutex.
func (session *Session) recordLocked(value string) {
	if len(value) < minRedactableLength {
		return
	}
	session.resolved = append(session.resolved, value)
}

// Redact replaces values learned by the session in diagnostic text.
func (session *Session) Redact(value string) string {
	if session == nil {
		return value
	}
	session.mu.Lock()
	values := append([]string(nil), session.resolved...)
	session.mu.Unlock()
	for _, secret := range values {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "<redacted>")
			if encoded, err := json.Marshal(secret); err == nil && len(encoded) >= 2 {
				value = strings.ReplaceAll(value, string(encoded[1:len(encoded)-1]), "<redacted>")
			}
		}
	}
	return value
}

// RedactError hides learned values while preserving error unwrapping.
func (session *Session) RedactError(err error) error {
	if session == nil || err == nil {
		return err
	}
	redacted := session.Redact(err.Error())
	if redacted == err.Error() {
		return err
	}
	return redactedError{text: redacted, err: err}
}

type redactedError struct {
	text string
	err  error
}

func (err redactedError) Error() string { return err.text }
func (err redactedError) Unwrap() error { return err.err }

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command Command) (string, error) {
	if command.Name == "" {
		return "", errors.New("command name is required")
	}
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Env = environmentList(command.Env)
	cmd.Stdin = command.Stdin
	cmd.Stderr = command.Stderr
	var stdout bytes.Buffer
	if command.Capture {
		cmd.Stdout = &stdout
	} else {
		cmd.Stdout = command.Stdout
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running %s: %w", command.Name, err)
	}
	return stdout.String(), nil
}

func processEnvironment() map[string]string {
	result := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, found := strings.Cut(pair, "=")
		if found {
			result[name] = value
		}
	}
	return result
}

func environmentList(environment map[string]string) []string {
	result := make([]string, 0, len(environment))
	for name, value := range environment {
		result = append(result, name+"="+value)
	}
	return result
}
