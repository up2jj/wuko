package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

const targetPickerWorkflow = `version: 1
name: deploy
targets:
  staging:
    description: Stage the release
    steps:
      - return: {to: picker, outputs: {}}
  production:
    description: Release to customers
    form: {title: Production}
    steps:
      - return: {to: picker, outputs: {}}
`

func TestWorkflowPickerGroupsExactSources(t *testing.T) {
	t.Parallel()
	sources := []workflow.Source{
		{Name: "deploy", Target: "staging", Path: "/project/deploy.yaml", Description: "Stage", WorkflowDescription: "Deploy the app", HasForm: true},
		{Name: "deploy", Target: "production", Path: "/project/./deploy.yaml", Description: "Ship", WorkflowDescription: "Deploy the app"},
		{Name: "deploy", Target: "test", Path: "/home/deploy.yaml"},
		{Name: "legacy", Path: "/project/legacy.yaml", Description: "Legacy", DependsOn: map[string]string{"build": "build"}},
		{Name: "remote", Target: "one", Path: "/snapshot/one.yaml", Locator: "github:acme/repo@main:one.yaml"},
		{Name: "remote", Target: "two", Path: "/snapshot/two.yaml", Locator: "github:acme/repo@main:one.yaml"},
	}
	groups, _ := groupWorkflowPickerSources(sources)
	if len(groups) != 4 || len(groups[0].targets) != 2 || len(groups[1].targets) != 1 || len(groups[3].targets) != 2 {
		t.Fatalf("groups = %#v", groups)
	}
	if diff := cmp.Diff([]workflow.Source{sources[1], sources[0]}, groups[0].targets); diff != "" {
		t.Fatalf("target sources (-want +got):\n%s", diff)
	}
	if groups[0].source.Target != "" || !groups[0].source.HasForm || groups[0].source.Description != "Deploy the app • 2 targets: production, staging" || groups[1].source.Description != "1 target: test" || groups[0].source.DependsOn != nil {
		t.Fatalf("group source = %#v", groups[0].source)
	}
	if diff := cmp.Diff(sources[3], groups[2].source); diff != "" {
		t.Fatalf("legacy source (-want +got):\n%s", diff)
	}
}

func TestWorkflowTargetPickerForwardsRequestedActions(t *testing.T) {
	t.Parallel()
	for _, intent := range []tui.SelectionIntent{tui.SelectionPrimary, tui.SelectionTargets, tui.SelectionUI, tui.SelectionValidate, tui.SelectionTree, tui.SelectionDryRun, tui.SelectionAlternate} {
		t.Run(workflowTargetActionName(intent), func(t *testing.T) {
			t.Parallel()
			calls := 0
			sources := []workflow.Source{{Name: "deploy", Path: "/deploy.yaml", Target: "only", Invokable: true, HasForm: true, Description: "Only target"}}
			deps := pickerDependencies(t.TempDir(), step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
				calls++
				if calls == 1 {
					if len(config.Options) != 1 || config.Options[0].Label != "deploy" || config.AllowBack {
						t.Fatalf("workflow config = %#v", config)
					}
					return tui.Selection{Option: config.Options[0], Intent: intent, Filter: "deploy"}, nil
				}
				wantIntent := intent
				if intent == tui.SelectionTargets {
					wantIntent = tui.SelectionPrimary
				}
				if len(config.Options) != 1 || config.Options[0].Label != "only" || config.InitialFilter != "" || !config.AllowBack || config.EnterIntent != wantIntent {
					t.Fatalf("target config = %#v", config)
				}
				return tui.Selection{Option: config.Options[0], Intent: config.EnterIntent}, nil
			})
			command := newRootCmd(deps)
			command.SetContext(t.Context())
			navigation := workflowPickerNavigation{}
			selection, err := navigation.selectWorkflow(command, deps, sources, workflowPickerState{}, workflowPickerSortName, "Workflows", "")
			if err != nil || calls != 2 {
				t.Fatalf("selection calls = %d, error = %v", calls, err)
			}
			if diff := cmp.Diff(sources[0], selection.Option.Value.(workflow.Source)); diff != "" {
				t.Fatalf("selected source (-want +got):\n%s", diff)
			}
		})
	}
}

