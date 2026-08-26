package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	bubblestable "charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	defaultTableWidth  = 80
	defaultTableHeight = 24
	tableCellPadding   = 2
)

// TableColumn describes one displayed table column.
type TableColumn struct {
	Header string
	Width  int
}

// TableConfig configures a paginated, display-only table.
type TableConfig struct {
	Message string
	Columns []TableColumn
	Rows    [][]string
}

type tableModel struct {
	config    TableConfig
	rows      []bubblestable.Row
	preferred []int
	table     bubblestable.Model
	width     int
	height    int
	pageSize  int
	cursor    int
	done      bool
	cancelled bool
}

func newTableModel(config TableConfig) tableModel {
	rows := make([]bubblestable.Row, len(config.Rows))
	for index, row := range config.Rows {
		rows[index] = bubblestable.Row(row)
	}
	model := tableModel{
		config: config, rows: rows, preferred: preferredTableWidths(config),
		width: defaultTableWidth, height: defaultTableHeight,
	}
	styles := bubblestable.DefaultStyles()
	styles.Header = interactiveStyles.label.Bold(true).Padding(0, 1)
	styles.Cell = interactiveStyles.content.Padding(0, 1)
	styles.Selected = interactiveStyles.selected
	model.table = bubblestable.New(
		bubblestable.WithFocused(true),
		bubblestable.WithStyles(styles),
	)
	model.layout()
	return model
}

func preferredTableWidths(config TableConfig) []int {
	widths := make([]int, len(config.Columns))
	for index, column := range config.Columns {
		widths[index] = max(ansi.StringWidth(column.Header), 1)
		if column.Width > 0 {
			widths[index] = column.Width
		}
	}
	for _, row := range config.Rows {
		for index, value := range row {
			if config.Columns[index].Width == 0 {
				widths[index] = max(widths[index], ansi.StringWidth(value))
			}
		}
	}
	return widths
}

func (m tableModel) Init() tea.Cmd { return nil }

func (m tableModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
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
			m.cursor = max(m.cursor-1, 0)
		case "down", "j":
			m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
		case "pgup":
			m.cursor = max(m.cursor-m.pageSize, 0)
		case "pgdown":
			m.cursor = min(m.cursor+m.pageSize, max(len(m.rows)-1, 0))
		case "home":
			m.cursor = 0
		case "end":
			m.cursor = max(len(m.rows)-1, 0)
		case "enter":
			m.done = true
			return m, tea.Quit
		default:
			return m, nil
		}
		m.layout()
	}
	return m, nil
}

func (m tableModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(interactiveStyles.message.Render(m.config.Message))
	view.WriteByte('\n')
	view.WriteString(interactiveStyles.status.Render(m.status()))
	view.WriteByte('\n')
	view.WriteString(m.table.View())
	view.WriteByte('\n')
	view.WriteString(renderHelpText(m.help()))
	view.WriteByte('\n')
	return tea.NewView(view.String())
}

func (m *tableModel) layout() {
	helpLines := strings.Count(m.help(), "\n")
	m.pageSize = max(m.height-3-helpLines, 1)
	m.cursor = min(m.cursor, max(len(m.rows)-1, 0))
	columns := make([]bubblestable.Column, len(m.config.Columns))
	widths := fitTableWidths(m.preferred, m.width)
	for index, column := range m.config.Columns {
		columns[index] = bubblestable.Column{Title: column.Header, Width: widths[index]}
	}
	m.table.SetColumns(columns)
	m.table.SetWidth(m.width)
	m.table.SetHeight(m.pageSize + 1)
	first, last := m.visibleRange()
	if len(m.rows) == 0 {
		m.table.SetRows(nil)
		return
	}
	m.table.SetRows(m.rows[first-1 : last])
	m.table.SetCursor(m.cursor - (first - 1))
}

func fitTableWidths(preferred []int, totalWidth int) []int {
	widths := slices.Clone(preferred)
	available := max(totalWidth-tableCellPadding*len(widths), len(widths))
	preferredTotal := 0
	for index := range widths {
		widths[index] = max(widths[index], 1)
		preferredTotal += widths[index]
	}
	if preferredTotal <= available {
		return widths
	}

	remaining := max(available-len(widths), 0)
	weightTotal := preferredTotal - len(widths)
	assigned := 0
	for index, width := range widths {
		extra := 0
		if weightTotal > 0 {
			extra = (width - 1) * remaining / weightTotal
		}
		widths[index] = 1 + extra
		assigned += extra
	}
	for index := 0; assigned < remaining; index = (index + 1) % len(widths) {
		if widths[index] >= preferred[index] {
			continue
		}
		widths[index]++
		assigned++
	}
	return widths
}

func (m tableModel) visibleRange() (int, int) {
	if len(m.rows) == 0 {
		return 0, 0
	}
	first := (m.cursor/m.pageSize)*m.pageSize + 1
	return first, min(first+m.pageSize-1, len(m.rows))
}

func (m tableModel) status() string {
	first, last := m.visibleRange()
	pages := 0
	page := 0
	if len(m.rows) > 0 {
		pages = (len(m.rows) + m.pageSize - 1) / m.pageSize
		page = m.cursor/m.pageSize + 1
	}
	return fmt.Sprintf("rows: %d • showing: %d-%d • page: %d/%d", len(m.rows), first, last, page, pages)
}

func (m tableModel) help() string {
	return wrapHelp([]string{"↑/↓ scroll", "pgup/pgdown page", "home/end bounds", "enter continue", cancelHelp}, m.width)
}

func validateTableConfig(config TableConfig) error {
	if strings.TrimSpace(config.Message) == "" {
		return fmt.Errorf("message is required")
	}
	if len(config.Columns) == 0 {
		return fmt.Errorf("at least one column is required")
	}
	for index, column := range config.Columns {
		if strings.TrimSpace(column.Header) == "" {
			return fmt.Errorf("column %d header is required", index+1)
		}
		if column.Width < 0 {
			return fmt.Errorf("column %d width must be positive", index+1)
		}
	}
	for index, row := range config.Rows {
		if len(row) != len(config.Columns) {
			return fmt.Errorf("row %d has %d cells; want %d", index+1, len(row), len(config.Columns))
		}
	}
	return nil
}

// Table displays a responsive, paginated table until the user presses Enter.
func Table(ctx context.Context, input io.Reader, output io.Writer, config TableConfig) error {
	if err := validateTableConfig(config); err != nil {
		return err
	}
	program := tea.NewProgram(newTableModel(config),
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return err
	}
	model := final.(tableModel)
	if model.cancelled || !model.done {
		return context.Canceled
	}
	return nil
}

// WriteTable writes the complete table without interactive status or controls.
func WriteTable(output io.Writer, config TableConfig, width int) error {
	if err := validateTableConfig(config); err != nil {
		return err
	}
	if width <= 0 {
		width = defaultTableWidth
	}
	model := newTableModel(config)
	model.width = width
	model.height = len(model.rows) + 4 + strings.Count(model.help(), "\n")
	model.layout()
	view := interactiveStyles.message.Render(config.Message) + "\n" + model.table.View() + "\n"
	_, err := io.WriteString(output, ansi.Strip(view))
	return err
}
