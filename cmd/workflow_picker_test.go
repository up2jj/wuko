package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

// Picker state lives in the global values root beside workflow-managed stores, so a
// workflow must not be able to read or rewrite it by name.
func TestPickerStoreNameIsReservedFromWorkflows(t *testing.T) {
	dir := t.TempDir()
	if _, err := keyvalue.OpenWorkflowScoped(dir, dir, keyvalue.Global, workflowPickerStoreName); err == nil {
		t.Fatalf("a workflow can open the picker store %q", workflowPickerStoreName)
	}
}

func TestWorkflowPickerStatePersistsAndPrunesUnavailable(t *testing.T) {
	configDir := t.TempDir()
	available := filepath.Join(t.TempDir(), "available.yaml")
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	state := workflowPickerState{
		Pinned: []string{missing, available, available},
		Recent: []string{missing, available, missing},
		Sort:   workflowPickerSortRecent.String(),
	}
	if err := saveWorkflowPickerState(t.Context(), configDir, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadWorkflowPickerState(t.Context(), configDir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.reconcile([]workflow.Source{{Path: available, Invokable: true}}) {
		t.Fatal("expected stale picker state to be pruned")
	}
	if err := saveWorkflowPickerState(t.Context(), configDir, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadWorkflowPickerState(t.Context(), configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Pinned) != 1 || loaded.Pinned[0] != workflowPickerPath(available) {
		t.Fatalf("pinned = %#v", loaded.Pinned)
	}
	if len(loaded.Recent) != 1 || loaded.Recent[0] != workflowPickerPath(available) {
		t.Fatalf("recent = %#v", loaded.Recent)
	}
	if loaded.sortMode() != workflowPickerSortRecent {
		t.Fatalf("sort = %v, want recent", loaded.sortMode())
	}
}

func TestWorkflowPickerSortsPinnedAndRecentSources(t *testing.T) {
	root := t.TempDir()
	sources := []workflow.Source{
		{Name: "alpha", Path: filepath.Join(root, "alpha.yaml")},
		{Name: "bravo", Path: filepath.Join(root, "bravo.yaml")},
		{Name: "charlie", Path: filepath.Join(root, "charlie.yaml")},
		{Name: "delta", Path: filepath.Join(root, "delta.yaml")},
	}
	state := workflowPickerState{
		Pinned: []string{sources[1].Path},
		Recent: []string{sources[2].Path, sources[0].Path},
	}
	nameSorted := sortWorkflowSources(sources, state, workflowPickerSortName)
	if got := sourceNames(nameSorted); !strings.EqualFold(got, "bravo,alpha,charlie,delta") {
		t.Fatalf("name order = %q", got)
	}
	recentSorted := sortWorkflowSources(sources, state, workflowPickerSortRecent)
	if got := sourceNames(recentSorted); !strings.EqualFold(got, "bravo,charlie,alpha,delta") {
		t.Fatalf("recent order = %q", got)
	}
}

func TestWorkflowPickerStateCapsRecentHistory(t *testing.T) {
	state := workflowPickerState{}
	paths := make([]string, workflowPickerRecentMax+5)
	for index := range paths {
		paths[index] = filepath.Join(t.TempDir(), "workflow.yaml")
		state.markRecent(paths[index])
	}
	if len(state.Recent) != workflowPickerRecentMax {
		t.Fatalf("recent length = %d, want %d", len(state.Recent), workflowPickerRecentMax)
	}
	if state.Recent[0] != workflowPickerPath(paths[len(paths)-1]) {
		t.Fatalf("most recent = %q", state.Recent[0])
	}
}

func TestWorkflowPickerOptionShowsPlainTextPin(t *testing.T) {
	option := workflowPickerOptionWithState(workflow.Source{
		Name: "build", Scope: "local", Description: "Build", Path: "/project/build.yaml",
	}, true, false)
	if !strings.Contains(option.Description, "[pinned]") {
		t.Fatalf("description = %q", option.Description)
	}
	if strings.Contains(option.Description, "📌") {
		t.Fatalf("description contains pin emoji: %q", option.Description)
	}
}

func TestWorkflowPickerOptionShowsPackageVersion(t *testing.T) {
	option := workflowPickerOption(workflow.Source{
		Name: "release", Scope: "local", Description: "Publish", PackageVersion: "1.4.0",
		Path: "/project/release.yaml",
	})
	if want := "local • Publish • package 1.4.0"; option.Description != want {
		t.Fatalf("description = %q, want %q", option.Description, want)
	}
}

func TestWorkflowPickerOptionCarriesMarketplaceURL(t *testing.T) {
	option := workflowPickerOption(workflow.Source{
		Name: "release", Path: "/project/release/wuko.yaml", MarketplaceURL: "https://example.test/marketplace/",
	})
	if option.URL != "https://example.test/marketplace/" {
		t.Fatalf("option URL = %q", option.URL)
	}
}

func TestWorkflowPickerActionsAreContextualAndOrdered(t *testing.T) {
	source := workflow.Source{Name: "release", HasForm: false, MarketplaceURL: "https://example.test/marketplace/"}
	actions := workflowPickerActions(source, true, workflowPickerSortRecent)
	want := []struct {
		label    string
		intent   tui.SelectionIntent
		disabled bool
	}{
		{"Run", tui.SelectionPrimary, false},
		{"Open form", tui.SelectionUI, true},
		{"Validate", tui.SelectionValidate, false},
		{"Show tree", tui.SelectionTree, false},
		{"Dry run", tui.SelectionDryRun, false},
		{"Print command", tui.SelectionAlternate, false},
		{"Edit workflow", tui.SelectionEditor, false},
		{"Unpin workflow", tui.SelectionTogglePin, false},
		{"Sort by name", tui.SelectionToggleSort, false},
		{"Open marketplace", tui.SelectionMarketplace, false},
		{"Reinstall", tui.SelectionReinstall, false},
	}
	if len(actions) != len(want) {
		t.Fatalf("actions = %#v", actions)
	}
	for index, expected := range want {
		if actions[index].Label != expected.label || actions[index].Intent != expected.intent || actions[index].Disabled != expected.disabled {
			t.Fatalf("action %d = %#v, want %#v", index, actions[index], expected)
		}
	}
	if actions[1].DisabledReason == "" {
		t.Fatal("disabled form action has no reason")
	}

	plain := workflowPickerActions(workflow.Source{Name: "build", HasForm: true}, false, workflowPickerSortName)
	if len(plain) != 9 || plain[1].Disabled || plain[7].Label != "Pin workflow" || plain[8].Label != "Sort by recent" {
		t.Fatalf("plain actions = %#v", plain)
	}
}

func TestWorkflowPickerStateRejectsMalformedState(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "wuko", "values", "picker.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"state": [}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWorkflowPickerState(t.Context(), configDir); err == nil {
		t.Fatal("expected malformed picker state error")
	}
}

func TestWorkflowPickerInspectionReturnsToFilterWithoutMarkingRecent(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workflowDir, "release.yaml")
	writeWorkflowData(t, path, "version: 1\nname: release\nsteps:\n  - return: {outputs: {}}\n")
	configDir := filepath.Join(root, "config")
	var selections int
	var viewed tui.TextViewerConfig
	deps := dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return configDir, nil }, registry: step.NewRegistry(), isInteractive: func(io.Reader) bool { return true },
		selectWorkflow: func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
			selections++
			if selections == 1 {
				if len(config.Options) != 1 || config.InitialFilter != "" {
					t.Fatalf("first picker config = %#v", config)
				}
				return tui.Selection{Option: config.Options[0], Intent: tui.SelectionValidate, Filter: "rel"}, nil
			}
			if config.InitialFilter != "rel" || !config.Options[0].Default {
				t.Fatalf("restored picker config = %#v", config)
			}
			return tui.Selection{}, context.Canceled
		},
		viewText: func(_ context.Context, _ io.Reader, _ io.Writer, config tui.TextViewerConfig) error {
			viewed = config
			return nil
		},
	}
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if selections != 2 || viewed.Failed || !strings.Contains(viewed.Title, "release") || !strings.Contains(viewed.Content, "release: valid") {
		t.Fatalf("selections = %d, viewed = %#v", selections, viewed)
	}
	state, err := loadWorkflowPickerState(t.Context(), configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Recent) != 0 {
		t.Fatalf("inspection changed recent history: %#v", state.Recent)
	}
}

