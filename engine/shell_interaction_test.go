package engine

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/ptyinteract"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
)

func TestShellInteractionSendRendersRuntimeStateAndRedactsSensitiveValue(t *testing.T) {
	registry := step.NewRegistry()
	if err := registry.Register("seed", func(map[string]any) (step.Runner, error) { return interactionSeedRunner{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, "interaction",
		workflow.Step{ID: "seed", Type: "seed"},
		workflow.Step{ID: "console", Type: "shell", With: map[string]any{
			"command": "console", "tty": true,
			"interactions": []any{map[string]any{
				"expect":    "prompt>",
				"send":      "{{ .vars.prefix }}:{{ .steps.seed.value }}:{{ .dependencies.build.artifact }}:{{ .env.LOGIN_PASSWORD }}",
				"sensitive": true,
			}},
		}},
	)
	definition.Vars = map[string]any{"prefix": "selected"}
	executor := &interactionRecordingExecutor{}
	var diagnostics []diagnostic.Event
	_, err := New(registry).Run(t.Context(), definition, Options{
		Env:          map[string]string{"LOGIN_PASSWORD": "secret-value"},
		Dependencies: map[string]map[string]any{"build": {"artifact": "release"}},
		Executor:     executor, Stdout: io.Discard, Stderr: io.Discard,
		Diagnostics: func(event diagnostic.Event) { diagnostics = append(diagnostics, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(executor.writes, "|"); got != "selected:from-step:release:secret-value" {
		t.Fatalf("interaction writes = %q", got)
	}
	for _, event := range diagnostics {
		if event.Phase != diagnostic.PhaseRender || event.Status != diagnostic.StatusSucceeded || len(event.Attributes) == 0 {
			continue
		}
		configuration := event.Attributes[0].Value
		if strings.Contains(configuration, "secret-value") {
			t.Fatalf("render diagnostic exposed sensitive send: %s", configuration)
		}
	}
}

type interactionSeedRunner struct{}

func (interactionSeedRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{Outputs: map[string]any{"value": "from-step"}}, nil
}

type interactionRecordingExecutor struct{ writes []string }

func (executor *interactionRecordingExecutor) Run(ctx context.Context, options process.Options) (process.Result, error) {
	output := make(chan []byte, 1)
	output <- []byte("prompt>")
	close(output)
	sink := &interactionRecordingSink{writes: &executor.writes}
	return process.Result{}, options.Interactions.Run(ctx, ptyinteract.Stream{Output: output}, nil, sink)
}

type interactionRecordingSink struct{ writes *[]string }

func (sink *interactionRecordingSink) Write(data []byte) (int, error) {
	*sink.writes = append(*sink.writes, string(data))
	return len(data), nil
}

func (sink *interactionRecordingSink) WriteSensitive(data []byte) error {
	*sink.writes = append(*sink.writes, string(data))
	return nil
}
