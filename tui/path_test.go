package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPathPickerShowsContextualShortcuts(t *testing.T) {
	root := pathPickerTree(t)
	single := mustPathModel(t, PathPickerConfig{Message: "Select", Root: root, Kind: "file", Required: true})
	for _, shortcut := range []string{"↑/↓ move", "→ open", "← back", "enter select", "/ filter", "ctrl+h hidden", "esc cancel"} {
		if !strings.Contains(single.View().Content, shortcut) {
			t.Fatalf("single view = %q, want %q", single.View().Content, shortcut)
		}
	}

	multiple := mustPathModel(t, PathPickerConfig{Message: "Select", Root: root, Kind: "file", Multiple: true, Required: true})
	for _, shortcut := range []string{"space toggle", "enter confirm", "esc cancel"} {
		if !strings.Contains(multiple.View().Content, shortcut) {
			t.Fatalf("multiple view = %q, want %q", multiple.View().Content, shortcut)
		}
	}

	updated, _ := single.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	filtering := updated.(pathPickerModel)
	for _, shortcut := range []string{"type filter", "enter apply", "esc clear", "ctrl+c cancel"} {
		if !strings.Contains(filtering.View().Content, shortcut) {
			t.Fatalf("filter view = %q, want %q", filtering.View().Content, shortcut)
		}
	}
}

func TestPathPickerStylesSelectedEntriesAndEntryKinds(t *testing.T) {
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: pathPickerTree(t), Kind: "file", Required: true})
	setPathCursor(t, &model, "a.go")
	view := model.View().Content

	for _, styled := range []string{
		pathStyles.message.Render("Select"),
		pathStyles.selected.Render("a.go"),
		pathStyles.directory.Render("nested/"),
	} {
		if !strings.Contains(view, styled) {
			t.Fatalf("view = %q, want styled content %q", view, styled)
		}
	}
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("view = %q, want ANSI color styling", view)
	}
}

func TestPathPickerWrapsButRetainsNarrowHelp(t *testing.T) {
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: pathPickerTree(t), Kind: "file", Required: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 12, Height: 30})
	view := updated.(pathPickerModel).View().Content
	for _, shortcut := range []string{"← back", "enter select", "esc cancel"} {
		if !strings.Contains(view, shortcut) {
			t.Fatalf("view = %q, want %q", view, shortcut)
		}
	}
	if strings.Count(view, "\n") < 8 {
		t.Fatalf("help did not wrap: %q", view)
	}
}

func TestPathPickerNavigatesAndSelects(t *testing.T) {
	root := pathPickerTree(t)
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: root, Kind: "file", Required: true})
	setPathCursor(t, &model, "nested")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model = updated.(pathPickerModel)
	if filepath.Base(model.current) != "nested" {
		t.Fatalf("current = %q", model.current)
	}
	setPathCursor(t, &model, "nested.go")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(pathPickerModel)
	if command == nil || !model.done || !slices.Equal(model.result, []string{"nested/nested.go"}) {
		t.Fatalf("result = %#v, done = %v, command nil = %v", model.result, model.done, command == nil)
	}
}

func TestPathPickerDirectorySelectionAndRootBoundary(t *testing.T) {
	root := pathPickerTree(t)
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: root, Kind: "directory", Required: true})
	setPathCursor(t, &model, "nested")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected := updated.(pathPickerModel)
	if command == nil || !slices.Equal(selected.result, []string{"nested"}) {
		t.Fatalf("result = %#v, command nil = %v", selected.result, command == nil)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	model = updated.(pathPickerModel)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if model.current != canonicalRoot {
		t.Fatalf("navigated above root to %q", model.current)
	}
	setPathCursor(t, &model, ".")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(pathPickerModel).result; !slices.Equal(got, []string{"."}) {
		t.Fatalf("result = %#v", got)
	}
}

func TestPathPickerMultipleSelectionPreservesSelectionOrder(t *testing.T) {
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: pathPickerTree(t), Kind: "file", Multiple: true, Required: true})
	setPathCursor(t, &model, "z.txt")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(pathPickerModel)
	setPathCursor(t, &model, "a.go")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(pathPickerModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(pathPickerModel)
	if command == nil || !slices.Equal(model.result, []string{"z.txt", "a.go"}) {
		t.Fatalf("result = %#v, command nil = %v", model.result, command == nil)
	}
}

func TestPathPickerHiddenAndFuzzyFiltering(t *testing.T) {
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: pathPickerTree(t), Kind: "file", Required: true})
	if pathEntryIndex(model.entries, ".hidden.go") >= 0 {
		t.Fatal("hidden entry is initially visible")
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	model = updated.(pathPickerModel)
	if pathEntryIndex(model.entries, ".hidden.go") < 0 {
		t.Fatal("hidden entry was not shown")
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	model = updated.(pathPickerModel)
	for _, character := range "ag" {
		updated, _ = model.Update(tea.KeyPressMsg{Code: character, Text: string(character)})
		model = updated.(pathPickerModel)
	}
	if len(model.visible) != 1 || model.visible[0].name != "a.go" {
		t.Fatalf("visible = %#v", model.visible)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(pathPickerModel)
	if model.filtering || model.filter.Value() != "" {
		t.Fatalf("filtering = %v, filter = %q", model.filtering, model.filter.Value())
	}
}

func TestPathPickerPatternAndEscapedSymlinkEntriesAreDisabled(t *testing.T) {
	root := pathPickerTree(t)
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside.go")); err != nil {
		t.Fatal(err)
	}
	model := mustPathModel(t, PathPickerConfig{
		Message: "Select", Root: root, Kind: "file", Required: true, Patterns: []string{"**/*.go"}, ShowHidden: true,
	})
	text := model.entries[pathEntryIndex(model.entries, "z.txt")]
	if text.selectable || !strings.Contains(text.description, "filtered") {
		t.Fatalf("text entry = %#v", text)
	}
	escaped := model.entries[pathEntryIndex(model.entries, "outside.go")]
	if escaped.selectable || !strings.Contains(escaped.description, "outside root") {
		t.Fatalf("escaped entry = %#v", escaped)
	}
}

func TestPathPickerOptionalAndCancellation(t *testing.T) {
	model := mustPathModel(t, PathPickerConfig{Message: "Select", Root: pathPickerTree(t), Kind: "file", Required: false})
	setPathCursor(t, &model, "(none)")
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(pathPickerModel).result; !slices.Equal(got, []string{""}) {
		t.Fatalf("result = %#v", got)
	}

	model = mustPathModel(t, PathPickerConfig{Message: "Select", Root: pathPickerTree(t), Kind: "file", Required: true})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	cancelled := updated.(pathPickerModel)
	if command == nil || !cancelled.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", cancelled.cancelled, command == nil)
	}
}

func pathPickerTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range map[string]bool{"a.go": true, "z.txt": true, ".hidden.go": true, "nested/nested.go": true} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustPathModel(t *testing.T, config PathPickerConfig) pathPickerModel {
	t.Helper()
	model, err := newPathPickerModel(config)
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func setPathCursor(t *testing.T, model *pathPickerModel, name string) {
	t.Helper()
	index := pathEntryIndex(model.visible, name)
	if index < 0 {
		t.Fatalf("entry %q not found in %#v", name, model.visible)
	}
	model.cursor = index
}

func pathEntryIndex(entries []pathEntry, name string) int {
	return slices.IndexFunc(entries, func(entry pathEntry) bool { return entry.name == name })
}