func TestWorkflowPickerReturnDestinationReopensAfterRun(t *testing.T) {
	root := t.TempDir()
	path := writePickerWorkflow(t, root, `version: 1
name: return-to-picker
description: Original
steps:
  - return: {to: picker, outputs: {}}
`)
	var selections int
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary, Filter: "return"}, nil
		}
		if config.InitialFilter != "return" || !config.Options[0].Default || config.Options[0].Value.(workflow.Source).Path != path {
			t.Fatalf("restored picker config = %#v", config)
		}
		return tui.Selection{}, context.Canceled
	})
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if selections != 2 {
		t.Fatalf("selections = %d, want 2", selections)
	}
}

func TestWorkflowPickerOrdinaryReturnStillExits(t *testing.T) {
	root := t.TempDir()
	writePickerWorkflow(t, root, "version: 1\nname: ordinary\nsteps:\n  - return: {outputs: {}}\n")
	selections := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections > 1 {
			t.Fatal("ordinary return reopened the picker")
		}
		return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
	})
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPickerReturnDestinationHonorsFailureBoundary(t *testing.T) {
	failure := errors.New("picker test failed")
	for _, test := range []struct {
		name       string
		definition string
		wantReopen bool
	}{
		{name: "failure before return exits", definition: "version: 1\nname: before\nsteps:\n  - {id: fail, type: picker_test}\n  - return: {to: picker, outputs: {}}\n"},
		{name: "cleanup failure after return reopens", wantReopen: true, definition: "version: 1\nname: after\nsteps:\n  - return: {to: picker, outputs: {}}\nfinally:\n  - {id: fail, type: picker_test}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePickerWorkflow(t, root, test.definition)
			registry := step.NewRegistry()
			if err := registry.Register("picker_test", func(map[string]any) (step.Runner, error) {
				return pickerTestRunner{run: func() error { return failure }}, nil
			}); err != nil {
				t.Fatal(err)
			}
			selections := 0
			deps := pickerDependencies(root, registry, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
				selections++
				if selections == 1 {
					return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
				}
				if !test.wantReopen || !strings.Contains(config.Notice, failure.Error()) {
					t.Fatalf("reopened picker config = %#v", config)
				}
				return tui.Selection{}, context.Canceled
			})
			command := newRootCmd(deps)
			command.SetArgs(nil)
			err := command.ExecuteContext(t.Context())
			if test.wantReopen {
				if err != nil || selections != 2 {
					t.Fatalf("error = %v, selections = %d", err, selections)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), failure.Error()) || selections != 1 {
				t.Fatalf("error = %v, selections = %d", err, selections)
			}
		})
	}
}

