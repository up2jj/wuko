package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChoiceModelMultipleSelection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}},
		Multiple: true, Required: true,
	})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || !slices.Equal(model.result, []int{1, 0}) {
		t.Fatalf("result = %#v, command nil = %v", model.result, command == nil)
	}
}

func TestChoiceModelOptionalSingleSelection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A", Value: "a"}}, Required: false,
	})
	if len(model.visible) != 2 || !model.visible[0].none {
		t.Fatalf("visible = %#v", model.visible)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || !model.done || len(model.result) != 0 {
		t.Fatalf("result = %#v, done = %v, command nil = %v", model.result, model.done, command == nil)
	}
}

func TestChoiceModelFiltersDescriptions(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{Message: "Pick", Options: []Option{
		{Label: "A", Description: "primary"}, {Label: "B", Description: "secondary"},
	}, Required: true})
	model.filter.SetValue("secondary")
	model.refreshVisible()
	if len(model.visible) != 1 || model.visible[0].label != "B" {
		t.Fatalf("visible = %#v", model.visible)
	}
	view := model.View().Content
	for _, want := range []string{"B", "secondary", "/ filter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
}

func TestChoiceModelPaginatesAndWrapsHelp(t *testing.T) {
	options := make([]Option, 12)
	for index := range options {
		options[index] = Option{Label: fmt.Sprintf("Option %d", index)}
	}
	model := newChoiceModel(ChoicePickerConfig{Message: "Pick", Options: options, Multiple: true, Required: true})
	model.width = 18
	model.height = 6
	model.cursor = len(model.visible) - 1
	start, end := model.visibleRange()
	if start == 0 || end != len(model.visible) || end-start >= len(model.visible) {
		t.Fatalf("range = %d:%d for %d choices", start, end, len(model.visible))
	}
	if strings.Count(model.help(), "\n") < 2 {
		t.Fatalf("help did not wrap: %q", model.help())
	}
}

func TestConfirmModelUsesDefaultSelection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Continue?", Options: []Option{{Label: "Yes", Value: true}, {Label: "No", Value: false}}, Required: true,
	})
	model.cursor = 1
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
	if model.intent != SelectionPrimary {
		t.Fatalf("intent = %v, want primary", model.intent)
	}
}

func TestSelectionModelShiftEnterUsesAlternateIntent(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.selected.Label != "build" || model.intent != SelectionAlternate {
		t.Fatalf("selected = %#v, intent = %v, done = %v, command nil = %v", model.selected, model.intent, model.done, command == nil)
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
	for _, shortcut := range []string{"enter", "run", "shift+enter", "print command"} {
		if !strings.Contains(model.View().Content, shortcut) {
			t.Fatalf("view = %q, want shortcut %q", model.View().Content, shortcut)
		}
	}
}
