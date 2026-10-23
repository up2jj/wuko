package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// DatePickerConfig contains resolved calendar dates and a parser that freezes
// date rules, including relative shortcuts, for the lifetime of the picker.
// Parse returns a zero time for an allowed empty selection.
type DatePickerConfig struct {
	Message, Value, Format string
	Required               bool
	Today, Min, Max        time.Time
	Parse                  func(string) (time.Time, error)
}

type datePickerModel struct {
	appearance
	config                   DatePickerConfig
	input                    textinput.Model
	cursor                   time.Time
	width, height            int
	editing, done, cancelled bool
	err, result              string
}

func newDatePickerModel(config DatePickerConfig) (datePickerModel, error) {
	if config.Parse == nil || config.Format == "" || config.Today.IsZero() {
		return datePickerModel{}, fmt.Errorf("date picker requires a format, reference date, and parser")
	}
	config.Today = CalendarDate(config.Today)
	if !config.Min.IsZero() {
		config.Min = CalendarDate(config.Min)
	}
	if !config.Max.IsZero() {
		config.Max = CalendarDate(config.Max)
	}
	m := datePickerModel{appearance: newAppearance(), config: config, width: 80, height: 24, cursor: config.Today}
	m.cursor = m.clamp(m.cursor)
	m.input = textinput.New()
	m.input.Prompt = "> "
	m.input.CharLimit = 0
	m.input.SetWidth(70)
	m.styles.styleTextInput(&m.input)
	value := config.Value
	if value == "" {
		value = m.cursor.Format(config.Format)
	}
	m.input.SetValue(value)
	if parsed, err := config.Parse(value); err != nil {
		m.err = err.Error()
		m.editing = true
		m.input.Focus()
	} else if !parsed.IsZero() {
		m.cursor = CalendarDate(parsed)
	}
	return m, nil
}

// CalendarDate stores a civil date in UTC so comparisons and day arithmetic
// never acquire a timezone offset or a daylight-saving gap. Noon keeps
// 0001-01-01 distinct from the zero time used for an empty selection.
func CalendarDate(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, time.UTC)
}

func (m datePickerModel) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, textinput.Blink)
}

func (m datePickerModel) calendarVisible() bool { return m.width >= 32 && m.height >= 20 }

func (m datePickerModel) clamp(date time.Time) time.Time {
	if !m.config.Min.IsZero() && date.Before(m.config.Min) {
		return m.config.Min
	}
	if !m.config.Max.IsZero() && date.After(m.config.Max) {
		return m.config.Max
	}
	return date
}

func (m *datePickerModel) move(days, months int) {
	date := m.cursor
	if months != 0 {
		first := time.Date(date.Year(), date.Month()+time.Month(months), 1, 12, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1).Day()
		date = first.AddDate(0, 0, min(date.Day(), last)-1)
	} else {
		date = date.AddDate(0, 0, days)
	}
	if date.Year() < 1 || date.Year() > 9999 {
		return
	}
	m.cursor = m.clamp(date)
	m.input.SetValue(m.cursor.Format(m.config.Format))
	m.err = ""
}

func (m datePickerModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.appearance.update(message) {
		m.styles.styleTextInput(&m.input)
		return m, nil
	}
	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width, m.height = max(size.Width, 1), max(size.Height, 1)
		m.input.SetWidth(max(m.width-9, 1))
		if !m.calendarVisible() {
			m.editing = true
			return m, m.input.Focus()
		}
		return m, nil
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		if isCancelKey(key) {
			m.cancelled = true
			return m, tea.Quit
		}
		switch key.String() {
		case "esc":
			return m, nil
		case "tab", "shift+tab":
			if !m.calendarVisible() {
				return m, nil
			}
			m.editing = !m.editing
			if m.editing {
				return m, m.input.Focus()
			}
			m.input.Blur()
			// The calendar always confirms what it highlights, so an unparseable
			// typed entry falls back to the cursor instead of blocking Enter.
			if _, err := m.config.Parse(m.input.Value()); err != nil {
				m.input.SetValue(m.cursor.Format(m.config.Format))
				m.err = ""
			}
			return m, nil
		case "ctrl+x":
			m.input.SetValue("")
			m.err = ""
			m.editing = true
			return m, m.input.Focus()
		case "enter":
			date, err := m.config.Parse(m.input.Value())
			if err != nil {
				m.err = err.Error()
				return m, nil
			}
			if !date.IsZero() {
				m.result = date.Format(m.config.Format)
			}
			m.done = true
			return m, tea.Quit
		}
		if !m.editing {
			switch key.String() {
			case "left":
				m.move(-1, 0)
			case "right":
				m.move(1, 0)
			case "up":
				m.move(-7, 0)
			case "down":
				m.move(7, 0)
			case "pgup":
				m.move(0, -1)
			case "pgdown":
				m.move(0, 1)
			case "shift+pgup":
				m.move(0, -12)
			case "shift+pgdown":
				m.move(0, 12)
			default:
				if key.Text == "" {
					return m, nil
				}
				// Printable keys start a fresh typed date or shortcut.
				m.input.SetValue("")
				m.editing = true
				m.input.Focus()
			}
			if !m.editing {
				return m, nil
			}
		}
	}
	if !m.editing {
		return m, nil
	}
	previous := m.input.Value()
	var command tea.Cmd
	m.input, command = m.input.Update(message)
	if m.input.Value() != previous {
		m.err = ""
		if parsed, err := m.config.Parse(m.input.Value()); err == nil && !parsed.IsZero() {
			m.cursor = CalendarDate(parsed)
		}
	}
	return m, command
}

