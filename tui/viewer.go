package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TextViewerConfig configures a scrollable read-only text view.
type TextViewerConfig struct {
	Title   string
	Content string
	Failed  bool
}

type textViewerModel struct {
	config    TextViewerConfig
	content   string
	lines     []string
	width     int
	height    int
	vertical  int
	done      bool
	cancelled bool
}

func newTextViewerModel(config TextViewerConfig) textViewerModel {
	content := sanitizeReviewContent(config.Content)
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}
	model := textViewerModel{config: config, content: content, width: 80, height: 24}
	model.layout()
	return model
}

func (m textViewerModel) Init() tea.Cmd { return nil }

func (m textViewerModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(message.Width, 1)
		m.height = max(message.Height, 1)
		m.layout()
	case tea.KeyPressMsg:
		if isCancelKey(message) {
			m.cancelled = true
			return m, tea.Quit
		}
		switch message.String() {
		case "up", "k":
			m.vertical = max(m.vertical-1, 0)
		case "down", "j":
			m.vertical = min(m.vertical+1, m.maxVertical())
		case "pgup":
			m.vertical = max(m.vertical-m.pageSize(), 0)
		case "pgdown":
			m.vertical = min(m.vertical+m.pageSize(), m.maxVertical())
		case "home":
			m.vertical = 0
		case "end":
			m.vertical = m.maxVertical()
		case "enter", "esc":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m textViewerModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(interactiveStyles.message.Render(m.config.Title))
	status := "complete"
	style := interactiveStyles.status
	if m.config.Failed {
		status = "failed"
		style = interactiveStyles.error
	}
	view.WriteString(style.Render(" • " + status))
	view.WriteString(interactiveStyles.status.Render(fmt.Sprintf(" • lines: %d • showing: %d-%d", len(m.lines), m.firstVisible(), m.lastVisible())))
	view.WriteByte('\n')

	end := min(m.vertical+m.pageSize(), len(m.lines))
	for index := m.vertical; index < end; index++ {
		view.WriteString(interactiveStyles.content.Render(m.lines[index]))
		view.WriteByte('\n')
	}
	for index := end - m.vertical; index < m.pageSize(); index++ {
		view.WriteByte('\n')
	}
	view.WriteString(renderHelpText(m.help()))
	view.WriteByte('\n')
	result := tea.NewView(view.String())
	result.AltScreen = true
	return result
}

func (m *textViewerModel) layout() {
	wrapped := ansi.Hardwrap(m.content, max(m.width, 1), true)
	m.lines = strings.Split(wrapped, "\n")
	if len(m.lines) > 1 && m.lines[len(m.lines)-1] == "" {
		m.lines = m.lines[:len(m.lines)-1]
	}
	if len(m.lines) == 0 {
		m.lines = []string{""}
	}
	m.vertical = min(m.vertical, m.maxVertical())
}

func (m textViewerModel) pageSize() int {
	helpLines := strings.Count(m.help(), "\n")
	return max(m.height-1-helpLines, 1)
}

func (m textViewerModel) maxVertical() int { return max(len(m.lines)-m.pageSize(), 0) }

func (m textViewerModel) firstVisible() int {
	if len(m.lines) == 0 {
		return 0
	}
	return m.vertical + 1
}

func (m textViewerModel) lastVisible() int {
	return min(m.vertical+m.pageSize(), len(m.lines))
}

func (m textViewerModel) help() string {
	return wrapHelp([]string{"↑/↓ scroll", "pgup/pgdown page", "home/end bounds", "enter/esc back", cancelHelp}, m.width)
}

// ViewText displays read-only content until the user returns or cancels.
func ViewText(ctx context.Context, input io.Reader, output io.Writer, config TextViewerConfig) error {
	program := tea.NewProgram(newTextViewerModel(config),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return err
	}
	model := final.(textViewerModel)
	if model.cancelled || !model.done {
		return context.Canceled
	}
	return nil
}
