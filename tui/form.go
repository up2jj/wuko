package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/up2jj/wuko/workflow"
)

// FormConfig contains a resolved, immutable set of fields for one terminal form.
type FormConfig struct {
	Title       string
	Description string
	Fields      []FormField
}

// FormField separates workflow configuration from widget state. Normalize builds
// typed draft values without custom validation. Validate receives the full draft
// and returns the typed output or a message that does not expose entered values.
type FormField struct {
	ID, Type, Label, Description string
	Required                     bool
	Value                        any
	Options                      []Option
	Path                         *PathPickerConfig
	Date                         *DatePickerConfig
	Normalize                    func(any) (any, error)
	Validate                     func(any, map[string]any) (any, error)
}

type formWidget struct {
	field           FormField
	input           textinput.Model
	area            textarea.Model
	value           any
	cursor          int
	selection       []int
	selectionEdited bool
	err             string
	// draftRaw and draftValue cache the last normalization; path normalization
	// touches the filesystem and must not rerun for unchanged fields.
	draftRaw, draftValue any
	draftCached          bool
}

type formModel struct {
	appearance
	config          FormConfig
	widgets         []formWidget
	focus           int
	width, height   int
	picker          *pathPickerModel
	datePicker      *datePickerModel
	done, cancelled bool
	result          map[string]any
}

func newFormModel(config FormConfig) formModel {
	m := formModel{appearance: newAppearance(), config: config, width: 80, height: 24}
	for _, field := range config.Fields {
		widget := formWidget{field: field, value: field.Value, cursor: 0}
		widget.input = textinput.New()
		widget.input.Prompt = "> "
		widget.input.CharLimit = 0
		m.styles.styleTextInput(&widget.input)
		if field.Type == "password" {
			widget.input.EchoMode = textinput.EchoPassword
		}
		widget.area = textarea.New()
		widget.area.Prompt = "> "
		widget.area.ShowLineNumbers = false
		widget.area.CharLimit = 0
		m.styles.styleTextArea(&widget.area)
		if field.Value != nil {
			text := fmt.Sprint(field.Value)
			widget.input.SetValue(text)
			widget.area.SetValue(text)
		}
		switch field.Type {
		case "select":
			widget.cursor = -1
			for i, option := range field.Options {
				if !option.Disabled && FormValuesEqual(option.Value, field.Value) {
					widget.cursor = i
					break
				}
			}
		case "multiselect":
			widget.cursor = -1
			widget.moveChoice(0, 1)
			values := reflect.ValueOf(field.Value)
			if values.IsValid() && (values.Kind() == reflect.Slice || values.Kind() == reflect.Array) {
				for i := 0; i < values.Len(); i++ {
					for index, option := range field.Options {
						if FormValuesEqual(option.Value, values.Index(i).Interface()) {
							widget.selection = append(widget.selection, index)
							break
						}
					}
				}
			}
		}
		m.widgets = append(m.widgets, widget)
	}
	m.layout()
	m.setFocus(0)
	return m
}

func (m *formModel) layout() {
	for i := range m.widgets {
		m.widgets[i].input.SetWidth(max(m.width-4, 1))
		m.widgets[i].area.SetWidth(max(m.width-2, 1))
		m.widgets[i].area.SetHeight(min(4, max(m.height-8, 1)))
	}
}

func (m *formModel) setFocus(index int) tea.Cmd {
	for i := range m.widgets {
		m.widgets[i].input.Blur()
		m.widgets[i].area.Blur()
	}
	m.focus = index
	if index >= len(m.widgets) {
		return nil
	}
	widget := &m.widgets[index]
	switch widget.field.Type {
	case "text", "password", "number":
		return widget.input.Focus()
	case "textarea":
		return widget.area.Focus()
	}
	return nil
}

func (m formModel) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, textinput.Blink, textarea.Blink)
}

