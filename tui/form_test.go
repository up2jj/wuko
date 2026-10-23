package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func updateForm(model formModel, message tea.Msg) formModel {
	updated, _ := model.Update(message)
	return updated.(formModel)
}

func formKey(model formModel, code rune, mods ...tea.KeyMod) formModel {
	var mod tea.KeyMod
	for _, item := range mods {
		mod |= item
	}
	return updateForm(model, tea.KeyPressMsg{Code: code, Mod: mod})
}

func TestFormNavigatesAndSubmitsAllFields(t *testing.T) {
	config := FormConfig{Title: "Settings", Fields: []FormField{
		{ID: "name", Type: "text", Label: "Name", Value: "Ada"},
		{ID: "notify", Type: "checkbox", Label: "Notify", Value: false},
		{ID: "region", Type: "select", Label: "Region", Options: []Option{{Label: "West", Value: "west"}, {Label: "East", Value: "east"}}},
		{ID: "features", Type: "multiselect", Label: "Features", Value: []any{}, Options: []Option{{Label: "Logs", Value: 1}, {Label: "Metrics", Value: 2}}},
	}}
	m := newFormModel(config)
	m = formKey(m, tea.KeyTab)
	if m.focus != 1 {
		t.Fatalf("focus=%d", m.focus)
	}
	m = formKey(m, tea.KeySpace)
	m = formKey(m, tea.KeyEnter)
	if m.focus != 2 {
		t.Fatalf("focus=%d", m.focus)
	}
	m = formKey(m, tea.KeyDown)
	m = formKey(m, tea.KeyEnter)
	m = formKey(m, tea.KeyDown)
	m = formKey(m, tea.KeySpace)
	m = formKey(m, tea.KeyUp)
	m = formKey(m, tea.KeySpace)
	m = formKey(m, tea.KeyEnter)
	if m.focus != 4 || m.done {
		t.Fatalf("focus=%d done=%v", m.focus, m.done)
	}
	m = formKey(m, tea.KeyEnter)
	want := map[string]any{"name": "Ada", "notify": true, "region": "west", "features": []any{2, 1}}
	if !m.done || !reflect.DeepEqual(m.result, want) {
		t.Fatalf("done=%v values=%#v", m.done, m.result)
	}
}

func TestFormFocusWrapsAndBlursWidgets(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{{ID: "one", Type: "text", Label: "One", Value: ""}, {ID: "two", Type: "textarea", Label: "Two", Value: ""}}})
	if !m.widgets[0].input.Focused() {
		t.Fatal("first field is not focused")
	}
	m = formKey(m, tea.KeyTab, tea.ModShift)
	if m.focus != 2 || m.widgets[0].input.Focused() {
		t.Fatalf("focus=%d", m.focus)
	}
	m = formKey(m, tea.KeyTab)
	if m.focus != 0 || !m.widgets[0].input.Focused() {
		t.Fatalf("focus=%d", m.focus)
	}
	m = formKey(m, tea.KeyTab)
	if !m.widgets[1].area.Focused() || m.widgets[0].input.Focused() {
		t.Fatal("focus was not transferred")
	}
}

func TestFormTextareaEnterInsertsNewline(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{{ID: "notes", Type: "textarea", Label: "Notes", Value: "first"}}})
	m = formKey(m, tea.KeyEnter)
	if m.done || m.focus != 0 || m.widgets[0].area.Value() != "first\n" {
		t.Fatalf("value=%q done=%v", m.widgets[0].area.Value(), m.done)
	}
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || m.result["notes"] != "first\n" {
		t.Fatalf("result=%#v", m.result)
	}
}

func TestFormValidationRetainsEditsAndFocusesFirstError(t *testing.T) {
	validate := func(value any, _ map[string]any) (any, error) {
		if value == "bad" {
			return nil, errors.New("choose a longer name")
		}
		return value, nil
	}
	m := newFormModel(FormConfig{Fields: []FormField{
		{ID: "name", Type: "text", Label: "Name", Value: "bad", Validate: validate},
		{ID: "notes", Type: "textarea", Label: "Notes", Value: "preserved"},
	}})
	m = formKey(m, tea.KeyTab)
	if m.widgets[0].err == "" {
		t.Fatal("blur did not validate")
	}
	m = formKey(m, 's', tea.ModCtrl)
	if m.done || m.focus != 0 || len(m.result) > 0 {
		t.Fatalf("model=%#v", m)
	}
	if m.widgets[1].area.Value() != "preserved" || !strings.Contains(m.View().Content, "choose a longer name") {
		t.Fatal("edits or validation message lost")
	}
	m.widgets[0].input.SetValue("good")
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || m.result["notes"] != "preserved" {
		t.Fatalf("result=%#v", m.result)
	}
}

