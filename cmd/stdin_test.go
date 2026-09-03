package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
)

func TestRunCommandReadsWorkflowFromStdinNonInteractively(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "fragments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fragments", "run.yaml"), []byte("- id: inspect\n  type: capture_stdin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := `version: 1
name: streamed
targets:
  staging:
    steps:
      - require: fragments/run.yaml
`
	var captured step.Request
	registry := step.NewRegistry()
	if err := registry.Register("capture_stdin", func(map[string]any) (step.Runner, error) {
		return stdinCaptureRunner{request: &captured}, nil
	}); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: strings.NewReader(data), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", "-", "staging"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if captured.Stdin != nil || captured.Interactive {
		t.Fatalf("stdin = %#v, interactive = %v", captured.Stdin, captured.Interactive)
	}
	if captured.WorkflowDir != root || captured.LocalValueDir != filepath.Join(root, ".wuko", "values") {
		t.Fatalf("workflow dir = %q, local values = %q", captured.WorkflowDir, captured.LocalValueDir)
	}
	if captured.WorkflowSource != "stdin" {
		t.Fatalf("workflow source = %q", captured.WorkflowSource)
	}
}

func TestRunCommandDryRunNamesStdinWorkflow(t *testing.T) {
	root := t.TempDir()
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin:  strings.NewReader("version: 1\nname: streamed\nsteps:\n  - id: run\n    type: shell\n    with: {script: \"true\"}\n"),
		stdout: &output, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", "-", "--dry-run"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Workflow streamed (stdin)") || strings.Contains(output.String(), filepath.Join(root, "-")) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunCommandReportsInvalidStdinWorkflow(t *testing.T) {
	root := t.TempDir()
	command := newRootCmd(dependencies{
		stdin: strings.NewReader("version: ["), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: step.NewRegistry(),
	})
	command.SetArgs([]string{"run", "--file", "-"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "decoding workflow stdin") || strings.Contains(err.Error(), root) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCommandTreatsDotSlashDashAsFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	data := "version: 1\nname: dash-file\nsteps:\n  - id: run\n    type: capture_stdin\n"
	if err := os.WriteFile(filepath.Join(root, "-"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	registry := step.NewRegistry()
	if err := registry.Register("capture_stdin", func(map[string]any) (step.Runner, error) {
		return stdinCaptureRunner{ran: &ran}, nil
	}); err != nil {
		t.Fatal(err)
	}
	command := newRootCmd(dependencies{
		stdin: strings.NewReader("not a workflow"), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: registry,
	})
	command.SetArgs([]string{"run", "--file", "./-"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("literal dash file did not run")
	}
}

func TestScheduledStdinWorkflowReusesSnapshot(t *testing.T) {
	root := t.TempDir()
	data := `version: 1
name: scheduled-stdin
cron: "1-59/2 * * * * *"
timezone: UTC
steps:
  - id: echo
    type: shell
    with: {script: "printf snapshot"}
`
	input := &failAfterEOFReader{reader: strings.NewReader(data)}
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Date(2026, time.August, 21, 10, 0, 0, 500_000_000, time.UTC)
	waits := 0
	var output bytes.Buffer
	command := newRootCmd(dependencies{
		stdin: input, stdout: &output, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return "", nil },
		configDir: func() (string, error) { return "", nil }, registry: registry,
		now: func() time.Time { return now }, waitUntil: func(_ context.Context, instant time.Time) error {
			waits++
			if waits == 1 {
				now = instant
				return nil
			}
			cancel()
			return context.Canceled
		},
	})
	command.SetArgs([]string{"run", "--file", "-"})
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if output.String() != "snapshot" || waits != 2 {
		t.Fatalf("output = %q, waits = %d", output.String(), waits)
	}
}

type stdinCaptureRunner struct {
	request *step.Request
	ran     *bool
}

func (runner stdinCaptureRunner) Run(_ context.Context, request step.Request) (step.Result, error) {
	if runner.request != nil {
		*runner.request = request
	}
	if runner.ran != nil {
		*runner.ran = true
	}
	return step.Result{}, nil
}

type failAfterEOFReader struct {
	reader    *strings.Reader
	exhausted bool
}

func (reader *failAfterEOFReader) Read(buffer []byte) (int, error) {
	if reader.exhausted {
		return 0, errors.New("stdin read after snapshot")
	}
	n, err := reader.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		reader.exhausted = true
	}
	return n, err
}
