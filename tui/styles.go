package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// appearance belongs to a single screen. Terminal replies must never change another
// screen's palette or reset the state of its embedded input components.
type appearance struct {
	styles interactiveStyleSet
	dark   bool
	plain  bool
}

func newAppearance() appearance {
	plain := os.Getenv("NO_COLOR") != ""
	return appearance{styles: newInteractiveStyles(true, plain), dark: true, plain: plain}
}

func (a *appearance) update(message tea.Msg) bool {
	background, ok := message.(tea.BackgroundColorMsg)
	if !ok {
		return false
	}
	a.dark = background.IsDark()
	a.styles = newInteractiveStyles(a.dark, a.plain)
	return true
}

type interactiveStyleSet struct {
	message, label, value, status, cursor, selected     lipgloss.Style
	content, description, disabled, error, button, help lipgloss.Style
	border, selection, addition, removal                lipgloss.Style
	accent                                              color.Color
}

func newInteractiveStyles(dark, plain bool) interactiveStyleSet {
	lightDark := lipgloss.LightDark(dark)
	accent := lightDark(lipgloss.Color("#087E99"), lipgloss.Color("#67D6EE"))
	foreground := lightDark(lipgloss.Color("#172033"), lipgloss.Color("#E5EAF1"))
	muted := lightDark(lipgloss.Color("#596579"), lipgloss.Color("#96A2B1"))
	border := lightDark(lipgloss.Color("#CED5DE"), lipgloss.Color("#303B49"))
	selection := lightDark(lipgloss.Color("#E2F2F7"), lipgloss.Color("#17323E"))
	red := lightDark(lipgloss.Color("#BF2542"), lipgloss.Color("#FF8292"))
	green := lightDark(lipgloss.Color("#158047"), lipgloss.Color("#85D5A0"))
	if plain {
		accent, foreground, muted, border, selection, red, green = nil, nil, nil, nil, nil, nil, nil
	}
	text := lipgloss.NewStyle().Foreground(foreground)
	subtle := lipgloss.NewStyle().Foreground(muted)
	return interactiveStyleSet{
		message: text.Bold(true), label: subtle, value: text.Bold(true),
		status: subtle, cursor: lipgloss.NewStyle().Foreground(accent).Bold(true),
		selected: lipgloss.NewStyle().Foreground(accent).Bold(true),
		content:  text, description: subtle, disabled: subtle,
		error:  lipgloss.NewStyle().Foreground(red).Bold(true),
		button: subtle, help: subtle,
		border:    lipgloss.NewStyle().Foreground(border),
		selection: lipgloss.NewStyle().Background(selection),
		addition:  lipgloss.NewStyle().Foreground(green),
		removal:   lipgloss.NewStyle().Foreground(red), accent: accent,
	}
}

func spacious(width, height int) bool { return width >= 60 && height >= 12 }

// heading includes only decoration that fits comfortably in the current terminal.
func (s interactiveStyleSet) heading(title string, width, height int) string {
	value := ansi.Hardwrap(s.message.Render(title), max(width, 1), true) + "\n"
	if spacious(width, height) {
		value += s.border.Render(strings.Repeat("─", max(width, 1))) + "\n"
	}
	return value
}

func (s interactiveStyleSet) footer(help string, width, height int) string {
	value := s.renderHelp(help)
	if spacious(width, height) {
		value = s.border.Render(strings.Repeat("─", max(width, 1))) + "\n" + value
	}
	return value + "\n"
}

func (s interactiveStyleSet) renderHelp(help string) string {
	// Keep the key brighter than its description without changing the text used
	// to calculate wrapping or the shortcuts users already know.
	lines := strings.Split(strings.TrimSuffix(help, "\n"), "\n")
	for index, line := range lines {
		tokens := strings.Split(line, " • ")
		for tokenIndex, token := range tokens {
			key, description, ok := strings.Cut(token, " ")
			if ok {
				tokens[tokenIndex] = s.content.Bold(true).Render(key) + " " + s.help.Render(description)
			} else {
				tokens[tokenIndex] = s.help.Render(token)
			}
		}
		lines[index] = strings.Join(tokens, s.help.Render(" • "))
	}
	return strings.Join(lines, "\n")
}

func (s interactiveStyleSet) renderFilter(input textinput.Model) string {
	return s.label.Render("Filter: ") + input.View()
}

