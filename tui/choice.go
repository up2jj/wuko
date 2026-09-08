package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

type Option struct {
	Label          string
	Description    string
	Path           string
	URL            string
	Value          any
	Disabled       bool
	DisabledReason string
	Default        bool
	Actions        []SelectionAction
}

// ChoiceMarkerKind identifies a display-only row in a choice picker.
type ChoiceMarkerKind uint8

const (
	ChoiceMarkerSection ChoiceMarkerKind = iota + 1
	ChoiceMarkerSeparator
)

// ChoiceMarker inserts a display-only row before an option index.
type ChoiceMarker struct {
	Kind   ChoiceMarkerKind
	Before int
	Label  string
}

// ChoicePickerConfig configures an interactive selection list.
type ChoicePickerConfig struct {
	Message     string
	Options     []Option
	Markers     []ChoiceMarker
	Multiple    bool
	SelectAll   bool
	Required    bool
	MinSelected *int
	MaxSelected *int
}

type choiceItem struct {
	index       int
	label       string
	description string
	disabled    bool
	reason      string
	none        bool
	block       int
}

type choiceBlock struct {
	section         string
	separatorBefore bool
}

type choiceRowKind uint8

const (
	choiceRowOption choiceRowKind = iota
	choiceRowSection
	choiceRowSeparator
)

type choiceRow struct {
	kind         choiceRowKind
	item         choiceItem
	visibleIndex int
	label        string
}

