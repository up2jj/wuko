package tui

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const dashboardMinPanelWidth = 28

const dashboardPanelGap = 1

// DashboardConfig describes a long-lived, refreshable terminal dashboard.
type DashboardConfig struct {
	Title          string
	MaxConcurrency int
	Groups         []DashboardGroup
	Refresh        func(context.Context, string) (map[string]any, error)
	Action         func(context.Context, string, string, string, map[string]any) (map[string]any, error)
	Render         func(string, string, DashboardWidgetDimensions, map[string]any, DashboardState, map[string]DashboardState) (string, error)
	Confirmation   func(string, string, string, DashboardWidgetDimensions, map[string]any, DashboardState, map[string]DashboardState) (string, error)
}

type DashboardGroup struct {
	ID      string
	Title   string
	Columns int
	Every   time.Duration
	Refresh bool
	Widgets []DashboardWidget
}

type DashboardWidget struct {
	ID      string
	Title   string
	Actions []DashboardAction
}

type DashboardAction struct {
	ID            string
	Label         string
	Confirm       bool
	RefreshGroups []string
}

// DashboardWidgetDimensions is the usable template body size in terminal cells. It excludes the
// widget border, horizontal padding, title, and the reserved action-status row.
type DashboardWidgetDimensions struct {
	Width  int
	Height int
}

// DashboardState is the stable result shape for refreshes and actions.
type DashboardState struct {
	Status    string
	Data      map[string]any
	Error     string
	UpdatedAt time.Time
	Duration  time.Duration
}

type DashboardWidgetResult struct {
	ID      string
	Actions map[string]DashboardState
}

type DashboardGroupResult struct {
	ID      string
	State   DashboardState
	Widgets []DashboardWidgetResult
}

type DashboardResult struct {
	Groups []DashboardGroupResult
}

type dashboardGroupState struct {
	DashboardState
	content        []string
	running        bool
	queued         bool
	paused         bool
	pendingRefresh bool
	pendingAction  *dashboardJob
	actions        []map[string]DashboardState
	// tickGeneration invalidates scheduled ticks once a newer refresh has completed, so manual
	// and action-triggered refreshes do not start additional periodic tick chains.
	tickGeneration uint64
}

type dashboardJobKind uint8

const (
	dashboardRefreshJob dashboardJobKind = iota
	dashboardActionJob
)

type dashboardConfirmationKind uint8

const (
	dashboardNoConfirmation dashboardConfirmationKind = iota
	dashboardActionConfirmation
	dashboardCloseConfirmation
)

type dashboardJob struct {
	kind          dashboardJobKind
	group         int
	widget        int
	action        int
	refreshGroups []string
	// data is the group snapshot captured on the update loop when an action starts.
	data map[string]any
}

type dashboardJobResultMsg struct {
	job      dashboardJob
	data     map[string]any
	err      error
	finished time.Time
	duration time.Duration
}

type dashboardTickMsg struct {
	group      int
	generation uint64
}

type dashboardWidgetRef struct{ group, widget int }

type dashboardWidgetLayout struct {
	DashboardWidgetDimensions
	panelWidth  int
	panelHeight int
}

type dashboardGroupLayout struct {
	columns int
	widgets []dashboardWidgetLayout
}

type dashboardLayout struct {
	groups []dashboardGroupLayout
}

type dashboardModel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	config    DashboardConfig
	groups    []dashboardGroupState
	queue     []dashboardJob
	running   int
	actionRun bool
	tracker   *sync.WaitGroup
	initial   tea.Cmd

	width, height int
	offset        int
	focus         int
	widgets       []dashboardWidgetRef
	menu          bool
	menuIndex     int
	confirm       dashboardConfirmationKind
	confirmText   string
	confirmJob    dashboardJob
	fullHelp      bool
	done          bool
	canceled      bool
	layout        dashboardLayout
}

