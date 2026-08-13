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
}

var errTooShort = errors.New("too short")
