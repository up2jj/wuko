package table

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

func TestConfigValidation(t *testing.T) {
	width := 12
	runner := steptest.Build(t, New, map[string]any{
		"message": "Releases", "from": "steps.fetch.items",
		"columns": []any{map[string]any{"header": "Version", "field": "version", "width": width}},
	})
	if got := *runner.(*Runner).config.Columns[0].Width; got != width {
		t.Fatalf("width = %d", got)
	}

	tests := []map[string]any{
		{"from": "vars.rows", "columns": []any{map[string]any{"header": "Name", "field": "name"}}},
		{"message": "Rows", "columns": []any{map[string]any{"header": "Name", "field": "name"}}},
		{"message": "Rows", "from": "vars.rows"},
		{"message": "Rows", "from": "vars.rows", "columns": []any{map[string]any{"field": "name"}}},
		{"message": "Rows", "from": "vars.rows", "columns": []any{map[string]any{"header": "Name"}}},
		{"message": "Rows", "from": "vars.rows", "columns": []any{map[string]any{"header": "Name", "field": "name", "width": 0}}},
		{"message": "Rows", "from": "vars.rows", "columns": []any{map[string]any{"header": "Name", "field": "name", "unknown": true}}},
		{"message": "Rows", "from": "vars.rows", "group_field": "state", "columns": []any{map[string]any{"header": "Name", "field": "name"}}},
		{"message": "Rows", "from": "vars.rows", "browse": false, "group_field": "state", "columns": []any{map[string]any{"header": "State", "field": "state"}}},
	}
	for _, raw := range tests {
		if _, err := New(raw); err == nil {
			t.Fatalf("New(%#v) succeeded", raw)
		}
	}
}

func TestNonInteractiveTableNormalizesAndPrintsRows(t *testing.T) {
	runner := newRunner(t)
	var output bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"rows": []any{
			map[string]any{
				"name":     "\x1b[31mfirst\x1b[0m\nrelease",
				"metadata": map[string]any{"channel": "stable"},
				"details":  map[string]any{"signed": true},
			},
			map[string]any{
				"name": "second", "metadata": map[string]any{"channel": nil},
				"details": []any{"linux", "darwin"},
			},
		}},
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs != nil || result.Variables != nil {
		t.Fatalf("result = %#v", result)
	}
	for _, expected := range []string{
		"Release table", "Name", "Channel", "Details", "first release", "stable",
		`{"signed":true}`, `["linux","darwin"]`,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output = %q, want %q", output.String(), expected)
		}
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("output contains ANSI: %q", output.String())
	}
}

func TestPrintModeWritesTableWithoutBrowsing(t *testing.T) {
	runner := steptest.BuildAs[*Runner](t, New, map[string]any{
		"message": "About to start", "from": "vars.rows", "browse": false,
		"columns": []any{map[string]any{"header": "Service", "field": "name"}},
	})
	var output bytes.Buffer
	// An interactive run with no input would block in the browser; print mode returns at once.
	_, err := runner.Run(t.Context(), step.Request{
		Vars:        map[string]any{"rows": []any{map[string]any{"name": "api"}}},
		Stdin:       strings.NewReader(""),
		Stdout:      &output,
		Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "About to start\n Service \n api"; !strings.HasPrefix(output.String(), want) {
		t.Fatalf("output = %q, want prefix %q", output.String(), want)
	}
}

func TestPrintModeGroupsRowsByFirstAppearance(t *testing.T) {
	runner := steptest.BuildAs[*Runner](t, New, map[string]any{
		"message": "Plan", "from": "vars.rows", "browse": false, "group_field": "state",
		"columns": []any{
			map[string]any{"header": "State", "field": "state"},
			map[string]any{"header": "Service", "field": "name"},
		},
	})
	var output bytes.Buffer
	_, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"rows": []any{
			map[string]any{"state": "start", "name": "api"},
			map[string]any{"state": "skip", "name": "db"},
			map[string]any{"state": "start", "name": "web"},
			map[string]any{"state": nil, "name": "cache"},
		}},
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for line := range strings.Lines(output.String()) {
		lines = append(lines, strings.TrimRight(line, " \n"))
	}
	want := []string{"Plan", " Service", "start", " api", " web", "", "skip", " db", "", "(none)", " cache", ""}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Fatalf("lines (-want +got):\n%s", diff)
	}
}

func TestEmptySourcePrintsEmptyMessage(t *testing.T) {
	runner := steptest.BuildAs[*Runner](t, New, map[string]any{
		"message": "About to start", "from": "vars.rows", "empty": "Nothing to start.",
		"columns": []any{map[string]any{"header": "Service", "field": "name"}},
	})
	var output bytes.Buffer
	_, err := runner.Run(t.Context(), step.Request{
		Vars: map[string]any{"rows": []any{}}, Stdin: strings.NewReader(""), Stdout: &output, Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "About to start\nNothing to start.\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestTableSourceErrorsIncludeRowAndColumn(t *testing.T) {
	runner := newRunner(t)
	tests := []struct {
		rows any
		want string
	}{
		{rows: "not a list", want: "is not a list"},
		{rows: []any{"not an object"}, want: "row 1 is not an object"},
		{rows: []any{map[string]any{"name": "release"}}, want: `row 1 column "Channel"`},
	}
	for _, test := range tests {
		_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"rows": test.rows}, Stdout: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("error = %v, want %q", err, test.want)
		}
	}

	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{}, Stdout: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "resolving table source") {
		t.Fatalf("lookup error = %v", err)
	}
}

func TestTableNormalizationHonorsCancellation(t *testing.T) {
	runner := newRunner(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := runner.Run(ctx, step.Request{
		Vars: map[string]any{"rows": []any{map[string]any{
			"name": "release", "metadata": map[string]any{"channel": "stable"}, "details": nil,
		}}},
		Stdout: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("error = %v", err)
	}
}

func TestFormatCellRejectsUnsupportedStructuredValue(t *testing.T) {
	if _, err := formatCell(make(chan int)); err == nil || !strings.Contains(err.Error(), "encoding cell as JSON") {
		t.Fatalf("error = %v", err)
	}
}

func newRunner(t testing.TB) *Runner {
	t.Helper()
	return steptest.BuildAs[*Runner](t, New, map[string]any{
		"message": "Release table", "from": "vars.rows",
		"columns": []any{
			map[string]any{"header": "Name", "field": "name", "width": 20},
			map[string]any{"header": "Channel", "field": "metadata.channel"},
			map[string]any{"header": "Details", "field": "details"},
		},
	})
}

func BenchmarkNormalizeTable1000Rows(b *testing.B)  { benchmarkNormalizeTable(b, 1_000) }
func BenchmarkNormalizeTable10000Rows(b *testing.B) { benchmarkNormalizeTable(b, 10_000) }

func benchmarkNormalizeTable(b *testing.B, count int) {
	runner := newRunner(b)
	rows := make([]any, count)
	for index := range rows {
		rows[index] = map[string]any{
			"name": "release", "metadata": map[string]any{"channel": "stable"},
			"details": map[string]any{"signed": true},
		}
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := runner.tableConfig(b.Context(), rows); err != nil {
			b.Fatal(err)
		}
	}
}

func TestNewRejectsFromOutsideVarsAndSteps(t *testing.T) {
	t.Parallel()
	_, err := New(map[string]any{"message": "Rows", "from": "rows", "columns": []any{map[string]any{"header": "A", "field": "a"}}})
	if err == nil || !strings.Contains(err.Error(), "rooted at vars or steps") {
		t.Fatalf("error = %v", err)
	}
}
