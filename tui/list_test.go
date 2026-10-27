package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectionPickersUseExplicitRunAndCancelKeys(t *testing.T) {
	t.Parallel()
	options := []Option{{Label: "build", Default: true}}
	for name, newModel := range map[string]func() tea.Model{
		"workflow":    func() tea.Model { return newSelectionModel("Workflows", options) },
		"marketplace": func() tea.Model { return newMultiSelectionModel("Marketplace", options) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := newModel()
			for _, key := range []tea.KeyPressMsg{{Code: 'v'}, {Code: 'q'}, {Code: tea.KeyEscape}} {
				updated, command := model.Update(key)
				model = updated
				if command != nil || model.View().Content == "" {
					t.Fatalf("%s unexpectedly exited the picker", key.String())
				}
			}
			for _, expanded := range []bool{false, true} {
				if expanded {
					model, _ = model.Update(tea.KeyPressMsg{Code: '?'})
				}
				view := ansi.Strip(model.View().Content)
				if strings.Contains(view, "v select") || strings.Contains(view, "q quit") {
					t.Fatalf("expanded=%t: inherited quit help remains: %s", expanded, view)
				}
				if !strings.Contains(view, "ctrl+c cancel") {
					t.Fatalf("expanded=%t: cancellation help is missing: %s", expanded, view)
				}
			}
			for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 'c', Mod: tea.ModCtrl}} {
				updated, command := newModel().Update(key)
				if command == nil || updated.View().Content != "" {
					t.Fatalf("%s did not finish the picker", key.String())
				}
				if _, quit := command().(tea.QuitMsg); !quit {
					t.Fatalf("%s did not quit the picker", key.String())
				}
			}
		})
	}
}

func TestMultiSelectionModelTogglesAndConfirmsInManifestOrder(t *testing.T) {
	model := newMultiSelectionModel("Marketplace", []Option{{Label: "A"}, {Label: "B"}, {Label: "C"}})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(multiSelectionModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(multiSelectionModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(multiSelectionModel)
	if command == nil || !model.done || !slices.Equal(model.selectedIndexes(), []int{1}) {
		t.Fatalf("selected = %#v, done = %v, command nil = %v", model.selectedIndexes(), model.done, command == nil)
	}
}

func TestSelectionModelUsesSharedPickerStyles(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build", Description: "local"}})
	view := model.View().Content
	for _, styled := range []string{
		model.styles.message.Render("Workflows"),
		model.styles.selected.Background(model.styles.selection.GetBackground()).Width(model.width).Render("› build"),
		model.styles.description.Background(model.styles.selection.GetBackground()).Width(model.width).Render("  local"),
	} {
		if !strings.Contains(view, styled) {
			t.Fatalf("view = %q, want styled content %q", view, styled)
		}
	}
}

func TestSelectionModelIgnoresUnavailableEditorShortcut(t *testing.T) {
	for _, actions := range [][]SelectionAction{
		{{Label: "Run", Intent: SelectionPrimary}},
		{{Label: "Edit", Intent: SelectionEditor, Disabled: true, DisabledReason: "read only"}},
	} {
		model := newSelectionModelWithConfig(SelectionPickerConfig{Title: "Workflows", InitialFilter: "build", Options: []Option{{Label: "build", Actions: actions}}})
		updated, command := model.Update(tea.KeyPressMsg{Code: 'e'})
		model = updated.(selectionModel)
		if command != nil || model.done || model.cancelled || model.list.FilterValue() != "build" {
			t.Fatalf("unavailable editor shortcut changed picker: done=%t, cancelled=%t, filter=%q", model.done, model.cancelled, model.list.FilterValue())
		}
	}
}

func TestMultiSelectionModelBulkActionsRespectFilterAndPickerStyle(t *testing.T) {
	model := newMultiSelectionModel("Marketplace", []Option{{Label: "A", Description: "visible"}, {Label: "B", Description: "hidden"}, {Label: "C", Description: "visible"}})
	model.list.SetFilterText("visible")
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	model = updated.(multiSelectionModel)
	if !slices.Equal(model.selectedIndexes(), []int{0, 2}) {
		t.Fatalf("selected after ctrl+a = %#v", model.selectedIndexes())
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model = updated.(multiSelectionModel)
	if len(model.selectedIndexes()) != 0 {
		t.Fatalf("selected after ctrl+x = %#v", model.selectedIndexes())
	}
	view := model.View().Content
	for _, want := range []string{"Marketplace", "[ ] A", "visible", "filter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
}