func workflowTargetActionName(intent tui.SelectionIntent) string {
	if intent == tui.SelectionTargets {
		return "targets"
	}
	for _, action := range workflowPickerActions(workflow.Source{}, false, workflowPickerSortName) {
		if action.Intent == intent {
			return action.Label
		}
	}
	return "unknown"
}

func TestWorkflowTargetPickerInspectionAndBackPreservePositions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "deploy.yaml"), targetPickerWorkflow)
	calls := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		calls++
		switch calls {
		case 1, 5:
			if len(config.Options) != 1 || config.Options[0].Label != "deploy" {
				t.Fatalf("workflow config = %#v", config)
			}
			if calls == 5 && (config.InitialFilter != "dep" || !config.Options[0].Default) {
				t.Fatalf("restored workflow config = %#v", config)
			}
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionTargets, Filter: "dep"}, nil
		case 2:
			if len(config.Options) != 2 || config.Options[0].Label != "production" || config.Options[1].Label != "staging" {
				t.Fatalf("target options = %#v", config.Options)
			}
			if !config.Options[1].Actions[1].Disabled || config.Options[0].Actions[1].Disabled {
				t.Fatal("target form actions do not reflect individual forms")
			}
			return tui.Selection{Option: config.Options[1], Intent: tui.SelectionValidate, Filter: "stag"}, nil
		case 3:
			if config.InitialFilter != "stag" || !config.Options[1].Default {
				t.Fatalf("restored target config = %#v", config)
			}
			return tui.Selection{Option: config.Options[1], Intent: tui.SelectionBack}, nil
		case 4:
			// The parent is restored immediately by the shared navigation loop.
			if config.InitialFilter != "dep" || !config.Options[0].Default || config.AllowBack {
				t.Fatalf("parent config = %#v", config)
			}
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionTogglePin, Filter: "dep"}, nil
		case 6:
			if !config.Options[1].Default || config.InitialFilter != "" {
				t.Fatalf("remembered target config = %#v", config)
			}
			return tui.Selection{}, context.Canceled
		default:
			t.Fatalf("unexpected picker call %d", calls)
			return tui.Selection{}, context.Canceled
		}
	})
	var viewed tui.TextViewerConfig
	deps.viewText = func(_ context.Context, _ io.Reader, _ io.Writer, config tui.TextViewerConfig) error {
		viewed = config
		return nil
	}
	command := newRootCmd(deps)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 6 || viewed.Failed || !strings.Contains(viewed.Content, "deploy (staging): valid") {
		t.Fatalf("calls = %d, viewed = %#v", calls, viewed)
	}
	state, err := loadWorkflowPickerState(t.Context(), filepath.Join(root, "config"))
	if err != nil || len(state.Recent) != 0 || len(state.Pinned) != 1 {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
}

func TestWorkflowTargetPickerRunReturnsToWorkflowList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".wuko", "workflows", "deploy.yaml")
	testfs.Write(t, path, targetPickerWorkflow)
	calls := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		calls++
		switch calls {
		case 1:
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary, Filter: "dep"}, nil
		case 2:
			return tui.Selection{Option: config.Options[1], Intent: tui.SelectionPrimary}, nil
		case 3:
			if config.AllowBack || config.InitialFilter != "dep" || !config.Options[0].Default {
				t.Fatalf("returned picker = %#v", config)
			}
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionTargets, Filter: "dep"}, nil
		case 4:
			if !config.AllowBack || !config.Options[1].Default {
				t.Fatalf("remembered target picker = %#v", config)
			}
			return tui.Selection{}, context.Canceled
		default:
			t.Fatal("unexpected picker call")
			return tui.Selection{}, context.Canceled
		}
	})
	if err := newRootCmd(deps).ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := loadWorkflowPickerState(t.Context(), filepath.Join(root, "config"))
	if err != nil || calls != 4 {
		t.Fatalf("calls = %d, error = %v", calls, err)
	}
	if diff := cmp.Diff([]string{workflowPickerPath(path)}, state.Recent); diff != "" {
		t.Fatalf("recent (-want +got):\n%s", diff)
	}
}

