package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func pickerDate(value string) time.Time {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return date.Add(12 * time.Hour)
}

func dateTestConfig(required bool) DatePickerConfig {
	config := DatePickerConfig{Message: "Choose a deadline", Format: "2006-01-02", Required: required, Today: pickerDate("2026-10-23")}
	config.Parse = func(value string) (time.Time, error) {
		if value == "" {
			if required {
				return time.Time{}, fmt.Errorf("a date is required")
			}
			return time.Time{}, nil
		}
		// This fake checks that the widget uses its supplied parser for previews
		// and confirmation; shortcut grammar is covered in steps/date.
		if value == "+2d" {
			return config.Today.AddDate(0, 0, 2), nil
		}
		date, err := time.Parse(config.Format, value)
		if err != nil {
			return time.Time{}, fmt.Errorf("enter a valid date")
		}
		return date.Add(12 * time.Hour), nil
	}
	return config
}

func dateModel(t *testing.T, config DatePickerConfig) datePickerModel {
	t.Helper()
	m, err := newDatePickerModel(config)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func dateKey(m datePickerModel, code rune, mods ...tea.KeyMod) datePickerModel {
	var mod tea.KeyMod
	for _, item := range mods {
		mod |= item
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return updated.(datePickerModel)
}

func TestDatePickerNavigatesCalendar(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, initial string
		key           rune
		mod           tea.KeyMod
		want          string
	}{
		{"next day", "2026-10-23", tea.KeyRight, 0, "2026-10-24"},
		{"previous day", "2026-10-23", tea.KeyLeft, 0, "2026-10-22"},
		{"next week", "2026-10-23", tea.KeyDown, 0, "2026-10-30"},
		{"previous week", "2026-10-23", tea.KeyUp, 0, "2026-10-16"},
		{"month end", "2026-01-31", tea.KeyPgDown, 0, "2026-02-28"},
		{"leap month", "2028-01-31", tea.KeyPgDown, 0, "2028-02-29"},
		{"previous month", "2026-03-31", tea.KeyPgUp, 0, "2026-02-28"},
		{"year end", "2026-12-31", tea.KeyPgDown, 0, "2027-01-31"},
		{"next year", "2028-02-29", tea.KeyPgDown, tea.ModShift, "2029-02-28"},
		{"previous year", "2028-02-29", tea.KeyPgUp, tea.ModShift, "2027-02-28"},
		{"first year", "0001-01-01", tea.KeyLeft, 0, "0001-01-01"},
		{"last year", "9999-12-31", tea.KeyRight, 0, "9999-12-31"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := dateTestConfig(true)
			config.Value = test.initial
			m := dateKey(dateModel(t, config), test.key, test.mod)
			m = dateKey(m, tea.KeyEnter)
			if !m.done || m.result != test.want {
				t.Fatalf("result = %s, done = %v, want %s", m.result, m.done, test.want)
			}
		})
	}
}

func TestDatePickerClampsTodayAndNavigation(t *testing.T) {
	t.Parallel()
	config := dateTestConfig(true)
	config.Min, config.Max = pickerDate("2026-10-25"), pickerDate("2026-10-27")
	m := dateModel(t, config)
	if !m.cursor.Equal(config.Min) || m.input.Value() != "2026-10-25" {
		t.Fatalf("initial date = %v, %q", m.cursor, m.input.Value())
	}
	m = dateKey(m, tea.KeyLeft)
	if !m.cursor.Equal(config.Min) {
		t.Fatal("moved below minimum")
	}
	m = dateKey(m, tea.KeyDown)
	if !m.cursor.Equal(config.Max) {
		t.Fatal("did not clamp to maximum")
	}
	m = dateKey(m, tea.KeyPgDown)
	if !m.cursor.Equal(config.Max) {
		t.Fatal("moved above maximum")
	}
}

func TestDatePickerTypedShortcutPreviewAndConfirmation(t *testing.T) {
	t.Parallel()
	m := dateKey(dateModel(t, dateTestConfig(true)), tea.KeyTab)
	if !m.editing || !m.input.Focused() {
		t.Fatal("typing did not receive focus")
	}
	m = dateKey(m, 'x', tea.ModCtrl)
	for _, char := range "+2d" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: char, Text: string(char)})
		m = updated.(datePickerModel)
	}
	if m.cursor.Format("2006-01-02") != "2026-10-25" || !strings.Contains(ansi.Strip(m.View().Content), "Sunday, 25 October 2026") {
		t.Fatalf("preview = %s", m.View().Content)
	}
	m = dateKey(m, tea.KeyEnter)
	if !m.done || m.result != "2026-10-25" {
		t.Fatalf("result = %q", m.result)
	}

	m = dateModel(t, dateTestConfig(true))
	updated, _ := m.Update(tea.KeyPressMsg{Code: '+', Text: "+"})
	m = updated.(datePickerModel)
	if !m.editing || m.input.Value() != "+" {
		t.Fatalf("typing from calendar = %q", m.input.Value())
	}
}

