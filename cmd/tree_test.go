package cmd

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestTreeCommandDisplaysWorkflowAndRemoteActionSteps(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := `version: 1
name: release
vars: {release: default}
steps:
  - id: test
    type: shell
  - id: build
    uses: https://actions.example.test/{{ .vars.release }}/build?token=secret
  - id: publish
    type: shell
    if: steps.build.ready
`
	if err := os.WriteFile(filepath.Join(workflowDir, "release.yaml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	action := `version: 1
name: build
steps:
  - id: compile
    type: shell
  - id: package
    type: shell
`
	client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://actions.example.test/v1/build?token=secret" {
			t.Fatalf("action URL = %q", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(action)),
			Request:    request,
		}, nil
	})}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
		cwd:       func() (string, error) { return root, nil },
		homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
		registry:  step.NewRegistry(), loader: workflow.NewLoader(client),
	})
	command.SetArgs([]string{"tree", "release", "--var", "release=v1"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := `release
├── test (shell)
├── build (uses https://actions.example.test/v1/build)
│   ├── compile (shell)
│   └── package (shell)
└── publish (shell) if: steps.build.ready
`
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestTreeCommandSupportsFileAndRemoteWorkflowSelectors(t *testing.T) {
	for _, test := range []struct {
		name string
		args func(string) []string
	}{
		{name: "file", args: func(path string) []string { return []string{"tree", "--file", path} }},
		{name: "remote", args: func(string) []string { return []string{"tree", "https://workflows.example.test/release.yaml"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "workflow.yaml")
			data := "version: 1\nname: selected\nsteps:\n  - id: run\n    type: shell\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data)), Request: request}, nil
			})}
			var output bytes.Buffer
			command := newRootCmd(dependencies{
				stdin: bytes.NewReader(nil), stdout: &output, stderr: &output,
				cwd:       func() (string, error) { return root, nil },
				homeDir:   func() (string, error) { return filepath.Join(root, "home"), nil },
				configDir: func() (string, error) { return filepath.Join(root, "config"), nil },
				registry:  step.NewRegistry(), loader: workflow.NewLoader(client),
			})
			command.SetArgs(test.args(path))
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if output.String() != "selected\n└── run (shell)\n" {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestTreeCommandRejectsMissingOrConflictingWorkflowSelector(t *testing.T) {
	for _, args := range [][]string{{"tree"}, {"tree", "name", "--file", "workflow.yaml"}} {
		command := newRootCmd(dependencies{
			stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
			cwd:       func() (string, error) { return t.TempDir(), nil },
			homeDir:   func() (string, error) { return "", nil },
			configDir: func() (string, error) { return "", nil },
			registry:  step.NewRegistry(),
		})
		command.SetArgs(args)
		if err := command.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("args %v: expected error", args)
		}
	}
}

func TestWorkflowTreeDisplaysExecutionPolicy(t *testing.T) {
	timeout := workflow.Duration(2 * time.Minute)
	definition := &workflow.Definition{Name: "release", Steps: []workflow.Step{{
		ID: "publish", Type: "shell", Timeout: &timeout,
		Retry: &workflow.RetryPolicy{MaxAttempts: 4, BackoffMultiplier: 1, MaxElapsedTime: workflow.Duration(6 * time.Minute)},
	}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	if want := "release\n└── publish (shell) [timeout 2m0s, 4 attempts within 6m0s]\n"; output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
