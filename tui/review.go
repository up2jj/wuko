package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	reviewPlain = "plain"
	reviewDiff  = "diff"
)

var reviewStyles = struct {
	message  lipgloss.Style
	label    lipgloss.Style
	value    lipgloss.Style
	status   lipgloss.Style
	content  lipgloss.Style
	addition lipgloss.Style
	removal  lipgloss.Style
	hunk     lipgloss.Style
	metadata lipgloss.Style
	selected lipgloss.Style
	button   lipgloss.Style
	help     lipgloss.Style
}{
	message:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	label:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
	value:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
	status:   lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	content:  lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
	addition: lipgloss.NewStyle().Foreground(lipgloss.Color("81")),
	removal:  lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
	hunk:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	metadata: lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
	button:   lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	help:     lipgloss.NewStyle().Foreground(lipgloss.Color("246")),
}

// ReviewConfig configures a scrollable plain-text or unified-diff review.
type ReviewConfig struct {
	Message string
	Content string
	Format  string
	Default bool
}

type reviewModel struct {
	config     ReviewConfig
	content    string
	lines      []string
	width      int
	height     int
	vertical   int
	horizontal int
	approved   bool
	result     bool
	done       bool
	cancelled  bool
}

func newReviewModel(config ReviewConfig) reviewModel {
	if config.Format == "" {
		config.Format = reviewPlain
	}
	model := reviewModel{
		config: config, content: sanitizeReviewContent(config.Content),
		width: 80, height: 24, approved: config.Default,
	}
	model.layout()
	return model
}

func (m reviewModel) Init() tea.Cmd { return nil }

func (m reviewModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(message.Width, 1)
		m.height = max(message.Height, 1)
		m.layout()
	case tea.KeyPressMsg:
		switch message.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
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
		case "left", "h":
			m.approved = false
		case "right", "l":
			m.approved = true
		case "shift+left", "H":
			if m.config.Format == reviewDiff {
				m.horizontal = max(m.horizontal-max(m.contentWidth()/2, 1), 0)
			}
		case "shift+right", "L":
			if m.config.Format == reviewDiff {
				m.horizontal = min(m.horizontal+max(m.contentWidth()/2, 1), m.maxHorizontal())
			}
		case "shift+home":
			m.horizontal = 0
		case "shift+end":
			if m.config.Format == reviewDiff {
				m.horizontal = m.maxHorizontal()
			}
		case "enter":
			m.result = m.approved
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m reviewModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(reviewStyles.message.Render(m.config.Message) + "\n")
	view.WriteString(reviewStyles.label.Render("Format: "))
	view.WriteString(reviewStyles.value.Render(m.config.Format))
	view.WriteString(reviewStyles.status.Render(fmt.Sprintf(" • lines: %d • showing: %d-%d", len(m.lines), m.firstVisible(), m.lastVisible())))
	if m.config.Format == reviewDiff && m.horizontal > 0 {
		view.WriteString(reviewStyles.status.Render(fmt.Sprintf(" • column: %d", m.horizontal+1)))
	}
	view.WriteByte('\n')

	end := min(m.vertical+m.pageSize(), len(m.lines))
	for index := m.vertical; index < end; index++ {
		line := m.styleLine(m.lines[index])
		if m.config.Format == reviewDiff {
			line = ansi.Cut(line, m.horizontal, m.horizontal+m.contentWidth())
		}
		view.WriteString(line)
		view.WriteByte('\n')
	}
	for index := end - m.vertical; index < m.pageSize(); index++ {
		view.WriteByte('\n')
	}

	reject := reviewStyles.button.Render("[ Reject ]")
	approve := reviewStyles.button.Render("[ Approve ]")
	if m.approved {
		approve = reviewStyles.selected.Render("[ Approve ]")
	} else {
		reject = reviewStyles.selected.Render("[ Reject ]")
	}
	view.WriteString(reject + "  " + approve + "\n")
	view.WriteString(reviewStyles.help.Render(strings.TrimSuffix(m.help(), "\n")))
	view.WriteByte('\n')
	return tea.NewView(view.String())
}

func (m *reviewModel) layout() {
	content := m.content
	if m.config.Format == reviewPlain {
		content = ansi.Hardwrap(content, m.contentWidth(), true)
	}
	m.lines = strings.Split(content, "\n")
	if len(m.lines) > 1 && m.lines[len(m.lines)-1] == "" {
		m.lines = m.lines[:len(m.lines)-1]
	}
	if len(m.lines) == 0 {
		m.lines = []string{""}
	}
	m.vertical = min(m.vertical, m.maxVertical())
	m.horizontal = min(m.horizontal, m.maxHorizontal())
}

func (m reviewModel) styleLine(line string) string {
	if m.config.Format != reviewDiff {
		return reviewStyles.content.Render(line)
	}
	switch {
	case strings.HasPrefix(line, "@@"):
		return reviewStyles.hunk.Render(line)
	case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
		return reviewStyles.metadata.Render(line)
	case strings.HasPrefix(line, "+"):
		return reviewStyles.addition.Render(line)
	case strings.HasPrefix(line, "-"):
		return reviewStyles.removal.Render(line)
	default:
		return reviewStyles.content.Render(line)
	}
}

func (m reviewModel) contentWidth() int { return max(m.width, 1) }

func (m reviewModel) pageSize() int {
	helpLines := strings.Count(m.help(), "\n")
	return max(m.height-3-helpLines, 1)
}

func (m reviewModel) maxVertical() int { return max(len(m.lines)-m.pageSize(), 0) }

func (m reviewModel) maxHorizontal() int {
	maximum := 0
	for _, line := range m.lines {
		maximum = max(maximum, ansi.StringWidth(line))
	}
	return max(maximum-m.contentWidth(), 0)
}

func (m reviewModel) firstVisible() int {
	if len(m.lines) == 0 {
		return 0
	}
	return m.vertical + 1
}

func (m reviewModel) lastVisible() int {
	return min(m.vertical+m.pageSize(), len(m.lines))
}

func (m reviewModel) help() string {
	tokens := []string{"↑/↓ scroll", "pgup/pgdown page", "home/end bounds", "←/→ decision", "enter confirm", "esc cancel"}
	if m.config.Format == reviewDiff {
		tokens = []string{"↑/↓ scroll", "pgup/pgdown page", "home/end bounds", "shift+←/→ pan", "←/→ decision", "enter confirm", "esc cancel"}
	}
	return wrapHelp(tokens, m.width)
}

func sanitizeReviewContent(value string) string {
	value = ansi.Strip(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\t", "    ")
	return strings.Map(func(r rune) rune {
		if r == '\n' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
}

// Review displays content and returns the user's approval decision.
func Review(ctx context.Context, input io.Reader, output io.Writer, config ReviewConfig) (bool, error) {
	program := tea.NewProgram(newReviewModel(config),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return false, err
	}
	model := final.(reviewModel)
	if model.cancelled || !model.done {
		return false, context.Canceled
	}
	return model.result, nil
}
