package engine

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/steps/temp"
	"github.com/up2jj/wuko/workflow"
)

const replaceStepYAML = `
  - id: connect
    type: shell
    with:
      command: sh
      tty: true
      replace: true
`

func replacementRegistry(t *testing.T) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := temp.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("service", func(map[string]any) (step.Runner, error) {
		return steptest.RunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
			return step.Result{}, request.Services.StartService(request.StepID, "test", step.ServiceOptions{}, func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			})
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func loadReplacementWorkflow(t *testing.T, body string) *workflow.Definition {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	testfs.WriteFile(t, path, []byte("version: 1\nname: handoff\n"+body), 0o644)
	definition, err := workflow.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func acceptReplacement(process.Replacement) error { return nil }

func TestValidateProcessReplacementPlacement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		hook func(process.Replacement) error
		want string
	}{
		{
			name: "final step",
			body: "steps:\n  - id: prepare\n    type: shell\n    with:\n      command: \"true\"\n" + replaceStepYAML,
			hook: acceptReplacement,
		},
		{
			name: "host without replacement",
			body: "steps:" + replaceStepYAML,
			want: "replace is available only to the final step",
		},
		{
			name: "not final",
			body: "steps:" + replaceStepYAML + "  - id: after\n    type: shell\n    with:\n      command: \"true\"\n",
			hook: acceptReplacement,
			want: "replace must be the final top-level step",
		},
		{
			name: "inside block",
			body: "steps:\n  - if: \"true\"\n    steps:" + strings.ReplaceAll(replaceStepYAML, "\n  ", "\n      "),
			hook: acceptReplacement,
			want: "replace must be the final top-level step",
		},
		{
			name: "twice",
			body: "steps:" + replaceStepYAML + strings.ReplaceAll(replaceStepYAML, "id: connect", "id: again"),
			hook: acceptReplacement,
			want: "only one step may replace the process",
		},
		{
			name: "finally",
			body: "steps:" + replaceStepYAML + "finally:\n  - id: cleanup\n    type: shell\n    with:\n      command: \"true\"\n",
			hook: acceptReplacement,
			want: "replace cannot be combined with workflow finally steps",
		},
		{
			name: "outputs",
			body: "outputs:\n  name:\n    type: string\n    value: '\"value\"'\nsteps:" + replaceStepYAML,
			hook: acceptReplacement,
			want: "replace cannot be combined with workflow outputs",
		},
		{
			name: "defer",
			body: "steps:\n  - id: prepare\n    type: shell\n    with:\n      command: \"true\"\n    defer:\n      - id: undo\n        type: shell\n        with:\n          command: \"true\"\n" + replaceStepYAML,
			hook: acceptReplacement,
			want: "deferred cleanup would never run",
		},
		{
			name: "managed resource",
			body: "steps:\n  - id: scratch\n    type: temp\n    with:\n      kind: directory\n" + replaceStepYAML,
			hook: acceptReplacement,
			want: "would skip that cleanup",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := loadReplacementWorkflow(t, test.body)
			err := New(replacementRegistry(t)).Validate(t.Context(), definition, Options{ReplaceProcess: test.hook, Stdout: io.Discard, Stderr: io.Discard})
			if test.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunProcessReplacementEndsWithoutOutputs(t *testing.T) {
	t.Parallel()
	definition := loadReplacementWorkflow(t, "steps:"+replaceStepYAML)
	var requested []process.Replacement
	var finished []ProgressEvent
	state, err := New(replacementRegistry(t)).Run(t.Context(), definition, Options{
		Interactive: true, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
		ReplaceProcess: func(replacement process.Replacement) error {
			requested = append(requested, replacement)
			return nil
		},
		Progress: func(event ProgressEvent) {
			if event.Kind == WorkflowFinished {
				finished = append(finished, event)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || requested[0].Argv[0] != "sh" {
		t.Fatalf("requested replacements = %#v", requested)
	}
	if !state.Replaced || len(state.Outputs) != 0 {
		t.Fatalf("state replaced = %v outputs = %#v", state.Replaced, state.Outputs)
	}
	if len(finished) != 1 || !finished[0].Replaced || finished[0].Status != StatusSucceeded {
		t.Fatalf("workflow finished events = %#v", finished)
	}
}

func TestRunProcessReplacementRefusesWhileServicesRun(t *testing.T) {
	t.Parallel()
	definition := loadReplacementWorkflow(t, "steps:\n  - id: tunnel\n    type: service\n"+replaceStepYAML)
	_, err := New(replacementRegistry(t)).Run(t.Context(), definition, Options{
		Interactive: true, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
		ReplaceProcess: func(process.Replacement) error {
			t.Error("ReplaceProcess called while a service runs")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "background service(s) are running") {
		t.Fatalf("Run() error = %v", err)
	}
}
