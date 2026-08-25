package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sahilm/fuzzy"
)

var pathStyles = struct {
	directory lipgloss.Style
	file      lipgloss.Style
}{
	directory: interactiveStyles.cursor,
	file:      interactiveStyles.content,
}

// PathPickerConfig configures an interactive filesystem path browser.
type PathPickerConfig struct {
	Message    string
	Root       string
	Kind       string
	Multiple   bool
	Required   bool
	Patterns   []string
	ShowHidden bool
}

type pathEntry struct {
	name        string
	description string
	relative    string
	absolute    string
	directory   bool
	selectable  bool
	parent      bool
	current     bool
	none        bool
}

type pathPickerModel struct {
	config    PathPickerConfig
	current   string
	entries   []pathEntry
	visible   []pathEntry
	cursor    int
	selected  map[string]bool
	order     []string
	filter    textinput.Model
	filtering bool
	width     int
	height    int
	err       string
	result    []string
	done      bool
	cancelled bool
}

func newPathPickerModel(config PathPickerConfig) (pathPickerModel, error) {
	root, err := filepath.EvalSymlinks(config.Root)
	if err != nil {
		return pathPickerModel{}, fmt.Errorf("resolving path root %s: %w", config.Root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return pathPickerModel{}, fmt.Errorf("inspecting path root %s: %w", root, err)
	}
	if !info.IsDir() {
		return pathPickerModel{}, fmt.Errorf("path root %s is not a directory", root)
	}
	config.Root = filepath.Clean(root)
	filter := textinput.New()
	filter.Prompt = "/"
	filter.Placeholder = "filter"
	styleInteractiveTextInput(&filter)
	filter.SetWidth(40)
	model := pathPickerModel{
		config: config, current: config.Root, selected: make(map[string]bool), filter: filter,
		width: 80, height: 24,
	}
	if err := model.load(); err != nil {
		return pathPickerModel{}, err
	}
	return model, nil
}

func (m pathPickerModel) Init() tea.Cmd { return nil }

func (m pathPickerModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = max(message.Width, 1)
		m.height = max(message.Height, 1)
		m.filter.SetWidth(max(m.width-2, 1))
		return m, nil
	case tea.KeyPressMsg:
		if isCancelKey(message) {
			m.cancelled = true
			return m, tea.Quit
		}
		if m.filtering {
			switch message.String() {
			case "esc":
				m.filter.SetValue("")
				m.filter.Blur()
				m.filtering = false
				m.refreshVisible()
				return m, nil
			case "enter":
				m.filter.Blur()
				m.filtering = false
				return m, nil
			}
			var command tea.Cmd
			m.filter, command = m.filter.Update(message)
			m.refreshVisible()
			return m, command
		}
		return m.updateKey(message)
	}
	return m, nil
}

func (m pathPickerModel) updateKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "/":
		m.filtering = true
		return m, m.filter.Focus()
	case "ctrl+h":
		m.config.ShowHidden = !m.config.ShowHidden
		if err := m.load(); err != nil {
			m.err = err.Error()
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = max(len(m.visible)-1, 0)
	case "pgup":
		m.cursor = max(m.cursor-m.pageSize(), 0)
	case "pgdown":
		m.cursor = min(m.cursor+m.pageSize(), max(len(m.visible)-1, 0))
	case "left", "h", "backspace":
		m.goParent()
	case "right", "l":
		m.openSelected()
	case "space":
		if m.config.Multiple {
			m.toggleSelected()
		}
	case "enter":
		if m.config.Multiple {
			if m.config.Required && len(m.order) == 0 {
				m.err = "select at least one path"
				return m, nil
			}
			m.result = slices.Clone(m.order)
			m.done = true
			return m, tea.Quit
		}
		if len(m.visible) == 0 {
			m.err = "no path is available"
			return m, nil
		}
		entry := m.visible[m.cursor]
		if entry.selectable {
			m.result = []string{entry.relative}
			m.done = true
			return m, tea.Quit
		}
		if entry.directory {
			m.open(entry)
		}
	}
	return m, nil
}