func TestWorkflowTargetPickerRefreshDiscardsRemovedTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".wuko", "workflows", "deploy.yaml")
	testfs.Write(t, path, targetPickerWorkflow)
	calls := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		calls++
		switch calls {
		case 1:
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionTargets}, nil
		case 2:
			return tui.Selection{Option: config.Options[1], Intent: tui.SelectionEditor, Filter: "stag"}, nil
		case 3:
			if len(config.Options) != 1 || config.Options[0].Label != "production" || config.InitialFilter != "" || config.Options[0].Default || !config.AllowBack {
				t.Fatalf("refreshed targets = %#v", config)
			}
			return tui.Selection{}, context.Canceled
		default:
			t.Fatal("unexpected picker call")
			return tui.Selection{}, context.Canceled
		}
	})
	deps.openEditor = func(context.Context, io.Reader, io.Writer, io.Writer, string) error {
		testfs.Write(t, path, "version: 1\nname: deploy\ntargets:\n  production:\n    steps: [{return: {outputs: {}}}]\n")
		return nil
	}
	if err := newRootCmd(deps).ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestWorkflowTargetPickerErrorsPropagate(t *testing.T) {
	t.Parallel()
	want := errors.New("target picker failed")
	calls := 0
	deps := pickerDependencies(t.TempDir(), step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		calls++
		if calls == 1 {
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionTargets}, nil
		}
		return tui.Selection{}, want
	})
	command := newRootCmd(deps)
	command.SetContext(t.Context())
	navigation := workflowPickerNavigation{}
	_, err := navigation.selectWorkflow(command, deps, []workflow.Source{{Name: "deploy", Path: "/deploy.yaml", Target: "only"}}, workflowPickerState{}, workflowPickerSortName, "Workflows", "")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestWorkflowTargetPickerNonInteractiveKeepsTargetRows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, ".wuko", "workflows", "deploy.yaml"), targetPickerWorkflow)
	var output bytes.Buffer
	deps := listTestDependencies(root, &output)
	if err := newRootCmd(deps).ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\ttarget ") != 2 || !strings.Contains(output.String(), "target staging") || !strings.Contains(output.String(), "target production") {
		t.Fatalf("non-interactive output = %q", output.String())
	}
}

func TestWorkflowTargetPickerUsesExactShadowedSource(t *testing.T) {
	t.Parallel()
	for _, intent := range []tui.SelectionIntent{tui.SelectionPrimary, tui.SelectionAlternate} {
		t.Run(workflowTargetActionName(intent), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			local := filepath.Join(root, ".wuko", "workflows", "deploy.yaml")
			shadow := filepath.Join(root, "home", ".wuko", "workflows", "deploy.yaml")
			definition := `version: 1
name: deploy
targets:
  staging:
    steps:
      - id: deploy_staging
        type: shell
        with: {command: printf, args: [SOURCE-staging]}
  production:
    steps:
      - id: deploy_production
        type: shell
        with: {command: printf, args: [SOURCE-production]}
`
			testfs.Write(t, local, strings.ReplaceAll(definition, "SOURCE", "local"))
			testfs.Write(t, shadow, strings.ReplaceAll(definition, "SOURCE", "shadow"))
			registry := step.NewRegistry()
			if err := shell.Register(registry); err != nil {
				t.Fatal(err)
			}
			calls := 0
			deps := pickerDependencies(root, registry, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
				calls++
				for _, option := range config.Options {
					source := option.Value.(workflow.Source)
					if source.Path == shadow && (calls == 1 || source.Target == "staging") {
						if calls == 1 && (len(config.Options) != 2 || source.Target != "" || source.Effective) {
							t.Fatalf("shadowed workflow config = %#v", config)
						}
						return tui.Selection{Option: option, Intent: intent}, nil
					}
				}
				t.Fatalf("shadowed source missing: %#v", config)
				return tui.Selection{}, context.Canceled
			})
			var output bytes.Buffer
			deps.stdout = &output
			if err := newRootCmd(deps).ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := "shadow-staging"
			if intent == tui.SelectionAlternate {
				want = "wuko run --file " + shellQuote(shadow) + " staging\n"
			}
			if calls != 2 || !strings.Contains(output.String(), want) || strings.Contains(output.String(), "local-staging") || strings.Contains(output.String(), "shadow-production") {
				t.Fatalf("calls = %d, output = %q, want %q", calls, output.String(), want)
			}
		})
	}
}