func (m datePickerModel) calendar() string {
	var b strings.Builder
	b.WriteString(m.styles.value.Render("  ‹ "+m.cursor.Format("January 2006")+" ›") + "\n\n")
	b.WriteString(m.styles.label.Render(" Mon Tue Wed Thu Fri Sat Sun") + "\n")
	first := time.Date(m.cursor.Year(), m.cursor.Month(), 1, 12, 0, 0, 0, time.UTC)
	offset := (int(first.Weekday()) + 6) % 7
	days := first.AddDate(0, 1, -1).Day()
	for row := range 6 {
		for column := range 7 {
			day := row*7 + column - offset + 1
			if day < 1 || day > days {
				b.WriteString("    ")
				continue
			}
			date := first.AddDate(0, 0, day-1)
			cell := fmt.Sprintf(" %2d ", day)
			switch {
			case !date.Equal(m.clamp(date)):
				cell = m.styles.disabled.Render(cell)
			case day == m.cursor.Day():
				cell = m.styles.selected.Background(m.styles.selection.GetBackground()).Render(fmt.Sprintf("[%2d]", day))
			case date.Equal(m.config.Today):
				cell = m.styles.cursor.Render(fmt.Sprintf(" %2d•", day))
			default:
				cell = m.styles.content.Render(cell)
			}
			b.WriteString(cell)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m datePickerModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	header := strings.Split(strings.TrimSuffix(m.styles.heading(m.config.Message, m.width, m.height), "\n"), "\n")
	var body []string
	if m.calendarVisible() {
		body = append(body, strings.Split(strings.TrimSuffix(m.calendar(), "\n"), "\n")...)
	}
	body = append(body, "Date: "+m.input.View())
	if m.err != "" {
		body = append(body, strings.Split(ansi.Hardwrap(m.styles.error.Render(m.err), m.width, true), "\n")...)
	} else if parsed, err := m.config.Parse(m.input.Value()); err == nil && !parsed.IsZero() {
		body = append(body, m.styles.description.Render("→ "+parsed.Format("Monday, 2 January 2006")))
	}
	body = append(body, m.styles.label.Render("Format: "+m.config.Format+" · +2d / -7d from today"))
	keys := []string{"enter select", "ctrl+x clear", cancelHelp}
	if m.calendarVisible() {
		mode := "tab type date"
		if m.editing {
			mode = "tab calendar"
		}
		keys = append([]string{"←/→ day", "↑/↓ week", "pgup/pgdown month", "shift+pgup/pgdown year", mode}, keys...)
	}
	footer := strings.Split(strings.TrimSuffix(m.styles.footer(wrapHelp(keys, m.width), m.width, m.height), "\n"), "\n")
	// Reserve the input and any validation error before decorative help on short
	// terminals. Extremely short screens still retain the editable input row.
	headerCount := min(len(header), max(m.height-len(body)-1, 0))
	bodyCount := min(len(body), m.height-headerCount)
	footerCount := min(len(footer), m.height-headerCount-bodyCount)
	lines := append([]string{}, header[:headerCount]...)
	lines = append(lines, body[:bodyCount]...)
	lines = append(lines, footer[len(footer)-footerCount:]...)
	return screenView(strings.Join(lines, "\n"), m.width)
}

// PickDate runs a date picker and returns an absolute date in config.Format.
func PickDate(ctx context.Context, input io.Reader, output io.Writer, config DatePickerConfig) (string, error) {
	model, err := newDatePickerModel(config)
	if err != nil {
		return "", err
	}
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return "", err
	}
	result := final.(datePickerModel)
	if result.cancelled || !result.done {
		return "", context.Canceled
	}
	return result.result, nil
}
