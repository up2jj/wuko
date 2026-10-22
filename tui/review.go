package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	reviewPlain = "plain"
	reviewDiff  = "diff"
)

// ReviewConfig configures a scrollable plain-text or unified-diff review.
type ReviewConfig struct {
	Message string
	Content string
	Format  string
	Default bool
}

type reviewModel struct {
	appearance
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
	appearance := newAppearance()
	if config.Format == "" {
		config.Format = reviewPlain
	}
	model := reviewModel{appearance: appearance,
		config: config, content: sanitizeReviewContent(config.Content),
		width: 80, height: 24, approved: config.Default,
	}
	model.layout()
	return model
}

func (m reviewModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m reviewModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.appearance.update(message) {

		return m, nil
	}
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
	view.WriteString(m.styles.heading(m.config.Message, m.width, m.height))
	var status strings.Builder
	status.WriteString(m.styles.label.Render("Format: "))
	status.WriteString(m.styles.value.Render(m.config.Format))
	status.WriteString(m.styles.status.Render(fmt.Sprintf(" • lines: %d • showing: %d-%d", len(m.lines), m.firstVisible(), m.lastVisible())))
	if m.config.Format == reviewDiff && m.horizontal > 0 {
		status.WriteString(m.styles.status.Render(fmt.Sprintf(" • column: %d", m.horizontal+1)))
	}
	status.WriteByte('\n')
	view.WriteString(fitLines(status.String(), m.width))

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

	view.WriteString(m.decisionView() + "\n")
	view.WriteString(m.styles.footer(m.help(), m.width, m.height))
	return screenView(view.String(), m.width)
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
		return m.styles.content.Render(line)
	}
	switch {
	case strings.HasPrefix(line, "@@"):
		return m.styles.selected.Render(line)
	case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
		return m.styles.description.Render(line)
	case strings.HasPrefix(line, "+"):
		return m.styles.addition.Render(line)
	case strings.HasPrefix(line, "-"):
		return m.styles.removal.Render(line)
	default:
		return m.styles.content.Render(line)
	}
}

func (m reviewModel) contentWidth() int { return max(m.width, 1) }

func (m reviewModel) decisionView() string {
	reject := m.styles.button.Render("[ Reject ]")
	approve := m.styles.button.Render("[ Approve ]")
	active := m.styles.selected.Background(m.styles.selection.GetBackground())
	if m.approved {
		approve = active.Render("[ Approve ]")
	} else {
		reject = active.Render("[ Reject ]")
	}
	separator := "  "
	if ansi.StringWidth(reject)+2+ansi.StringWidth(approve) > m.width {
		separator = "\n"
	}
	return reject + separator + approve
}

func (m reviewModel) pageSize() int {
	helpLines := renderedLines(m.styles.footer(m.help(), m.width, m.height))
	return max(m.height-renderedLines(m.styles.heading(m.config.Message, m.width, m.height))-1-renderedLines(m.decisionView())-helpLines, 1)
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
	tokens := []string{"↑/↓ scroll", "pgup/pgdown page", "home/end bounds", "←/→ decision", "enter confirm", cancelHelp}
	if m.config.Format == reviewDiff {
		tokens = []string{"↑/↓ scroll", "pgup/pgdown page", "home/end bounds", "shift+←/→ pan", "←/→ decision", "enter confirm", cancelHelp}
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
