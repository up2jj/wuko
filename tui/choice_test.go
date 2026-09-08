package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestChoiceModelMultipleSelection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}},
		Multiple: true, Required: true,
	})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || !slices.Equal(model.result, []int{1, 0}) {
		t.Fatalf("result = %#v, command nil = %v", model.result, command == nil)
	}
}

func TestChoiceModelOptionalSingleSelection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A", Value: "a"}}, Required: false,
	})
	if len(model.visible) != 2 || !model.visible[0].none {
		t.Fatalf("visible = %#v", model.visible)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || !model.done || len(model.result) != 0 {
		t.Fatalf("result = %#v, done = %v, command nil = %v", model.result, model.done, command == nil)
	}
}

func TestChoiceModelFiltersDescriptions(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{Message: "Pick", Options: []Option{
		{Label: "A", Description: "primary"}, {Label: "B", Description: "secondary"},
	}, Required: true})
	model.filter.SetValue("secondary")
	model.refreshVisible()
	if len(model.visible) != 1 || model.visible[0].label != "B" {
		t.Fatalf("visible = %#v", model.visible)
	}
	view := model.View().Content
	for _, want := range []string{"B", "secondary", "/ filter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	for _, styled := range []string{
		interactiveStyles.message.Render("Pick"),
		interactiveStyles.selected.Render("B"),
		interactiveStyles.description.Render("secondary"),
		renderHelpText(model.help()),
	} {
		if !strings.Contains(view, styled) {
			t.Fatalf("view = %q, want styled content %q", view, styled)
		}
	}
}

func TestChoiceModelRendersNonSelectableSectionsAndSeparators(t *testing.T) {
	config := ChoicePickerConfig{
		Message: "Pick", Required: true,
		Options: []Option{
			{Label: "Development"},
			{Label: "Staging"},
			{Label: "Production"},
		},
		Markers: []ChoiceMarker{
			{Kind: ChoiceMarkerSection, Before: 0, Label: "Common"},
			{Kind: ChoiceMarkerSeparator, Before: 2},
			{Kind: ChoiceMarkerSection, Before: 2, Label: "Restricted"},
		},
	}
	if err := validateChoicePickerConfig(config); err != nil {
		t.Fatal(err)
	}
	model := newChoiceModel(config)
	rows := model.displayRows()
	wantKinds := []choiceRowKind{
		choiceRowSection, choiceRowOption, choiceRowOption,
		choiceRowSeparator, choiceRowSection, choiceRowOption,
	}
	if len(rows) != len(wantKinds) {
		t.Fatalf("rows = %#v", rows)
	}
	for index, want := range wantKinds {
		if rows[index].kind != want {
			t.Fatalf("row %d kind = %v, want %v", index, rows[index].kind, want)
		}
	}

	view := model.View().Content
	for _, want := range []string{"── Common", "── Restricted", "Development", "Production"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(choiceModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(choiceModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || !slices.Equal(model.result, []int{2}) {
		t.Fatalf("result = %#v, command nil = %v", model.result, command == nil)
	}
}

func TestChoiceModelKeepsOptionalNoneBeforeFirstSection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: false,
		Options: []Option{{Label: "Development", Default: true}},
		Markers: []ChoiceMarker{{Kind: ChoiceMarkerSection, Before: 0, Label: "Common"}},
	})
	rows := model.displayRows()
	if len(rows) != 3 || rows[0].kind != choiceRowOption || !rows[0].item.none || rows[1].kind != choiceRowSection {
		t.Fatalf("rows = %#v", rows)
	}
	if model.cursor != 1 || model.visible[model.cursor].index != 0 {
		t.Fatalf("cursor = %d, visible = %#v", model.cursor, model.visible)
	}
}

func TestChoiceModelHidesSeparatorWhenOnlyNonePrecedesIt(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: false,
		Options: []Option{{Label: "aaa"}, {Label: "zzz value"}},
		Markers: []ChoiceMarker{{Kind: ChoiceMarkerSeparator, Before: 1}},
	})
	// "value" matches the synthetic "(none)" row and the second option, so the first
	// block has no visible choice for the separator to sit after.
	model.filter.SetValue("value")
	model.refreshVisible()
	rows := model.displayRows()
	if slices.IndexFunc(rows, func(row choiceRow) bool { return row.kind == choiceRowSeparator }) >= 0 {
		t.Fatalf("separator rendered without a choice above it: %#v", rows)
	}
}

