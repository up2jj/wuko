package cmd

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func TestWorkflowPickerScopeCycleStartsWithAllEachInvocation(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "deploy.yaml"), targetPickerWorkflow)
	definition := "version: 1\nname: deploy\nsteps:\n  - return: {outputs: {}}\n"
	testfs.Write(t, filepath.Join(parent, ".wuko", "workflows", "parent.yaml"), definition)
	testfs.Write(t, filepath.Join(root, "home", ".wuko", "workflows", "deploy.yaml"), definition)
	testfs.Write(t, filepath.Join(root, "config", "wuko", "workflows", "config.yaml"), definition)
	for range 2 {
		calls := 0
		deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
			wantScope := []string{"all", "local", "global", "all"}[calls]
			wantCount := []int{4, 2, 2, 4}[calls]
			if !config.AllowScopeToggle || !strings.Contains(config.Title, "scope: "+wantScope) || len(config.Options) != wantCount {
				t.Fatalf("step %d: config = %#v", calls, config)
			}
			if calls > 0 && config.InitialFilter != "dep" {
				t.Fatalf("filter = %q, want dep", config.InitialFilter)
			}
			for _, option := range config.Options {
				source := option.Value.(workflow.Source)
				if wantScope != "all" && source.Scope != wantScope {
					t.Fatalf("%s scope includes %#v", wantScope, source)
				}
				if source.Scope == "global" && source.Name == "deploy" && source.Effective {
					t.Fatal("scope filtering changed shadowed global workflow precedence")
				}
				if source.Scope == "local" && source.Name == "deploy" && (source.Target != "" || !strings.Contains(source.Description, "2 targets")) {
					t.Fatalf("target grouping = %#v", source)
				}
			}
			calls++
			if calls == 4 {
				return tui.Selection{}, context.Canceled
			}
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionToggleScope, Filter: "dep"}, nil
		})
		command := newRootCmd(deps)
		if err := command.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if calls != 4 {
			t.Fatalf("picker calls = %d, want 4", calls)
		}
	}
}

func TestWorkflowPickerScopeEmptyRecoveryPreservesTargetHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "deploy.yaml")
	sources := []workflow.Source{{Name: "deploy", Path: path, Scope: "local", Target: "staging", Invokable: true}}
	position := targetPickerPosition{target: "staging", filter: "stag"}
	navigation := workflowPickerNavigation{
		allowScopeToggle: true, selected: path,
		targets: map[string]targetPickerPosition{path: position},
	}
	calls := 0
	deps := pickerDependencies(t.TempDir(), step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		calls++
		if calls <= 5 {
			wantScope := []string{"all", "local", "global", "all", "local"}[calls-1]
			if !config.AllowScopeToggle || !strings.Contains(config.Title, "scope: "+wantScope) {
				t.Fatalf("workflow config = %#v", config)
			}
			if calls == 3 {
				if len(config.Options) != 0 {
					t.Fatalf("empty global options = %#v", config.Options)
				}
				return tui.Selection{Intent: tui.SelectionToggleScope, Filter: "dep"}, nil
			}
			if (calls == 2 || calls == 4) && !config.Options[0].Default {
				t.Fatalf("step %d: scope switch lost workflow selection", calls)
			}
			intent := tui.SelectionToggleScope
			if calls == 5 {
				intent = tui.SelectionTargets
			}
			return tui.Selection{Option: config.Options[0], Intent: intent, Filter: "dep"}, nil
		}
		if config.AllowScopeToggle || !config.AllowBack || config.InitialFilter != "stag" || !config.Options[0].Default {
			t.Fatalf("restored target config = %#v", config)
		}
		return tui.Selection{Option: config.Options[0], Intent: tui.SelectionValidate, Filter: "stag"}, nil
	})
	command := newRootCmd(deps)
	command.SetContext(t.Context())
	selection, err := navigation.selectWorkflow(command, deps, sources, workflowPickerState{}, workflowPickerSortName, "Workflows", "")
	if err != nil || selection.Intent != tui.SelectionValidate || calls != 6 || navigation.scope != workflowPickerScopeLocal {
		t.Fatalf("selection=%#v, calls=%d, error=%v", selection, calls, err)
	}
	if diff := cmp.Diff(position, navigation.targets[path], cmp.AllowUnexported(targetPickerPosition{})); diff != "" {
		t.Fatalf("target history (-want +got):\n%s", diff)
	}
	// A subsequent picker call (such as after an inspection) retains the target position.
	if _, err := navigation.selectWorkflow(command, deps, sources, workflowPickerState{}, workflowPickerSortName, "Workflows", ""); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPickerScopeSurvivesRefreshAndReturn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".wuko", "workflows", "deploy.yaml")
	definition := "version: 1\nname: deploy\ndescription: Original\nsteps:\n  - return: {to: picker, outputs: {}}\n"
	testfs.Write(t, path, definition)
	calls := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		calls++
		if calls > 1 && (!strings.Contains(config.Title, "scope: local") || config.InitialFilter != "dep" || !config.Options[0].Default) {
			t.Fatalf("restored workflow config = %#v", config)
		}
		if calls >= 3 && !strings.Contains(config.Options[0].Description, "Updated") {
			t.Fatalf("refresh did not update workflow: %#v", config.Options[0])
		}
		if calls == 4 {
			return tui.Selection{}, context.Canceled
		}
		intent := []tui.SelectionIntent{tui.SelectionToggleScope, tui.SelectionEditor, tui.SelectionPrimary}[calls-1]
		return tui.Selection{Option: config.Options[0], Intent: intent, Filter: "dep"}, nil
	})
	deps.openEditor = func(_ context.Context, _ io.Reader, _, _ io.Writer, editedPath string) error {
		testfs.Write(t, editedPath, strings.ReplaceAll(definition, "Original", "Updated"))
		return nil
	}
	if err := newRootCmd(deps).ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("picker calls = %d, want 4", calls)
	}
}