func (m formModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok && isCancelKey(key) {
		m.cancelled = true
		return m, tea.Quit
	}
	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width = max(size.Width, 1)
		m.height = max(size.Height, 1)
		m.layout()
	}
	if m.appearance.update(message) {
		for i := range m.widgets {
			m.styles.styleTextInput(&m.widgets[i].input)
			m.styles.styleTextArea(&m.widgets[i].area)
		}
	}
	if m.picker != nil {
		return m.updatePicker(message)
	}
	if m.datePicker != nil {
		return m.updateDatePicker(message)
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "tab", "shift+tab":
			if m.focus < len(m.widgets) {
				m.validateField(m.focus)
			}
			next := (m.focus + 1) % (len(m.widgets) + 1)
			if key.String() == "shift+tab" {
				next = (m.focus + len(m.widgets)) % (len(m.widgets) + 1)
			}
			command := m.setFocus(next)
			return m, command
		case "ctrl+s":
			return m.submit()
		case "enter":
			if m.focus == len(m.widgets) {
				return m.submit()
			}
			widget := &m.widgets[m.focus]
			switch widget.field.Type {
			case "textarea": // Enter belongs to the text area.
			case "path":
				return m.openPicker()
			case "date":
				return m.openDatePicker()
			default:
				m.validateField(m.focus)
				command := m.setFocus(m.focus + 1)
				return m, command
			}
		case "esc":
			return m, nil
		}
	}
	if m.focus == len(m.widgets) {
		return m, nil
	}
	widget := &m.widgets[m.focus]
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch widget.field.Type {
		case "checkbox":
			if key.String() == "space" {
				checked, _ := widget.value.(bool)
				widget.value = !checked
				widget.err = ""
			}
			return m, nil
		case "select", "multiselect":
			switch key.String() {
			case "up":
				if widget.cursor < 0 {
					widget.moveChoice(0, 1)
				} else {
					widget.moveChoice(widget.cursor-1, -1)
				}
			case "down":
				widget.moveChoice(widget.cursor+1, 1)
			case "home":
				widget.moveChoice(0, 1)
			case "end":
				widget.moveChoice(len(widget.field.Options)-1, -1)
			case "ctrl+x":
				if widget.field.Type == "select" {
					widget.cursor = -1
					widget.value = nil
				} else {
					widget.selection = nil
					widget.selectionEdited = true
				}
			case "space":
				if widget.cursor >= 0 && widget.cursor < len(widget.field.Options) && !widget.field.Options[widget.cursor].Disabled {
					if widget.field.Type == "multiselect" {
						widget.selectionEdited = true
						if index := slices.Index(widget.selection, widget.cursor); index >= 0 {
							widget.selection = slices.Delete(widget.selection, index, index+1)
						} else {
							widget.selection = append(widget.selection, widget.cursor)
						}
					}
				}
			}
			if widget.field.Type == "select" && widget.cursor >= 0 && widget.cursor < len(widget.field.Options) {
				option := widget.field.Options[widget.cursor]
				if !option.Disabled {
					widget.value = option.Value
				}
			}
			widget.err = ""
			return m, nil
		case "path", "date":
			if key.String() == "ctrl+x" {
				widget.value = ""
				if widget.field.Path != nil && widget.field.Path.Multiple {
					widget.value = []any{}
				}
				widget.err = ""
			}
			return m, nil
		}
		widget.err = ""
	}
	var command tea.Cmd
	switch widget.field.Type {
	case "text", "password", "number":
		widget.input, command = widget.input.Update(message)
	case "textarea":
		widget.area, command = widget.area.Update(message)
	}
	return m, command
}

// moveChoice keeps the cursor on an enabled option, retaining its position
// when there are no more choices in the requested direction.
func (widget *formWidget) moveChoice(start, direction int) {
	for index := start; index >= 0 && index < len(widget.field.Options); index += direction {
		if !widget.field.Options[index].Disabled {
			widget.cursor = index
			return
		}
	}
}

func (m *formModel) fieldValue(index int) any {
	widget := &m.widgets[index]
	switch widget.field.Type {
	case "text", "password", "number":
		return widget.input.Value()
	case "textarea":
		return widget.area.Value()
	case "multiselect":
		// Retain unmatched initial choices until the user edits the selection.
		if !widget.selectionEdited {
			return widget.value
		}
		values := make([]any, len(widget.selection))
		for i, index := range widget.selection {
			values[i] = widget.field.Options[index].Value
		}
		return values
	default:
		return widget.value
	}
}

