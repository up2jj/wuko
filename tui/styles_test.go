package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestInteractiveScreensFitTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	for _, size := range []tea.WindowSizeMsg{
		{Width: 80, Height: 24}, {Width: 120, Height: 32}, {Width: 40, Height: 10},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			var options []Option
			var rows [][]string
			for index := range 20 {
				options = append(options, Option{Label: fmt.Sprintf("check-%d 世界", index), Description: "Run project checks", Path: strings.Repeat("directory/", 20) + "check.yaml"})
				rows = append(rows, []string{fmt.Sprint(index), "healthy"})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			models := map[string]tea.Model{
				"picker":   newSelectionModel("Workflows", options),
				"multi":    newMultiSelectionModel("Marketplace", options),
				"choice":   newChoiceModel(ChoicePickerConfig{Message: "Choose checks", Options: options, Required: true}),
				"path":     mustPathModel(t, PathPickerConfig{Message: "Choose a file", Root: pathPickerTree(t), Kind: "file"}),
				"text":     newTextModel("Release name", strings.Repeat("世界", 50), true, textinput.EchoNormal, nil),
				"textarea": newTextAreaModel("Release notes", strings.Repeat("notes\n", 20), true, nil),
				"review":   newReviewModel(ReviewConfig{Message: "Review changes", Format: reviewDiff, Content: strings.Repeat("+"+strings.Repeat("world", 30)+"\n", 20)}),
				"viewer":   newTextViewerModel(TextViewerConfig{Title: "Workflow output", Content: strings.Repeat("world\n", 20)}),
				"table":    newTableModel(TableConfig{Message: "Checks", Columns: []TableColumn{{Header: "ID"}, {Header: "Status"}}, Rows: rows}),
				"dashboard": newDashboardModel(ctx, cancel, DashboardConfig{
					Title: "Services", Groups: []DashboardGroup{{ID: "local", Title: "Development", Widgets: []DashboardWidget{{ID: "api", Title: "API"}}}},
					Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
						return "healthy", nil
					},
				}, &sync.WaitGroup{}),
			}
			for name, model := range models {
				t.Run(name, func(t *testing.T) {
					updated, _ := model.Update(size)
					assertScreenFits(t, updated.View().Content, size.Width, size.Height)
					for _, background := range []color.Color{color.White, color.Black} {
						updated, _ = updated.Update(tea.BackgroundColorMsg{Color: background})
						assertScreenFits(t, updated.View().Content, size.Width, size.Height)
					}
					if name == "picker" && spacious(size.Width, size.Height) {
						updated, _ = updated.Update(tea.KeyPressMsg{Code: '?'})
						assertScreenFits(t, updated.View().Content, size.Width, size.Height)
					}
				})
			}
			palette := actionPalette{styles: newAppearance().styles, Title: "Actions", Description: "Run checks", Path: strings.Repeat("directory/", 20), Actions: []SelectionAction{{Label: "Run"}, {Label: "Validate"}}, Width: size.Width, Height: size.Height}
			assertScreenFits(t, screenView(palette.view(), size.Width).Content, size.Width, size.Height)
		})
	}
}

func assertScreenFits(t *testing.T, view string, width, height int) {
	t.Helper()
	if strings.HasSuffix(view, "\n") {
		t.Fatal("a trailing newline can scroll a full-height screen's title out of view")
	}
	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("screen has %d rows, terminal has %d:\n%s", len(lines), height, ansi.Strip(view))
	}
	for index, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("row %d has width %d, terminal has %d: %q\n%s", index, got, width, line, ansi.Strip(view))
		}
	}
}

func TestThemeChangesPreserveInputAndOtherScreens(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	original := newTextModel("Name", "release", true, textinput.EchoNormal, nil)
	original.err = "validation error"
	before := original.View().Content
	updated, command := original.Update(tea.BackgroundColorMsg{Color: color.White})
	light := updated.(textModel)
	if command != nil || light.dark || !original.dark || light.input.Value() != "release" || !light.input.Focused() || light.err != original.err {
		t.Fatalf("theme change altered screen state: %#v", light)
	}
	if before == light.View().Content || before != original.View().Content {
		t.Fatal("theme did not update locally")
	}
	updated, _ = light.Update(tea.BackgroundColorMsg{Color: color.Black})
	if updated.(textModel).View().Content != before {
		t.Fatal("dark palette was not restored")
	}
}