func TestChoiceModelFiltersDecoratedChoicesBySectionInSourceOrder(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: true,
		Options: []Option{
			{Label: "Development", Description: "shared"},
			{Label: "Staging", Description: "shared"},
			{Label: "Production", Description: "shared"},
			{Label: "Emergency rollback", Description: "special"},
		},
		Markers: []ChoiceMarker{
			{Kind: ChoiceMarkerSection, Before: 0, Label: "Common"},
			{Kind: ChoiceMarkerSeparator, Before: 2},
			{Kind: ChoiceMarkerSection, Before: 2, Label: "Restricted"},
		},
	})

	model.filter.SetValue("Restricted")
	model.refreshVisible()
	if got := []int{model.visible[0].index, model.visible[1].index}; !slices.Equal(got, []int{2, 3}) {
		t.Fatalf("section matches = %#v", model.visible)
	}
	rows := model.displayRows()
	if len(rows) != 3 || rows[0].kind != choiceRowSection || rows[0].label != "Restricted" {
		t.Fatalf("section rows = %#v", rows)
	}

	model.filter.SetValue("special")
	model.refreshVisible()
	rows = model.displayRows()
	if len(model.visible) != 1 || model.visible[0].index != 3 || len(rows) != 2 || rows[0].kind != choiceRowSection {
		t.Fatalf("option rows = %#v, visible = %#v", rows, model.visible)
	}

	model.filter.SetValue("shared")
	model.refreshVisible()
	if got := []int{model.visible[0].index, model.visible[1].index, model.visible[2].index}; !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("source order = %#v", model.visible)
	}
	rows = model.displayRows()
	if slices.IndexFunc(rows, func(row choiceRow) bool { return row.kind == choiceRowSeparator }) < 0 {
		t.Fatalf("divider missing from rows = %#v", rows)
	}
}

func TestChoiceModelMarkersConsumeViewportRows(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: true,
		Options: []Option{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		Markers: []ChoiceMarker{
			{Kind: ChoiceMarkerSection, Before: 0, Label: "First"},
			{Kind: ChoiceMarkerSeparator, Before: 2},
			{Kind: ChoiceMarkerSection, Before: 2, Label: "Second"},
		},
	})
	model.height = 5
	model.cursor = 2
	rows := model.displayRows()
	start, end := model.visibleRange(rows)
	if end-start != model.pageSize() || end-start >= len(rows) {
		t.Fatalf("range = %d:%d, page = %d, rows = %d", start, end, model.pageSize(), len(rows))
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	model = updated.(choiceModel)
	if model.cursor >= 2 {
		t.Fatalf("page up cursor = %d", model.cursor)
	}
}

func TestChoiceRulesFitTerminalWidth(t *testing.T) {
	for _, width := range []int{1, 8, 24} {
		for _, label := range []string{"", "Restricted environments"} {
			rule := renderChoiceRule(label, width, interactiveStyles.label)
			if got := ansi.StringWidth(rule); got != width {
				t.Fatalf("width = %d for %q, want %d: %q", got, label, width, rule)
			}
		}
	}
}

func TestChoiceModelPaginatesAndWrapsHelp(t *testing.T) {
	options := make([]Option, 12)
	for index := range options {
		options[index] = Option{Label: fmt.Sprintf("Option %d", index)}
	}
	model := newChoiceModel(ChoicePickerConfig{Message: "Pick", Options: options, Multiple: true, Required: true})
	model.width = 18
	model.height = 6
	model.cursor = len(model.visible) - 1
	start, end := model.visibleRange(model.displayRows())
	if start == 0 || end != len(model.visible) || end-start >= len(model.visible) {
		t.Fatalf("range = %d:%d for %d choices", start, end, len(model.visible))
	}
	if strings.Count(model.help(), "\n") < 2 {
		t.Fatalf("help did not wrap: %q", model.help())
	}
}

func TestChoiceModelUsesDefaultsAndShowsBounds(t *testing.T) {
	minimum, maximum := 1, 3
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, Required: true,
		MinSelected: &minimum, MaxSelected: &maximum,
		Options: []Option{
			{Label: "A", Default: true},
			{Label: "B"},
			{Label: "C", Default: true},
		},
	})
	if !slices.Equal(model.order, []int{0, 2}) {
		t.Fatalf("default order = %#v", model.order)
	}
	view := model.View().Content
	for _, want := range []string{"selected: 2", "min: 1", "max: 3", "ctrl+a select all", "ctrl+x clear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}

	single := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: false,
		Options: []Option{{Label: "A"}, {Label: "B", Default: true}},
	})
	if single.cursor != 2 || single.visible[single.cursor].label != "B" {
		t.Fatalf("cursor = %d, visible = %#v", single.cursor, single.visible)
	}
}

