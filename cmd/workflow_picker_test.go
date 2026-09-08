package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