func newDashboardModel(ctx context.Context, cancel context.CancelFunc, config DashboardConfig, tracker *sync.WaitGroup) dashboardModel {
	model := dashboardModel{ctx: ctx, cancel: cancel, config: config, width: 100, height: 30, tracker: tracker}
	model.groups = make([]dashboardGroupState, len(config.Groups))
	for groupIndex, group := range config.Groups {
		state := dashboardGroupState{DashboardState: DashboardState{Status: "static", Data: map[string]any{}}, actions: make([]map[string]DashboardState, len(group.Widgets))}
		for widgetIndex, widget := range group.Widgets {
			model.widgets = append(model.widgets, dashboardWidgetRef{group: groupIndex, widget: widgetIndex})
			state.actions[widgetIndex] = make(map[string]DashboardState, len(widget.Actions))
			for _, action := range widget.Actions {
				state.actions[widgetIndex][action.ID] = DashboardState{Status: "idle", Data: map[string]any{}}
			}
		}
		model.groups[groupIndex] = state
		if group.Refresh {
			model.groups[groupIndex].Status = "loading"
			model.queue = append(model.queue, dashboardJob{kind: dashboardRefreshJob, group: groupIndex})
		}
	}
	model.initial = tea.Batch(model.startQueued()...)
	model.layout = model.calculateLayout()
	for groupIndex, group := range config.Groups {
		if group.Refresh {
			continue
		}
		if err := model.renderGroup(groupIndex, model.groups[groupIndex].Data); err != nil {
			model.groups[groupIndex].Status = "failed"
			model.groups[groupIndex].Error = err.Error()
		}
	}
	model.reflow()
	return model
}

func (m dashboardModel) Init() tea.Cmd { return m.initial }

func (m dashboardModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(message.Width, 1)
		m.height = max(message.Height, 1)
		m.reflow()
		m.clampOffset()
	case dashboardTickMsg:
		if message.group < 0 || message.group >= len(m.groups) || message.generation != m.groups[message.group].tickGeneration {
			break
		}
		m.queueRefresh(message.group)
		commands = append(commands, m.startQueued()...)
	case dashboardJobResultMsg:
		commands = append(commands, m.finishJob(message)...)
	case tea.KeyPressMsg:
		switch message.String() {
		case "ctrl+c":
			m.done = true
			m.canceled = true
			m.cancel()
			return m, tea.Quit
		case "q":
			// An open prompt must be answered first; overwriting it would silently drop a pending action.
			if m.confirm == dashboardNoConfirmation {
				m.menu = false
				m.confirm = dashboardCloseConfirmation
				m.confirmText = "Close this dashboard?"
				m.reflow()
				m.clampOffset()
				return m, nil
			}
		}
		if m.confirm != dashboardNoConfirmation {
			// The prompt shows [y/N], so only an explicit yes confirms; enter takes the default answer.
			switch message.String() {
			case "y", "Y":
				if m.confirm == dashboardCloseConfirmation {
					m.done = true
					m.cancel()
					return m, tea.Quit
				}
				m.confirm = dashboardNoConfirmation
				m.queueAction(m.confirmJob)
				commands = append(commands, m.startQueued()...)
			case "n", "N", "esc", "enter":
				m.confirm = dashboardNoConfirmation
			}
			// Toggling the prompt changes the page and help heights, so the layout must follow.
			m.reflow()
			m.clampOffset()
			return m, tea.Batch(commands...)
		}
		if m.menu {
			ref := m.focusedWidget()
			actions := m.config.Groups[ref.group].Widgets[ref.widget].Actions
			switch message.String() {
			case "enter":
				if len(actions) > 0 && !m.actionRun {
					job := dashboardJob{kind: dashboardActionJob, group: ref.group, widget: ref.widget, action: m.menuIndex, refreshGroups: slices.Clone(actions[m.menuIndex].RefreshGroups)}
					m.menu = false
					if actions[m.menuIndex].Confirm {
						text, err := m.confirmation(job)
						if err != nil {
							m.recordActionError(job, err)
						} else {
							m.confirm = dashboardActionConfirmation
							m.confirmText = text
							m.confirmJob = job
						}
					} else {
						m.queueAction(job)
						commands = append(commands, m.startQueued()...)
					}
				}
			case "esc":
				m.menu = false
			default:
				if cursor, moved := m.actionPalette().move(message.String()); moved {
					m.menuIndex = cursor
				}
			}
			m.reflow()
			m.clampOffset()
			return m, tea.Batch(commands...)
		}

		switch message.String() {
		case "?":
			m.fullHelp = !m.fullHelp
		case "r":
			for groupIndex := range m.config.Groups {
				m.queueRefresh(groupIndex)
			}
			commands = append(commands, m.startQueued()...)
		case "tab":
			if len(m.widgets) > 0 {
				m.focus = (m.focus + 1) % len(m.widgets)
			}
		case "shift+tab":
			if len(m.widgets) > 0 {
				m.focus = (m.focus - 1 + len(m.widgets)) % len(m.widgets)
			}
		case "enter", "a":
			if len(m.widgets) > 0 && !m.actionRun {
				ref := m.focusedWidget()
				if len(m.config.Groups[ref.group].Widgets[ref.widget].Actions) > 0 {
					m.menu = true
					m.menuIndex = 0
				}
			}
		case "up", "k":
			m.offset = max(m.offset-1, 0)
		case "down", "j":
			m.offset++
		case "pgup":
			m.offset = max(m.offset-m.pageSize(), 0)
		case "pgdown":
			m.offset += m.pageSize()
		case "home":
			m.offset = 0
		case "end":
			m.offset = m.maxOffset()
		}
		m.clampOffset()
	}
	m.reflow()
	m.clampOffset()
	return m, tea.Batch(commands...)
}

