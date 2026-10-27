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
	SelectionValidate
	SelectionTree
	SelectionDryRun
)

// SelectionAction describes one action available for a picker option.
type SelectionAction struct {
	Label          string
	Description    string
	Intent         SelectionIntent
	Disabled       bool
	DisabledReason string
}

// Selection contains the selected option and the requested action.
type Selection struct {
	Option Option
	Intent SelectionIntent
	Filter string
}

// SelectionPickerConfig configures a filterable picker with contextual actions.
type SelectionPickerConfig struct {
	Title         string
	Options       []Option
	InitialFilter string
	Notice        string
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
	styles interactiveStyleSet
	base   list.DefaultDelegate
}

func newSelectionDelegate(styles interactiveStyleSet) selectionDelegate {
	base := list.NewDefaultDelegate()
	base.SetHeight(3)
	base.Styles.NormalTitle = styles.content
	base.Styles.NormalDesc = styles.description
	base.Styles.SelectedTitle = styles.selected
	base.Styles.SelectedDesc = styles.description
	base.Styles.DimmedTitle = styles.disabled
	base.Styles.DimmedDesc = styles.disabled
	base.Styles.FilterMatch = styles.selected.Underline(true)
	return selectionDelegate{base: base, styles: styles}
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
		prefix = "› "
	}
	textWidth := max(model.Width()-ansi.StringWidth(prefix), 1)
	title = ansi.Truncate(title, textWidth, "…")
	description := ansi.Truncate(option.Description, textWidth, "…")
	if option.Path != "" {
		description += "\n" + ansi.Truncate(option.Path, textWidth, "…")
	}

	emptyFilter := model.FilterState() == list.Filtering && model.FilterValue() == ""
	titleStyle, descriptionStyle := styles.NormalTitle, styles.NormalDesc
	if option.Disabled || emptyFilter {
		titleStyle, descriptionStyle = styles.DimmedTitle, styles.DimmedDesc
	} else if selected && model.FilterState() != list.Filtering {
		background := delegate.styles.selection.GetBackground()
		titleStyle = styles.SelectedTitle.Background(background)
		descriptionStyle = styles.SelectedDesc.Background(background)
	}
	descriptionPrefix := strings.Repeat(" ", ansi.StringWidth(prefix))
	lines := []string{prefix + title}
	for _, line := range strings.Split(description, "\n") {
		lines = append(lines, descriptionPrefix+line)
	}
	// Rows always occupy the delegate's declared height, even without a path.
	for len(lines) < delegate.Height() {
		lines = append(lines, "")
	}
	for index, line := range lines {
		style := descriptionStyle
		if index == 0 {
			style = titleStyle
		}
		lines[index] = style.Width(model.Width()).Render(line)
	}
	fmt.Fprint(writer, strings.Join(lines, "\n")) //nolint:errcheck
}

type selectionModel struct {
	appearance
	list         list.Model
	selected     Option
	intent       SelectionIntent
	filter       string
	actionMode   bool
	actionOption Option
	actionCursor int
	actionError  string
	width        int
	height       int
	done         bool
	cancelled    bool
}

func newSelectionModel(title string, options []Option) selectionModel {
	return newSelectionModelWithConfig(SelectionPickerConfig{Title: title, Options: options})
}

