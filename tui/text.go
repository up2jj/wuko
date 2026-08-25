package tui

import (
	"context"
	"io"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

type textModel struct {
	input     textinput.Model
	message   string
	required  bool
	width     int
	done      bool
	cancelled bool
	err       string
}

func newTextModel(message, value string, required bool, echoMode textinput.EchoMode, validate textinput.ValidateFunc) textModel {
	input := textinput.New()
	styleInteractiveTextInput(&input)
	input.Prompt = "> "
	input.SetWidth(78)
	input.EchoMode = echoMode
	input.Validate = validate
	input.SetValue(value)
	input.Focus()
	return textModel{input: input, message: message, required: required, width: 80}
}

func (m textModel) Init() tea.Cmd { return textinput.Blink }

func (m textModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width = max(size.Width, 1)
		m.input.SetWidth(max(m.width-2, 1))
		return m, nil
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		if isCancelKey(key) {
			m.cancelled = true
			return m, tea.Quit
		}
		if key.String() == "esc" {
			return m, nil
		}
		switch key.String() {
		case "enter":
			if m.input.Err != nil {
				m.err = m.input.Err.Error()
				return m, nil
			}
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
	if m.err != "" {
		m.err = ""
		if m.input.Err != nil {
			m.err = m.input.Err.Error()
		}
	}
	return m, command
}

func (m textModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(interactiveStyles.message.Render(m.message))
	view.WriteByte('\n')
	view.WriteString(m.input.View())
	view.WriteByte('\n')
	if m.err != "" {
		view.WriteString(interactiveStyles.error.Render(m.err))
		view.WriteByte('\n')
	}
	view.WriteString(renderHelpText(m.help()))
	view.WriteByte('\n')
	return tea.NewView(view.String())
}

func (m textModel) help() string {
	return wrapHelp([]string{"enter confirm", cancelHelp}, m.width)
}

// Text runs an interactive Bubble Tea text prompt with an editable initial value.
func Text(ctx context.Context, input io.Reader, output io.Writer, message, value string, required bool) (string, error) {
	return runText(ctx, input, output, message, value, required, textinput.EchoNormal, nil)
}

// TextWithValidation runs a text prompt with an additional validation function.
func TextWithValidation(ctx context.Context, input io.Reader, output io.Writer, message, value string, required bool, validate func(string) error) (string, error) {
	return runText(ctx, input, output, message, value, required, textinput.EchoNormal, validate)
}

// Password runs an interactive Bubble Tea text prompt that masks the entered value.
func Password(ctx context.Context, input io.Reader, output io.Writer, message string, required bool) (string, error) {
	return runText(ctx, input, output, message, "", required, textinput.EchoPassword, nil)
}

// PasswordWithValidation runs a masked text prompt with a validation function.
func PasswordWithValidation(ctx context.Context, input io.Reader, output io.Writer, message string, required bool, validate func(string) error) (string, error) {
	return runText(ctx, input, output, message, "", required, textinput.EchoPassword, validate)
}

func runText(ctx context.Context, input io.Reader, output io.Writer, message, value string, required bool, echoMode textinput.EchoMode, validate textinput.ValidateFunc) (string, error) {
	program := tea.NewProgram(newTextModel(message, value, required, echoMode, validate),
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