func (m dashboardModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	if m.menu {
		view := tea.NewView(m.actionPalette().view())
		view.AltScreen = true
		return view
	}
	lines := strings.Split(m.renderPage(), "\n")
	start := min(m.offset, max(len(lines)-1, 0))
	end := min(start+m.pageSize(), len(lines))
	visible := strings.Join(lines[start:end], "\n")
	for end-start < m.pageSize() {
		visible += "\n"
		end++
	}
	view := tea.NewView(visible + "\n" + m.dashboardHelpText() + "\n")
	view.AltScreen = true
	return view
}

func (m *dashboardModel) startQueued() []tea.Cmd {
	var commands []tea.Cmd
	var blocked []dashboardJob
	limit := max(m.config.MaxConcurrency, 1)
	for m.running < limit && len(m.queue) > 0 {
		job := m.queue[0]
		m.queue = m.queue[1:]
		group := &m.groups[job.group]
		if job.kind == dashboardRefreshJob {
			group.queued = false
			if group.paused || group.running {
				group.pendingRefresh = true
				continue
			}
			group.running = true
			group.Status = "running"
		} else {
			if group.running {
				// Re-queued when the group's refresh finishes.
				group.pendingAction = &job
				continue
			}
			if m.actionRun {
				// Another action owns the action slot; keep this one queued until it finishes.
				blocked = append(blocked, job)
				continue
			}
			m.actionRun = true
			job.data = maps.Clone(group.Data)
			m.setActionStatus(job, DashboardState{Status: "running", Data: map[string]any{}})
			_ = m.renderGroup(job.group, group.Data)
		}
		m.running++
		m.tracker.Add(1)
		commands = append(commands, m.runJob(job))
	}
	if len(blocked) > 0 {
		m.queue = append(blocked, m.queue...)
	}
	return commands
}

func (m dashboardModel) runJob(job dashboardJob) tea.Cmd {
	return func() tea.Msg {
		defer m.tracker.Done()
		started := time.Now()
		var data map[string]any
		var err error
		if job.kind == dashboardRefreshJob {
			data, err = m.config.Refresh(m.ctx, m.config.Groups[job.group].ID)
		} else {
			group := m.config.Groups[job.group]
			widget := group.Widgets[job.widget]
			action := widget.Actions[job.action]
			data, err = m.config.Action(m.ctx, group.ID, widget.ID, action.ID, job.data)
		}
		return dashboardJobResultMsg{job: job, data: data, err: err, finished: time.Now(), duration: time.Since(started)}
	}
}

