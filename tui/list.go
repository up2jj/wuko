package tui

import (
	"context"
	"fmt"
	"io"
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

type selectionDelegate struct {
	base list.DefaultDelegate
}

func newSelectionDelegate() selectionDelegate {
	base := list.NewDefaultDelegate()
	base.SetHeight(3)
	return selectionDelegate{base: base}
}

func (delegate selectionDelegate) Height() int  { return delegate.base.Height() }
func (delegate selectionDelegate) Spacing() int { return delegate.base.Spacing() }
func (delegate selectionDelegate) Update(message tea.Msg, model *list.Model) tea.Cmd {
	return delegate.base.Update(message, model)
}

func (delegate selectionDelegate) Render(writer io.Writer, model list.Model, index int, item list.Item) {
	option, ok := item.(listOption)
	if !ok || model.Width() <= 0 {
		return
	}

	styles := &delegate.base.Styles
	textWidth := model.Width() - styles.NormalTitle.GetPaddingLeft() - styles.NormalTitle.GetPaddingRight()
	title := ansi.Truncate(option.Title(), textWidth, "…")
	description := ansi.Truncate(option.Description(), textWidth, "…")
	if option.Path != "" {
		description += "\n" + option.Path
	}

	selected := index == model.Index()
	emptyFilter := model.FilterState() == list.Filtering && model.FilterValue() == ""
	if emptyFilter {
		title = styles.DimmedTitle.Render(title)
		description = styles.DimmedDesc.Render(description)
	} else if selected && model.FilterState() != list.Filtering {
		title = styles.SelectedTitle.Render(title)
		description = styles.SelectedDesc.Render(description)
	} else {
		title = styles.NormalTitle.Render(title)
		description = styles.NormalDesc.Render(description)
	}
	fmt.Fprintf(writer, "%s\n%s", title, description) //nolint:errcheck
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
	openEditor := key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit"))
	togglePin := key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin"))
	toggleSort := key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort"))
	cancel := key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel"))
	workflowList.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{run, openUI, printCommand, openEditor, togglePin, toggleSort, cancel}
	}
	workflowList.AdditionalFullHelpKeys = func() []key.Binding {
		return []key.Binding{run, openUI, printCommand, openEditor, togglePin, toggleSort, cancel}
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
		case "enter", "shift+enter", "u", "e", "p", "s":
			if !m.list.SettingFilter() {
				item, ok := m.list.SelectedItem().(listOption)
				if ok {
					m.selected = item.Option
					if key.String() == "shift+enter" {
						m.intent = SelectionAlternate
					} else if key.String() == "u" {
						m.intent = SelectionUI
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