func TestDatePickerInvalidInputClearAndCancel(t *testing.T) {
	t.Parallel()
	config := dateTestConfig(true)
	config.Value = "2026-02-30"
	m := dateKey(dateModel(t, config), tea.KeyEnter)
	if m.done || !m.editing || m.err == "" || m.input.Value() != config.Value {
		t.Fatalf("invalid input lost: %#v", m)
	}
	m = dateKey(m, 'x', tea.ModCtrl)
	m = dateKey(m, tea.KeyEnter)
	if m.done || m.err != "a date is required" {
		t.Fatal("required date accepted empty value")
	}
	m = dateKey(m, 'c', tea.ModCtrl)
	if !m.cancelled || m.done || m.result != "" {
		t.Fatal("cancel published a date")
	}
	optional := dateKey(dateModel(t, dateTestConfig(false)), 'x', tea.ModCtrl)
	optional = dateKey(optional, tea.KeyEnter)
	if !optional.done || optional.result != "" {
		t.Fatal("optional date did not clear")
	}
}

func TestDatePickerCalendarModeConfirmsCursorAfterInvalidTyping(t *testing.T) {
	t.Parallel()
	m := dateKey(dateModel(t, dateTestConfig(true)), tea.KeyTab)
	for _, char := range "abc" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: char, Text: string(char)})
		m = updated.(datePickerModel)
	}
	m = dateKey(m, tea.KeyEnter)
	if m.done || m.err == "" {
		t.Fatal("typed mode accepted an invalid date")
	}
	m = dateKey(m, tea.KeyTab)
	if m.editing || m.err != "" || m.input.Value() != "2026-10-23" {
		t.Fatalf("calendar mode kept invalid input %q (error %q)", m.input.Value(), m.err)
	}
	m = dateKey(m, tea.KeyEnter)
	if !m.done || m.result != "2026-10-23" {
		t.Fatalf("result = %q, done = %v", m.result, m.done)
	}

	// A cleared optional field is valid, so returning to the calendar keeps it empty.
	optional := dateKey(dateModel(t, dateTestConfig(false)), 'x', tea.ModCtrl)
	optional = dateKey(dateKey(optional, tea.KeyTab), tea.KeyEnter)
	if !optional.done || optional.result != "" {
		t.Fatalf("optional result = %q", optional.result)
	}
}

func TestDatePickerResizesAndPreservesThemeState(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := dateModel(t, dateTestConfig(true))
	m = dateKey(m, tea.KeyRight)
	before := m.input.Value()
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 32, Height: 20}, {Width: 40, Height: 10}, {Width: 12, Height: 6}, {Width: 1, Height: 1}} {
		updated, _ := m.Update(size)
		m = updated.(datePickerModel)
		for _, background := range []color.Color{color.White, color.Black} {
			updated, _ = m.Update(tea.BackgroundColorMsg{Color: background})
			m = updated.(datePickerModel)
			assertScreenFits(t, m.View().Content, size.Width, size.Height)
			if m.input.Value() != before {
				t.Fatal("resize or theme changed the value")
			}
		}
		if !m.calendarVisible() && (!m.editing || !m.input.Focused()) {
			t.Fatal("compact terminal cannot edit date")
		}
	}
}

func TestDatePickerNoColorAndCalendarStructure(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := dateModel(t, dateTestConfig(true))
	view := ansi.Strip(m.View().Content)
	for _, value := range []string{"October 2026", "Mon Tue Wed Thu Fri Sat Sun", "[23]", "+2d / -7d", "ctrl+c cancel"} {
		if !strings.Contains(view, value) {
			t.Fatalf("missing %q:\n%s", value, view)
		}
	}
	if m.styles.accent != nil {
		t.Fatal("NO_COLOR retained accent")
	}
}

func TestFormDatePickerConfirmationEscapeAndCancellation(t *testing.T) {
	t.Parallel()
	config := dateTestConfig(false)
	newForm := func() formModel {
		return newFormModel(FormConfig{Title: "Schedule", Fields: []FormField{{ID: "due", Type: "date", Label: "Due date", Value: "", Date: &config}}})
	}
	m := formKey(newForm(), tea.KeyEnter)
	if m.datePicker == nil || m.fieldValue(0) != "" {
		t.Fatal("opening picker populated untouched field")
	}
	m = formKey(m, tea.KeyRight)
	m = formKey(m, tea.KeyEscape)
	if m.datePicker != nil || m.fieldValue(0) != "" || m.done {
		t.Fatal("Escape changed form")
	}
	m = formKey(m, tea.KeyEnter)
	m = formKey(m, tea.KeyRight)
	m = formKey(m, tea.KeyEnter)
	if m.datePicker != nil || m.fieldValue(0) != "2026-10-24" || m.done {
		t.Fatalf("confirmation = %#v, done = %v", m.fieldValue(0), m.done)
	}
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || m.result["due"] != "2026-10-24" {
		t.Fatalf("result = %v", m.result)
	}
	m = formKey(newForm(), tea.KeyEnter)
	m = formKey(m, 'c', tea.ModCtrl)
	if !m.cancelled || len(m.result) != 0 {
		t.Fatal("cancel published form")
	}
	m = newForm()
	m.widgets[0].value = "2026-10-24"
	m = formKey(m, 'x', tea.ModCtrl)
	if m.fieldValue(0) != "" {
		t.Fatal("form date did not clear")
	}
}