func (s interactiveStyleSet) textInputStyles() textinput.Styles {
	styles := textinput.DefaultStyles(true)
	state := textinput.StyleState{
		Placeholder: s.disabled, Suggestion: s.description,
		Prompt: s.cursor, Text: s.content,
	}
	styles.Focused, styles.Blurred = state, state
	styles.Cursor.Color = s.accent
	return styles
}

func (s interactiveStyleSet) styleTextInput(input *textinput.Model) {
	input.SetStyles(s.textInputStyles())
}

func (s interactiveStyleSet) styleTextArea(input *textarea.Model) {
	styles := textarea.DefaultStyles(true)
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.EndOfBuffer = s.disabled
	styles.Focused.Placeholder = s.disabled
	styles.Focused.Prompt = s.cursor
	styles.Focused.Text = s.content
	styles.Blurred = styles.Focused
	styles.Cursor.Color = s.accent
	input.SetStyles(styles)
}

func (s interactiveStyleSet) styleSelectionList(model *list.Model) {
	styles := model.Styles
	styles.TitleBar = lipgloss.NewStyle()
	if spacious(model.Width(), model.Height()) {
		styles.TitleBar = styles.TitleBar.BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(s.border.GetForeground()).Width(model.Width()).PaddingBottom(1)
	}
	styles.Title = s.message
	styles.Spinner = s.cursor
	styles.Filter = s.textInputStyles()
	styles.StatusBar = s.status
	styles.StatusEmpty = s.disabled
	styles.StatusBarActiveFilter = s.value
	styles.StatusBarFilterCount = s.description
	styles.NoItems = s.disabled
	styles.PaginationStyle = s.help
	styles.ArabicPagination = s.help
	styles.DividerDot = s.description.SetString(" • ")
	styles.ActivePaginationDot = s.cursor.SetString("•")
	styles.InactivePaginationDot = s.disabled.SetString("•")
	styles.HelpStyle = s.help
	if spacious(model.Width(), model.Height()) {
		styles.HelpStyle = styles.HelpStyle.BorderTop(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(s.border.GetForeground()).Width(model.Width()).PaddingTop(1)
	}
	model.Styles = styles
	model.Help.Styles.ShortKey = s.content.Bold(true)
	model.Help.Styles.FullKey = s.content.Bold(true)
	model.Help.Styles.ShortDesc = s.help
	model.Help.Styles.FullDesc = s.help
	model.Help.Styles.ShortSeparator = s.help
	model.Help.Styles.FullSeparator = s.help
	model.Help.Styles.Ellipsis = s.help
	model.FilterInput.Prompt = "Filter: "
	model.FilterInput.Placeholder = "filter"
	s.styleTextInput(&model.FilterInput)
	model.SetDelegate(newSelectionDelegate(s))
}

func wrapHelp(tokens []string, width int) string {
	width = max(width, 1)
	var result strings.Builder
	lineWidth := 0
	for _, token := range tokens {
		separator := ""
		if lineWidth > 0 {
			separator = " • "
		}
		if lineWidth > 0 && lineWidth+ansi.StringWidth(separator)+ansi.StringWidth(token) > width {
			result.WriteByte('\n')
			separator = ""
			lineWidth = 0
		}
		result.WriteString(separator)
		wrapped := ansi.Hardwrap(token, width, true)
		result.WriteString(wrapped)
		if position := strings.LastIndexByte(wrapped, '\n'); position >= 0 {
			lineWidth = ansi.StringWidth(wrapped[position+1:])
		} else {
			lineWidth += ansi.StringWidth(separator) + ansi.StringWidth(wrapped)
		}
	}
	if len(tokens) > 0 {
		result.WriteByte('\n')
	}
	return result.String()
}

func renderedLines(value string) int {
	return strings.Count(strings.TrimSuffix(value, "\n"), "\n") + 1
}

// fitLines prevents long metadata and paths from causing implicit terminal wraps
// that would invalidate the screen's viewport calculations.
func fitLines(value string, width int) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, max(width, 1), "…")
	}
	return strings.Join(lines, "\n")
}

func (s interactiveStyleSet) inputBorder(width int) lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.accent).Padding(0, 1).Width(max(width, 1)).MaxWidth(max(width, 1))
}

// A trailing newline on a full-height inline view scrolls its title out of the
// terminal. Bubble Tea owns cursor placement between frames.
func screenView(content string, width int) tea.View {
	return tea.NewView(fitLines(strings.TrimSuffix(content, "\n"), width))
}
