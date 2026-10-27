package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectionModelScopeShortcut(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		enabled bool
		options []Option
		filter  string
	}{
		{name: "generic", options: []Option{{Label: "deploy"}}},
		{name: "workflow", enabled: true, options: []Option{{Label: "deploy"}}, filter: "dep"},
		{name: "empty scope", enabled: true},
		{name: "no search matches", enabled: true, options: []Option{{Label: "deploy"}}, filter: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			model := newSelectionModelWithConfig(SelectionPickerConfig{
				Title: "Workflows (scope: local)", Options: test.options,
				AllowScopeToggle: test.enabled, InitialFilter: test.filter,
			})
			for _, full := range []bool{false, true} {
				model.list.Help.ShowAll = full
				view := ansi.Strip(model.View().Content)
				if strings.Contains(view, ". scope") != test.enabled {
					t.Fatalf("full=%t: unexpected scope help:\n%s", full, view)
				}
				if test.name == "empty scope" && strings.Count(view, "No workflows in this scope") != 1 {
					t.Fatalf("empty scope message must appear once:\n%s", view)
				}
			}
			updated, command := model.Update(tea.KeyPressMsg{Code: '.', Text: "."})
			model = updated.(selectionModel)
			if model.done != test.enabled || (command != nil) != test.enabled || model.cancelled {
				t.Fatalf("done=%t, cancelled=%t, command=%v", model.done, model.cancelled, command)
			}
			if test.enabled && (model.intent != SelectionToggleScope || model.filter != test.filter) {
				t.Fatalf("intent=%v, filter=%q", model.intent, model.filter)
			}
			if test.name == "workflow" && model.selected.Label != "deploy" {
				t.Fatalf("selected option = %#v", model.selected)
			}
		})
	}
}

func TestSelectionModelScopeShortcutTypesInFilter(t *testing.T) {
	t.Parallel()
	model := newSelectionModelWithConfig(SelectionPickerConfig{
		Title: "Workflows", AllowScopeToggle: true, Options: []Option{{Label: "deploy.yaml"}},
	})
	model.list.SetFilterState(list.Filtering)
	updated, _ := model.Update(tea.KeyPressMsg{Code: '.', Text: "."})
	model = updated.(selectionModel)
	if model.done || model.list.FilterValue() != "." {
		t.Fatalf("done=%t, filter=%q", model.done, model.list.FilterValue())
	}
}