func TestChoiceModelSelectsAllEnabledOptionsByDefault(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, SelectAll: true,
		Options: []Option{
			{Label: "A"},
			{Label: "Unavailable", Disabled: true, DisabledReason: "not ready"},
			{Label: "C"},
		},
	})
	if !slices.Equal(model.order, []int{0, 2}) {
		t.Fatalf("select-all order = %#v", model.order)
	}
	if model.selected[1] {
		t.Fatal("disabled option was selected")
	}
}

func TestChoiceModelShowsAndBlocksDisabledChoice(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Required: true,
		Options: []Option{
			{Label: "Unavailable", Description: "Production", Disabled: true, DisabledReason: "maintenance window"},
			{Label: "Available"},
		},
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command != nil || model.done || model.err != "maintenance window" {
		t.Fatalf("done = %v, err = %q, command nil = %v", model.done, model.err, command == nil)
	}
	view := model.View().Content
	for _, want := range []string{"Unavailable", "Production", "disabled: maintenance window"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want %q", view, want)
		}
	}
	model.filter.SetValue("maintenance")
	model.refreshVisible()
	if len(model.visible) != 1 || model.visible[0].label != "Unavailable" {
		t.Fatalf("visible = %#v", model.visible)
	}
}

func TestChoiceModelBulkActionsRespectFilterAndMaximum(t *testing.T) {
	maximum := 3
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, MaxSelected: &maximum,
		Options: []Option{
			{Label: "A", Description: "visible"},
			{Label: "B", Description: "visible"},
			{Label: "C", Description: "visible"},
			{Label: "D", Description: "hidden", Default: true},
		},
	})
	model.filter.SetValue("visible")
	model.refreshVisible()
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	model = updated.(choiceModel)
	if !slices.Equal(model.order, []int{3, 0, 1}) {
		t.Fatalf("order after select all = %#v", model.order)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model = updated.(choiceModel)
	if !slices.Equal(model.order, []int{3}) || !model.selected[3] {
		t.Fatalf("order after clear = %#v, selected = %#v", model.order, model.selected)
	}
}

func TestChoiceModelEnforcesSelectionBounds(t *testing.T) {
	minimum, maximum := 2, 2
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Multiple: true, MinSelected: &minimum, MaxSelected: &maximum,
		Options: []Option{{Label: "A"}, {Label: "B"}, {Label: "C"}},
	})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command != nil || model.err != "select at least 2 values" {
		t.Fatalf("err = %q, command nil = %v", model.err, command == nil)
	}
	model.cursor = 1
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	model.cursor = 2
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	model = updated.(choiceModel)
	if model.err != "select at most 2 values" || !slices.Equal(model.order, []int{0, 1}) {
		t.Fatalf("err = %q, order = %#v", model.err, model.order)
	}
}