func TestWorkflowPickerReturnDestinationRefreshesDiscovery(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(string) error
		wantDesc   string
		wantNotice string
		wantEmpty  bool
	}{
		{name: "rediscovery updates workflow", wantDesc: "Updated", mutate: func(path string) error {
			return os.WriteFile(path, []byte("version: 1\nname: refreshed\ndescription: Updated\nsteps:\n  - return: {to: picker, outputs: {}}\n"), 0o644)
		}},
		{name: "failed rediscovery keeps snapshot", wantDesc: "Original", wantNotice: "refresh failed:", mutate: func(path string) error {
			return os.WriteFile(path, []byte("invalid: true\n"), 0o644)
		}},
		{name: "empty rediscovery exits", wantEmpty: true, mutate: os.Remove},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := writePickerWorkflow(t, root, "version: 1\nname: refreshed\ndescription: Original\nsteps:\n  - {id: mutate, type: picker_mutate}\n  - return: {to: picker, outputs: {}}\n")
			registry := step.NewRegistry()
			if err := registry.Register("picker_mutate", func(map[string]any) (step.Runner, error) {
				return pickerTestRunner{run: func() error { return test.mutate(path) }}, nil
			}); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			selections := 0
			deps := pickerDependencies(root, registry, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
				selections++
				if selections == 1 {
					return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary, Filter: "ref"}, nil
				}
				if test.wantEmpty || config.InitialFilter != "ref" || !config.Options[0].Default || !strings.Contains(config.Options[0].Description, test.wantDesc) || !strings.Contains(config.Notice, test.wantNotice) {
					t.Fatalf("refreshed picker config = %#v", config)
				}
				return tui.Selection{}, context.Canceled
			})
			deps.stdout = &output
			command := newRootCmd(deps)
			command.SetArgs(nil)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if test.wantEmpty {
				if selections != 1 || !strings.Contains(output.String(), "No workflows found") {
					t.Fatalf("selections = %d, output = %q", selections, output.String())
				}
			} else if selections != 2 {
				t.Fatalf("selections = %d, want 2", selections)
			}
		})
	}
}

