package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/validation"
)

type fakeRunner struct {
	mu       sync.Mutex
	commands []Command
	run      func(Command) (string, error)
}

func (runner *fakeRunner) Run(_ context.Context, command Command) (string, error) {
	runner.mu.Lock()
	runner.commands = append(runner.commands, command)
	runner.mu.Unlock()
	return runner.run(command)
}

func TestResolveProvidersAndCache(t *testing.T) {
	runner := &fakeRunner{run: func(command Command) (string, error) {
		switch command.Name {
		case "op":
			return "op-value", nil
		case "bw":
			return "bw-value\n", nil
		default:
			return "", fmt.Errorf("unexpected command %s", command.Name)
		}
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})

	opValue, err := session.Resolve("op://Production/API/token")
	if err != nil || opValue != "op-value" {
		t.Fatalf("Resolve(op) = %q, %v", opValue, err)
	}
	bwValue, err := session.Resolve("bw://password/Container%20Registry")
	if err != nil || bwValue != "bw-value" {
		t.Fatalf("Resolve(bw) = %q, %v", bwValue, err)
	}
	if _, err := session.Resolve("op://Production/API/token"); err != nil {
		t.Fatal(err)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(runner.commands))
	}
	if got := runner.commands[0].Args; fmt.Sprint(got) != "[read --no-newline op://Production/API/token]" {
		t.Fatalf("op args = %v", got)
	}
	if got := runner.commands[1].Args; fmt.Sprint(got) != "[get password Container Registry]" {
		t.Fatalf("bw args = %v", got)
	}
}