func (m *dashboardModel) finishJob(message dashboardJobResultMsg) []tea.Cmd {
	m.running = max(m.running-1, 0)
	group := &m.groups[message.job.group]
	if message.job.kind == dashboardRefreshJob {
		group.running = false
		candidate := DashboardState{Status: "succeeded", Data: maps.Clone(message.data), UpdatedAt: message.finished.UTC(), Duration: message.duration}
		if message.err != nil {
			candidate.Status = "failed"
			candidate.Error = message.err.Error()
			candidate.Data = maps.Clone(group.Data)
		}
		if message.err == nil {
			previous := group.DashboardState
			group.DashboardState = candidate
			if err := m.renderGroup(message.job.group, candidate.Data); err != nil {
				group.DashboardState = DashboardState{Status: "failed", Data: maps.Clone(previous.Data), Error: err.Error(), UpdatedAt: message.finished.UTC(), Duration: message.duration}
			} else {
				group.Data = maps.Clone(candidate.Data)
			}
		} else {
			group.DashboardState = candidate
		}
		var commands []tea.Cmd
		group.tickGeneration++
		if group.pendingAction != nil {
			job := *group.pendingAction
			group.pendingAction = nil
			m.queue = append([]dashboardJob{job}, m.queue...)
		} else if group.pendingRefresh {
			group.pendingRefresh = false
			m.queueRefresh(message.job.group)
		} else if m.config.Groups[message.job.group].Every > 0 && !group.paused {
			index, generation := message.job.group, group.tickGeneration
			commands = append(commands, tea.Tick(m.config.Groups[index].Every, func(time.Time) tea.Msg {
				return dashboardTickMsg{group: index, generation: generation}
			}))
		}
		commands = append(commands, m.startQueued()...)
		return commands
	}

	m.actionRun = false
	group.paused = false
	// The refresh queued below supersedes any requested while the action ran.
	group.pendingRefresh = false
	state := DashboardState{Status: "succeeded", Data: maps.Clone(message.data), UpdatedAt: message.finished.UTC(), Duration: message.duration}
	if message.err != nil {
		state.Status = "failed"
		state.Error = message.err.Error()
	}
	m.setActionStatus(message.job, state)
	_ = m.renderGroup(message.job.group, group.Data)
	m.queueRefresh(message.job.group)
	for _, id := range message.job.refreshGroups {
		if index := m.groupIndex(id); index >= 0 {
			m.queueRefresh(index)
		}
	}
	return m.startQueued()
}

func (m *dashboardModel) queueRefresh(groupIndex int) {
	if groupIndex < 0 || groupIndex >= len(m.config.Groups) || !m.config.Groups[groupIndex].Refresh {
		return
	}
	group := &m.groups[groupIndex]
	if group.paused || group.running || group.queued {
		group.pendingRefresh = true
		return
	}
	group.queued = true
	m.queue = append(m.queue, dashboardJob{kind: dashboardRefreshJob, group: groupIndex})
}

func (m *dashboardModel) queueAction(job dashboardJob) {
	group := &m.groups[job.group]
	group.paused = true
	group.pendingRefresh = false
	filtered := m.queue[:0]
	for _, queued := range m.queue {
		if queued.kind == dashboardRefreshJob && queued.group == job.group {
			group.queued = false
			continue
		}
		filtered = append(filtered, queued)
	}
	m.queue = filtered
	if group.running {
		group.pendingAction = &job
		return
	}
	m.queue = append([]dashboardJob{job}, m.queue...)
}

func (m dashboardModel) confirmation(job dashboardJob) (string, error) {
	group := m.config.Groups[job.group]
	widget := group.Widgets[job.widget]
	action := widget.Actions[job.action]
	if m.config.Confirmation == nil {
		return action.Label + "?", nil
	}
	return m.config.Confirmation(group.ID, widget.ID, action.ID, m.widgetDimensions(job.group, job.widget), maps.Clone(m.groups[job.group].Data), m.groups[job.group].DashboardState, maps.Clone(m.groups[job.group].actions[job.widget]))
}

