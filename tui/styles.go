package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

var interactiveStyles = struct {
	message     lipgloss.Style
	label       lipgloss.Style
	value       lipgloss.Style
	status      lipgloss.Style
	cursor      lipgloss.Style
	selected    lipgloss.Style
	content     lipgloss.Style
	description lipgloss.Style
	disabled    lipgloss.Style
	error       lipgloss.Style
	button      lipgloss.Style
	help        lipgloss.Style
}{
	message:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	label:       lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
	value:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
	status:      lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	cursor:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
	selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	content:     lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
	description: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
	disabled:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	error:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")),
	button:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	help:        lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
}

func interactiveTextInputStyles() textinput.Styles {
	styles := textinput.DefaultStyles(true)
	styles.Focused = textinput.StyleState{
		Placeholder: interactiveStyles.disabled,
		Suggestion:  interactiveStyles.description,
		Prompt:      interactiveStyles.cursor,
		Text:        interactiveStyles.content,
	}
	styles.Blurred = textinput.StyleState{
		Placeholder: interactiveStyles.disabled,
		Suggestion:  interactiveStyles.description,
		Prompt:      interactiveStyles.cursor,
		Text:        interactiveStyles.content,
	}
	styles.Cursor.Color = lipgloss.Color("81")
	return styles
}

func styleInteractiveTextInput(input *textinput.Model) {
	input.SetStyles(interactiveTextInputStyles())
}

func styleSelectionList(model *list.Model) {
	styles := model.Styles
	styles.TitleBar = lipgloss.NewStyle().PaddingBottom(1)
	styles.Title = interactiveStyles.message
	styles.Spinner = interactiveStyles.cursor
	styles.Filter = interactiveTextInputStyles()
	styles.StatusBar = interactiveStyles.status
	styles.StatusEmpty = interactiveStyles.disabled
	styles.StatusBarActiveFilter = interactiveStyles.value
	styles.StatusBarFilterCount = interactiveStyles.description
	styles.NoItems = interactiveStyles.disabled
	styles.PaginationStyle = interactiveStyles.help
	styles.ArabicPagination = interactiveStyles.help
	styles.DividerDot = interactiveStyles.description.SetString(" • ")
	styles.ActivePaginationDot = interactiveStyles.cursor.SetString("•")
	styles.InactivePaginationDot = interactiveStyles.disabled.SetString("•")
	styles.HelpStyle = interactiveStyles.help
	model.Styles = styles

	model.FilterInput.Prompt = "Filter: "
	model.FilterInput.Placeholder = "filter"
	styleInteractiveTextInput(&model.FilterInput)
}

func renderFilter(input textinput.Model) string {
	return interactiveStyles.label.Render("Filter: ") + input.View()
}

func renderHelpText(help string) string {
	return interactiveStyles.help.Render(strings.TrimSuffix(help, "\n"))
}

func wrapHelp(tokens []string, width int) string {
	width = max(width, 1)
	var result strings.Builder
	lineWidth := 0
	for index, token := range tokens {
		separator := ""
		if lineWidth > 0 {
			separator = " • "
		}
		tokenWidth := utf8.RuneCountInString(token)
		separatorWidth := utf8.RuneCountInString(separator)
		if lineWidth > 0 && lineWidth+separatorWidth+tokenWidth > width {
			result.WriteByte('\n')
			separator = ""
			separatorWidth = 0
			lineWidth = 0
		}
		result.WriteString(separator)
		result.WriteString(token)
		lineWidth += separatorWidth + tokenWidth
		if index == len(tokens)-1 {
			result.WriteByte('\n')
		}
	}
	return result.String()
}
