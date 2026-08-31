package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/up2jj/wuko/secret"
)

type workflowSecretRunner struct{ reads int }

func (runner *workflowSecretRunner) Run(_ context.Context, command secret.Command) (string, error) {
	if command.Name == "op" && len(command.Args) > 0 && command.Args[0] == "read" {
		runner.reads++
		return "workflow-token", nil
	}
	return "", nil
}

func TestWorkflowSecretTemplateUsesOccurrenceCache(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "workflow.yaml")
	data := []byte("version: 1\nname: secrets\nenv:\n  FIRST: '{{ secret \"op://Production/API/token\" }}'\n  SECOND: '{{ secret \"op://Production/API/token\" }}'\nsteps:\n  - id: done\n    type: set\n    with:\n      variable: done\n      value: true\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &workflowSecretRunner{}
	session := secret.NewSession(t.Context(), secret.Options{Runner: runner, BaseEnv: map[string]string{}})
	definition, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: directory, BaseEnv: map[string]string{}, SecretSession: session})
	if err != nil {
		t.Fatal(err)
	}
	_, environment, err := PrepareValues(definition, LoadOptions{RunDir: directory, BaseEnv: map[string]string{}, SecretSession: session})
	if err != nil {
		t.Fatal(err)
	}
	if environment["FIRST"] != "workflow-token" || environment["SECOND"] != "workflow-token" {
		t.Fatalf("environment = %#v", environment)
	}
	if runner.reads != 1 {
		t.Fatalf("reads = %d, want 1", runner.reads)
	}
}

func TestPrepareRedactsResolvedValuesInErrors(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "workflow.yaml")
	data := []byte("version: 1\nname: secrets\nenv:\n  BAD: '{{ secret \"op://Production/API/token\" | parseTime \"2006-01-02\" }}'\nsteps:\n  - id: done\n    type: set\n    with:\n      variable: done\n      value: true\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	session := secret.NewSession(t.Context(), secret.Options{Runner: &workflowSecretRunner{}, BaseEnv: map[string]string{}})
	_, err := NewLoader(nil).Load(t.Context(), path, LoadOptions{RunDir: directory, BaseEnv: map[string]string{}, SecretSession: session})
	if err == nil {
		t.Fatal("expected a preparation error")
	}
	if strings.Contains(err.Error(), "workflow-token") {
		t.Fatalf("error leaks the resolved secret: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("error = %v", err)
	}
}

type authSecretRunner struct {
	mu       sync.Mutex
	commands []string
}

func (runner *authSecretRunner) Run(_ context.Context, command secret.Command) (string, error) {
	runner.mu.Lock()
	runner.commands = append(runner.commands, command.Name)
	runner.mu.Unlock()
	if command.Name == "bw" {
		return `{"status":"unlocked"}`, nil
	}
	return "", nil
}

func TestPrepareAuthenticatesOnlyWhenRequested(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "workflow.yaml")
	data := []byte("version: 1\nname: secrets\nsecrets:\n  ensure_auth:\n    - provider: bw\n      login:\n        fallback:\n          command: company-vault-login\nsteps:\n  - id: done\n    type: set\n    with:\n      variable: done\n      value: true\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		auth  bool
		wants bool
	}{
		{"read-only load", false, false},
		{"executing load", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &authSecretRunner{}
			session := secret.NewSession(t.Context(), secret.Options{Runner: runner, BaseEnv: map[string]string{}})
			options := LoadOptions{RunDir: directory, BaseEnv: map[string]string{}, SecretSession: session, EnsureSecretAuth: test.auth}
			if _, err := NewLoader(nil).Load(t.Context(), path, options); err != nil {
				t.Fatal(err)
			}
			runner.mu.Lock()
			defer runner.mu.Unlock()
			if checked := len(runner.commands) > 0; checked != test.wants {
				t.Fatalf("provider commands = %v, want authentication %v", runner.commands, test.wants)
			}
		})
	}
}

func TestRendererWithoutSessionReportsUnavailableResolver(t *testing.T) {
	renderer, err := NewRendererWithSecrets(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.Render(`{{ secret "op://Production/API/token" }}`, map[string]any{}); err == nil {
		t.Fatal("expected an error from a renderer without a secret session")
	}
	var missing *secret.Session
	renderer, err = NewRendererWithSecrets(nil, missing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.Render(`{{ secret "op://Production/API/token" }}`, map[string]any{}); err == nil {
		t.Fatal("expected an error from a renderer holding a nil session")
	}
}