func (m *dashboardModel) recordActionError(job dashboardJob, err error) {
	m.setActionStatus(job, DashboardState{Status: "failed", Data: map[string]any{}, Error: err.Error(), UpdatedAt: time.Now().UTC()})
	_ = m.renderGroup(job.group, m.groups[job.group].Data)
}

func (m *dashboardModel) setActionStatus(job dashboardJob, state DashboardState) {
	action := m.config.Groups[job.group].Widgets[job.widget].Actions[job.action]
	m.groups[job.group].actions[job.widget][action.ID] = state
}

func (m *dashboardModel) renderGroup(groupIndex int, data map[string]any) error {
	group := m.config.Groups[groupIndex]
	contents := make([]string, len(group.Widgets))
	for widgetIndex, widget := range group.Widgets {
		content, err := m.config.Render(group.ID, widget.ID, m.widgetDimensions(groupIndex, widgetIndex), maps.Clone(data), m.groups[groupIndex].DashboardState, maps.Clone(m.groups[groupIndex].actions[widgetIndex]))
		if err != nil {
			return fmt.Errorf("widget %q: %w", widget.ID, err)
		}
		contents[widgetIndex] = sanitizeDashboardContent(content)
	}
	m.groups[groupIndex].content = contents
	return nil
}

func (m dashboardModel) renderPage() string {
	var page strings.Builder
	page.WriteString(m.wrapDashboardText(interactiveStyles.message.Render(m.config.Title)))
	page.WriteString("\n\n")
	for groupIndex := range m.config.Groups {
		page.WriteString(m.dashboardGroupHeader(groupIndex))
		page.WriteByte('\n')
		if !m.groupShowsWidgets(groupIndex) {
			page.WriteString(m.dashboardGroupMessage(groupIndex))
			page.WriteString("\n\n")
			continue
		}
		page.WriteString(m.renderWidgetGrid(groupIndex))
		page.WriteString("\n\n")
	}
	if m.confirm != dashboardNoConfirmation {
		page.WriteString(m.dashboardConfirmationText())
		page.WriteByte('\n')
	}
	return strings.TrimSuffix(page.String(), "\n")
}

func (m dashboardModel) renderWidgetGrid(groupIndex int) string {
	group := m.config.Groups[groupIndex]
	layout := m.layout.groups[groupIndex]
	var rows []string
	for start := 0; start < len(group.Widgets); start += layout.columns {
		var panels []string
		for index := start; index < min(start+layout.columns, len(group.Widgets)); index++ {
			widget := group.Widgets[index]
			geometry := layout.widgets[index]
			style := dashboardPanelStyle(geometry.panelWidth, geometry.panelHeight, m.isFocused(groupIndex, index))
			content := ""
			if index < len(m.groups[groupIndex].content) {
				content = m.groups[groupIndex].content[index]
			}
			var body strings.Builder
			body.WriteString(fitDashboardLine(interactiveStyles.value.Render(widget.Title), geometry.Width))
			body.WriteByte('\n')
			body.WriteString(fitDashboardContent(content, geometry.Width, geometry.Height))
			if len(widget.Actions) > 0 {
				body.WriteByte('\n')
				body.WriteString(fitDashboardLine(interactiveStyles.status.Render(latestActionLine(widget, m.groups[groupIndex].actions[index])), geometry.Width))
			}
			if len(panels) > 0 {
				panels = append(panels, strings.Repeat(" ", dashboardPanelGap))
			}
			panels = append(panels, style.Render(body.String()))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, panels...))
	}
	return strings.Join(rows, "\n")
}

func dashboardPanelStyle(width, height int, focused bool) lipgloss.Style {
	color := lipgloss.Color("240")
	if focused {
		color = lipgloss.Color("205")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color).
		Padding(0, 1).
		Width(width).
		Height(height).
		MaxWidth(width).
		MaxHeight(height)
}

func fitDashboardLine(value string, width int) string {
	width = max(width, 1)
	value = strings.ReplaceAll(value, "\t", "    ")
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Render(ansi.Truncate(value, width, "…"))
}

