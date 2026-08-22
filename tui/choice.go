package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"
)

type Option struct {
	Label       string
	Description string
	Value       any
}

// ChoicePickerConfig configures an interactive selection list.
type ChoicePickerConfig struct {
	Message  string
	Options  []Option
	Multiple bool
	Required bool
}

var choiceStyles = struct {
	message     lipgloss.Style
	status      lipgloss.Style
	cursor      lipgloss.Style
	selected    lipgloss.Style
	description lipgloss.Style
	error       lipgloss.Style
	help        lipgloss.Style
}{
	message:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	status:      lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	cursor:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
	selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	description: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
	error:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")),
	help:        lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
}

type choiceItem struct {
	index       int
	label       string
	description string
	none        bool
}

type choiceModel struct {
	config    ChoicePickerConfig
	items     []choiceItem
	visible   []choiceItem
	cursor    int
	selected  map[int]bool
	order     []int
	filter    textinput.Model
	filtering bool
	width     int
	height    int
	result    []int
	done      bool
	cancelled bool
	err       string
}

func newChoiceModel(config ChoicePickerConfig) choiceModel {
	items := make([]choiceItem, 0, len(config.Options)+1)
	if !config.Required && !config.Multiple {
		items = append(items, choiceItem{index: -1, label: "(none)", description: "select no value", none: true})
	}
	for index, option := range config.Options {
		items = append(items, choiceItem{index: index, label: option.Label, description: option.Description})
	}
	filter := textinput.New()
	filter.Prompt = "/"
	filter.Placeholder = "filter"
	filter.SetWidth(40)
	model := choiceModel{
		config: config, items: items, selected: make(map[int]bool), filter: filter,
		width: 80, height: 24,
	}
	model.refreshVisible()
	return model
}

func (m choiceModel) Init() tea.Cmd { return nil }

func (m choiceModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(message.Width, 1)
		m.height = max(message.Height, 1)
		m.filter.SetWidth(max(m.width-2, 1))
		return m, nil
	case tea.KeyPressMsg:
		if message.String() == "ctrl+c" {
			m.cancelled = true
			return m, tea.Quit
		}
		if m.filtering {
			switch message.String() {
			case "esc":
				m.filter.SetValue("")
				m.filter.Blur()
				m.filtering = false
				m.refreshVisible()
				return m, nil
			case "enter":
				m.filter.Blur()
				m.filtering = false
				return m, nil
			}
			var command tea.Cmd
			m.filter, command = m.filter.Update(message)
			m.refreshVisible()
			return m, command
		}
		return m.updateKey(message)
	}
	return m, nil
}

func (m choiceModel) updateKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.cancelled = true
		return m, tea.Quit
	case "/":
		m.filtering = true
		return m, m.filter.Focus()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = max(len(m.visible)-1, 0)
	case "pgup":
		m.cursor = max(m.cursor-m.pageSize(), 0)
	case "pgdown":
		m.cursor = min(m.cursor+m.pageSize(), max(len(m.visible)-1, 0))
	case "space":
		if m.config.Multiple {
			m.toggleSelected()
		}
	case "enter":
		if m.config.Multiple {
			if m.config.Required && len(m.order) == 0 {
				m.err = "select at least one value"
				return m, nil
			}
			m.result = slices.Clone(m.order)
			m.done = true
			return m, tea.Quit
		}
		if len(m.visible) == 0 {
			m.err = "no value is available"
			return m, nil
		}
		item := m.visible[m.cursor]
		if !item.none {
			m.result = []int{item.index}
		}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *choiceModel) toggleSelected() {
	if len(m.visible) == 0 {
		return
	}
	index := m.visible[m.cursor].index
	if m.selected[index] {
		delete(m.selected, index)
		position := slices.Index(m.order, index)
		if position >= 0 {
			m.order = slices.Delete(m.order, position, position+1)
		}
	} else {
		m.selected[index] = true
		m.order = append(m.order, index)
	}
	m.err = ""
}

