package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// SelectionIntent describes whether a list selection requested its primary or alternate action.
type SelectionIntent uint8

const (
	SelectionPrimary SelectionIntent = iota
	SelectionAlternate
	SelectionUI
	SelectionMarketplace
	SelectionReinstall
	SelectionEditor
	SelectionTogglePin
	SelectionToggleSort
)

// Selection contains the selected option and the requested action.
type Selection struct {
	Option Option
	Intent SelectionIntent
}

type listOption struct {
	Option
}

func (item listOption) Title() string       { return item.Label }
func (item listOption) Description() string { return item.Option.Description }
func (item listOption) FilterValue() string {
	return strings.Join([]string{item.Label, item.Option.Description, item.Option.Path}, " ")
}

type multiListOption struct {
	Option
	index    int
	selected bool
}

func (item multiListOption) Title() string {
	marker := "[ ] "
	if item.selected {
		marker = "[x] "
	}
	return marker + item.Label
}

func (item multiListOption) Description() string { return item.Option.Description }
func (item multiListOption) FilterValue() string {
	return strings.Join([]string{item.Label, item.Option.Description, item.Option.Path}, " ")
}

type selectionDelegate struct {
	base list.DefaultDelegate
}

func newSelectionDelegate() selectionDelegate {
	base := list.NewDefaultDelegate()
	base.SetHeight(3)
	base.Styles.NormalTitle = interactiveStyles.content
	base.Styles.NormalDesc = interactiveStyles.description
	base.Styles.SelectedTitle = interactiveStyles.selected
	base.Styles.SelectedDesc = interactiveStyles.description
	base.Styles.DimmedTitle = interactiveStyles.disabled
	base.Styles.DimmedDesc = interactiveStyles.disabled
	base.Styles.FilterMatch = interactiveStyles.selected.Underline(true)
	return selectionDelegate{base: base}
}

func (delegate selectionDelegate) Height() int  { return delegate.base.Height() }
func (delegate selectionDelegate) Spacing() int { return delegate.base.Spacing() }
func (delegate selectionDelegate) Update(message tea.Msg, model *list.Model) tea.Cmd {
	return delegate.base.Update(message, model)
}

func (delegate selectionDelegate) Render(writer io.Writer, model list.Model, index int, item list.Item) {
	var option Option
	var title string
	switch item := item.(type) {
	case listOption:
		option = item.Option
		title = item.Title()
	case multiListOption:
		option = item.Option
		title = item.Title()
	default:
		return
	}
	if model.Width() <= 0 {
		return
	}

	styles := &delegate.base.Styles
	prefix := "  "
	selected := index == model.Index() && model.FilterState() != list.Filtering
	if selected && !option.Disabled {
		prefix = interactiveStyles.cursor.Render("> ")
	}
	textWidth := max(model.Width()-ansi.StringWidth(prefix), 1)
	title = ansi.Truncate(title, textWidth, "…")
	description := ansi.Truncate(option.Description, textWidth, "…")
	if option.Path != "" {
		description += "\n" + option.Path
	}

	emptyFilter := model.FilterState() == list.Filtering && model.FilterValue() == ""
	if option.Disabled || emptyFilter {
		title = styles.DimmedTitle.Render(title)
		description = styles.DimmedDesc.Render(description)
	} else if selected && model.FilterState() != list.Filtering {
		title = styles.SelectedTitle.Render(title)
		description = styles.SelectedDesc.Render(description)
	} else {
		title = styles.NormalTitle.Render(title)
		description = styles.NormalDesc.Render(description)
	}
	descriptionPrefix := strings.Repeat(" ", ansi.StringWidth(prefix))
	fmt.Fprintf(writer, "%s%s\n%s%s", prefix, title, descriptionPrefix, description) //nolint:errcheck
}

type selectionModel struct {
	list      list.Model
	selected  Option
	intent    SelectionIntent
	done      bool
	cancelled bool
}

func newSelectionModel(title string, options []Option) selectionModel {
	items := make([]list.Item, len(options))
	for i, option := range options {
		items[i] = listOption{Option: option}
	}
	delegate := newSelectionDelegate()
	workflowList := list.New(items, delegate, 80, 20)
	styleSelectionList(&workflowList)
	workflowList.Title = title
	for index, option := range options {
		if option.Default {
			workflowList.Select(index)
			break
		}
	}
	run := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run"))
	printCommand := key.NewBinding(key.WithKeys("shift+enter"), key.WithHelp("shift+enter", "print command"))
	openUI := key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "open UI"))
	openMarketplace := key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "open marketplace"))
	reinstall := key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reinstall"))
	openEditor := key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit"))
	togglePin := key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin"))
	toggleSort := key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort"))
	cancel := key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel"))
	workflowList.AdditionalShortHelpKeys = func() []key.Binding {
		bindings := []key.Binding{run, openUI, printCommand}
		if slices.ContainsFunc(options, func(option Option) bool { return option.URL != "" }) {
			bindings = append(bindings, openMarketplace, reinstall)
		}
		return append(bindings, openEditor, togglePin, toggleSort, cancel)
	}
	workflowList.AdditionalFullHelpKeys = func() []key.Binding {
		return workflowList.AdditionalShortHelpKeys()
	}
	return selectionModel{list: workflowList}
}

func (m selectionModel) Init() tea.Cmd { return nil }

