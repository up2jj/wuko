//go:build !windows

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/creack/pty"
	"github.com/up2jj/wuko/step"
)

func TestRunCommandKeepsInteractiveInputWhileStreamingReport(t *testing.T) {
	root := t.TempDir()
	workflowPath := filepath.Join(root, "interactive.yaml")
	writeWorkflowData(t, workflowPath, `version: 1
name: interactive
steps:
  - id: prompt
    type: stream_probe
`)
	input, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = terminal.Close()
	})

	var observed step.Request
	registry := step.NewRegistry()
	if err := registry.Register("stream_probe", func(map[string]any) (step.Runner, error) {
		return streamProbeRunner{observed: &observed}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: terminal, stdout: &stdout, stderr: &stderr,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil },
		configDir: func() (string, error) { return root, nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", workflowPath, "--report-json", "-"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !observed.Interactive || observed.Stdin != terminal {
		t.Fatalf("request interactive = %v, stdin = %T; want terminal stdin", observed.Interactive, observed.Stdin)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("interactive-output")) {
		t.Fatalf("stderr = %q, want interactive output", stderr.String())
	}
	if report := decodeStreamExecutionReport(t, stdout.Bytes()); report.Status != "succeeded" {
		t.Fatalf("report = %#v", report)
	}
}

type streamProbeRunner struct {
	observed *step.Request
}

func (runner streamProbeRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	*runner.observed = request
	_, err := fmt.Fprint(request.Stdout, "interactive-output")
	return step.Result{}, err
}
