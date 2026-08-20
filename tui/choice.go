package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type Option struct {
	Label       string
	Description string
	Value       any
}

type choiceModel struct {
	message   string
	options   []Option
	multiple  bool
	required  bool
	cursor    int
	selected  map[int]bool
	result    []int
	done      bool
	cancelled bool
	err       string
}

func newChoiceModel(message string, options []Option, multiple, required bool) choiceModel {
	return choiceModel{message: message, options: options, multiple: multiple, required: required, selected: make(map[int]bool)}
}

func (m choiceModel) Init() tea.Cmd { return nil }

func (m choiceModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		m.cancelled = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.options)-1 {
			m.cursor++
		}
	case "space":
		if m.multiple {
			m.selected[m.cursor] = !m.selected[m.cursor]
			m.err = ""
		}
	case "enter":
		if !m.multiple {
			m.result = []int{m.cursor}
			m.done = true
			return m, tea.Quit
		}
		for i := range m.options {
			if m.selected[i] {
				m.result = append(m.result, i)
			}
		}
		if m.required && len(m.result) == 0 {
			m.err = "select at least one value"
			return m, nil
		}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m choiceModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	fmt.Fprintf(&view, "%s\n", m.message)
	for i, option := range m.options {
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		mark := " "
		if m.multiple {
			mark = "[ ]"
			if m.selected[i] {
				mark = "[x]"
			}
		}
		fmt.Fprintf(&view, "%s %s %s\n", cursor, mark, option.Label)
	}
	if m.err != "" {
		view.WriteString(m.err + "\n")
	}
	if m.multiple {
		view.WriteString("↑/↓ move • space toggle • enter confirm • esc cancel\n")
	} else {
		view.WriteString("↑/↓ move • enter confirm • esc cancel\n")
	}
	return tea.NewView(view.String())
}

// Choose runs an interactive Bubble Tea choice prompt and returns selected indexes in option order.
func Choose(ctx context.Context, input io.Reader, output io.Writer, message string, options []Option, multiple, required bool) ([]int, error) {
	return choose(ctx, input, output, newChoiceModel(message, options, multiple, required))
}

// Confirm runs an interactive yes/no prompt with the configured initial selection.
func Confirm(ctx context.Context, input io.Reader, output io.Writer, message string, defaultValue bool) (bool, error) {
	model := newChoiceModel(message, []Option{{Label: "Yes", Value: true}, {Label: "No", Value: false}}, false, true)
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
	if finalModel.cancelled {
		return nil, context.Canceled
	}
	return finalModel.result, nil
}
