package tui

import (
	"context"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// SelectionIntent describes whether a list selection requested its primary or alternate action.
type SelectionIntent uint8

const (
	SelectionPrimary SelectionIntent = iota
	SelectionAlternate
	SelectionUI
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
	return strings.Join([]string{item.Label, item.Option.Description}, " ")
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
	delegate := list.NewDefaultDelegate()
	workflowList := list.New(items, delegate, 80, 20)
	workflowList.Title = title
	run := key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run"))
	printCommand := key.NewBinding(key.WithKeys("shift+enter"), key.WithHelp("shift+enter", "print command"))
	openUI := key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "open UI"))
	workflowList.AdditionalShortHelpKeys = func() []key.Binding { return []key.Binding{run, openUI, printCommand} }
	workflowList.AdditionalFullHelpKeys = func() []key.Binding { return []key.Binding{run, openUI, printCommand} }
	return selectionModel{list: workflowList}
}

func (m selectionModel) Init() tea.Cmd { return nil }

func (m selectionModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter", "shift+enter", "u":
			if !m.list.SettingFilter() {
				item, ok := m.list.SelectedItem().(listOption)
				if ok {
					m.selected = item.Option
					if key.String() == "shift+enter" {
						m.intent = SelectionAlternate
					} else if key.String() == "u" {
						m.intent = SelectionUI
					}
					m.done = true
					return m, tea.Quit
				}
			}
		case "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "esc":
			if m.list.FilterState() == list.Unfiltered {
				m.cancelled = true
				return m, tea.Quit
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