// FormDraft normalizes a complete set of raw values without running custom rules.
// Unparseable values become nil in the draft; validating their raw inputs still
// reports the original field error. Each caller owns the returned values.
func FormDraft(fields []FormField, values map[string]any) map[string]any {
	draft := make(map[string]any, len(fields))
	for _, field := range fields {
		draft[field.ID] = normalizeDraftValue(field, values[field.ID])
	}
	return draft
}

func normalizeDraftValue(field FormField, raw any) any {
	value := workflow.Clone(raw)
	if field.Normalize == nil {
		return value
	}
	normalized, err := field.Normalize(value)
	if err != nil {
		return nil
	}
	return normalized
}

// draft shares cached values; each validator receives its own clone.
func (m *formModel) draft() map[string]any {
	draft := make(map[string]any, len(m.widgets))
	for i := range m.widgets {
		widget := &m.widgets[i]
		raw := m.fieldValue(i)
		if !widget.draftCached || !reflect.DeepEqual(widget.draftRaw, raw) {
			widget.draftRaw = workflow.Clone(raw)
			widget.draftValue = normalizeDraftValue(widget.field, raw)
			widget.draftCached = true
		}
		draft[widget.field.ID] = widget.draftValue
	}
	return draft
}

func (m *formModel) validateField(index int) (any, bool) {
	draft := m.draft()
	value, valid := m.validateFieldWithDraft(index, draft)
	// Refresh already-visible errors after a dependency changes, without showing
	// required errors on every untouched field as the user first fills the form.
	for i := range m.widgets {
		if i != index && m.widgets[i].err != "" {
			m.validateFieldWithDraft(i, draft)
		}
	}
	return value, valid
}

func (m *formModel) validateFieldWithDraft(index int, draft map[string]any) (any, bool) {
	value := m.fieldValue(index)
	validate := m.widgets[index].field.Validate
	if validate == nil {
		m.widgets[index].err = ""
		return value, true
	}
	normalized, err := validate(value, workflow.CloneMap(draft))
	if err != nil {
		m.widgets[index].err = err.Error()
		return nil, false
	}
	m.widgets[index].err = ""
	return normalized, true
}

func (m formModel) submit() (tea.Model, tea.Cmd) {
	values := make(map[string]any, len(m.widgets))
	draft := m.draft()
	firstInvalid := -1
	for i := range m.widgets {
		value, valid := m.validateFieldWithDraft(i, draft)
		if !valid {
			if firstInvalid < 0 {
				firstInvalid = i
			}
			continue
		}
		values[m.widgets[i].field.ID] = value
	}
	if firstInvalid >= 0 {
		command := m.setFocus(firstInvalid)
		return m, command
	}
	m.result = values
	m.done = true
	return m, tea.Quit
}

func (m formModel) openDatePicker() (tea.Model, tea.Cmd) {
	widget := &m.widgets[m.focus]
	if widget.field.Date == nil {
		widget.err = "date picker is unavailable"
		return m, nil
	}
	config := *widget.field.Date
	config.Value, _ = widget.value.(string)
	picker, err := newDatePickerModel(config)
	if err != nil {
		widget.err = err.Error()
		return m, nil
	}
	updated, _ := picker.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	picker = updated.(datePickerModel)
	m.datePicker = &picker
	return m, picker.Init()
}

func (m formModel) updateDatePicker(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok && key.String() == "esc" {
		m.datePicker = nil
		return m, m.setFocus(m.focus)
	}
	updated, command := m.datePicker.Update(message)
	picker := updated.(datePickerModel)
	if picker.done {
		m.widgets[m.focus].value = picker.result
		m.datePicker = nil
		m.validateField(m.focus)
		// The child confirms one field; it must not quit the entire form.
		return m, m.setFocus(m.focus)
	}
	m.datePicker = &picker
	return m, command
}

