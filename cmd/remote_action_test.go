package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

type commandRoundTripFunc func(*http.Request) (*http.Response, error)

func (function commandRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRunRemoteActionUsesCLIValuesDuringLoading(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	caller := `version: 1
name: remote
vars: {release: default, message: default}
steps:
  - id: call
    uses: https://actions.example.test/{{ .vars.release }}/echo
    with:
      message: "{{ .vars.message }}"
`
	if err := os.WriteFile(filepath.Join(workflowDir, "remote.yaml"), []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 1
name: echo
inputs:
  message: {type: string, required: true}
outputs:
  text: {value: steps.echo.stdout}
steps:
  - id: echo
    type: shell
    with:
      script: "printf '%s' \"$1\""
      args: ["{{ .inputs.message }}"]
`
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/echo" {
			t.Fatalf("path = %q, want /v1/echo", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(manifest)), Header: make(http.Header)}, nil
	})}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  registry, loader: workflow.NewLoader(client),
	})
	command.SetArgs([]string{"run", "remote", "--var", "release=v1", "--var", "message=hello"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "hello") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunRemoteWorkflowFromHTTPS(t *testing.T) {
	workflowData := `version: 1
name: remote-workflow
steps:
  - id: echo
    type: shell
    with:
      script: "printf '%s' \"$1\""
      args: ["{{ .vars.message }}"]
`
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://workflows.example.test/release.yaml" {
			return nil, fmt.Errorf("unexpected URL %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(workflowData)), Header: make(http.Header)}, nil
	})}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd: func() (string, error) { return t.TempDir(), nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry, loader: workflow.NewLoader(client),
	})
	command.SetArgs([]string{"run", "https://workflows.example.test/release.yaml", "--var", "message=hello"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "hello") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunAndUIRejectDependencyOnlyRemoteWorkflows(t *testing.T) {
	workflowData := `version: 1
name: remote-build
invokable: false
steps:
  - id: action
    uses:
      command: wuko-test-must-not-run
`
	for _, tt := range []struct {
		name    string
		command string
		locator string
	}{
		{name: "run https", command: "run", locator: "https://workflows.example.test/build.yaml"},
		{name: "ui https", command: "ui", locator: "https://workflows.example.test/build.yaml"},
		{name: "run github", command: "run", locator: "github:acme/workflows:build.yaml"},
		{name: "ui github", command: "ui", locator: "github:acme/workflows:build.yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: commandRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(workflowData)), Header: make(http.Header)}, nil
			})}
			root := t.TempDir()
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
				configDir: func() (string, error) { return "", nil }, registry: step.NewRegistry(), loader: workflow.NewLoader(client),
			})
			command.SetArgs([]string{tt.command, tt.locator})
			err := command.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), `workflow "remote-build" is not directly invokable`) {
				t.Fatalf("error = %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestRemoteWorkflowValueStoreScopes(t *testing.T) {
	tests := []struct {
		name      string
		scope     string
		wantError string
	}{
		{"local rejected", "local", "local key-value storage is unavailable"},
		{"global available", "global", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowData := fmt.Sprintf(`version: 1
name: remote-values
steps:
  - id: save
    type: key_value
    with: {operation: set, scope: %s, store: prefs, key: theme, value: dark}
`, tt.scope)
			client := &http.Client{Transport: commandRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(workflowData)), Header: make(http.Header)}, nil
			})}
			registry := step.NewRegistry()
			if err := keyvaluestep.Register(registry); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			configDir := filepath.Join(root, "config")
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
				configDir: func() (string, error) { return configDir, nil }, registry: registry, loader: workflow.NewLoader(client),
			})
			command.SetArgs([]string{"run", "https://workflows.example.test/values.yaml"})
			err := command.ExecuteContext(t.Context())
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(configDir, "wuko", "values", "prefs.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateRemoteActionAcceptsVarFileAndEnvFlags(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	caller := `version: 1
name: remote
steps:
  - id: call
    uses: https://actions.example.test/{{ .vars.release }}/{{ .env.CHANNEL }}
`
	if err := os.WriteFile(filepath.Join(dir, "remote.yaml"), []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "variables.toml"), []byte("release = \"v2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\nname: validate\nsteps:\n  - id: run\n    type: shell\n    with: {script: 'true'}\n"
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v2/stable" {
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(manifest)), Header: make(http.Header)}, nil
	})}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry, loader: workflow.NewLoader(client),
	})
	command.SetArgs([]string{"validate", "remote", "--var-file", "variables.toml", "--env", "CHANNEL=stable"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "remote: valid") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestValidateCommandBasedActionSource(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	caller := `version: 1
name: command-source
steps:
  - id: call
    uses:
      command: sh
      args: [-c, 'printf "%s" "$ACTION_MANIFEST"']
`
	if err := os.WriteFile(filepath.Join(dir, "command-source.yaml"), []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\nname: command-action\nsteps:\n  - id: run\n    type: shell\n    with: {script: 'true'}\n"
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		registry: registry, loader: workflow.NewLoader(nil),
	})
	command.SetArgs([]string{"validate", "command-source", "--env", "ACTION_MANIFEST=" + manifest})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "command-source: valid") {
		t.Fatalf("output = %q", output.String())
	}
}