func newSelectionModelWithConfig(config SelectionPickerConfig) selectionModel {
	appearance := newAppearance()
	options := config.Options
	items := make([]list.Item, len(options))
	for i, option := range options {
		items[i] = listOption{Option: option}
	}
	delegate := newSelectionDelegate(appearance.styles)
	workflowList := list.New(items, delegate, 80, 20)
	workflowList.DisableQuitKeybindings()
	workflowList.SetShowHelp(false)
	appearance.styles.styleSelectionList(&workflowList)
	workflowList.Title = config.Title
	if config.Notice != "" {
		workflowList.NewStatusMessage(config.Notice)
	}
	if config.InitialFilter != "" {
		workflowList.SetFilterText(config.InitialFilter)
	}
	selectDefaultListOption(&workflowList)
	run := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run"))
	actions := key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "actions"))
	printCommand := key.NewBinding(key.WithKeys("shift+enter"), key.WithHelp("shift+enter", "print command"))
	openUI := key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "open UI"))
	openMarketplace := key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "open marketplace"))
	reinstall := key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reinstall"))
	openEditor := key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit"))
	togglePin := key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin"))
	toggleSort := key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort"))
	cancel := key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel"))
	workflowList.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{run, actions, cancel}
	}
	workflowList.AdditionalFullHelpKeys = func() []key.Binding {
		bindings := []key.Binding{run, actions, openUI, printCommand}
		if slices.ContainsFunc(options, func(option Option) bool { return option.URL != "" }) {
			bindings = append(bindings, openMarketplace, reinstall)
		}
		if slices.ContainsFunc(options, func(option Option) bool { return selectionActionEnabled(option, SelectionEditor) }) {
			bindings = append(bindings, openEditor)
		}
		return append(bindings, togglePin, toggleSort, cancel)
	}
	layoutSelectionList(&workflowList, appearance.styles, 80, 20)
	return selectionModel{appearance: appearance, list: workflowList, width: 80, height: 20}
}

func selectDefaultListOption(workflowList *list.Model) {
	for index, item := range workflowList.VisibleItems() {
		option, ok := item.(listOption)
		if ok && option.Default {
			workflowList.Select(index)
			return
		}
	}
}