func (m formModel) openPicker() (tea.Model, tea.Cmd) {
	widget := &m.widgets[m.focus]
	if widget.field.Path == nil {
		widget.err = "path picker is unavailable"
		return m, nil
	}
	picker, err := newPathPickerModel(*widget.field.Path)
	if err != nil {
		widget.err = err.Error()
		return m, nil
	}
	if picker.config.Multiple {
		values := reflect.ValueOf(widget.value)
		if values.IsValid() && (values.Kind() == reflect.Slice || values.Kind() == reflect.Array) {
			for i := 0; i < values.Len(); i++ {
				if path, ok := values.Index(i).Interface().(string); ok && !picker.selected[path] {
					picker.order = append(picker.order, path)
					picker.selected[path] = true
				}
			}
		}
	} else if value, ok := widget.value.(string); ok && value != "" {
		// Opening an existing selection should start in its containing directory.
		directory := filepath.Join(picker.config.Root, filepath.FromSlash(filepath.Dir(value)))
		canonical, err := filepath.EvalSymlinks(directory)
		inside, containmentErr := pathWithin(picker.config.Root, canonical)
		if err == nil && containmentErr == nil && inside {
			picker.current = canonical
			if err := picker.load(); err != nil {
				widget.err = err.Error()
				return m, nil
			}
			for i, entry := range picker.visible {
				if entry.relative == value {
					picker.cursor = i
					break
				}
			}
		}
	}
	updated, _ := picker.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	picker = updated.(pathPickerModel)
	m.picker = &picker
	return m, picker.Init()
}

func (m formModel) updatePicker(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok && key.String() == "esc" {
		m.picker = nil
		command := m.setFocus(m.focus)
		return m, command
	}
	updated, command := m.picker.Update(message)
	picker := updated.(pathPickerModel)
	if picker.done {
		widget := &m.widgets[m.focus]
		if picker.config.Multiple {
			values := make([]any, len(picker.result))
			for i, value := range picker.result {
				values[i] = value
			}
			widget.value = values
		} else if len(picker.result) > 0 {
			widget.value = picker.result[0]
		}
		m.picker = nil
		m.validateField(m.focus)
		// Discard the child's Quit command; only submission can finish the parent.
		command := m.setFocus(m.focus)
		return m, command
	}
	m.picker = &picker
	return m, command
}

func (m formModel) View() tea.View {
	if m.done || m.cancelled {
		return tea.NewView("")
	}
	if m.picker != nil {
		return m.picker.View()
	}
	if m.datePicker != nil {
		return m.datePicker.View()
	}
	header := m.styles.heading(m.config.Title, m.width, m.height)
	if m.config.Description != "" {
		header += ansi.Hardwrap(m.styles.description.Render(m.config.Description), m.width, true) + "\n"
	}
	help := "tab/shift+tab move • ctrl+s submit • ctrl+c cancel"
	if m.focus < len(m.widgets) {
		switch m.widgets[m.focus].field.Type {
		case "textarea":
			help = "enter newline • " + help
		case "date":
			help = "enter calendar • ctrl+x clear • " + help
		case "path":
			help = "enter browse • ctrl+x clear • " + help
		case "checkbox", "multiselect":
			help = "space toggle • ↑/↓ choices • " + help
		case "select":
			help = "↑/↓ choose • ctrl+x clear • " + help
		}
	}
	footer := m.styles.footer(ansi.Hardwrap(help, m.width, true), m.width, m.height)
	var lines []string
	focusEnd := 0
	for i, widget := range m.widgets {
		start := len(lines)
		block := m.widgetView(widget, i == m.focus)
		lines = append(lines, strings.Split(ansi.Hardwrap(block, m.width, true), "\n")...)
		if i == m.focus {
			focusEnd = max(len(lines)-1, start+1)
		}
	}
	submit := "  [ Submit ]"
	if m.focus == len(m.widgets) {
		submit = m.styles.selected.Render("› [ Submit ]")
		focusEnd = len(lines) + 1
	}
	lines = append(lines, submit)
	headerLines := strings.Split(strings.TrimSuffix(header, "\n"), "\n")
	footerLines := strings.Split(strings.TrimSuffix(footer, "\n"), "\n")
	// Reserve at least one content row even on very short terminals.
	headerCount := min(len(headerLines), max(m.height-2, 0))
	footerCount := min(len(footerLines), max(m.height-headerCount-1, 0))
	available := max(m.height-headerCount-footerCount, 1)
	start := max(focusEnd-available, 0)
	start = min(start, max(len(lines)-available, 0))
	end := min(start+available, len(lines))
	rendered := append([]string{}, headerLines[:headerCount]...)
	rendered = append(rendered, lines[start:end]...)
	if footerCount > 0 {
		rendered = append(rendered, footerLines[len(footerLines)-footerCount:]...)
	}
	return screenView(strings.Join(rendered, "\n"), m.width)
}