func TestWorkflowPickerDependencyReturnDestinationStaysLocal(t *testing.T) {
	root := t.TempDir()
	workflowDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowData(t, filepath.Join(workflowDir, "child.yaml"), "version: 1\nname: child\ninvokable: false\nsteps:\n  - return: {to: picker, outputs: {}}\n")
	writeWorkflowData(t, filepath.Join(workflowDir, "root.yaml"), "version: 1\nname: root\ndepends_on: {child: child}\nsteps:\n  - return: {outputs: {}}\n")
	selections := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections > 1 {
			t.Fatal("dependency return reopened the picker")
		}
		return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
	})
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPickerReturnDestinationCancellationExits(t *testing.T) {
	root := t.TempDir()
	writePickerWorkflow(t, root, "version: 1\nname: canceled\nsteps:\n  - return: {to: picker, outputs: {}}\nfinally:\n  - {id: cancel, type: picker_cancel}\n")
	ctx, cancel := context.WithCancel(t.Context())
	registry := step.NewRegistry()
	if err := registry.Register("picker_cancel", func(map[string]any) (step.Runner, error) {
		return pickerTestRunner{run: func() error { cancel(); return nil }}, nil
	}); err != nil {
		t.Fatal(err)
	}
	selections := 0
	deps := pickerDependencies(root, registry, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
	})
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(ctx); !errors.Is(err, context.Canceled) || selections != 1 {
		t.Fatalf("error = %v, selections = %d", err, selections)
	}
}

func TestWorkflowPickerScheduledReturnDestinationStopsAfterOccurrence(t *testing.T) {
	root := t.TempDir()
	writePickerWorkflow(t, root, "version: 1\nname: scheduled-return\ncron: '* * * * * *'\ntimezone: UTC\nsteps:\n  - return: {to: picker, outputs: {}}\n")
	selections := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
		}
		return tui.Selection{}, context.Canceled
	})
	deps.now = func() time.Time { return time.Date(2026, time.September, 9, 12, 0, 0, 500_000_000, time.UTC) }
	deps.waitUntil = func(context.Context, time.Time) error {
		t.Fatal("scheduled picker run waited after its return destination triggered")
		return nil
	}
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if selections != 2 {
		t.Fatalf("selections = %d, want 2", selections)
	}
}

// A failed occurrence keeps its schedule under the picker exactly as it does under a direct
// scheduled run: only the return destination ends the loop and reopens the picker.
func TestWorkflowPickerScheduledFailureKeepsSchedule(t *testing.T) {
	root := t.TempDir()
	writePickerWorkflow(t, root, "version: 1\nname: scheduled-flaky\ncron: '* * * * * *'\ntimezone: UTC\nsteps:\n  - {id: flaky, type: picker_test}\n  - return: {to: picker, outputs: {}}\n")
	registry := step.NewRegistry()
	attempts := 0
	if err := registry.Register("picker_test", func(map[string]any) (step.Runner, error) {
		return pickerTestRunner{run: func() error {
			attempts++
			if attempts == 1 {
				return errors.New("occurrence failed")
			}
			return nil
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	selections := 0
	deps := pickerDependencies(root, registry, func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionPrimary}, nil
		}
		return tui.Selection{}, context.Canceled
	})
	now := time.Date(2026, time.September, 9, 12, 0, 0, 500_000_000, time.UTC)
	waits := 0
	deps.now = func() time.Time { return now }
	deps.waitUntil = func(_ context.Context, instant time.Time) error {
		waits++
		now = instant
		return nil
	}
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || waits != 1 || selections != 2 {
		t.Fatalf("attempts = %d, waits = %d, selections = %d", attempts, waits, selections)
	}
}

func TestWorkflowPickerReturnDestinationReopensAfterFormRun(t *testing.T) {
	root := t.TempDir()
	writePickerWorkflow(t, root, `version: 1
name: form-return
vars: {confirmed: false}
form:
  title: Return
  fields:
    - {variable: confirmed, label: Confirmed, type: boolean}
steps:
  - return: {to: picker, outputs: {}}
`)
	opener, clientErr := workflowFormOpener(t)
	selections := 0
	deps := pickerDependencies(root, step.NewRegistry(), func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
		selections++
		if selections == 1 {
			return tui.Selection{Option: config.Options[0], Intent: tui.SelectionUI, Filter: "form"}, nil
		}
		if config.InitialFilter != "form" || !config.Options[0].Default {
			t.Fatalf("restored picker config = %#v", config)
		}
		return tui.Selection{}, context.Canceled
	})
	deps.openURL = opener
	command := newRootCmd(deps)
	command.SetArgs(nil)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-clientErr; err != nil {
		t.Fatal(err)
	}
	if selections != 2 {
		t.Fatalf("selections = %d, want 2", selections)
	}
}