func TestThemeChangesPreserveFilterSelectionAndReview(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	background := tea.BackgroundColorMsg{Color: color.White}
	picker := newSelectionModel("Workflows", []Option{{Label: "check"}, {Label: "release"}})
	picker.list.Select(1)
	picker.list.SetFilterText("release")
	picker.actionMode = true
	updated, _ := picker.Update(background)
	lightPicker := updated.(selectionModel)
	if lightPicker.list.FilterValue() != "release" || lightPicker.list.SelectedItem().(listOption).Label != "release" || !lightPicker.actionMode {
		t.Fatal("picker lost its state during a theme change")
	}
	choice := newChoiceModel(ChoicePickerConfig{Message: "Choose", FilterOnType: true, Required: true, Options: []Option{{Label: "check"}, {Label: "release"}}})
	choice.filter.SetValue("release")
	choice.refreshVisible()
	updated, _ = choice.Update(background)
	lightChoice := updated.(choiceModel)
	if lightChoice.filter.Value() != "release" || !lightChoice.filter.Focused() || len(lightChoice.visible) != 1 {
		t.Fatal("choice lost its filter during a theme change")
	}
	review := newReviewModel(ReviewConfig{Message: "Review", Content: strings.Repeat("line\n", 100), Default: true})
	review.vertical = 5
	updated, _ = review.Update(background)
	lightReview := updated.(reviewModel)
	if !lightReview.approved || lightReview.vertical != 5 {
		t.Fatal("review lost decision or scroll position during a theme change")
	}
	notes := newTextAreaModel("Notes", "First line\nSecond line", true, nil)
	notes.input.CursorStart()
	updated, _ = notes.Update(background)
	lightNotes := updated.(textAreaModel)
	if lightNotes.input.Value() != notes.input.Value() || lightNotes.input.Line() != notes.input.Line() || !lightNotes.input.Focused() {
		t.Fatal("multiline input lost its value, cursor, or focus during a theme change")
	}
}

func TestPromptValidationErrorsRemainVisibleInCompactTerminal(t *testing.T) {
	notes := newTextAreaModel("Release notes", "value", true, func(string) error {
		return fmt.Errorf("%s", strings.Repeat("Invalid release notes. ", 3))
	})
	updated, _ := notes.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	updated, _ = updated.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assertScreenFits(t, updated.View().Content, 40, 10)
	if !strings.Contains(strings.ReplaceAll(ansi.Strip(updated.View().Content), "\n", ""), strings.TrimSpace(notes.validate("").Error())) {
		t.Fatal("validation error was truncated")
	}
}

func TestNoColorRetainsStructureAndPasswordMasking(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	model := newTextModel("Password", "secret", true, textinput.EchoPassword, nil)
	updated, _ := model.Update(tea.BackgroundColorMsg{Color: color.White})
	model = updated.(textModel)
	view := model.View().Content
	if strings.Contains(view, "secret") || !strings.Contains(view, strings.Repeat(string(model.input.EchoCharacter), 6)) {
		t.Fatal("password masking changed")
	}
	_, noForeground := model.styles.content.GetForeground().(lipgloss.NoColor)
	_, noBackground := model.styles.selection.GetBackground().(lipgloss.NoColor)
	if model.styles.accent != nil || !noForeground || !noBackground {
		t.Fatal("NO_COLOR left colors enabled")
	}
	if !strings.Contains(ansi.Strip(view), "ctrl+c cancel") || !strings.Contains(view, "─") {
		t.Fatal("uncolored screen lost controls or structure")
	}
}

func TestWrapHelpUsesTerminalCellWidths(t *testing.T) {
	for _, width := range []int{1, 8, 12, 40} {
		value := wrapHelp([]string{"世界 select", "e\u0301 edit", "ctrl+c cancel"}, width)
		for _, line := range strings.Split(value, "\n") {
			// A terminal cell cannot contain half a double-width character.
			if ansi.StringWidth(line) > max(width, 2) {
				t.Fatalf("width %d: line %q does not fit", width, line)
			}
		}
	}
}