func TestEnsureAuthUsesFallbackSessionEnvironment(t *testing.T) {
	checks := 0
	runner := &fakeRunner{run: func(command Command) (string, error) {
		if command.Name == "company-vault-login" {
			return "session-token\n", nil
		}
		if command.Name == "bw" && len(command.Args) == 1 && command.Args[0] == "status" {
			checks++
			if checks == 1 {
				return `{"status":"unauthenticated"}`, nil
			}
			if command.Env["BW_SESSION"] != "session-token" {
				t.Fatalf("BW_SESSION = %q", command.Env["BW_SESSION"])
			}
			return `{"status":"unlocked"}`, nil
		}
		return "", fmt.Errorf("unexpected command: %s %v", command.Name, command.Args)
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	err := session.EnsureAuth(Config{EnsureAuth: []AuthConfig{{
		Provider: "bw",
		Login: LoginConfig{Fallback: &FallbackConfig{
			Command: "company-vault-login", Args: []string{"bitwarden"}, SessionEnv: "BW_SESSION",
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if checks != 2 {
		t.Fatalf("checks = %d, want 2", checks)
	}
}

func TestResolveCoalescesConcurrentLookups(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runner := &fakeRunner{run: func(Command) (string, error) {
		once.Do(func() { close(started) })
		<-release
		return "shared", nil
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			value, err := session.Resolve("op://Production/API/token")
			if err == nil && value != "shared" {
				err = fmt.Errorf("value = %q", value)
			}
			results <- err
		}()
	}
	<-started
	close(release)
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(runner.commands))
	}
}

func TestEnsureAuthSkipsNativeLoginWhenHeadless(t *testing.T) {
	runner := &fakeRunner{run: func(command Command) (string, error) {
		if command.Name == "op" && len(command.Args) == 1 && command.Args[0] == "whoami" {
			return "", fmt.Errorf("not signed in")
		}
		return "", fmt.Errorf("unexpected command: %s %v", command.Name, command.Args)
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}, Interactive: false})
	err := session.EnsureAuth(Config{EnsureAuth: []AuthConfig{{Provider: "op", Login: LoginConfig{Native: true}}}})
	if err == nil || !contains(err.Error(), "interactive terminal") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{"unknown provider", Config{EnsureAuth: []AuthConfig{{Provider: "vault"}}}},
		{"duplicate", Config{EnsureAuth: []AuthConfig{{Provider: "op"}, {Provider: "op"}}}},
		{"ambiguous fallback", Config{EnsureAuth: []AuthConfig{{Provider: "bw", Login: LoginConfig{Fallback: &FallbackConfig{Command: "login", Script: "login"}}}}}},
		{"invalid session env", Config{EnsureAuth: []AuthConfig{{Provider: "bw", Login: LoginConfig{Fallback: &FallbackConfig{Command: "login", SessionEnv: "BAD-NAME"}}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRedactPreservesUnwrapAndEscapedDiagnostics(t *testing.T) {
	runner := &fakeRunner{run: func(Command) (string, error) { return "line one\nline two", nil }}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	if _, err := session.Resolve("op://Production/API/token"); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("provider failure")
	underlying := fmt.Errorf("token is line one\nline two: %w", sentinel)
	redacted := session.RedactError(underlying)
	if redacted.Error() != "token is <redacted>: provider failure" {
		t.Fatalf("error = %q", redacted)
	}
	if !errors.Is(redacted, sentinel) {
		t.Fatal("redacted error does not preserve unwrapping")
	}
	if got := session.Redact(`{"value":"line one\nline two"}`); got != `{"value":"<redacted>"}` {
		t.Fatalf("diagnostic = %s", got)
	}
}

func TestRedactErrorRedactsStructuredValidationIssues(t *testing.T) {
	runner := &fakeRunner{run: func(Command) (string, error) { return "hunter2-secret", nil }}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	if _, err := session.Resolve("op://Production/API/token"); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("cause")
	underlying := fmt.Errorf("workflow: %w", &validation.Error{Issues: []validation.Issue{
		{Code: validation.CodeInvalidValue, Message: "safe first"},
		{Code: validation.CodeInvalidValue, Message: `invalid value "hunter2-secret"`, SourceLine: "token: hunter2-secret", Cause: sentinel},
	}})
	redacted := session.RedactError(underlying)
	for _, issue := range validation.Issues(redacted) {
		if strings.Contains(issue.Message, "hunter2") || strings.Contains(issue.SourceLine, "hunter2") {
			t.Fatalf("issue leaked secret: %#v", issue)
		}
	}
	if len(validation.Issues(redacted)) != 2 || !errors.Is(redacted, sentinel) {
		t.Fatalf("redacted error lost issues or unwrapping: %v", redacted)
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

func TestFallbackScriptArgumentsStartAtOne(t *testing.T) {
	var script Command
	checks := 0
	runner := &fakeRunner{run: func(command Command) (string, error) {
		if command.Name == "bw" {
			checks++
			if checks == 1 {
				return `{"status":"unauthenticated"}`, nil
			}
			return `{"status":"unlocked"}`, nil
		}
		script = command
		return "", nil
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	err := session.EnsureAuth(Config{EnsureAuth: []AuthConfig{{
		Provider: "bw",
		Login: LoginConfig{Fallback: &FallbackConfig{
			Script: `login "$1" "$2"`, Args: []string{"alpha", "beta"},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[-c login "$1" "$2" /bin/sh alpha beta]`
	if got := fmt.Sprint(script.Args); got != want {
		t.Fatalf("script args = %s, want %s", got, want)
	}
}

func TestEnsureAuthRunsFallbackWhenStatusFails(t *testing.T) {
	attempted := false
	checks := 0
	runner := &fakeRunner{run: func(command Command) (string, error) {
		if command.Name == "bw" {
			checks++
			if checks == 1 {
				return "", fmt.Errorf("bw status: vault is not reachable")
			}
			return `{"status":"unlocked"}`, nil
		}
		attempted = true
		return "", nil
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	err := session.EnsureAuth(Config{EnsureAuth: []AuthConfig{{
		Provider: "bw",
		Login:    LoginConfig{Fallback: &FallbackConfig{Command: "company-vault-login"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !attempted {
		t.Fatal("fallback login was not attempted after a failing status check")
	}
}

func TestEnsureAuthChecksEachProviderOncePerSession(t *testing.T) {
	checks := 0
	runner := &fakeRunner{run: func(command Command) (string, error) {
		if command.Name != "op" {
			return "", fmt.Errorf("unexpected command %s", command.Name)
		}
		checks++
		return "signed in", nil
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	config := Config{EnsureAuth: []AuthConfig{{Provider: "op"}}}
	// A dependency plan and repeated child-workflow invocations share one session and each
	// prepare their workflow, so the provider CLI must not be consulted again every time.
	for range 3 {
		if err := session.EnsureAuth(config); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 1 {
		t.Fatalf("provider status checks = %d, want 1", checks)
	}
}

func TestEnsureAuthReportsRepeatedRefusalWithoutRetrying(t *testing.T) {
	attempts := 0
	runner := &fakeRunner{run: func(command Command) (string, error) {
		attempts++
		return "", fmt.Errorf("not signed in")
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	config := Config{EnsureAuth: []AuthConfig{{Provider: "op"}}}
	first := session.EnsureAuth(config)
	if first == nil {
		t.Fatal("expected the unauthenticated provider to be refused")
	}
	second := session.EnsureAuth(config)
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("second refusal = %v, want %v", second, first)
	}
	if attempts != 1 {
		t.Fatalf("provider invocations = %d, want 1", attempts)
	}
}

func TestEnsureAuthSerializesConcurrentCallers(t *testing.T) {
	var mu sync.Mutex
	active, peak := 0, 0
	runner := &fakeRunner{run: func(command Command) (string, error) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()
		return "signed in", nil
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	// Parallel branches share one session, and a login command reads the session's single
	// stdin, so two of them must never be in flight at once.
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := session.EnsureAuth(Config{EnsureAuth: []AuthConfig{{Provider: "op"}}}); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Fatalf("concurrent provider invocations = %d, want 1", peak)
	}
}

func TestNativeLoginStoresAccountScopedOPSession(t *testing.T) {
	signedIn := false
	var checkEnv map[string]string
	runner := &fakeRunner{run: func(command Command) (string, error) {
		switch {
		case len(command.Args) == 1 && command.Args[0] == "whoami":
			if !signedIn {
				return "", fmt.Errorf("not signed in")
			}
			checkEnv = command.Env
			return "signed in", nil
		case len(command.Args) == 2 && command.Args[0] == "signin":
			signedIn = true
			return "signin-token\n", nil
		case len(command.Args) == 3 && command.Args[0] == "account":
			return `[{"url":"https://my.1password.com","user_uuid":"ABC123"}]`, nil
		}
		return "", fmt.Errorf("unexpected command: %s %v", command.Name, command.Args)
	}}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}, Interactive: true})
	if err := session.EnsureAuth(Config{EnsureAuth: []AuthConfig{{Provider: "op", Login: LoginConfig{Native: true}}}}); err != nil {
		t.Fatal(err)
	}
	if checkEnv["OP_SESSION_my"] != "signin-token" || checkEnv["OP_SESSION_ABC123"] != "signin-token" {
		t.Fatalf("session environment = %#v", checkEnv)
	}
	if _, exists := checkEnv["OP_SESSION"]; exists {
		t.Fatal("bare OP_SESSION is not read by op and must not be set")
	}
}

func TestRedactKeepsShortValues(t *testing.T) {
	runner := &fakeRunner{run: func(Command) (string, error) { return "8080", nil }}
	session := NewSession(t.Context(), Options{Runner: runner, BaseEnv: map[string]string{}})
	if _, err := session.Resolve("op://Production/API/port"); err != nil {
		t.Fatal(err)
	}
	if got := session.Redact("listening on 8080 after 8080ms"); got != "listening on 8080 after 8080ms" {
		t.Fatalf("diagnostic = %q", got)
	}
}

func TestResolveOnNilSessionReportsUnavailableResolver(t *testing.T) {
	var session *Session
	if _, err := session.Resolve("op://Production/API/token"); err == nil {
		t.Fatal("expected an error from a nil session")
	}
	if got := session.Redact("value"); got != "value" {
		t.Fatalf("Redact = %q", got)
	}
}
