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
	Label          string
	Description    string
	Value          any
	Disabled       bool
	DisabledReason string
	Default        bool
}

// ChoicePickerConfig configures an interactive selection list.
type ChoicePickerConfig struct {
	Message     string
	Options     []Option
	Multiple    bool
	Required    bool
	MinSelected *int
	MaxSelected *int
}

var choiceStyles = struct {
	message     lipgloss.Style
	status      lipgloss.Style
	cursor      lipgloss.Style
	selected    lipgloss.Style
	description lipgloss.Style
	disabled    lipgloss.Style
	error       lipgloss.Style
	help        lipgloss.Style
}{
	message:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	status:      lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	cursor:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
	selected:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	description: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
	disabled:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	error:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")),
	help:        lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
}

type choiceItem struct {
	index       int
	label       string
	description string
	disabled    bool
	reason      string
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
		items = append(items, choiceItem{
			index: index, label: option.Label, description: option.Description,
			disabled: option.Disabled, reason: option.DisabledReason,
		})
	}
	filter := textinput.New()
	filter.Prompt = "/"
	filter.Placeholder = "filter"
	filter.SetWidth(40)
	model := choiceModel{
		config: config, items: items, selected: make(map[int]bool), filter: filter,
		width: 80, height: 24,
	}
	for index, option := range config.Options {
		if !option.Default {
			continue
		}
		if config.Multiple {
			model.selected[index] = true
			model.order = append(model.order, index)
			continue
		}
		model.cursor = index
		if !config.Required {
			model.cursor++
		}
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
	case "ctrl+a":
		if m.config.Multiple {
			m.selectAllVisible()
		}
	case "ctrl+x":
		if m.config.Multiple {
			m.clearVisible()
		}
	case "enter":
		if m.config.Multiple {
			if len(m.order) < m.minimum() {
				m.err = fmt.Sprintf("select at least %d values", m.minimum())
				return m, nil
			}
			if maximum := m.maximum(); maximum != nil && len(m.order) > *maximum {
				m.err = fmt.Sprintf("select at most %d values", *maximum)
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
		if item.disabled {
			m.err = item.reason
			return m, nil
		}
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
	item := m.visible[m.cursor]
	if item.disabled {
		m.err = item.reason
		return
	}
	index := item.index
	if m.selected[index] {
		delete(m.selected, index)
		position := slices.Index(m.order, index)
		if position >= 0 {
			m.order = slices.Delete(m.order, position, position+1)
		}
	} else {
		if maximum := m.maximum(); maximum != nil && len(m.order) >= *maximum {
			m.err = fmt.Sprintf("select at most %d values", *maximum)
			return
		}
		m.selected[index] = true
		m.order = append(m.order, index)
	}
	m.err = ""
}

func (m *choiceModel) selectAllVisible() {
	for _, item := range m.visible {
		if item.none || item.disabled || m.selected[item.index] {
			continue
		}
		if maximum := m.maximum(); maximum != nil && len(m.order) >= *maximum {
			break
		}
		m.selected[item.index] = true
		m.order = append(m.order, item.index)
	}
	m.err = ""
}

func (m *choiceModel) clearVisible() {
	visible := make(map[int]struct{}, len(m.visible))
	for _, item := range m.visible {
		visible[item.index] = struct{}{}
		delete(m.selected, item.index)
	}
	m.order = slices.DeleteFunc(m.order, func(index int) bool {
		_, ok := visible[index]
		return ok
	})
	m.err = ""
}

func (m *choiceModel) refreshVisible() {
	query := strings.TrimSpace(m.filter.Value())
	if query == "" {
		m.visible = slices.Clone(m.items)
	} else {
		search := make([]string, len(m.items))
		for index, item := range m.items {
			search[index] = strings.Join([]string{item.label, item.description, item.reason}, " ")
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
		status := fmt.Sprintf(" • selected: %d", len(m.order))
		if minimum := m.minimum(); minimum > 0 || m.config.MinSelected != nil {
			status += fmt.Sprintf(" • min: %d", minimum)
		}
		if maximum := m.maximum(); maximum != nil {
			status += fmt.Sprintf(" • max: %d", *maximum)
		}
		view.WriteString(choiceStyles.status.Render(status))
	}
	view.WriteByte('\n')
	if m.filtering || m.filter.Value() != "" {
		view.WriteString("Filter: " + m.filter.View() + "\n")
	}

	start, end := m.visibleRange()
	if len(m.visible) == 0 {
		view.WriteString(choiceStyles.disabled.Render("  (no matching choices)") + "\n")
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
		if item.disabled {
			label = choiceStyles.disabled.Render(label)
		} else if index == m.cursor {
			label = choiceStyles.selected.Render(label)
		}
		fmt.Fprintf(&view, "%s %s %s", cursor, mark, label)
		if item.description != "" {
			fmt.Fprintf(&view, " %s %s", choiceStyles.description.Render("—"), choiceStyles.description.Render(item.description))
		}
		if item.disabled {
			separator := "—"
			if item.description != "" {
				separator = "•"
			}
			fmt.Fprintf(&view, " %s %s", choiceStyles.disabled.Render(separator), choiceStyles.disabled.Render("disabled: "+item.reason))
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
		tokens = []string{"↑/↓ move", "space toggle", "ctrl+a select all", "ctrl+x clear", "enter confirm", "/ filter", "esc cancel"}
	} else {
		tokens = []string{"↑/↓ move", "enter select", "/ filter", "esc cancel"}
	}
	return wrapHelp(tokens, m.width)
}

func (m choiceModel) minimum() int {
	if m.config.MinSelected != nil || m.config.MaxSelected != nil {
		if m.config.MinSelected != nil {
			return *m.config.MinSelected
		}
		return 0
	}
	if m.config.Required {
		return 1
	}
	return 0
}

func (m choiceModel) maximum() *int { return m.config.MaxSelected }

func validateChoicePickerConfig(config ChoicePickerConfig) error {
	if (config.MinSelected != nil || config.MaxSelected != nil) && !config.Multiple {
		return fmt.Errorf("selection bounds require multiple choice mode")
	}
	if config.MinSelected != nil && *config.MinSelected < 0 {
		return fmt.Errorf("minimum selected cannot be negative")
	}
	if config.MaxSelected != nil && *config.MaxSelected < 0 {
		return fmt.Errorf("maximum selected cannot be negative")
	}
	if config.MinSelected != nil && config.MaxSelected != nil && *config.MinSelected > *config.MaxSelected {
		return fmt.Errorf("minimum selected cannot exceed maximum selected")
	}

	enabled := 0
	defaults := 0
	for index, option := range config.Options {
		if option.Disabled {
			if strings.TrimSpace(option.DisabledReason) == "" {
				return fmt.Errorf("choice %d is disabled without a reason", index+1)
			}
			if option.Default {
				return fmt.Errorf("choice %d cannot be both disabled and default", index+1)
			}
		} else {
			enabled++
		}
		if option.Default {
			defaults++
		}
	}
	if !config.Multiple && defaults > 1 {
		return fmt.Errorf("single choice mode allows at most one default")
	}
	model := choiceModel{config: config}
	if model.minimum() > enabled {
		return fmt.Errorf("minimum selected %d exceeds %d enabled choices", model.minimum(), enabled)
	}
	if maximum := model.maximum(); maximum != nil && defaults > *maximum {
		return fmt.Errorf("%d default choices exceed maximum selected %d", defaults, *maximum)
	}
	return nil
}

// Choose runs an interactive Bubble Tea choice prompt and returns indexes in selection order.
func Choose(ctx context.Context, input io.Reader, output io.Writer, config ChoicePickerConfig) ([]int, error) {
	if err := validateChoicePickerConfig(config); err != nil {
		return nil, err
	}
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
