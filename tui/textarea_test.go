package tui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
)

func TestTextAreaModelStartsWithEditableMultilineValue(t *testing.T) {
	model := newTextAreaModel("Enter the release notes", "First line\nSecond line", true, nil)
	if got := model.input.Value(); got != "First line\nSecond line" {
		t.Fatalf("value = %q, want multiline value", got)
	}
	if model.input.Height() != textAreaHeight {
		t.Fatalf("height = %d, want %d", model.input.Height(), textAreaHeight)
	}
	if model.input.ShowLineNumbers {
		t.Fatal("line numbers are enabled")
	}
	view := model.View().Content
	for _, content := range []string{
		"Enter the release notes",
		"First line",
		"Second line",
		model.input.Styles().Focused.Prompt.Render("> "),
		renderHelpText("enter newline • ctrl+s confirm • ctrl+c cancel"),
	} {
		if !strings.Contains(view, content) {
			t.Fatalf("view = %q, want content %q", view, content)
		}
	}
}

func TestTextAreaModelEnterInsertsNewlineAndCtrlSConfirms(t *testing.T) {
	model := newTextAreaModel("Notes", "First line", true, nil)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(textAreaModel)
	if command == nil || model.done {
		t.Fatalf("after enter: done = %v, command nil = %v", model.done, command == nil)
	}
	if got := model.input.Value(); got != "First line\n" {
		t.Fatalf("value = %q, want newline appended", got)
	}

	updated, command = model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(textAreaModel)
	if command == nil || !model.done {
		t.Fatalf("after ctrl+s: done = %v, command nil = %v", model.done, command == nil)
	}
}

func TestTextAreaModelShowsValidationErrorAndStaysOpen(t *testing.T) {
	model := newTextAreaModel("Notes", "ab", false, func(value string) error {
		if len(value) < 3 {
			return errTooShort
		}
		return nil
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(textAreaModel)
	if command != nil || model.done {
		t.Fatalf("done = %v, command nil = %v", model.done, command == nil)
	}
	if !strings.Contains(model.View().Content, interactiveStyles.error.Render(errTooShort.Error())) {
		t.Fatalf("view = %q, want styled validation error", model.View().Content)
	}
}

func TestTextAreaModelKeepsValidationErrorUntilNextEdit(t *testing.T) {
	model := newTextAreaModel("Notes", "ab", false, func(value string) error {
		if len(value) < 3 {
			return errTooShort
		}
		return nil
	})
	updated, _ := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(textAreaModel)

	updated, _ = model.Update(cursor.BlinkMsg{})
	model = updated.(textAreaModel)
	if model.err != errTooShort.Error() {
		t.Fatalf("after blink: error = %q, want %q", model.err, errTooShort)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	model = updated.(textAreaModel)
	if model.err != "" {
		t.Fatalf("after edit: error = %q, want cleared", model.err)
	}
}

func TestTextAreaModelRequiresNonEmptyValue(t *testing.T) {
	model := newTextAreaModel("Notes", " \n ", true, nil)
	updated, command := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(textAreaModel)
	if command != nil || model.done {
		t.Fatalf("done = %v, command nil = %v", model.done, command == nil)
	}
	if !strings.Contains(model.View().Content, "a value is required") {
		t.Fatalf("view = %q, want required error", model.View().Content)
	}
}

func TestTextAreaModelResizesWithTerminal(t *testing.T) {
	model := newTextAreaModel("Notes", "value", true, nil)
	updated, command := model.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	model = updated.(textAreaModel)
	if command != nil || model.width != 20 {
		t.Fatalf("width = %d, command nil = %v", model.width, command == nil)
	}
	if got := model.input.Width(); got != 18 {
		t.Fatalf("input width = %d, want 18", got)
	}
}

func TestTextAreaModelShrinksInsideShortTerminal(t *testing.T) {
	model := newTextAreaModel("Notes", "value", true, nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 6})
	model = updated.(textAreaModel)
	if got := model.input.Height(); got != 3 {
		t.Fatalf("input height = %d, want 3", got)
	}
	if lines := strings.Count(model.View().Content, "\n"); lines > 6 {
		t.Fatalf("view spans %d lines, want at most 6", lines)
	}

	updated, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	model = updated.(textAreaModel)
	if got := model.input.Height(); got != textAreaHeight {
		t.Fatalf("input height = %d, want %d", got, textAreaHeight)
	}
}

func TestTextAreaModelCancelsWithCtrlCAndIgnoresEscape(t *testing.T) {
	model := newTextAreaModel("Notes", "", false, nil)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(textAreaModel)
	if command != nil || model.cancelled {
		t.Fatalf("after escape: cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}

	updated, command = model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(textAreaModel)
	if command == nil || !model.cancelled {
		t.Fatalf("after ctrl+c: cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestTextAreaModelUsesValidatorRequiredMessage(t *testing.T) {
	want := errors.New("Write something useful")
	model := newTextAreaModel("Notes", "", true, func(string) error { return want })
	updated, _ := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(textAreaModel)
	if model.err != want.Error() {
		t.Fatalf("error = %q, want %q", model.err, want)
	}
}