type choiceModel struct {
	config    ChoicePickerConfig
	items     []choiceItem
	visible   []choiceItem
	blocks    []choiceBlock
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
		items = append(items, choiceItem{index: -1, label: "(none)", description: "select no value", none: true, block: -1})
	}
	blocks := []choiceBlock{{}}
	markerIndex := 0
	blockOptions := 0
	for index, option := range config.Options {
		for markerIndex < len(config.Markers) && config.Markers[markerIndex].Before == index {
			marker := config.Markers[markerIndex]
			switch marker.Kind {
			case ChoiceMarkerSeparator:
				blocks = append(blocks, choiceBlock{separatorBefore: true})
				blockOptions = 0
			case ChoiceMarkerSection:
				if blockOptions == 0 {
					blocks[len(blocks)-1].section = strings.TrimSpace(marker.Label)
				} else {
					blocks = append(blocks, choiceBlock{section: strings.TrimSpace(marker.Label)})
					blockOptions = 0
				}
			}
			markerIndex++
		}
		items = append(items, choiceItem{
			index: index, label: option.Label, description: option.Description,
			disabled: option.Disabled, reason: option.DisabledReason, block: len(blocks) - 1,
		})
		blockOptions++
	}
	filter := textinput.New()
	filter.Prompt = "/"
	filter.Placeholder = "filter"
	styleInteractiveTextInput(&filter)
	filter.SetWidth(40)
	model := choiceModel{
		config: config, items: items, blocks: blocks, selected: make(map[int]bool), filter: filter,
		width: 80, height: 24,
	}
	for index, option := range config.Options {
		if config.Multiple && config.SelectAll && !option.Disabled {
			model.selected[index] = true
			model.order = append(model.order, index)
		}
		if !option.Default {
			continue
		}
		if config.Multiple {
			if model.selected[index] {
				continue
			}
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
		if isCancelKey(message) {
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
		m.movePage(-1)
	case "pgdown":
		m.movePage(1)
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
	} else if len(m.config.Markers) > 0 {
		matchingSections := make(map[int]bool)
		for index, block := range m.blocks {
			if block.section != "" && fuzzyMatches(query, block.section) {
				matchingSections[index] = true
			}
		}
		m.visible = make([]choiceItem, 0, len(m.items))
		for _, item := range m.items {
			metadata := strings.Join([]string{item.label, item.description, item.reason}, " ")
			if matchingSections[item.block] || fuzzyMatches(query, metadata) {
				m.visible = append(m.visible, item)
			}
		}
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

func fuzzyMatches(query, value string) bool {
	return len(fuzzy.FindNoSort(query, []string{value})) > 0
}

func (m choiceModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(interactiveStyles.message.Render(m.config.Message))
	if m.config.Multiple {
		status := fmt.Sprintf(" • selected: %d", len(m.order))
		if minimum := m.minimum(); minimum > 0 || m.config.MinSelected != nil {
			status += fmt.Sprintf(" • min: %d", minimum)
		}
		if maximum := m.maximum(); maximum != nil {
			status += fmt.Sprintf(" • max: %d", *maximum)
		}
		view.WriteString(interactiveStyles.status.Render(status))
	}
	view.WriteByte('\n')
	if m.filtering || m.filter.Value() != "" {
		view.WriteString(renderFilter(m.filter) + "\n")
	}

	rows := m.displayRows()
	start, end := m.visibleRange(rows)
	if len(m.visible) == 0 {
		view.WriteString(interactiveStyles.disabled.Render("  (no matching choices)") + "\n")
	}
	for _, row := range rows[start:end] {
		if row.kind == choiceRowSection {
			view.WriteString(renderChoiceRule(row.label, m.width, interactiveStyles.label.Bold(true)))
			view.WriteByte('\n')
			continue
		}
		if row.kind == choiceRowSeparator {
			view.WriteString(renderChoiceRule("", m.width, interactiveStyles.disabled))
			view.WriteByte('\n')
			continue
		}
		item := row.item
		cursor := " "
		if row.visibleIndex == m.cursor {
			cursor = interactiveStyles.cursor.Render(">")
		}
		mark := " "
		if m.config.Multiple {
			mark = "[ ]"
			if m.selected[item.index] {
				mark = interactiveStyles.selected.Render("[x]")
			}
		}
		label := item.label
		if item.disabled {
			label = interactiveStyles.disabled.Render(label)
		} else if row.visibleIndex == m.cursor {
			label = interactiveStyles.selected.Render(label)
		}
		fmt.Fprintf(&view, "%s %s %s", cursor, mark, label)
		if item.description != "" {
			fmt.Fprintf(&view, " %s %s", interactiveStyles.description.Render("—"), interactiveStyles.description.Render(item.description))
		}
		if item.disabled {
			separator := "—"
			if item.description != "" {
				separator = "•"
			}
			fmt.Fprintf(&view, " %s %s", interactiveStyles.disabled.Render(separator), interactiveStyles.disabled.Render("disabled: "+item.reason))
		}
		view.WriteByte('\n')
	}
	if m.err != "" {
		view.WriteString(interactiveStyles.error.Render(m.err) + "\n")
	}
	view.WriteString(renderHelpText(m.help()))
	view.WriteByte('\n')
	return tea.NewView(view.String())
}

func (m choiceModel) displayRows() []choiceRow {
	rows := make([]choiceRow, 0, len(m.visible)+len(m.config.Markers))
	previousBlock := -2
	for visibleIndex, item := range m.visible {
		if item.block >= 0 && item.block != previousBlock {
			// previousBlock is negative before the first block row and for the synthetic
			// "(none)" row, neither of which is a choice a separator can sit after.
			if previousBlock >= 0 && m.separatorBetween(previousBlock, item.block) {
				rows = append(rows, choiceRow{kind: choiceRowSeparator})
			}
			if section := m.blocks[item.block].section; section != "" {
				rows = append(rows, choiceRow{kind: choiceRowSection, label: section})
			}
		}
		rows = append(rows, choiceRow{kind: choiceRowOption, item: item, visibleIndex: visibleIndex})
		previousBlock = item.block
	}
	return rows
}

func (m choiceModel) separatorBetween(previous, current int) bool {
	for index := previous + 1; index <= current && index < len(m.blocks); index++ {
		if m.blocks[index].separatorBefore {
			return true
		}
	}
	return false
}

func (m choiceModel) visibleRange(rows []choiceRow) (int, int) {
	page := m.pageSize()
	if len(rows) <= page {
		return 0, len(rows)
	}
	cursorRow := m.cursorRow(rows)
	start := cursorRow - page/2
	start = max(start, 0)
	start = min(start, len(rows)-page)
	return start, start + page
}

func (m choiceModel) cursorRow(rows []choiceRow) int {
	index := slices.IndexFunc(rows, func(row choiceRow) bool {
		return row.kind == choiceRowOption && row.visibleIndex == m.cursor
	})
	return max(index, 0)
}

func (m *choiceModel) movePage(direction int) {
	if len(m.visible) == 0 {
		return
	}
	rows := m.displayRows()
	target := min(max(m.cursorRow(rows)+direction*m.pageSize(), 0), len(rows)-1)
	if direction < 0 {
		for index := target; index >= 0; index-- {
			if rows[index].kind == choiceRowOption {
				m.cursor = rows[index].visibleIndex
				return
			}
		}
		m.cursor = 0
		return
	}
	for index := target; index < len(rows); index++ {
		if rows[index].kind == choiceRowOption {
			m.cursor = rows[index].visibleIndex
			return
		}
	}
	m.cursor = len(m.visible) - 1
}

func renderChoiceRule(label string, width int, style interface{ Render(...string) string }) string {
	width = max(width, 1)
	if label == "" {
		return style.Render(strings.Repeat("─", width))
	}
	prefix := "── "
	text := ansi.Truncate(prefix+label+" ", width, "")
	rule := text + strings.Repeat("─", max(width-ansi.StringWidth(text), 0))
	return style.Render(rule)
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
		tokens = []string{"type filter", "enter apply", "esc clear", cancelHelp}
	} else if m.config.Multiple {
		tokens = []string{"↑/↓ move", "space toggle", "ctrl+a select all", "ctrl+x clear", "enter confirm", "/ filter", cancelHelp}
	} else {
		tokens = []string{"↑/↓ move", "enter select", "/ filter", cancelHelp}
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
	if config.SelectAll && !config.Multiple {
		return fmt.Errorf("select all requires multiple choice mode")
	}
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
	if err := validateChoiceMarkers(config.Options, config.Markers); err != nil {
		return err
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
	if config.SelectAll && config.MaxSelected != nil && enabled > *config.MaxSelected {
		return fmt.Errorf("select all would select %d choices, exceeding maximum selected %d", enabled, *config.MaxSelected)
	}
	if maximum := model.maximum(); maximum != nil && defaults > *maximum {
		return fmt.Errorf("%d default choices exceed maximum selected %d", defaults, *maximum)
	}
	return nil
}

func validateChoiceMarkers(options []Option, markers []ChoiceMarker) error {
	previousBefore := -1
	var previousKind ChoiceMarkerKind
	for index, marker := range markers {
		position := index + 1
		if marker.Before < previousBefore {
			return fmt.Errorf("choice marker %d is out of order", position)
		}
		if marker.Before < 0 || marker.Before >= len(options) {
			return fmt.Errorf("choice marker %d must precede a choice", position)
		}
		samePosition := marker.Before == previousBefore
		switch marker.Kind {
		case ChoiceMarkerSection:
			if strings.TrimSpace(marker.Label) == "" {
				return fmt.Errorf("choice section %d has an empty label", position)
			}
			if samePosition && previousKind != ChoiceMarkerSeparator {
				return fmt.Errorf("choice section %d has no choices", position)
			}
		case ChoiceMarkerSeparator:
			if marker.Label != "" {
				return fmt.Errorf("choice separator %d cannot have a label", position)
			}
			if marker.Before == 0 {
				return fmt.Errorf("choice separator %d cannot be first", position)
			}
			if samePosition {
				return fmt.Errorf("choice separator %d has no choices before it", position)
			}
		default:
			return fmt.Errorf("choice marker %d has an unknown kind", position)
		}
		previousBefore = marker.Before
		previousKind = marker.Kind
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
