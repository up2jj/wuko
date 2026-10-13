package main

import (
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/agent"
	"github.com/up2jj/wuko/steps/choice"
	gitstep "github.com/up2jj/wuko/steps/git"
	inputstep "github.com/up2jj/wuko/steps/input"
	luastep "github.com/up2jj/wuko/steps/lua"
	markdowneditstep "github.com/up2jj/wuko/steps/markdown_edit"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/steps/shell"
	tablestep "github.com/up2jj/wuko/steps/table"
	timestep "github.com/up2jj/wuko/steps/time"
	transformstep "github.com/up2jj/wuko/steps/transform"
	"github.com/up2jj/wuko/workflow"
)

func TestTimeExampleValidates(t *testing.T) {
	t.Parallel()
	definition, err := workflow.NewLoader(nil).Load(t.Context(), filepath.Join("examples", "time.yaml"), workflow.LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{timestep.Register, setstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestTransformExampleValidates(t *testing.T) {
	t.Parallel()
	definition, err := workflow.NewLoader(nil).Load(t.Context(), filepath.Join("examples", "transform.yaml"), workflow.LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := transformstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestMarkdownEditExampleValidates(t *testing.T) {
	t.Parallel()
	definition, err := workflow.NewLoader(nil).Load(t.Context(), filepath.Join("examples", "markdown-edit.yaml"), workflow.LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	registry := step.NewRegistry()
	if err := markdowneditstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeLifecycleExampleValidates(t *testing.T) {
	path := filepath.Join("examples", "worktree-lifecycle.yaml")
	for _, target := range []string{"list", "start", "shell", "remove", "merge"} {
		t.Run(target, func(t *testing.T) {
			definition, err := workflow.NewLoader(nil).Load(t.Context(), path, workflow.LoadOptions{Target: target, RunDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			registry := step.NewRegistry()
			for _, register := range []func(*step.Registry) error{gitstep.Register, shell.Register, tablestep.Register} {
				if err := register(registry); err != nil {
					t.Fatal(err)
				}
			}
			if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStartTaskExampleValidates(t *testing.T) {
	t.Setenv("WUKO_DEMO_TOKEN", "demo")
	path := filepath.Join("examples", ".wuko", "workflows", "start-task.yaml")
	definition, err := workflow.NewLoader(nil).Load(t.Context(), path, workflow.LoadOptions{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		inputstep.Register, choice.Register, luastep.Register, shell.Register, agent.Register,
	} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}

	if err := engine.New(registry).Validate(t.Context(), definition, engine.Options{RunDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}