func TestDirectWorkflowCommandsIgnoreReturnDestination(t *testing.T) {
	root := t.TempDir()
	path := writePickerWorkflow(t, root, `version: 1
name: direct-return
vars: {confirmed: false}
form:
  title: Return
  fields:
    - {variable: confirmed, label: Confirmed, type: boolean}
steps:
  - return: {to: picker, outputs: {}}
`)
	for _, test := range []struct {
		name string
		args []string
		ui   bool
	}{
		{name: "run", args: []string{"run", "--file", path}},
		{name: "ui", args: []string{"ui", "--file", path}, ui: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := pickerDependencies(root, step.NewRegistry(), nil)
			var clientErr <-chan error
			if test.ui {
				deps.openURL, clientErr = workflowFormOpener(t)
			}
			command := newRootCmd(deps)
			command.SetArgs(test.args)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if test.ui {
				if err := <-clientErr; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestWorkflowPickerInspectionsUseExactShadowedPathAndTarget(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	path := filepath.Join(home, ".wuko", "workflows", "release.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowData(t, path, `version: 1
name: release
targets:
  production:
    steps:
      - id: global_production
        type: shell
        with: {script: "printf global"}
`)
	localDir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkflowData(t, filepath.Join(localDir, "release.yaml"), "invalid: true\n")
	registry := step.NewRegistry()
	if err := shell.Register(registry); err != nil {
		t.Fatal(err)
	}
	deps := dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return home, nil }, configDir: func() (string, error) { return configDir, nil },
		registry: registry,
	}
	command := newRootCmd(deps)
	command.SetContext(t.Context())
	source := workflow.Source{Name: "release", Target: "production", Path: path, Effective: false}
	for _, test := range []struct {
		intent tui.SelectionIntent
		want   string
	}{
		{tui.SelectionValidate, "release (production): valid"},
		{tui.SelectionTree, "global_production"},
		{tui.SelectionDryRun, "global_production"},
	} {
		title, content, failed := runWorkflowPickerInspection(command, deps, source, test.intent)
		if failed || !strings.Contains(title, "release production") || !strings.Contains(content, test.want) {
			t.Fatalf("intent %v title = %q, failed = %v, content = %q", test.intent, title, failed, content)
		}
	}
}

func TestWorkflowPickerRefreshesAfterEditorAndKeepsSnapshotOnFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		intent      tui.SelectionIntent
		replacement string
		wantDesc    string
		wantNotice  string
	}{
		{name: "refreshes valid edit", intent: tui.SelectionEditor, replacement: "version: 1\nname: release\ndescription: Updated\nsteps:\n  - return: {outputs: {}}\n", wantDesc: "Updated"},
		{name: "refreshes successful reinstall", intent: tui.SelectionReinstall, replacement: "version: 1\nname: release\ndescription: Reinstalled\nsteps:\n  - return: {outputs: {}}\n", wantDesc: "Reinstalled"},
		{name: "keeps snapshot after invalid edit", intent: tui.SelectionEditor, replacement: "invalid: true\n", wantDesc: "Original", wantNotice: "refresh failed:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			workflowDir := filepath.Join(root, ".wuko", "workflows")
			if err := os.MkdirAll(workflowDir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workflowDir, "release.yaml")
			writeWorkflowData(t, path, "version: 1\nname: release\ndescription: Original\nsteps:\n  - return: {outputs: {}}\n")
			var selections int
			deps := dependencies{
				stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
				cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
				configDir: func() (string, error) { return filepath.Join(root, "config"), nil }, registry: step.NewRegistry(), isInteractive: func(io.Reader) bool { return true },
				openEditor: func(context.Context, io.Reader, io.Writer, io.Writer, string) error {
					return os.WriteFile(path, []byte(test.replacement), 0o644)
				},
				reinstallWorkflow: func(*cobra.Command, workflow.Source) error {
					return os.WriteFile(path, []byte(test.replacement), 0o644)
				},
				selectWorkflow: func(_ context.Context, _ io.Reader, _ io.Writer, config tui.SelectionPickerConfig) (tui.Selection, error) {
					selections++
					if selections == 1 {
						return tui.Selection{Option: config.Options[0], Intent: test.intent, Filter: "rel"}, nil
					}
					if config.InitialFilter != "rel" || !strings.Contains(config.Options[0].Description, test.wantDesc) || !strings.Contains(config.Notice, test.wantNotice) {
						t.Fatalf("refreshed picker config = %#v", config)
					}
					return tui.Selection{}, context.Canceled
				},
			}
			command := newRootCmd(deps)
			command.SetArgs(nil)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if selections != 2 {
				t.Fatalf("selections = %d, want 2", selections)
			}
		})
	}
}

func TestOpenWorkflowEditorUsesVisual(t *testing.T) {
	var output bytes.Buffer
	editor := openWorkflowEditor(func(name string) string {
		if name == "VISUAL" {
			return "/bin/echo"
		}
		return "should-not-be-used"
	})
	path := "/tmp/workflow.yaml"
	if err := editor(t.Context(), strings.NewReader(""), &output, io.Discard, path); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != path {
		t.Fatalf("editor output = %q", output.String())
	}
}

func sourceNames(sources []workflow.Source) string {
	names := make([]string, len(sources))
	for index, source := range sources {
		names[index] = source.Name
	}
	return strings.Join(names, ",")
}

func writePickerWorkflow(t *testing.T, root, data string) string {
	t.Helper()
	dir := filepath.Join(root, ".wuko", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workflow.yaml")
	writeWorkflowData(t, path, data)
	return path
}

func pickerDependencies(root string, registry *step.Registry, selectWorkflow func(context.Context, io.Reader, io.Writer, tui.SelectionPickerConfig) (tui.Selection, error)) dependencies {
	return dependencies{
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
		cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return filepath.Join(root, "home"), nil },
		configDir: func() (string, error) { return filepath.Join(root, "config"), nil }, registry: registry,
		isInteractive: func(io.Reader) bool { return true }, selectWorkflow: selectWorkflow,
	}
}

func workflowFormOpener(t *testing.T) (func(string) error, <-chan error) {
	t.Helper()
	clientErr := make(chan error, 1)
	opener := func(target string) error {
		go func() {
			client := &http.Client{Timeout: 5 * time.Second}
			response, err := client.Get(target)
			if err != nil {
				clientErr <- err
				return
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				clientErr <- readErr
				return
			}
			match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
			if len(match) != 2 {
				clientErr <- fmt.Errorf("csrf token not found")
				return
			}
			values := url.Values{"csrf": {match[1]}, "field_0": {"true"}}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target+"submit", strings.NewReader(values.Encode()))
			if err != nil {
				clientErr <- err
				return
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", "null")
			response, err = client.Do(request)
			if err != nil {
				clientErr <- err
				return
			}
			_ = response.Body.Close()
			for range 100 {
				response, err = client.Get(target)
				if err != nil {
					clientErr <- err
					return
				}
				body, readErr = io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil {
					clientErr <- readErr
					return
				}
				if strings.Contains(string(body), "Workflow complete") {
					clientErr <- nil
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			clientErr <- fmt.Errorf("workflow result page was not served")
		}()
		return nil
	}
	return opener, clientErr
}

type pickerTestRunner struct {
	run func() error
}

func (runner pickerTestRunner) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, runner.run()
}
