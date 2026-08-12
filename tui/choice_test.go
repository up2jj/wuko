package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChoiceModelMultipleSelection(t *testing.T) {
	model := newChoiceModel("Pick", []Option{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}, true, true)
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || len(model.result) != 1 || model.result[0] != 1 {
		t.Fatalf("result = %#v, command nil = %v", model.result, command == nil)
	}
}

func TestSelectionModelNavigationAndEnter(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}, {Label: "deploy"}})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(selectionModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(selectionModel)
	if command == nil || model.selected.Label != "deploy" || !model.done {
		t.Fatalf("selected = %#v, done = %v, command nil = %v", model.selected, model.done, command == nil)
	}
}

func TestSelectionModelFiltering(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build", Description: "local"}, {Label: "deploy", Description: "global"}})
	model.list.SetFilterText("global")
	if got := len(model.list.VisibleItems()); got != 1 {
		t.Fatalf("visible items = %d, want 1", got)
	}
	item, ok := model.list.SelectedItem().(listOption)
	if !ok || item.Label != "deploy" {
		t.Fatalf("selected item = %#v, want deploy", model.list.SelectedItem())
	}
}

func TestSelectionModelEscapeCancels(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if command == nil || !model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestSelectionModelViewIncludesDescription(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build", Description: "local • Build it • /tmp/build.yaml"}})
	if !strings.Contains(model.View().Content, "local") {
		t.Fatalf("view = %q", model.View().Content)
	}
}