func (m *pathPickerModel) load() error {
	directoryEntries, err := os.ReadDir(m.current)
	if err != nil {
		return fmt.Errorf("reading %s: %w", m.current, err)
	}
	entries := make([]pathEntry, 0, len(directoryEntries)+3)
	if !m.config.Required && !m.config.Multiple {
		entries = append(entries, pathEntry{name: "(none)", description: "select no path", relative: "", selectable: true, none: true})
	}
	if m.config.Kind == "directory" || m.config.Kind == "either" {
		relative, err := relativePath(m.config.Root, m.current)
		if err != nil {
			return err
		}
		entries = append(entries, pathEntry{
			name: ".", description: "current directory", relative: relative, absolute: m.current,
			directory: true, selectable: true, current: true,
		})
	}
	if filepath.Clean(m.current) != filepath.Clean(m.config.Root) {
		entries = append(entries, pathEntry{
			name: "..", description: "parent directory", absolute: filepath.Dir(m.current), directory: true, parent: true,
		})
	}

	loaded := make([]pathEntry, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		if !m.config.ShowHidden && strings.HasPrefix(directoryEntry.Name(), ".") {
			continue
		}
		entry := m.inspectEntry(directoryEntry)
		loaded = append(loaded, entry)
	}
	slices.SortStableFunc(loaded, func(first, second pathEntry) int {
		if first.directory != second.directory {
			if first.directory {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(first.name), strings.ToLower(second.name))
	})
	m.entries = append(entries, loaded...)
	m.err = ""
	m.refreshVisible()
	return nil
}

func (m pathPickerModel) inspectEntry(entry os.DirEntry) pathEntry {
	absolute := filepath.Join(m.current, entry.Name())
	relative, err := relativePath(m.config.Root, absolute)
	if err != nil {
		return pathEntry{name: entry.Name(), absolute: absolute, description: err.Error()}
	}
	result := pathEntry{name: entry.Name(), relative: relative, absolute: absolute}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		result.description = "unavailable: " + err.Error()
		return result
	}
	inside, err := pathWithin(m.config.Root, canonical)
	if err != nil || !inside {
		result.description = "unavailable: resolves outside root"
		return result
	}
	info, err := os.Stat(canonical)
	if err != nil {
		result.description = "unavailable: " + err.Error()
		return result
	}
	result.directory = info.IsDir()
	link := ""
	if entry.Type()&os.ModeSymlink != 0 {
		link = " symlink"
	}
	switch {
	case info.IsDir():
		result.description = "directory" + link
		result.selectable = m.config.Kind == "directory" || m.config.Kind == "either"
	case info.Mode().IsRegular():
		result.description = "file" + link
		result.selectable = (m.config.Kind == "file" || m.config.Kind == "either") && matchesPathPatterns(m.config.Patterns, relative)
		if !result.selectable && len(m.config.Patterns) > 0 {
			result.description += " • filtered by pattern"
		}
	default:
		result.description = "unsupported entry"
	}
	return result
}

func (m *pathPickerModel) refreshVisible() {
	query := strings.TrimSpace(m.filter.Value())
	if query == "" {
		m.visible = slices.Clone(m.entries)
	} else {
		search := make([]string, len(m.entries))
		for index, entry := range m.entries {
			search[index] = entry.name + " " + entry.description
		}
		matches := fuzzy.Find(query, search)
		m.visible = make([]pathEntry, 0, len(matches))
		for _, match := range matches {
			m.visible = append(m.visible, m.entries[match.Index])
		}
	}
	m.cursor = min(m.cursor, max(len(m.visible)-1, 0))
}

func (m *pathPickerModel) openSelected() {
	if len(m.visible) == 0 {
		return
	}
	entry := m.visible[m.cursor]
	if entry.directory && !entry.current {
		m.open(entry)
	}
}

func (m *pathPickerModel) open(entry pathEntry) {
	previous := m.current
	m.current = entry.absolute
	if err := m.load(); err != nil {
		m.current = previous
		m.err = err.Error()
		return
	}
	m.cursor = 0
}

func (m *pathPickerModel) goParent() {
	if filepath.Clean(m.current) == filepath.Clean(m.config.Root) {
		return
	}
	parent := filepath.Dir(m.current)
	inside, err := pathWithin(m.config.Root, parent)
	if err != nil || !inside {
		m.err = "cannot navigate above root"
		return
	}
	m.open(pathEntry{absolute: parent, directory: true})
}

func (m *pathPickerModel) toggleSelected() {
	if len(m.visible) == 0 {
		return
	}
	entry := m.visible[m.cursor]
	if !entry.selectable || entry.none {
		m.err = "highlighted entry cannot be selected"
		return
	}
	if m.selected[entry.relative] {
		delete(m.selected, entry.relative)
		index := slices.Index(m.order, entry.relative)
		if index >= 0 {
			m.order = slices.Delete(m.order, index, index+1)
		}
	} else {
		m.selected[entry.relative] = true
		m.order = append(m.order, entry.relative)
	}
	m.err = ""
}