func TestFormDoesNotDropInvalidInitialMultiselectValues(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{{ID: "features", Type: "multiselect", Label: "Features", Value: []any{"missing"}, Options: []Option{{Label: "One", Value: "one"}}, Validate: func(value any, _ map[string]any) (any, error) {
		if reflect.DeepEqual(value, []any{"missing"}) {
			return nil, errors.New("unavailable choice")
		}
		return value, nil
	}}}})
	m = formKey(m, 's', tea.ModCtrl)
	if m.done || m.widgets[0].err == "" {
		t.Fatal("invalid selection was silently dropped")
	}
	m = formKey(m, 'x', tea.ModCtrl)
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || !reflect.DeepEqual(m.result["features"], []any{}) {
		t.Fatalf("result=%#v", m.result)
	}
}

func TestFormDisabledOptionsCannotBeSelected(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{{ID: "features", Type: "multiselect", Label: "Features", Value: []any{}, Options: []Option{{Label: "Blocked", Value: "blocked", Disabled: true, DisabledReason: "Unavailable"}}}}})
	m = formKey(m, tea.KeySpace)
	if len(m.widgets[0].selection) > 0 {
		t.Fatal("disabled option was selected")
	}
	if !strings.Contains(m.View().Content, "Unavailable") {
		t.Fatal("missing disabled reason")
	}
}

func TestFormPathBrowserReturnsToParent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.yaml"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newFormModel(FormConfig{Fields: []FormField{{ID: "manifest", Type: "path", Label: "Manifest", Value: "", Path: &PathPickerConfig{Root: root, Kind: "file", Required: true}}}})
	m = formKey(m, tea.KeyEnter)
	if m.picker == nil {
		t.Fatal("picker did not open")
	}
	setPathCursor(t, m.picker, "a.yaml")
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(formModel)
	if m.picker != nil || m.done || m.widgets[0].value != "a.yaml" {
		t.Fatalf("picker=%v done=%v value=%v", m.picker, m.done, m.widgets[0].value)
	}
	if command != nil {
		if _, quit := command().(tea.QuitMsg); quit {
			t.Fatal("child quit escaped into parent")
		}
	}
	m = formKey(m, tea.KeyEnter)
	m = formKey(m, tea.KeyEscape)
	if m.picker != nil || m.cancelled || m.widgets[0].value != "a.yaml" {
		t.Fatal("escape lost the current selection")
	}
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || m.result["manifest"] != "a.yaml" {
		t.Fatalf("result=%#v", m.result)
	}
}

func TestFormPathCancelCancelsEntireForm(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{{ID: "path", Type: "path", Label: "Path", Value: "", Path: &PathPickerConfig{Root: t.TempDir(), Kind: "file"}}}})
	m = formKey(m, tea.KeyEnter)
	m = formKey(m, 'c', tea.ModCtrl)
	if !m.cancelled || m.done || len(m.result) > 0 {
		t.Fatal("form cancellation did not discard values")
	}
}

func TestFormResizesAndScrollsToFocusedField(t *testing.T) {
	config := FormConfig{Title: "Settings"}
	for i := range 15 {
		config.Fields = append(config.Fields, FormField{ID: fmt.Sprintf("field_%d", i), Type: "text", Label: fmt.Sprintf("Field %d", i), Value: "text"})
	}
	m := newFormModel(config)
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 12, Height: 6}, {Width: 1, Height: 1}, {Width: 80, Height: 24}} {
		m = updateForm(m, size)
		m.setFocus(14)
		content := m.View().Content
		lines := strings.Split(content, "\n")
		if len(lines) > size.Height {
			t.Fatalf("height=%d, got %d lines", size.Height, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("width=%d, line=%q", size.Width, line)
			}
		}
		if size.Width >= 40 && !strings.Contains(content, "Field 14") {
			t.Fatalf("focused field not visible: %q", content)
		}
	}
}

