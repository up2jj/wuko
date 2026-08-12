package tui

import (
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