func (m pathPickerModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	var view strings.Builder
	view.WriteString(interactiveStyles.message.Render(m.config.Message) + "\n")
	relative, err := relativePath(m.config.Root, m.current)
	if err != nil {
		relative = "."
	}
	view.WriteString(interactiveStyles.label.Render("Path: "))
	view.WriteString(interactiveStyles.value.Render(relative))
	if m.config.Multiple {
		view.WriteString(interactiveStyles.status.Render(fmt.Sprintf(" • selected: %d", len(m.order))))
	}
	if m.config.ShowHidden {
		view.WriteString(interactiveStyles.status.Render(" • hidden: shown"))
	}
	view.WriteByte('\n')
	if m.filtering || m.filter.Value() != "" {
		view.WriteString(renderFilter(m.filter) + "\n")
	}

	start, end := m.visibleRange()
	if len(m.visible) == 0 {
		view.WriteString(interactiveStyles.disabled.Render("  (no matching entries)") + "\n")
	}
	for index := start; index < end; index++ {
		entry := m.visible[index]
		cursor := " "
		if index == m.cursor {
			cursor = interactiveStyles.cursor.Render(">")
		}
		mark := " "
		if m.config.Multiple {
			mark = "[ ]"
			if m.selected[entry.relative] {
				mark = interactiveStyles.selected.Render("[x]")
			}
		}
		suffix := ""
		if entry.directory && !entry.current && !entry.none {
			suffix = "/"
		}
		name := entry.name + suffix
		switch {
		case !entry.selectable && !entry.directory:
			name = interactiveStyles.disabled.Render(name)
		case index == m.cursor:
			name = interactiveStyles.selected.Render(name)
		case entry.directory:
			name = pathStyles.directory.Render(name)
		default:
			name = pathStyles.file.Render(name)
		}
		fmt.Fprintf(&view, "%s %s %s", cursor, mark, name)
		if entry.description != "" {
			fmt.Fprintf(&view, " %s %s", interactiveStyles.description.Render("—"), interactiveStyles.description.Render(entry.description))
		}
		view.WriteByte('\n')
	}
	if m.err != "" {
		view.WriteString(interactiveStyles.error.Render(m.err) + "\n")
	}
	view.WriteString(renderHelpText(m.help()))
	view.WriteByte('\n')
	if !strings.HasSuffix(view.String(), "\n") {
		view.WriteByte('\n')
	}
	return tea.NewView(view.String())
}

func (m pathPickerModel) visibleRange() (int, int) {
	page := m.pageSize()
	if len(m.visible) <= page {
		return 0, len(m.visible)
	}
	start := m.cursor - page/2
	start = max(start, 0)
	start = min(start, len(m.visible)-page)
	return start, start + page
}

func (m pathPickerModel) pageSize() int {
	helpLines := strings.Count(m.help(), "\n")
	headerLines := 2
	if m.filtering || m.filter.Value() != "" {
		headerLines++
	}
	if m.err != "" {
		headerLines++
	}
	return max(m.height-headerLines-helpLines, 1)
}

func (m pathPickerModel) help() string {
	var tokens []string
	if m.filtering {
		tokens = []string{"type filter", "enter apply", "esc clear", cancelHelp}
	} else if m.config.Multiple {
		tokens = []string{"↑/↓ move", "→ open", "← back", "space toggle", "enter confirm", "/ filter", "ctrl+h hidden", cancelHelp}
	} else {
		tokens = []string{"↑/↓ move", "→ open", "← back", "enter select", "/ filter", "ctrl+h hidden", cancelHelp}
	}
	return wrapHelp(tokens, m.width)
}

func relativePath(root, value string) (string, error) {
	relative, err := filepath.Rel(root, value)
	if err != nil {
		return "", fmt.Errorf("resolving path relative to root: %w", err)
	}
	if relative == "" {
		relative = "."
	}
	return filepath.ToSlash(relative), nil
}

func pathWithin(root, candidate string) (bool, error) {
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func matchesPathPatterns(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if doublestar.MatchUnvalidated(pattern, value) {
			return true
		}
	}
	return false
}

// PickPaths runs an interactive filesystem browser and returns root-relative paths.
func PickPaths(ctx context.Context, input io.Reader, output io.Writer, config PathPickerConfig) ([]string, error) {
	model, err := newPathPickerModel(config)
	if err != nil {
		return nil, err
	}
	program := tea.NewProgram(model,
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return nil, err
	}
	finalModel := final.(pathPickerModel)
	if finalModel.cancelled || !finalModel.done {
		return nil, context.Canceled
	}
	return finalModel.result, nil
}
