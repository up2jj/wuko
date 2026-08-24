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

func TestChoiceModelUsesDefaultsAndShowsBounds(t *testing.T) {
	minimum, maximum := 1, 3
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, Required: true,
		MinSelected: &minimum, MaxSelected: &maximum,
		Options: []Option{
			{Label: "A", Default: true},
			{Label: "B"},
			{Label: "C", Default: true},
		},
	})
	if !slices.Equal(model.order, []int{0, 2}) {
		t.Fatalf("default order = %#v", model.order)
	}
	view := model.View().Content
	for _, want := range []string{"selected: 2", "min: 1", "max: 3", "ctrl+a select all", "ctrl+x clear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}

	single := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: false,
		Options: []Option{{Label: "A"}, {Label: "B", Default: true}},
	})
	if single.cursor != 2 || single.visible[single.cursor].label != "B" {
		t.Fatalf("cursor = %d, visible = %#v", single.cursor, single.visible)
	}
}

func TestChoiceModelShowsAndBlocksDisabledChoice(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: true,
		Options: []Option{
			{Label: "Unavailable", Description: "Production", Disabled: true, DisabledReason: "maintenance window"},
			{Label: "Available"},
		},
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command != nil || model.done || model.err != "maintenance window" {
		t.Fatalf("done = %v, err = %q, command nil = %v", model.done, model.err, command == nil)
	}
	view := model.View().Content
	for _, want := range []string{"Unavailable", "Production", "disabled: maintenance window"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	model.filter.SetValue("maintenance")
	model.refreshVisible()
	if len(model.visible) != 1 || model.visible[0].label != "Unavailable" {
		t.Fatalf("visible = %#v", model.visible)
	}
}

func TestChoiceModelBulkActionsRespectFilterAndMaximum(t *testing.T) {
	maximum := 3
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, MaxSelected: &maximum,
		Options: []Option{
			{Label: "A", Description: "visible"},
			{Label: "B", Description: "visible"},
			{Label: "C", Description: "visible"},
			{Label: "D", Description: "hidden", Default: true},
		},
	})
	model.filter.SetValue("visible")
	model.refreshVisible()
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	model = updated.(choiceModel)
	if !slices.Equal(model.order, []int{3, 0, 1}) {
		t.Fatalf("order after select all = %#v", model.order)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model = updated.(choiceModel)
	if !slices.Equal(model.order, []int{3}) || !model.selected[3] {
		t.Fatalf("order after clear = %#v, selected = %#v", model.order, model.selected)
	}
}

func TestChoiceModelEnforcesSelectionBounds(t *testing.T) {
	minimum, maximum := 2, 2
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, MinSelected: &minimum, MaxSelected: &maximum,
		Options: []Option{{Label: "A"}, {Label: "B"}, {Label: "C"}},
	})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command != nil || model.err != "select at least 2 values" {
		t.Fatalf("err = %q, command nil = %v", model.err, command == nil)
	}
	model.cursor = 1
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	model.cursor = 2
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	if model.err != "select at most 2 values" || !slices.Equal(model.order, []int{0, 1}) {
		t.Fatalf("err = %q, order = %#v", model.err, model.order)
	}
}

func TestChoicePickerConfigValidation(t *testing.T) {
	negative, zero, one := -1, 0, 1
	tests := []struct {
		name   string
		config ChoicePickerConfig
	}{
		{name: "bounds in single mode", config: ChoicePickerConfig{MinSelected: &zero}},
		{name: "negative minimum", config: ChoicePickerConfig{Multiple: true, MinSelected: &negative}},
		{name: "inverted bounds", config: ChoicePickerConfig{Multiple: true, MinSelected: &one, MaxSelected: &zero}},
		{name: "disabled without reason", config: ChoicePickerConfig{Options: []Option{{Disabled: true}}}},
		{name: "disabled default", config: ChoicePickerConfig{Options: []Option{{Disabled: true, DisabledReason: "no", Default: true}}}},
		{name: "multiple single defaults", config: ChoicePickerConfig{Options: []Option{{Default: true}, {Default: true}}}},
		{name: "minimum exceeds enabled", config: ChoicePickerConfig{Multiple: true, MinSelected: &one, Options: []Option{{Disabled: true, DisabledReason: "no"}}}},
		{name: "defaults exceed maximum", config: ChoicePickerConfig{Multiple: true, MaxSelected: &zero, Options: []Option{{Default: true}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateChoicePickerConfig(tt.config); err == nil {
				t.Fatal("expected validation error")
			}
		})
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

func TestSelectionModelUUsesBrowserUIIntent(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'u'})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.selected.Label != "build" || model.intent != SelectionUI {
		t.Fatalf("selected = %#v, intent = %v, done = %v, command nil = %v", model.selected, model.intent, model.done, command == nil)
	}
}

func TestSelectionModelSupportsPickerActions(t *testing.T) {
	tests := []struct {
		name   string
		input  tea.KeyPressMsg
		intent SelectionIntent
	}{
		{name: "editor", input: tea.KeyPressMsg{Code: 'e'}, intent: SelectionEditor},
		{name: "pin", input: tea.KeyPressMsg{Code: 'p'}, intent: SelectionTogglePin},
		{name: "sort", input: tea.KeyPressMsg{Code: 's'}, intent: SelectionToggleSort},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newSelectionModel("Workflows", []Option{{Label: "build"}})
			updated, command := model.Update(test.input)
			model = updated.(selectionModel)
			if command == nil || !model.done || model.intent != test.intent {
				t.Fatalf("intent = %v, done = %v, command nil = %v", model.intent, model.done, command == nil)
			}
		})
	}
}

func TestSelectionModelUsesDefaultOption(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}, {Label: "deploy", Default: true}})
	item, ok := model.list.SelectedItem().(listOption)
	if !ok || item.Label != "deploy" {
		t.Fatalf("selected item = %#v, want deploy", model.list.SelectedItem())
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

func TestSelectionModelCtrlCCancels(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(selectionModel)
	if command == nil || !model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestSelectionModelEscapeDoesNotCancel(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if command != nil || model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestChoiceModelEscapeDoesNotCancel(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A"}}, Required: true,
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(choiceModel)
	if command != nil || model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestChoiceModelCancelsWithCtrlC(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A"}}, Required: true,
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(choiceModel)
	if command == nil || !model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestSelectionModelViewIncludesDescription(t *testing.T) {
	path := "/a/" + strings.Repeat("very-long-workflow-directory/", 5) + "build.yaml"
	model := newSelectionModel("Workflows", []Option{{Label: "build", Description: "local • Build it", Path: path}})
	if !strings.Contains(model.View().Content, "local") {
		t.Fatalf("view = %q", model.View().Content)
	}
	if !strings.Contains(model.View().Content, path) {
		t.Fatalf("view = %q, want full path", model.View().Content)
	}
	for _, shortcut := range []string{"enter", "run", "u", "open UI", "shift+enter", "print command", "e", "edit", "p", "pin", "s", "sort", "ctrl+c", "cancel"} {
		if !strings.Contains(model.View().Content, shortcut) {
			t.Fatalf("view = %q, want shortcut %q", model.View().Content, shortcut)
		}
	}
}