func (m selectionModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m selectionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.appearance.update(message) {
		layoutSelectionList(&m.list, m.styles, m.width, m.height)
		return m, nil
	}

	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width = max(size.Width, 1)
		m.height = max(size.Height, 1)
		var command tea.Cmd
		m.list, command = m.list.Update(message)
		layoutSelectionList(&m.list, m.styles, m.width, m.height)
		return m, command
	}
	if m.actionMode {
		return m.updateActionMenu(message)
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		if isCancelKey(key) {
			m.cancelled = true
			return m, tea.Quit
		}
		switch key.String() {
		case "a":
			if !m.list.SettingFilter() {
				item, ok := m.list.SelectedItem().(listOption)
				if ok && len(item.Actions) > 0 {
					m.actionMode = true
					m.actionOption = item.Option
					m.actionCursor = firstEnabledAction(item.Actions)
					m.actionError = ""
					return m, nil
				}
			}
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
					intent := SelectionPrimary
					if key.String() == "shift+enter" {
						intent = SelectionAlternate
					} else if key.String() == "u" {
						intent = SelectionUI
					} else if key.String() == "m" {
						intent = SelectionMarketplace
					} else if key.String() == "r" {
						intent = SelectionReinstall
					} else if key.String() == "e" {
						intent = SelectionEditor
					} else if key.String() == "p" {
						intent = SelectionTogglePin
					} else if key.String() == "s" {
						intent = SelectionToggleSort
					}
					if !selectionActionEnabled(item.Option, intent) {
						return m, nil
					}
					m.selected = item.Option
					m.filter = m.list.FilterValue()
					m.intent = intent
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
	layoutSelectionList(&m.list, m.styles, m.width, m.height)
	return m, command
}

func selectionActionEnabled(option Option, intent SelectionIntent) bool {
	return len(option.Actions) == 0 || slices.ContainsFunc(option.Actions, func(action SelectionAction) bool {
		return action.Intent == intent && !action.Disabled
	})
}

func (m selectionModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	if m.actionMode {
		return screenView(m.actionMenuView(), m.width)
	}
	return screenView(m.list.View()+"\n"+selectionListFooter(m.list, m.styles, m.width, m.height), m.width)
}

func firstEnabledAction(actions []SelectionAction) int {
	for index, action := range actions {
		if !action.Disabled {
			return index
		}
	}
	return 0
}

func (m selectionModel) updateActionMenu(message tea.Msg) (tea.Model, tea.Cmd) {
	keyMessage, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if isCancelKey(keyMessage) {
		m.cancelled = true
		return m, tea.Quit
	}
	actions := m.actionOption.Actions
	switch keyMessage.String() {
	case "esc":
		m.actionMode = false
		m.actionError = ""
	case "enter":
		if len(actions) == 0 {
			return m, nil
		}
		action := actions[m.actionCursor]
		if action.Disabled {
			m.actionError = action.DisabledReason
			return m, nil
		}
		m.selected = m.actionOption
		m.intent = action.Intent
		m.filter = m.list.FilterValue()
		m.done = true
		return m, tea.Quit
	default:
		palette := m.actionPalette()
		if cursor, moved := palette.move(keyMessage.String()); moved {
			m.actionCursor = cursor
			m.actionError = ""
		}
	}
	return m, nil
}

func (m selectionModel) actionMenuView() string {
	return m.actionPalette().view()
}

func (m selectionModel) actionPalette() actionPalette {
	return actionPalette{
		styles: m.styles, Title: m.actionOption.Label, Description: m.actionOption.Description, Path: m.actionOption.Path,
		Actions: m.actionOption.Actions, Cursor: m.actionCursor, Error: m.actionError,
		Width: m.width, Height: m.height,
	}
}

type multiSelectionModel struct {
	appearance
	width, height int
	list          list.Model
	options       []Option
	selected      map[int]bool
	title         string
	done          bool
	cancelled     bool
}

func newMultiSelectionModel(title string, options []Option) multiSelectionModel {
	appearance := newAppearance()
	items := make([]list.Item, len(options))
	selected := make(map[int]bool, len(options))
	for index, option := range options {
		selected[index] = option.Default
		items[index] = multiListOption{Option: option, index: index, selected: option.Default}
	}
	delegate := newSelectionDelegate(appearance.styles)
	workflowList := list.New(items, delegate, 80, 20)
	workflowList.DisableQuitKeybindings()
	workflowList.SetShowHelp(false)
	appearance.styles.styleSelectionList(&workflowList)
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
	layoutSelectionList(&workflowList, appearance.styles, 80, 20)
	return multiSelectionModel{appearance: appearance, list: workflowList, options: options, selected: selected, title: title, width: 80, height: 20}
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

func (m multiSelectionModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m multiSelectionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.appearance.update(message) {
		layoutSelectionList(&m.list, m.styles, m.width, m.height)
		return m, nil
	}

	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width, m.height = max(size.Width, 1), max(size.Height, 1)
		layoutSelectionList(&m.list, m.styles, m.width, m.height)
		return m, nil
	}

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
	layoutSelectionList(&m.list, m.styles, m.width, m.height)
	return m, command
}

func (m multiSelectionModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	return screenView(m.list.View()+"\n"+selectionListFooter(m.list, m.styles, m.width, m.height), m.width)
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
	return SelectWithIntentConfig(ctx, input, output, SelectionPickerConfig{Title: title, Options: options})
}

// SelectWithIntentConfig runs a configured filterable list and reports the selected action.
func SelectWithIntentConfig(ctx context.Context, input io.Reader, output io.Writer, config SelectionPickerConfig) (Selection, error) {
	program := tea.NewProgram(newSelectionModelWithConfig(config),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return Selection{}, err
	}
	model := final.(selectionModel)
	if model.cancelled || !model.done {
		return Selection{}, context.Canceled
	}
	return Selection{Option: model.selected, Intent: model.intent, Filter: model.filter}, nil
}

// Select runs a filterable single-selection list and returns the selected option.
func Select(ctx context.Context, input io.Reader, output io.Writer, title string, options []Option) (Option, error) {
	selection, err := SelectWithIntent(ctx, input, output, title, options)
	return selection.Option, err
}

func selectionListFooter(model list.Model, styles interactiveStyleSet, width, height int) string {
	bindings := model.ShortHelp()
	if model.Help.ShowAll {
		bindings = nil
		for _, group := range model.FullHelp() {
			bindings = append(bindings, group...)
		}
	}
	var tokens []string
	for _, binding := range bindings {
		if binding.Enabled() {
			help := binding.Help()
			tokens = append(tokens, help.Key+" "+help.Desc)
		}
	}
	return styles.footer(wrapHelp(tokens, width), width, height)
}

func layoutSelectionList(model *list.Model, styles interactiveStyleSet, width, height int) {
	model.SetSize(width, height)
	styles.styleSelectionList(model)
	model.SetSize(width, max(height-renderedLines(selectionListFooter(*model, styles, width, height)), 1))
}
