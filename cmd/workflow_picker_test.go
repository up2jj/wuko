package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/keyvalue"
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
