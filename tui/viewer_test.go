package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestTextViewerWrapsSanitizesAndReportsStatus(t *testing.T) {
	model := newTextViewerModel(TextViewerConfig{Title: "Validate — release", Content: "\x1b[31mabcdefghij\x1b[0m\tvalue\r\nnext", Failed: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 8, Height: 12})
	model = updated.(textViewerModel)
	if strings.Contains(model.content, "\x1b[") || strings.Contains(model.content, "\r") || strings.Contains(model.content, "\t") {
		t.Fatalf("content was not sanitized: %q", model.content)
	}
	rendered := model.View()
	if !rendered.AltScreen {
		t.Fatal("viewer did not request the alternate screen")
	}
	view := rendered.Content
	for _, want := range []string{"Validate", "failed", "enter/esc back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	if len(model.lines) < 3 {
		t.Fatalf("wrapped lines = %#v", model.lines)
	}
}

func TestTextViewerScrollsReturnsAndCancels(t *testing.T) {
	model := newTextViewerModel(TextViewerConfig{Title: "Tree", Content: "one\ntwo\nthree\nfour"})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 20, Height: 4})
	model = updated.(textViewerModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(textViewerModel)
	if model.vertical != 1 {
		t.Fatalf("vertical = %d, want 1", model.vertical)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command == nil || !updated.(textViewerModel).done {
		t.Fatal("escape did not return from viewer")
	}

	model = newTextViewerModel(TextViewerConfig{Title: "Tree", Content: "one"})
	updated, command = model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil || !updated.(textViewerModel).cancelled {
		t.Fatal("ctrl+c did not cancel viewer")
	}
}
