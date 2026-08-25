package tui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

func TestTextModelStartsWithEditableValue(t *testing.T) {
	model := newTextModel("Enter the release name", "from an earlier step", true, textinput.EchoNormal, nil)
	if got := model.input.Value(); got != "from an earlier step" {
		t.Fatalf("value = %q, want %q", got, "from an earlier step")
	}
	view := model.View().Content
	if !strings.Contains(view, "Enter the release name") {
		t.Fatalf("view = %q, want input message", view)
	}
	if !strings.Contains(view, "from an earlier step") {
		t.Fatalf("view = %q, want prepopulated value", view)
	}
	for _, styled := range []string{
		interactiveStyles.message.Render("Enter the release name"),
		model.input.Styles().Focused.Prompt.Render("> "),
		renderHelpText("enter confirm • ctrl+c cancel"),
	} {
		if !strings.Contains(view, styled) {
			t.Fatalf("view = %q, want styled content %q", view, styled)
		}
	}
}

func TestTextModelResizesInputWithTerminal(t *testing.T) {
	model := newTextModel("Name", "value", true, textinput.EchoNormal, nil)
	updated, command := model.Update(tea.WindowSizeMsg{Width: 12, Height: 10})
	model = updated.(textModel)
	if command != nil || model.width != 12 {
		t.Fatalf("width = %d, command nil = %v", model.width, command == nil)
	}
	if got := model.input.View(); !strings.Contains(got, "> ") {
		t.Fatalf("input view = %q, want prompt", got)
	}
}

func TestPasswordModelMasksValue(t *testing.T) {
	model := newTextModel("Password", "secret", true, textinput.EchoPassword, nil)
	view := model.View().Content
	if strings.Contains(view, "secret") {
		t.Fatalf("view exposes password: %q", view)
	}
	if !strings.Contains(view, strings.Repeat(string(model.input.EchoCharacter), len("secret"))) {
		t.Fatalf("view = %q, want password mask", view)
	}
}

func TestTextModelShowsValidationErrorAndStaysOpen(t *testing.T) {
	model := newTextModel("Name", "ab", false, textinput.EchoNormal, func(value string) error {
		if len(value) < 3 {
			return errTooShort
		}
		return nil
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(textModel)
	if command != nil || model.done {
		t.Fatalf("done = %v, command nil = %v", model.done, command == nil)
	}
	if !strings.Contains(model.View().Content, errTooShort.Error()) {
		t.Fatalf("view = %q, want validation error", model.View().Content)
	}
	if !strings.Contains(model.View().Content, interactiveStyles.error.Render(errTooShort.Error())) {
		t.Fatalf("view = %q, want styled validation error", model.View().Content)
	}
}

func TestTextModelCancelsWithCtrlC(t *testing.T) {
	model := newTextModel("Name", "", true, textinput.EchoNormal, nil)
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(textModel)
	if command == nil || !model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestTextModelEscapeDoesNotCancel(t *testing.T) {
	model := newTextModel("Name", "", true, textinput.EchoNormal, nil)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(textModel)
	if command != nil || model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

var errTooShort = errors.New("too short")
