package tui

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestTablePaginationNavigationAndResize(t *testing.T) {
	config := TableConfig{
		Message: "Releases",
		Columns: []TableColumn{{Header: "Version"}, {Header: "Channel"}},
		Rows: [][]string{
			{"1.0.0", "stable"}, {"1.1.0", "stable"}, {"1.2.0", "beta"},
			{"2.0.0", "stable"}, {"2.1.0", "beta"}, {"2.2.0", "edge"}, {"3.0.0", "stable"},
		},
	}
	model := newTableModel(config)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 9})
	model = updated.(tableModel)
	if model.pageSize != 5 || model.status() != "rows: 7 • showing: 1-5 • page: 1/2" {
		t.Fatalf("page size = %d, status = %q", model.pageSize, model.status())
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	model = updated.(tableModel)
	if model.cursor != 5 || model.status() != "rows: 7 • showing: 6-7 • page: 2/2" {
		t.Fatalf("cursor = %d, status = %q", model.cursor, model.status())
	}
	if len(model.table.Rows()) != 2 {
		t.Fatalf("rendered rows = %d, want 2", len(model.table.Rows()))
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	model = updated.(tableModel)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	model = updated.(tableModel)
	if model.cursor != 6 {
		t.Fatalf("end cursor = %d", model.cursor)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if updated.(tableModel).cursor != 1 {
		t.Fatalf("page-up cursor = %d", updated.(tableModel).cursor)
	}
}

func TestTableViewUsesEstablishedStylesAndHelp(t *testing.T) {
	model := newTableModel(TableConfig{
		Message: "Releases",
		Columns: []TableColumn{{Header: "Version"}},
		Rows:    [][]string{{"1.0.0"}},
	})
	view := model.View().Content
	for _, expected := range []string{
		interactiveStyles.message.Render("Releases"),
		interactiveStyles.label.Bold(true).Padding(0, 1).Render("Version"),
		"rows: 1 • showing: 1-1 • page: 1/1",
		"↑/↓ scroll", "pgup/pgdown page", "enter continue", cancelHelp,
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view = %q, want %q", view, expected)
		}
	}
}

func TestTableCompletionCancellationAndEmptyState(t *testing.T) {
	config := TableConfig{Message: "Empty", Columns: []TableColumn{{Header: "Value"}}}
	model := newTableModel(config)
	if model.status() != "rows: 0 • showing: 0-0 • page: 0/0" {
		t.Fatalf("status = %q", model.status())
	}

	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || !updated.(tableModel).done {
		t.Fatalf("done = %v, command nil = %v", updated.(tableModel).done, command == nil)
	}

	model = newTableModel(config)
	updated, command = model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command == nil || !updated.(tableModel).cancelled {
		t.Fatalf("cancelled = %v, command nil = %v", updated.(tableModel).cancelled, command == nil)
	}
}

func TestTableWidthsFitWithoutRescanningRows(t *testing.T) {
	config := TableConfig{
		Message: "Widths",
		Columns: []TableColumn{{Header: "Fixed", Width: 10}, {Header: "Automatic"}},
		Rows:    [][]string{{"long fixed value", "an even longer automatic value"}},
	}
	model := newTableModel(config)
	if !slices.Equal(model.preferred, []int{10, 30}) {
		t.Fatalf("preferred widths = %v", model.preferred)
	}
	before := slices.Clone(model.preferred)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 12, Height: 10})
	model = updated.(tableModel)
	if !slices.Equal(model.preferred, before) {
		t.Fatalf("resize changed cached widths: before %v, after %v", before, model.preferred)
	}
	widths := model.table.Columns()
	if widths[0].Width+widths[1].Width+2*tableCellPadding > 12 {
		t.Fatalf("fitted widths = %#v", widths)
	}
	if !strings.Contains(model.table.View(), "…") {
		t.Fatalf("narrow table did not truncate: %q", model.table.View())
	}
}

func TestTableLargeDataKeepsOnlyCurrentPageInWidget(t *testing.T) {
	rows := make([][]string, 10_000)
	for index := range rows {
		rows[index] = []string{"record"}
	}
	model := newTableModel(TableConfig{
		Message: "Records", Columns: []TableColumn{{Header: "Name"}}, Rows: rows,
	})
	if len(model.rows) != 10_000 {
		t.Fatalf("normalized rows = %d", len(model.rows))
	}
	if len(model.table.Rows()) != model.pageSize {
		t.Fatalf("widget rows = %d, page size = %d", len(model.table.Rows()), model.pageSize)
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	model = updated.(tableModel)
	if model.cursor != 9_999 || len(model.table.Rows()) > model.pageSize {
		t.Fatalf("cursor = %d, widget rows = %d", model.cursor, len(model.table.Rows()))
	}
}

func TestWriteTableIsPlainAndComplete(t *testing.T) {
	var output bytes.Buffer
	err := WriteTable(&output, TableConfig{
		Message: "Releases",
		Columns: []TableColumn{{Header: "Version"}},
		Rows:    [][]string{{"1.0.0"}, {"2.0.0"}},
	}, 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("static output contains ANSI: %q", output.String())
	}
	for _, expected := range []string{"Releases", "Version", "1.0.0", "2.0.0"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output = %q, want %q", output.String(), expected)
		}
	}
}

func TestTableConfigValidation(t *testing.T) {
	tests := []TableConfig{
		{},
		{Message: "Table"},
		{Message: "Table", Columns: []TableColumn{{}}},
		{Message: "Table", Columns: []TableColumn{{Header: "Value", Width: -1}}},
		{Message: "Table", Columns: []TableColumn{{Header: "Value"}}, Rows: [][]string{{"one", "two"}}},
	}
	for _, config := range tests {
		if err := validateTableConfig(config); err == nil {
			t.Fatalf("validateTableConfig(%#v) succeeded", config)
		}
	}
}

func BenchmarkTableModel1000Rows(b *testing.B)  { benchmarkTableModel(b, 1_000) }
func BenchmarkTableModel10000Rows(b *testing.B) { benchmarkTableModel(b, 10_000) }

func benchmarkTableModel(b *testing.B, count int) {
	rows := make([][]string, count)
	for index := range rows {
		rows[index] = []string{"v1.2.3", "stable", "2026-08-26"}
	}
	config := TableConfig{
		Message: "Releases",
		Columns: []TableColumn{{Header: "Version"}, {Header: "Channel"}, {Header: "Published"}},
		Rows:    rows,
	}
	b.ResetTimer()
	for b.Loop() {
		model := newTableModel(config)
		_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	}
}
