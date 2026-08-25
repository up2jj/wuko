package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestReviewDiffStylesAndSafeDefault(t *testing.T) {
	model := newReviewModel(ReviewConfig{
		Message: "Review", Format: "diff",
		Content: "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n same",
	})
	view := model.View().Content
	for _, styled := range []string{
		interactiveStyles.message.Render("Review"),
		reviewDiffStyles.metadata.Render("--- a/a"),
		reviewDiffStyles.hunk.Render("@@ -1 +1 @@"),
		reviewDiffStyles.removal.Render("-old"),
		reviewDiffStyles.addition.Render("+new"),
		interactiveStyles.selected.Render("[ Reject ]"),
	} {
		if !strings.Contains(view, styled) {
			t.Fatalf("view = %q, want %q", view, styled)
		}
	}
}

func TestReviewDecisionAndSubmission(t *testing.T) {
	model := newReviewModel(ReviewConfig{Message: "Review", Content: "change"})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	model = updated.(reviewModel)
	if !model.approved {
		t.Fatal("right did not focus approve")
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(reviewModel)
	if command == nil || !model.done || !model.result {
		t.Fatalf("model = %#v, command nil = %v", model, command == nil)
	}

	model = newReviewModel(ReviewConfig{Message: "Review", Content: "change", Default: true})
	if !model.approved {
		t.Fatal("configured default did not focus approve")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if updated.(reviewModel).approved {
		t.Fatal("left did not focus reject")
	}
}

func TestReviewScrollPanResizeAndHelp(t *testing.T) {
	model := newReviewModel(ReviewConfig{Message: "Review", Format: "diff", Content: "+abcdefghijklmnopqrstuvwxyz\n second\n third\n fourth"})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 12, Height: 10})
	model = updated.(reviewModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(reviewModel)
	if model.vertical != 1 {
		t.Fatalf("vertical = %d", model.vertical)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	model = updated.(reviewModel)
	if model.horizontal == 0 {
		t.Fatal("diff did not pan horizontally")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(reviewModel)
	styled := reviewDiffStyles.addition.Render(model.lines[0])
	visible := ansi.Cut(styled, model.horizontal, model.horizontal+model.contentWidth())
	if !strings.Contains(model.View().Content, visible) {
		t.Fatalf("view did not preserve addition style after panning: %q", model.View().Content)
	}
	for _, shortcut := range []string{"↑/↓ scroll", "shift+←/→ pan", "←/→ decision", "ctrl+c cancel"} {
		if !strings.Contains(model.View().Content, shortcut) {
			t.Fatalf("view = %q, want %q", model.View().Content, shortcut)
		}
	}
}

func TestReviewPlainWrapsAndSanitizes(t *testing.T) {
	model := newReviewModel(ReviewConfig{Message: "Review", Content: "\x1b[31mabcdefghij\x1b[0m\tvalue\r\nnext"})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 8, Height: 20})
	model = updated.(reviewModel)
	if strings.Contains(model.content, "\x1b[") || strings.Contains(model.content, "\r") || strings.Contains(model.content, "\t") {
		t.Fatalf("content was not sanitized: %q", model.content)
	}
	if len(model.lines) < 3 {
		t.Fatalf("wrapped lines = %#v", model.lines)
	}
}

func TestReviewCancellation(t *testing.T) {
	model := newReviewModel(ReviewConfig{Message: "Review", Content: "change"})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	unchanged := updated.(reviewModel)
	if command != nil || unchanged.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", unchanged.cancelled, command == nil)
	}

	updated, command = unchanged.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil || !updated.(reviewModel).cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", updated.(reviewModel).cancelled, command == nil)
	}
}