func (m selectionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		if isCancelKey(key) {
			m.cancelled = true
			return m, tea.Quit
		}
		switch key.String() {
		case "enter", "shift+enter", "u", "m", "r", "e", "p", "s":
			if !m.list.SettingFilter() {
				item, ok := m.list.SelectedItem().(listOption)
				if ok {
					if key.String() == "m" && item.Option.URL == "" {
						return m, nil
					}
					if key.String() == "r" && item.Option.URL == "" {
						return m, nil
					}
					m.selected = item.Option
					if key.String() == "shift+enter" {
						m.intent = SelectionAlternate
					} else if key.String() == "u" {
						m.intent = SelectionUI
					} else if key.String() == "m" {
						m.intent = SelectionMarketplace
					} else if key.String() == "r" {
						m.intent = SelectionReinstall
					} else if key.String() == "e" {
						m.intent = SelectionEditor
					} else if key.String() == "p" {
						m.intent = SelectionTogglePin
					} else if key.String() == "s" {
						m.intent = SelectionToggleSort
					}
					m.done = true
					return m, tea.Quit
				}
			}
		case "esc":
			if m.list.FilterState() == list.Unfiltered {
				return m, nil
			}
		}
	}
	var command tea.Cmd
	m.list, command = m.list.Update(message)
	return m, command
}

func (m selectionModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	return tea.NewView(m.list.View())
}

type multiSelectionModel struct {
	list      list.Model
	options   []Option
	selected  map[int]bool
	title     string
	done      bool
	cancelled bool
}

func newMultiSelectionModel(title string, options []Option) multiSelectionModel {
	items := make([]list.Item, len(options))
	selected := make(map[int]bool, len(options))
	for index, option := range options {
		selected[index] = option.Default
		items[index] = multiListOption{Option: option, index: index, selected: option.Default}
	}
	delegate := newSelectionDelegate()
	workflowList := list.New(items, delegate, 80, 20)
	styleSelectionList(&workflowList)
	workflowList.Title = multiSelectionTitle(title, selected)
	toggle := key.NewBinding(key.WithKeys(" ", "space"), key.WithHelp("space", "toggle"))
	selectAll := key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "select visible"))
	clear := key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "clear visible"))
	confirm := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "install"))
	cancel := key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel"))
	workflowList.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{toggle, selectAll, clear, confirm, cancel}
	}
	workflowList.AdditionalFullHelpKeys = func() []key.Binding {
		return []key.Binding{toggle, selectAll, clear, confirm, cancel}
	}
	return multiSelectionModel{list: workflowList, options: options, selected: selected, title: title}
}

func multiSelectionTitle(title string, selected map[int]bool) string {
	count := 0
	for _, isSelected := range selected {
		if isSelected {
			count++
		}
	}
	return fmt.Sprintf("%s (selected: %d)", title, count)
}

func (m *multiSelectionModel) updateItem(index int) tea.Cmd {
	return m.list.SetItem(index, multiListOption{Option: m.options[index], index: index, selected: m.selected[index]})
}

func (m *multiSelectionModel) setVisible(selected bool) tea.Cmd {
	var command tea.Cmd
	for _, item := range m.list.VisibleItems() {
		option, ok := item.(multiListOption)
		if !ok || option.Disabled {
			continue
		}
		m.selected[option.index] = selected
		command = m.updateItem(option.index)
	}
	m.list.Title = multiSelectionTitle(m.title, m.selected)
	return command
}

func (m multiSelectionModel) selectedIndexes() []int {
	result := make([]int, 0, len(m.selected))
	for index, selected := range m.selected {
		if selected && !m.options[index].Disabled {
			result = append(result, index)
		}
	}
	slices.Sort(result)
	return result
}

func (m multiSelectionModel) Init() tea.Cmd { return nil }

func (m multiSelectionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyPressMsg); ok {
		if keyMessage.String() == "ctrl+c" {
			m.cancelled = true
			return m, tea.Quit
		}
		if !m.list.SettingFilter() {
			switch keyMessage.String() {
			case " ", "space":
				if option, ok := m.list.SelectedItem().(multiListOption); ok && !option.Disabled {
					m.selected[option.index] = !m.selected[option.index]
					command := m.updateItem(option.index)
					m.list.Title = multiSelectionTitle(m.title, m.selected)
					return m, command
				}
			case "ctrl+a":
				return m, m.setVisible(true)
			case "ctrl+x":
				return m, m.setVisible(false)
			case "enter":
				if len(m.selectedIndexes()) == 0 {
					m.list.Title = m.title + " — select at least one workflow"
					return m, nil
				}
				m.done = true
				return m, tea.Quit
			}
		}
	}
	var command tea.Cmd
	m.list, command = m.list.Update(message)
	return m, command
}

func (m multiSelectionModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	return tea.NewView(m.list.View())
}

// SelectMany runs the same searchable list UI as the bare wuko picker and returns selected indexes.
func SelectMany(ctx context.Context, input io.Reader, output io.Writer, title string, options []Option) ([]int, error) {
	program := tea.NewProgram(newMultiSelectionModel(title, options),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return nil, err
	}
	model := final.(multiSelectionModel)
	if model.cancelled || !model.done {
		return nil, context.Canceled
	}
	return model.selectedIndexes(), nil
}

// SelectWithIntent runs a filterable list and reports the selected action.
func SelectWithIntent(ctx context.Context, input io.Reader, output io.Writer, title string, options []Option) (Selection, error) {
	program := tea.NewProgram(newSelectionModel(title, options),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return Selection{}, err
	}
	model := final.(selectionModel)
	if model.cancelled || !model.done {
		return Selection{}, context.Canceled
	}
	return Selection{Option: model.selected, Intent: model.intent}, nil
}

// Select runs a filterable single-selection list and returns the selected option.
func Select(ctx context.Context, input io.Reader, output io.Writer, title string, options []Option) (Option, error) {
	selection, err := SelectWithIntent(ctx, input, output, title, options)
	return selection.Option, err
}
