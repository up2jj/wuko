package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/cursor"
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
		model.styles.message.Render("Enter the release name"),
		model.input.Styles().Focused.Prompt.Render("> "),
		model.styles.renderHelp("enter confirm • ctrl+c cancel"),
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
	if !strings.Contains(model.View().Content, model.styles.error.Render(errTooShort.Error())) {
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

func TestTextModelKeepsRequiredErrorUntilValueChanges(t *testing.T) {
	for _, mode := range []textinput.EchoMode{textinput.EchoNormal, textinput.EchoPassword} {
		for _, paste := range []bool{false, true} {
			t.Run(fmt.Sprintf("echo=%d/paste=%t", mode, paste), func(t *testing.T) {
				model := newTextModel("Value", "", true, mode, nil)
				updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				model = updated.(textModel)
				for _, message := range []tea.Msg{
					cursor.BlinkMsg{},
					tea.WindowSizeMsg{Width: 40, Height: 12},
					tea.KeyPressMsg{Code: tea.KeyLeft},
				} {
					updated, _ = model.Update(message)
					model = updated.(textModel)
					if model.err != "a value is required" || !strings.Contains(model.View().Content, model.err) {
						t.Fatalf("after %T: error = %q, want visible required error", message, model.err)
					}
				}
				var edit tea.Msg = tea.KeyPressMsg{Code: 'x', Text: "x"}
				if paste {
					edit = tea.PasteMsg{Content: "x"}
				}
				updated, _ = model.Update(edit)
				model = updated.(textModel)
				if model.err != "" || model.input.Value() != "x" {
					t.Fatalf("after edit: error = %q, value = %q", model.err, model.input.Value())
				}
				updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if !updated.(textModel).done {
					t.Fatal("valid value did not submit")
				}
			})
		}
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