func fitDashboardContent(value string, width, height int) string {
	width, height = max(width, 1), max(height, 1)
	value = strings.ReplaceAll(value, "\t", "    ")
	lines := strings.Split(ansi.Hardwrap(value, width, true), "\n")
	overflow := len(lines) > height
	lines = lines[:min(len(lines), height)]
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], width, "")
	}
	if overflow {
		if width == 1 {
			lines[len(lines)-1] = "…"
		} else {
			line := ansi.Truncate(lines[len(lines)-1], width-1, "")
			lines[len(lines)-1] = lipgloss.NewStyle().Width(width-1).MaxWidth(width-1).Render(line) + "…"
		}
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxWidth(width).MaxHeight(height).Render(strings.Join(lines, "\n"))
}

func (m dashboardModel) calculateLayout() dashboardLayout {
	layout := dashboardLayout{groups: make([]dashboardGroupLayout, len(m.config.Groups))}
	panelFrameWidth, panelFrameHeight := dashboardPanelStyle(1, 1, false).GetFrameSize()
	availableWidth := max(m.width, panelFrameWidth+1)

	for groupIndex, group := range m.config.Groups {
		columns := min(max(group.Columns, 1), max(len(group.Widgets), 1))
		columns = min(columns, max((availableWidth+dashboardPanelGap)/(dashboardMinPanelWidth+dashboardPanelGap), 1))
		columnWidths := distributeDashboardSpace(availableWidth-dashboardPanelGap*(columns-1), columns)
		groupLayout := dashboardGroupLayout{columns: columns, widgets: make([]dashboardWidgetLayout, len(group.Widgets))}
		for start := 0; start < len(group.Widgets); start += columns {
			end := min(start+columns, len(group.Widgets))
			rowHeight := panelFrameHeight + 2
			for index := start; index < end; index++ {
				if len(group.Widgets[index].Actions) > 0 {
					rowHeight = panelFrameHeight + 3
				}
			}
			for index := start; index < end; index++ {
				panelWidth := columnWidths[index-start]
				actionHeight := 0
				if len(group.Widgets[index].Actions) > 0 {
					actionHeight = 1
				}
				groupLayout.widgets[index] = dashboardWidgetLayout{
					DashboardWidgetDimensions: DashboardWidgetDimensions{
						Width:  max(panelWidth-panelFrameWidth, 1),
						Height: max(rowHeight-panelFrameHeight-1-actionHeight, 1),
					},
					panelWidth:  panelWidth,
					panelHeight: rowHeight,
				}
			}
		}
		layout.groups[groupIndex] = groupLayout
	}

	type rowRef struct{ group, start, end int }
	var rows []rowRef
	for groupIndex, group := range m.config.Groups {
		if !m.groupShowsWidgets(groupIndex) {
			continue
		}
		columns := layout.groups[groupIndex].columns
		for start := 0; start < len(group.Widgets); start += columns {
			rows = append(rows, rowRef{group: groupIndex, start: start, end: min(start+columns, len(group.Widgets))})
		}
	}
	extra := max(m.pageSize()-m.minimumPageHeight(layout), 0)
	for rowIndex, row := range rows {
		addition := extra / max(len(rows), 1)
		if rowIndex < extra%max(len(rows), 1) {
			addition++
		}
		for widgetIndex := row.start; widgetIndex < row.end; widgetIndex++ {
			widget := &layout.groups[row.group].widgets[widgetIndex]
			widget.Height += addition
			widget.panelHeight += addition
		}
	}
	return layout
}

func distributeDashboardSpace(total, count int) []int {
	count = max(count, 1)
	total = max(total, count)
	widths := make([]int, count)
	for index := range count {
		widths[index] = total / count
		if index < total%count {
			widths[index]++
		}
	}
	return widths
}

func (m dashboardModel) minimumPageHeight(layout dashboardLayout) int {
	height := lipgloss.Height(m.wrapDashboardText(interactiveStyles.message.Render(m.config.Title))) + 1
	for groupIndex := range m.config.Groups {
		height += lipgloss.Height(m.dashboardGroupHeader(groupIndex))
		if m.groupShowsWidgets(groupIndex) {
			groupLayout := layout.groups[groupIndex]
			for start := 0; start < len(groupLayout.widgets); start += groupLayout.columns {
				height += groupLayout.widgets[start].panelHeight
			}
		} else {
			height += lipgloss.Height(m.dashboardGroupMessage(groupIndex))
		}
		height++
	}
	if m.confirm != dashboardNoConfirmation {
		height += lipgloss.Height(m.dashboardConfirmationText())
	}
	return max(height, 1)
}

