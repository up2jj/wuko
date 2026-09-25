package tui

import (
	"context"
	"io"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

const (
	textAreaHeight                = 6
	textAreaDefaultWidth          = 80
	textAreaDefaultTerminalHeight = 24
)

type textAreaModel struct {
	input     textarea.Model
	message   string
	required  bool
	validate  func(string) error
	width     int
	height    int
	done      bool
	cancelled bool
	err       string
}

func newTextAreaModel(message, value string, required bool, validate func(string) error) textAreaModel {
	input := textarea.New()
	styleInteractiveTextArea(&input)
	input.Prompt = "> "
	input.ShowLineNumbers = false
	model := textAreaModel{
		input:    input,
		message:  message,
		required: required,
		validate: validate,
		width:    textAreaDefaultWidth,
		height:   textAreaDefaultTerminalHeight,
	}
	model.layout()
	model.input.SetValue(value)
	model.input.Focus()
	return model
}

// layout sizes the text area so the whole prompt fits inside the terminal.
func (m *textAreaModel) layout() {
	m.input.SetWidth(m.width)
	m.input.SetHeight(min(max(m.height-m.chromeLines(), 1), textAreaHeight))
}

// chromeLines counts the rows the view spends around the text area: the
// message, a possible validation error, and the help line or lines.
func (m textAreaModel) chromeLines() int {
	return countLines(m.message, m.width) + 1 + strings.Count(m.help(), "\n")
}

func countLines(text string, width int) int {
	width = max(width, 1)
	lines := 0
	for _, line := range strings.Split(text, "\n") {
		lines += max((utf8.RuneCountInString(line)+width-1)/width, 1)
	}
	return lines
}

func (m textAreaModel) Init() tea.Cmd { return textarea.Blink }

func (m textAreaModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width = max(size.Width, 1)
		m.height = max(size.Height, 1)
		m.layout()
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
		if key.String() == "ctrl+s" {
			value := m.input.Value()
			if m.validate != nil {
				if err := m.validate(value); err != nil {
					m.err = err.Error()
					return m, nil
				}
			}
			if m.required && strings.TrimSpace(value) == "" {
				m.err = "a value is required"
				return m, nil
			}
			m.done = true
			return m, tea.Quit
		}
	}
	var command tea.Cmd
	m.input, command = m.input.Update(message)
	// Any key press clears the message, so it survives until the user reacts:
	// cursor blink ticks and other background messages must not wipe a
	// validation error the user has not addressed yet.
	if _, pressed := message.(tea.KeyPressMsg); pressed {
		m.err = ""
	}
	// The text area keeps clipboard failures in Err forever, so consume it
	// instead of letting it mask every later validation error.
	if m.input.Err != nil {
		m.err = m.input.Err.Error()
		m.input.Err = nil
	}
	return m, command
}

func (m textAreaModel) View() tea.View {
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

func (m textAreaModel) help() string {
	return wrapHelp([]string{"enter newline", "ctrl+s confirm", cancelHelp}, m.width)
}

// TextAreaWithValidation runs a multiline text prompt with an additional validation function.
func TextAreaWithValidation(ctx context.Context, input io.Reader, output io.Writer, message, value string, required bool, validate func(string) error) (string, error) {
	program := tea.NewProgram(newTextAreaModel(message, value, required, validate),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return "", err
	}
	model := final.(textAreaModel)
	if model.cancelled {
		return "", context.Canceled
	}
	return model.input.Value(), nil
}