func (m *choiceModel) refreshVisible() {
	query := strings.TrimSpace(m.filter.Value())
	if query == "" {
		m.visible = slices.Clone(m.items)
	} else {
		search := make([]string, len(m.items))
		for index, item := range m.items {
			search[index] = item.label + " " + item.description
		}
		matches := fuzzy.Find(query, search)
		m.visible = make([]choiceItem, 0, len(matches))
		for _, match := range matches {
			m.visible = append(m.visible, m.items[match.Index])
		}
	}
	m.cursor = min(m.cursor, max(len(m.visible)-1, 0))
}

func (m choiceModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(choiceStyles.message.Render(m.config.Message))
	if m.config.Multiple {
		view.WriteString(choiceStyles.status.Render(fmt.Sprintf(" • selected: %d", len(m.order))))
	}
	view.WriteByte('\n')
	if m.filtering || m.filter.Value() != "" {
		view.WriteString("Filter: " + m.filter.View() + "\n")
	}

	start, end := m.visibleRange()
	if len(m.visible) == 0 {
		view.WriteString("  (no matching choices)\n")
	}
	for index := start; index < end; index++ {
		item := m.visible[index]
		cursor := " "
		if index == m.cursor {
			cursor = choiceStyles.cursor.Render(">")
		}
		mark := " "
		if m.config.Multiple {
			mark = "[ ]"
			if m.selected[item.index] {
				mark = choiceStyles.selected.Render("[x]")
			}
		}
		label := item.label
		if index == m.cursor {
			label = choiceStyles.selected.Render(label)
		}
		fmt.Fprintf(&view, "%s %s %s", cursor, mark, label)
		if item.description != "" {
			fmt.Fprintf(&view, " %s %s", choiceStyles.description.Render("—"), choiceStyles.description.Render(item.description))
		}
		view.WriteByte('\n')
	}
	if m.err != "" {
		view.WriteString(choiceStyles.error.Render(m.err) + "\n")
	}
	view.WriteString(choiceStyles.help.Render(strings.TrimSuffix(m.help(), "\n")))
	view.WriteByte('\n')
	return tea.NewView(view.String())
}

func (m choiceModel) visibleRange() (int, int) {
	page := m.pageSize()
	if len(m.visible) <= page {
		return 0, len(m.visible)
	}
	start := m.cursor - page/2
	start = max(start, 0)
	start = min(start, len(m.visible)-page)
	return start, start + page
}

func (m choiceModel) pageSize() int {
	headerLines := 1
	if m.filtering || m.filter.Value() != "" {
		headerLines++
	}
	if m.err != "" {
		headerLines++
	}
	helpLines := strings.Count(m.help(), "\n")
	return max(m.height-headerLines-helpLines, 1)
}

func (m choiceModel) help() string {
	var tokens []string
	if m.filtering {
		tokens = []string{"type filter", "enter apply", "esc clear", "ctrl+c cancel"}
	} else if m.config.Multiple {
		tokens = []string{"↑/↓ move", "space toggle", "enter confirm", "/ filter", "esc cancel"}
	} else {
		tokens = []string{"↑/↓ move", "enter select", "/ filter", "esc cancel"}
	}
	return wrapHelp(tokens, m.width)
}

// Choose runs an interactive Bubble Tea choice prompt and returns indexes in selection order.
func Choose(ctx context.Context, input io.Reader, output io.Writer, config ChoicePickerConfig) ([]int, error) {
	return choose(ctx, input, output, newChoiceModel(config))
}

// Confirm runs an interactive yes/no prompt with the configured initial selection.
func Confirm(ctx context.Context, input io.Reader, output io.Writer, message string, defaultValue bool) (bool, error) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: message, Options: []Option{{Label: "Yes", Value: true}, {Label: "No", Value: false}}, Required: true,
	})
	if !defaultValue {
		model.cursor = 1
	}
	indexes, err := choose(ctx, input, output, model)
	if err != nil {
		return false, err
	}
	if len(indexes) != 1 {
		return false, fmt.Errorf("confirmation ended without a selection")
	}
	return indexes[0] == 0, nil
}

func choose(ctx context.Context, input io.Reader, output io.Writer, model choiceModel) ([]int, error) {
	program := tea.NewProgram(model,
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return nil, err
	}
	finalModel := final.(choiceModel)
	if finalModel.cancelled || !finalModel.done {
		return nil, context.Canceled
	}
	return finalModel.result, nil
}