func (m dashboardModel) groupShowsWidgets(groupIndex int) bool {
	state := m.groups[groupIndex]
	return state.Status != "failed" && state.Status != "loading" && !(state.Status == "running" && len(state.content) == 0)
}

func (m dashboardModel) dashboardGroupHeader(groupIndex int) string {
	group, state := m.config.Groups[groupIndex], m.groups[groupIndex]
	status := state.Status
	if !state.UpdatedAt.IsZero() {
		status += " • " + state.UpdatedAt.Local().Format("15:04:05")
	}
	header := interactiveStyles.label.Bold(true).Render(group.Title) + interactiveStyles.status.Render(" • "+status)
	return m.wrapDashboardText(header)
}

func (m dashboardModel) dashboardGroupMessage(groupIndex int) string {
	state := m.groups[groupIndex]
	if state.Status == "failed" {
		return m.wrapDashboardText(interactiveStyles.error.Render("refresh failed: " + state.Error))
	}
	return m.wrapDashboardText(interactiveStyles.description.Render("Loading…"))
}

func (m dashboardModel) dashboardConfirmationText() string {
	return m.wrapDashboardText(interactiveStyles.error.Bold(true).Render(m.confirmText + " [y/N]"))
}

func (m dashboardModel) wrapDashboardText(value string) string {
	return ansi.Hardwrap(value, max(m.width, 1), true)
}

func (m dashboardModel) widgetDimensions(groupIndex, widgetIndex int) DashboardWidgetDimensions {
	if groupIndex >= 0 && groupIndex < len(m.layout.groups) && widgetIndex >= 0 && widgetIndex < len(m.layout.groups[groupIndex].widgets) {
		return m.layout.groups[groupIndex].widgets[widgetIndex].DashboardWidgetDimensions
	}
	return DashboardWidgetDimensions{Width: 1, Height: 1}
}

func (m *dashboardModel) reflow() {
	for range len(m.config.Groups) + 1 {
		previous := m.layout
		next := m.calculateLayout()
		m.layout = next
		failed := false
		for groupIndex := range m.config.Groups {
			if len(m.groups[groupIndex].content) == 0 || !m.groupShowsWidgets(groupIndex) || dashboardGroupLayoutsEqual(previous, next, groupIndex) {
				continue
			}
			if err := m.renderGroup(groupIndex, m.groups[groupIndex].Data); err != nil {
				m.groups[groupIndex].Status = "failed"
				m.groups[groupIndex].Error = err.Error()
				failed = true
			}
		}
		if !failed {
			return
		}
	}
}

func dashboardGroupLayoutsEqual(left, right dashboardLayout, groupIndex int) bool {
	if groupIndex >= len(left.groups) || groupIndex >= len(right.groups) {
		return false
	}
	leftGroup, rightGroup := left.groups[groupIndex], right.groups[groupIndex]
	if leftGroup.columns != rightGroup.columns || len(leftGroup.widgets) != len(rightGroup.widgets) {
		return false
	}
	for index := range leftGroup.widgets {
		if leftGroup.widgets[index] != rightGroup.widgets[index] {
			return false
		}
	}
	return true
}

func latestActionLine(widget DashboardWidget, states map[string]DashboardState) string {
	var selected DashboardAction
	var selectedState DashboardState
	for _, action := range widget.Actions {
		state := states[action.ID]
		if state.Status == "idle" {
			continue
		}
		if state.Status == "running" || selectedState.Status == "" || state.UpdatedAt.After(selectedState.UpdatedAt) {
			selected, selectedState = action, state
		}
	}
	if selectedState.Status == "" {
		return ""
	}
	line := selected.Label + ": " + selectedState.Status
	if selectedState.Error != "" {
		line += " • " + selectedState.Error
	}
	return line
}

