package cmd

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/transporttest"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func githubPickerDependencies(t *testing.T, root, definition string, selectWorkflow func(context.Context, io.Reader, io.Writer, tui.SelectionPickerConfig) (tui.Selection, error)) dependencies {
	t.Helper()
	t.Setenv("GH_TOKEN", "test-token")
	tarball := commandGitHubTarballEntries(t, map[string][]byte{"build/wuko.yaml": []byte(definition)})
	client := &http.Client{Transport: transporttest.RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(tarball)), Header: make(http.Header)}, nil
	})}
	deps := pickerDependencies(root, step.NewRegistry(), selectWorkflow)
	deps.loader = workflow.NewLoader(client)
	return deps
}

func TestGitHubWorkflowPickerEditorKeepsSelection(t *testing.T) {
	selections := 0
	var output bytes.Buffer
	deps := githubPickerDependencies(t, t.TempDir(), "version: 1\nname: build\nsteps:\n  - return: {outputs: {}}\n", func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionEditor, Filter: "build"}, nil
		}
		if config.InitialFilter != "build" || !config.Options[0].Default || !strings.Contains(config.Notice, "cannot be edited") {
			t.Fatalf("restored picker config = %#v", config)
		}
		return tui.Selection{}, context.Canceled
	})
	deps.stdout = &output
	deps.openEditor = func(context.Context, io.Reader, io.Writer, io.Writer, string) error {
		t.Fatal("remote workflow opened in editor")
		return nil
	}
	command := newRootCmd(deps)
	command.SetArgs([]string{"run", "--pick", "github:acme/automation@main"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if selections != 2 || output.Len() != 0 {
		t.Fatalf("selections = %d, output = %q, want two selections and no printed command", selections, output.String())
	}
}

func TestGitHubWorkflowPickerRemembersSuccessfulFormRun(t *testing.T) {
	root := t.TempDir()
	selections := 0
	var locator string
	deps := githubPickerDependencies(t, root, `version: 1
name: build
vars: {confirmed: false}
form:
  title: Build
  fields:
    - {variable: confirmed, label: Confirmed, type: boolean}
steps:
  - return: {to: picker, outputs: {}}
`, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			locator = config.Options[0].Value.(workflow.Source).Locator
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionUI, Filter: "build"}, nil
		}
		if config.InitialFilter != "build" || !config.Options[0].Default {
			t.Fatalf("restored picker config = %#v", config)
		}
		return tui.Selection{}, context.Canceled
	})
	opener, clientErr := workflowFormOpener(t)
	deps.openURL = opener
	command := newRootCmd(deps)
	command.SetArgs([]string{"run", "--pick", "github:acme/automation@main"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-clientErr; err != nil {
		t.Fatal(err)
	}
	if selections != 2 {
		t.Fatalf("selections = %d, want 2", selections)
	}
	state, err := loadWorkflowPickerState(t.Context(), filepath.Join(root, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if locator == "" || !slices.Equal(state.Recent, []string{locator}) {
		t.Fatalf("recent = %#v, want successful form run %q", state.Recent, locator)
	}
}

func TestGitHubWorkflowPickerSelectsTargets(t *testing.T) {
	for _, intent := range []tui.SelectionIntent{tui.SelectionPrimary, tui.SelectionAlternate} {
		t.Run(workflowTargetActionName(intent), func(t *testing.T) {
			calls := 0
			var locator string
			var output bytes.Buffer
			deps := githubPickerDependencies(t, t.TempDir(), targetPickerWorkflow, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
				calls++
				switch calls {
				case 1:
					if len(config.Options) != 1 || config.Options[0].Label != "deploy" {
						t.Fatalf("GitHub workflow config = %#v", config)
					}
					return tui.Selection{Option: config.Options[0], Intent: intent, Filter: "dep"}, nil
				case 2:
					if !config.AllowBack || len(config.Options) != 2 || config.Options[1].Label != "staging" {
						t.Fatalf("GitHub target config = %#v", config)
					}
					source := config.Options[1].Value.(workflow.Source)
					locator = source.Locator
					for _, action := range config.Options[1].Actions {
						if action.Intent == tui.SelectionEditor {
							t.Fatal("remote target offers editing")
						}
					}
					return tui.Selection{Option: config.Options[1], Intent: config.EnterIntent}, nil
				case 3:
					if config.AllowBack || config.InitialFilter != "dep" || !config.Options[0].Default {
						t.Fatalf("GitHub return config = %#v", config)
					}
					return tui.Selection{}, context.Canceled
				default:
					t.Fatal("unexpected GitHub picker call")
					return tui.Selection{}, context.Canceled
				}
			})
			deps.stdout = &output
			command := newRootCmd(deps)
			command.SetArgs([]string{"run", "--pick", "github:acme/automation@main"})
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if intent == tui.SelectionAlternate {
				if calls != 2 || output.String() != "wuko run "+shellQuote(locator)+" staging\n" {
					t.Fatalf("calls = %d, command = %q", calls, output.String())
				}
			} else if calls != 3 {
				t.Fatalf("calls = %d, want root, targets, root", calls)
			}
		})
	}
}