func (m formModel) widgetView(widget formWidget, focused bool) string {
	var b strings.Builder
	marker := "  "
	if focused {
		marker = m.styles.cursor.Render("› ")
	}
	label := widget.field.Label
	if widget.field.Required {
		label += " *"
	}
	b.WriteString(marker + m.styles.message.Render(label) + "\n")
	if widget.field.Description != "" {
		b.WriteString(m.styles.description.Render(widget.field.Description) + "\n")
	}
	switch widget.field.Type {
	case "text", "password", "number":
		b.WriteString(widget.input.View())
	case "textarea":
		b.WriteString(widget.area.View())
	case "checkbox":
		checked, _ := widget.value.(bool)
		if checked {
			b.WriteString(m.styles.selected.Render("  [x]"))
		} else {
			b.WriteString("  [ ]")
		}
	case "select", "multiselect":
		start := max(widget.cursor-2, 0)
		start = min(start, max(len(widget.field.Options)-5, 0))
		end := min(start+5, len(widget.field.Options))
		if len(widget.field.Options) == 0 {
			b.WriteString("  (no available choices)")
		}
		for index := start; index < end; index++ {
			option := widget.field.Options[index]
			prefix := "  "
			if focused && index == widget.cursor {
				prefix = "› "
			}
			mark := "( ) "
			if widget.field.Type == "multiselect" {
				mark = "[ ] "
				if slices.Contains(widget.selection, index) {
					mark = "[x] "
				}
			} else if FormValuesEqual(widget.value, option.Value) {
				mark = "(●) "
			}
			text := prefix + mark + option.Label
			if option.Description != "" {
				text += " — " + option.Description
			}
			if option.Disabled {
				if option.DisabledReason != "" {
					text += " — " + option.DisabledReason
				}
				text = m.styles.disabled.Render(text)
			}
			b.WriteString(text)
			if index < end-1 {
				b.WriteByte('\n')
			}
		}
		if end < len(widget.field.Options) {
			b.WriteString(fmt.Sprintf("\n  … %d more", len(widget.field.Options)-end))
		}
	case "date":
		value, _ := widget.value.(string)
		if value == "" {
			value = "(none)"
		}
		b.WriteString("  " + value + " [Choose date]")
	case "path":
		value := fmt.Sprint(widget.value)
		if value == "" || value == "[]" {
			value = "(none)"
		}
		b.WriteString("  " + value + " [Browse]")
	}
	if widget.err != "" {
		b.WriteString("\n" + m.styles.error.Render(widget.err))
	}
	b.WriteByte('\n')
	return b.String()
}

// FormValuesEqual compares option values by their JSON encoding, so frozen choices
// match supplied values regardless of numeric representation.
func FormValuesEqual(left, right any) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
	return err == nil && string(a) == string(b)
}

// Form runs one program and returns values only after the entire form is valid.
func Form(ctx context.Context, input io.Reader, output io.Writer, config FormConfig) (map[string]any, error) {
	if len(config.Fields) == 0 {
		return nil, fmt.Errorf("form must contain fields")
	}
	program := tea.NewProgram(newFormModel(config), tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		return nil, err
	}
	model := final.(formModel)
	if model.cancelled || !model.done {
		return nil, context.Canceled
	}
	return model.result, nil
}
