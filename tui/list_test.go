package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

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
		interactiveStyles.message.Render("Workflows"),
		interactiveStyles.selected.Render("build"),
		interactiveStyles.description.Render("local"),
	} {
		if !strings.Contains(view, styled) {
			t.Fatalf("view = %q, want styled content %q", view, styled)
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
