package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectionModelTargetShortcutIsContextual(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		actions []SelectionAction
		want    bool
	}{
		{name: "generic picker"},
		{name: "plain workflow", actions: []SelectionAction{{Intent: SelectionPrimary}}},
		{name: "targeted workflow", actions: []SelectionAction{{Intent: SelectionTargets}}, want: true},
		{name: "disabled", actions: []SelectionAction{{Intent: SelectionTargets, Disabled: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			model := newSelectionModel("Workflows", []Option{{Label: "deploy", Actions: test.actions}})
			view := ansi.Strip(model.View().Content)
			if strings.Contains(view, "t targets") != test.want {
				t.Fatalf("target help: %s", view)
			}
			updated, command := model.Update(tea.KeyPressMsg{Code: 't'})
			model = updated.(selectionModel)
			if model.done != test.want || (command != nil) != test.want || (test.want && model.intent != SelectionTargets) {
				t.Fatalf("done = %v, intent = %v, command = %v", model.done, model.intent, command)
			}
		})
	}
}

func TestSelectionModelTargetShortcutTypesInFilter(t *testing.T) {
	t.Parallel()
	model := newSelectionModel("Workflows", []Option{{Label: "staging", Actions: []SelectionAction{{Intent: SelectionTargets}}}})
	model.list.SetFilterState(list.Filtering)
	updated, _ := model.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	model = updated.(selectionModel)
	if model.done || model.list.FilterValue() != "t" {
		t.Fatalf("done = %v, filter = %q", model.done, model.list.FilterValue())
	}
}

func TestSelectionModelTargetBackRespectsPaletteAndFilter(t *testing.T) {
	t.Parallel()
	option := Option{Label: "staging", Actions: []SelectionAction{{Label: "Run", Intent: SelectionPrimary}}}
	model := newSelectionModelWithConfig(SelectionPickerConfig{Title: "Targets", Options: []Option{option}, AllowBack: true, InitialFilter: "stag"})
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a'})
	model = updated.(selectionModel)
	if !model.actionMode {
		t.Fatal("action palette did not open")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if model.done || model.actionMode || model.list.FilterValue() != "stag" {
		t.Fatal("Escape did not dismiss only the palette")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if model.done || model.list.FilterValue() != "" {
		t.Fatal("Escape did not clear only the filter")
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.cancelled || model.intent != SelectionBack || model.selected.Label != "staging" {
		t.Fatalf("back navigation = %#v", model)
	}
}

func TestSelectionModelPendingTargetAction(t *testing.T) {
	t.Parallel()
	options := []Option{{Label: "staging", Actions: []SelectionAction{
		{Label: "Run", Intent: SelectionPrimary}, {Label: "Validate", Intent: SelectionValidate},
	}}}
	config := SelectionPickerConfig{Title: "Targets", Options: options, AllowBack: true, EnterIntent: SelectionValidate}
	model := newSelectionModelWithConfig(config)
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "enter validate") || !strings.Contains(view, "esc back") {
		t.Fatalf("pending action help: %s", view)
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(selectionModel); !got.done || got.intent != SelectionValidate {
		t.Fatalf("Enter intent = %v", got.intent)
	}
	model = newSelectionModelWithConfig(config)
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'a'})
	updated, _ = updated.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := updated.(selectionModel); !got.done || got.intent != SelectionPrimary {
		t.Fatalf("Run palette intent = %v", got.intent)
	}
	model = newSelectionModelWithConfig(config)
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if got := updated.(selectionModel); !got.cancelled || got.done {
		t.Fatal("Ctrl+C did not cancel target selection")
	}
}

func TestSelectionModelPendingFormCannotSelectTargetWithoutForm(t *testing.T) {
	t.Parallel()
	model := newSelectionModelWithConfig(SelectionPickerConfig{
		Title: "Targets", EnterIntent: SelectionUI, AllowBack: true,
		Options: []Option{
			{Label: "cli", Actions: []SelectionAction{{Label: "Run", Intent: SelectionPrimary}, {Label: "Open form", Intent: SelectionUI, Disabled: true, DisabledReason: "workflow does not declare a form"}}},
			{Label: "web", Actions: []SelectionAction{{Label: "Run", Intent: SelectionPrimary}, {Label: "Open form", Intent: SelectionUI}}},
		},
	})
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "enter open form") {
		t.Fatalf("pending action help: %s", view)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(selectionModel)
	if got.done || command != nil {
		t.Fatal("Enter selected a target without a form")
	}
	if !got.actionMode || got.actionCursor != 1 || got.actionError != "workflow does not declare a form" {
		t.Fatalf("action palette = %v, cursor = %d, error = %q", got.actionMode, got.actionCursor, got.actionError)
	}
}