func TestFormPasswordNeverAppearsInView(t *testing.T) {
	m := newFormModel(FormConfig{Title: "Credentials", Fields: []FormField{{ID: "credential", Type: "password", Label: "Credential", Value: "private-value"}}})
	if strings.Contains(m.View().Content, "private-value") {
		t.Fatal("password exposed")
	}
	m = formKey(m, tea.KeyTab)
	if strings.Contains(m.View().Content, "private-value") {
		t.Fatal("blurred password exposed")
	}
}

func TestFormRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	values, err := Form(ctx, strings.NewReader(""), io.Discard, FormConfig{Title: "Test", Fields: []FormField{{ID: "name", Type: "text", Label: "Name", Value: ""}}})
	if err == nil || len(values) > 0 {
		t.Fatalf("values=%v error=%v", values, err)
	}
}

func TestFormShortViewportKeepsControlVisible(t *testing.T) {
	m := newFormModel(FormConfig{Title: "Settings", Fields: []FormField{{ID: "name", Type: "text", Label: "Name", Description: strings.Repeat("Description ", 20), Value: "editable"}}})
	m = updateForm(m, tea.WindowSizeMsg{Width: 40, Height: 6})
	if !strings.Contains(m.View().Content, "editable") {
		t.Fatalf("control was clipped: %q", m.View().Content)
	}
}

func TestFormPathClearRetainsOtherEdits(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{
		{ID: "notes", Type: "text", Label: "Notes", Value: "preserved"},
		{ID: "path", Type: "path", Label: "Path", Value: "invalid.yaml", Path: &PathPickerConfig{Root: t.TempDir(), Kind: "file"}},
	}})
	m = formKey(m, tea.KeyTab)
	m = formKey(m, 'x', tea.ModCtrl)
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || m.result["path"] != "" || m.result["notes"] != "preserved" {
		t.Fatalf("result=%#v", m.result)
	}
}

func TestFormRevalidatesCrossFieldErrorsAfterDependencyChanges(t *testing.T) {
	m := newFormModel(FormConfig{Fields: []FormField{
		{ID: "password", Type: "password", Label: "Password", Value: "first"},
		{ID: "confirmation", Type: "password", Label: "Confirm", Value: "second", Validate: func(value any, fields map[string]any) (any, error) {
			if value != fields["password"] {
				return nil, errors.New("Passwords must match")
			}
			return value, nil
		}},
	}})
	m = formKey(m, 's', tea.ModCtrl)
	if m.done || m.focus != 1 || m.widgets[1].err == "" {
		t.Fatal("expected confirmation error")
	}
	m = formKey(m, tea.KeyTab, tea.ModShift)
	m.widgets[0].input.SetValue("second")
	m = formKey(m, tea.KeyTab)
	if m.widgets[1].err != "" {
		t.Fatalf("stale error=%s", m.widgets[1].err)
	}
	m = formKey(m, 's', tea.ModCtrl)
	if !m.done || m.result["password"] != "second" || m.result["confirmation"] != "second" {
		t.Fatalf("result=%#v", m.result)
	}
}

func TestFormSubmissionUsesOneIsolatedDraftSnapshot(t *testing.T) {
	var snapshots []map[string]any
	capture := func(value any, fields map[string]any) (any, error) {
		snapshots = append(snapshots, map[string]any{"first": fields["first"], "second": fields["second"]})
		fields["second"] = "mutated"
		return value, nil
	}
	m := newFormModel(FormConfig{Fields: []FormField{
		{ID: "first", Type: "text", Label: "First", Value: "initial", Validate: capture},
		{ID: "second", Type: "text", Label: "Second", Value: "later", Validate: capture},
	}})
	m.widgets[0].input.SetValue("edited")
	m.widgets[1].input.SetValue("latest")
	m = formKey(m, 's', tea.ModCtrl)
	want := map[string]any{"first": "edited", "second": "latest"}
	if !m.done || len(snapshots) != 2 || !reflect.DeepEqual(snapshots[0], want) || !reflect.DeepEqual(snapshots[1], want) || !reflect.DeepEqual(m.result, want) {
		t.Fatalf("result=%#v snapshots=%#v", m.result, snapshots)
	}
}