func (m dashboardModel) actionPalette() actionPalette {
	ref := m.focusedWidget()
	widget := m.config.Groups[ref.group].Widgets[ref.widget]
	actions := make([]SelectionAction, len(widget.Actions))
	for index, action := range widget.Actions {
		actions[index] = SelectionAction{Label: action.Label}
	}
	return actionPalette{Title: widget.Title, Actions: actions, Cursor: m.menuIndex, Width: m.width, Height: m.height}
}

func (m dashboardModel) focusedWidget() dashboardWidgetRef {
	if len(m.widgets) == 0 {
		return dashboardWidgetRef{}
	}
	return m.widgets[min(m.focus, len(m.widgets)-1)]
}

func (m dashboardModel) isFocused(group, widget int) bool {
	if len(m.widgets) == 0 {
		return false
	}
	ref := m.focusedWidget()
	return ref.group == group && ref.widget == widget
}

func (m dashboardModel) groupIndex(id string) int {
	for index, group := range m.config.Groups {
		if group.ID == id {
			return index
		}
	}
	return -1
}

func (m dashboardModel) dashboardHelpText() string {
	help := []string{"↑/k up", "↓/j down", "tab focus", "enter/a actions", "r refresh", "q close", cancelHelp, "? more"}
	if m.fullHelp {
		help = []string{
			"↑/k up", "↓/j down", "pgup/pgdown page", "home/end bounds",
			"tab/shift+tab focus", "enter/a actions", "r refresh", "q close", cancelHelp, "? less",
		}
	}
	if m.confirm != dashboardNoConfirmation {
		help = []string{"y confirm", "n/esc/enter cancel", cancelHelp}
	}
	return renderHelpText(wrapHelp(help, m.width))
}

func (m dashboardModel) pageSize() int {
	return max(m.height-lipgloss.Height(m.dashboardHelpText())-1, 1)
}

func (m dashboardModel) maxOffset() int {
	lines := strings.Count(m.renderPage(), "\n") + 1
	return max(lines-m.pageSize(), 0)
}

func (m *dashboardModel) clampOffset() { m.offset = min(max(m.offset, 0), m.maxOffset()) }

func sanitizeDashboardContent(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
	return strings.TrimSpace(value)
}

func (m dashboardModel) result() DashboardResult {
	result := DashboardResult{Groups: make([]DashboardGroupResult, len(m.config.Groups))}
	for groupIndex, group := range m.config.Groups {
		state := m.groups[groupIndex]
		item := DashboardGroupResult{ID: group.ID, State: cloneDashboardState(state.DashboardState), Widgets: make([]DashboardWidgetResult, len(group.Widgets))}
		for widgetIndex, widget := range group.Widgets {
			actions := make(map[string]DashboardState, len(state.actions[widgetIndex]))
			for id, actionState := range state.actions[widgetIndex] {
				actions[id] = cloneDashboardState(actionState)
			}
			item.Widgets[widgetIndex] = DashboardWidgetResult{ID: widget.ID, Actions: actions}
		}
		result.Groups[groupIndex] = item
	}
	return result
}

func cloneDashboardState(state DashboardState) DashboardState {
	state.Data = maps.Clone(state.Data)
	return state
}

// Dashboard runs the alternate-screen dashboard until the user closes it or ctx is canceled.
func Dashboard(ctx context.Context, input io.Reader, output io.Writer, config DashboardConfig) (DashboardResult, error) {
	if strings.TrimSpace(config.Title) == "" {
		return DashboardResult{}, fmt.Errorf("dashboard title is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var tracker sync.WaitGroup
	model := newDashboardModel(runCtx, cancel, config, &tracker)
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	cancel()
	tracker.Wait()
	if err != nil {
		return DashboardResult{}, err
	}
	if ctx.Err() != nil {
		return DashboardResult{}, ctx.Err()
	}
	completed := final.(dashboardModel)
	if completed.canceled {
		return DashboardResult{}, context.Canceled
	}
	return completed.result(), nil
}
