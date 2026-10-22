package tui

import (
	"context"
	"io"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	textAreaHeight                = 6
	textAreaDefaultWidth          = 80
	textAreaDefaultTerminalHeight = 24
)

type textAreaModel struct {
	appearance
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
	appearance := newAppearance()
	input := textarea.New()
	appearance.styles.styleTextArea(&input)
	input.Prompt = "> "
	input.ShowLineNumbers = false
	model := textAreaModel{appearance: appearance,
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
	m.input.SetWidth(max(m.width-m.inputFrameWidth(), 1))
	m.input.SetHeight(min(max(m.height-m.chromeLines(), 1), textAreaHeight))
}

// chromeLines counts the rows the view spends around the text area: the
// message, a possible validation error, and the help line or lines.
func (m textAreaModel) chromeLines() int {
	errorLines := renderedLines(ansi.Hardwrap(m.err, max(m.width, 1), true))
	return renderedLines(m.styles.heading(m.message, m.width, m.height)) + errorLines + renderedLines(m.styles.footer(m.help(), m.width, m.height)) + m.inputFrameHeight()
}

func (m textAreaModel) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, textarea.Blink) }

func (m textAreaModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.appearance.update(message) {
		m.styles.styleTextArea(&m.input)
		return m, nil
	}
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
					m.layout()
					return m, nil
				}
			}
			if m.required && strings.TrimSpace(value) == "" {
				m.err = "a value is required"
				m.layout()
				return m, nil
			}
			m.done = true
			return m, tea.Quit
		}
	}
	previousError := m.err
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
	if m.err != previousError {
		m.layout()
	}
	return m, command
}

func (m textAreaModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(m.styles.heading(m.message, m.width, m.height))
	input := m.input.View()
	if spacious(m.width, m.height) {
		input = m.styles.inputBorder(m.width).Render(input)
	}
	view.WriteString(input)
	view.WriteByte('\n')
	if m.err != "" {
		view.WriteString(m.styles.error.Render(ansi.Hardwrap(m.err, max(m.width, 1), true)))
		view.WriteByte('\n')
	}
	view.WriteString(m.styles.footer(m.help(), m.width, m.height))
	return screenView(view.String(), m.width)
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

func (m textAreaModel) inputFrameWidth() int {
	if spacious(m.width, m.height) {
		return 4
	}
	return 0
}

func (m textAreaModel) inputFrameHeight() int {
	if spacious(m.width, m.height) {
		return 2
	}
	return 0
}
