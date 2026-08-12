package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type textModel struct {
	input     textinput.Model
	message   string
	required  bool
	done      bool
	cancelled bool
	err       string
}

func newTextModel(message, defaultValue string, required bool) textModel {
	input := textinput.New()
	input.SetValue(defaultValue)
	input.Focus()
	return textModel{input: input, message: message, required: required}
}

func (m textModel) Init() tea.Cmd { return textinput.Blink }

func (m textModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			if m.required && strings.TrimSpace(m.input.Value()) == "" {
				m.err = "a value is required"
				return m, nil
			}
			m.done = true
			return m, tea.Quit
		}
	}
	var command tea.Cmd
	m.input, command = m.input.Update(message)
	return m, command
}

func (m textModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	view := fmt.Sprintf("%s\n%s\n", m.message, m.input.View())
	if m.err != "" {
		view += m.err + "\n"
	}
	return tea.NewView(view + "enter confirm • esc cancel\n")
}

// Text runs an interactive Bubble Tea text prompt.
func Text(ctx context.Context, input io.Reader, output io.Writer, message, defaultValue string, required bool) (string, error) {
	program := tea.NewProgram(newTextModel(message, defaultValue, required),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return "", err
	}
	model := final.(textModel)
	if model.cancelled {
		return "", context.Canceled
	}
	return model.input.Value(), nil
}