func TestChoicePickerConfigValidation(t *testing.T) {
	negative, zero, one := -1, 0, 1
	tests := []struct {
		name   string
		config ChoicePickerConfig
	}{
		{name: "bounds in single mode", config: ChoicePickerConfig{MinSelected: &zero}},
		{name: "negative minimum", config: ChoicePickerConfig{Multiple: true, MinSelected: &negative}},
		{name: "inverted bounds", config: ChoicePickerConfig{Multiple: true, MinSelected: &one, MaxSelected: &zero}},
		{name: "select all in single mode", config: ChoicePickerConfig{SelectAll: true}},
		{name: "disabled without reason", config: ChoicePickerConfig{Options: []Option{{Disabled: true}}}},
		{name: "disabled default", config: ChoicePickerConfig{Options: []Option{{Disabled: true, DisabledReason: "no", Default: true}}}},
		{name: "multiple single defaults", config: ChoicePickerConfig{Options: []Option{{Default: true}, {Default: true}}}},
		{name: "minimum exceeds enabled", config: ChoicePickerConfig{Multiple: true, MinSelected: &one, Options: []Option{{Disabled: true, DisabledReason: "no"}}}},
		{name: "defaults exceed maximum", config: ChoicePickerConfig{Multiple: true, MaxSelected: &zero, Options: []Option{{Default: true}}}},
		{name: "select all exceeds maximum", config: ChoicePickerConfig{Multiple: true, SelectAll: true, MaxSelected: &zero, Options: []Option{{}, {}}}},
		{name: "empty section", config: ChoicePickerConfig{Options: []Option{{}}, Markers: []ChoiceMarker{{Kind: ChoiceMarkerSection, Before: 0}}}},
		{name: "leading separator", config: ChoicePickerConfig{Options: []Option{{}}, Markers: []ChoiceMarker{{Kind: ChoiceMarkerSeparator, Before: 0}}}},
		{name: "trailing marker", config: ChoicePickerConfig{Options: []Option{{}}, Markers: []ChoiceMarker{{Kind: ChoiceMarkerSection, Before: 1, Label: "Later"}}}},
		{name: "duplicate separator", config: ChoicePickerConfig{Options: []Option{{}, {}}, Markers: []ChoiceMarker{{Kind: ChoiceMarkerSeparator, Before: 1}, {Kind: ChoiceMarkerSeparator, Before: 1}}}},
		{name: "section then separator", config: ChoicePickerConfig{Options: []Option{{}, {}}, Markers: []ChoiceMarker{{Kind: ChoiceMarkerSection, Before: 1, Label: "Later"}, {Kind: ChoiceMarkerSeparator, Before: 1}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateChoicePickerConfig(tt.config); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestConfirmModelUsesDefaultSelection(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Continue?", Options: []Option{{Label: "Yes", Value: true}, {Label: "No", Value: false}}, Required: true,
	})
	model.cursor = 1
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(choiceModel)
	if command == nil || len(model.result) != 1 || model.result[0] != 1 {
		t.Fatalf("result = %#v, command nil = %v", model.result, command == nil)
	}
}

func TestSelectionModelNavigationAndEnter(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}, {Label: "deploy"}})
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(selectionModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(selectionModel)
	if command == nil || model.selected.Label != "deploy" || !model.done {
		t.Fatalf("selected = %#v, done = %v, command nil = %v", model.selected, model.done, command == nil)
	}
	if model.intent != SelectionPrimary {
		t.Fatalf("intent = %v, want primary", model.intent)
	}
}

func TestSelectionModelShiftEnterUsesAlternateIntent(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.selected.Label != "build" || model.intent != SelectionAlternate {
		t.Fatalf("selected = %#v, intent = %v, done = %v, command nil = %v", model.selected, model.intent, model.done, command == nil)
	}
}

func TestSelectionModelUUsesBrowserUIIntent(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'u'})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.selected.Label != "build" || model.intent != SelectionUI {
		t.Fatalf("selected = %#v, intent = %v, done = %v, command nil = %v", model.selected, model.intent, model.done, command == nil)
	}
}

func TestSelectionModelMUsesMarketplaceIntent(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "release", URL: "https://example.test/marketplace/"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'm'})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.selected.Label != "release" || model.intent != SelectionMarketplace {
		t.Fatalf("selected = %#v, intent = %v, done = %v, command nil = %v", model.selected, model.intent, model.done, command == nil)
	}
}

func TestSelectionModelRUsesReinstallIntent(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "release", URL: "https://example.test/marketplace/"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'r'})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.selected.Label != "release" || model.intent != SelectionReinstall {
		t.Fatalf("selected = %#v, intent = %v, done = %v, command nil = %v", model.selected, model.intent, model.done, command == nil)
	}
}

func TestSelectionModelSupportsPickerActions(t *testing.T) {
	tests := []struct {
		name   string
		input  tea.KeyPressMsg
		intent SelectionIntent
	}{
		{name: "editor", input: tea.KeyPressMsg{Code: 'e'}, intent: SelectionEditor},
		{name: "pin", input: tea.KeyPressMsg{Code: 'p'}, intent: SelectionTogglePin},
		{name: "sort", input: tea.KeyPressMsg{Code: 's'}, intent: SelectionToggleSort},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newSelectionModel("Workflows", []Option{{Label: "build"}})
			updated, command := model.Update(test.input)
			model = updated.(selectionModel)
			if command == nil || !model.done || model.intent != test.intent {
				t.Fatalf("intent = %v, done = %v, command nil = %v", model.intent, model.done, command == nil)
			}
		})
	}
}

func TestSelectionModelUsesDefaultOption(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}, {Label: "deploy", Default: true}})
	item, ok := model.list.SelectedItem().(listOption)
	if !ok || item.Label != "deploy" {
		t.Fatalf("selected item = %#v, want deploy", model.list.SelectedItem())
	}
}

func TestSelectionModelFiltering(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build", Description: "local"}, {Label: "deploy", Description: "global"}})
	model.list.SetFilterText("global")
	if got := len(model.list.VisibleItems()); got != 1 {
		t.Fatalf("visible items = %d, want 1", got)
	}
	item, ok := model.list.SelectedItem().(listOption)
	if !ok || item.Label != "deploy" {
		t.Fatalf("selected item = %#v, want deploy", model.list.SelectedItem())
	}
}

