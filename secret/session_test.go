package secret

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
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