func TestSelectionModelCtrlCCancels(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(selectionModel)
	if command == nil || !model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestSelectionModelEscapeDoesNotCancel(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build"}})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if command != nil || model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestChoiceModelEscapeDoesNotCancel(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A"}}, Required: true,
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(choiceModel)
	if command != nil || model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestChoiceModelCancelsWithCtrlC(t *testing.T) {
	model := newChoiceModel(ChoicePickerConfig{
		Message: "Pick", Options: []Option{{Label: "A"}}, Required: true,
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(choiceModel)
	if command == nil || !model.cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", model.cancelled, command == nil)
	}
}

func TestSelectionModelViewIncludesDescription(t *testing.T) {
	path := "/a/" + strings.Repeat("very-long-workflow-directory/", 5) + "build.yaml"
	model := newSelectionModel("Workflows", []Option{{Label: "build", Description: "local • Build it", Path: path}})
	if !strings.Contains(model.View().Content, "local") {
		t.Fatalf("view = %q", model.View().Content)
	}
	if !strings.Contains(model.View().Content, path) {
		t.Fatalf("view = %q, want full path", model.View().Content)
	}
	for _, shortcut := range []string{"enter", "run", "a", "actions", "ctrl+c", "cancel"} {
		if !strings.Contains(model.View().Content, shortcut) {
			t.Fatalf("view = %q, want shortcut %q", model.View().Content, shortcut)
		}
	}
	for _, hidden := range []string{"open UI", "print command", "edit", "pin", "sort"} {
		if strings.Contains(model.View().Content, hidden) {
			t.Fatalf("short help = %q, unexpectedly contains %q", model.View().Content, hidden)
		}
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: '?'})
	fullHelp := updated.(selectionModel).View().Content
	for _, shortcut := range []string{"open UI", "print command", "edit", "pin", "sort"} {
		if !strings.Contains(fullHelp, shortcut) {
			t.Fatalf("full help = %q, want shortcut %q", fullHelp, shortcut)
		}
	}
}

func TestSelectionModelActionPaletteSelectsIntentAndReturnsFromDisabledAction(t *testing.T) {
	model := newSelectionModelWithConfig(SelectionPickerConfig{
		Title: "Workflows", InitialFilter: "release", Options: []Option{{
			Label: "release", Description: "local", Actions: []SelectionAction{
				{Label: "Run", Intent: SelectionPrimary},
				{Label: "Open form", Intent: SelectionUI, Disabled: true, DisabledReason: "no form"},
				{Label: "Validate", Intent: SelectionValidate},
			},
		}},
	})
	updated, command := model.Update(tea.KeyPressMsg{Code: 'a'})
	model = updated.(selectionModel)
	if command != nil || !model.actionMode || !strings.Contains(model.View().Content, "Actions — release") {
		t.Fatalf("action mode = %v, command nil = %v, view = %q", model.actionMode, command == nil, model.View().Content)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(selectionModel)
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(selectionModel)
	if command != nil || model.done || model.actionError != "no form" {
		t.Fatalf("disabled action done = %v, command nil = %v, error = %q", model.done, command == nil, model.actionError)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(selectionModel)
	if model.actionMode || model.list.FilterValue() != "release" {
		t.Fatalf("action mode = %v, filter = %q", model.actionMode, model.list.FilterValue())
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'a'})
	model = updated.(selectionModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(selectionModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(selectionModel)
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(selectionModel)
	if command == nil || !model.done || model.intent != SelectionValidate || model.filter != "release" {
		t.Fatalf("intent = %v, filter = %q, done = %v, command nil = %v", model.intent, model.filter, model.done, command == nil)
	}
}

func TestSelectionModelActionPaletteCtrlCCancels(t *testing.T) {
	model := newSelectionModel("Workflows", []Option{{Label: "build", Actions: []SelectionAction{{Label: "Run"}}}})
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'a'})
	model = updated.(selectionModel)
	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil || !updated.(selectionModel).cancelled {
		t.Fatal("ctrl+c did not cancel from action palette")
	}
}

func TestSelectionModelShowsNoticeAndUsesVisibleDefault(t *testing.T) {
	model := newSelectionModelWithConfig(SelectionPickerConfig{
		Title: "Workflows", InitialFilter: "global", Notice: "refresh failed", Options: []Option{
			{Label: "local", Description: "local"},
			{Label: "global", Description: "global", Default: true},
		},
	})
	item, ok := model.list.SelectedItem().(listOption)
	if !ok || item.Label != "global" {
		t.Fatalf("selected item = %#v, want visible default", model.list.SelectedItem())
	}
	if !strings.Contains(model.View().Content, "refresh failed") {
		t.Fatalf("view = %q, want notice", model.View().Content)
	}
}
