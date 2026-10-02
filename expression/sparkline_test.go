package expression

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestTemplateSparklineRendersNumericSequences(t *testing.T) {
	tests := []struct {
		name   string
		values any
	}{
		{name: "interfaces", values: []any{int64(1), uint32(2), float32(3.5), json.Number("4.25")}},
		{name: "typed integers", values: []int{1, 2, 3, 4}},
		{name: "typed floats", values: [4]float64{1, 2, 3, 4}},
		{name: "zeroes", values: []float64{0, 0, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := templateSparkline(8, 3, test.values)
			if err != nil {
				t.Fatal(err)
			}
			if got == "" {
				t.Fatal("sparkline is empty")
			}
			if width, height := lipgloss.Size(got); width != 8 || height != 3 {
				t.Fatalf("sparkline size = %dx%d, want 8x3\n%s", width, height, got)
			}
		})
	}
}

func TestTemplateSparklineEmptySequenceIsEmpty(t *testing.T) {
	got, err := templateSparkline(8, 3, []float64{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("sparkline = %q, want empty", got)
	}
}

func TestTemplateSparklineKeepsNewestValues(t *testing.T) {
	all, err := templateSparkline(3, 2, []float64{100, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	newest, err := templateSparkline(3, 2, []float64{2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if all != newest {
		t.Fatalf("sparkline retained values beyond its width\nall: %q\nnewest: %q", all, newest)
	}
}

func TestTemplateSparklineScalesFractionalValuesToTheirMaximum(t *testing.T) {
	fractional, err := templateSparkline(3, 3, []float64{0.25, 0.5, 1})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := templateSparkline(3, 3, []float64{1, 2, 4})
	if err != nil {
		t.Fatal(err)
	}
	if fractional != whole {
		t.Fatalf("fractional values were not scaled to their maximum\nfractional: %q\nwhole: %q", fractional, whole)
	}
}

func TestTemplateSparklineSupportsPipelineSyntax(t *testing.T) {
	got, err := renderTemplate(`{{ list 1 2 3 | sparkline 3 2 }}`)
	if err != nil {
		t.Fatal(err)
	}
	if width, height := lipgloss.Size(got); width != 3 || height != 2 {
		t.Fatalf("pipeline sparkline size = %dx%d, want 3x2\n%s", width, height, got)
	}
}

func TestTemplateSparklineRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		values any
		want   string
	}{
		{name: "width", width: 0, height: 1, values: []int{1}, want: "width must be positive"},
		{name: "height", width: 1, height: -1, values: []int{1}, want: "height must be positive"},
		{name: "not a list", width: 1, height: 1, values: 1, want: "numeric list"},
		{name: "negative", width: 1, height: 1, values: []int{-1}, want: "index 0 must not be negative"},
		{name: "not numeric", width: 1, height: 1, values: []any{"one"}, want: "index 0: must be numeric"},
		{name: "null", width: 1, height: 1, values: []any{nil}, want: "index 0: must be numeric, got null"},
		{name: "nan", width: 1, height: 1, values: []float64{math.NaN()}, want: "index 0 must be finite"},
		{name: "infinity", width: 1, height: 1, values: []float64{math.Inf(1)}, want: "index 0 must be finite"},
		{name: "overflow", width: 1, height: 1, values: []any{json.Number("1e400")}, want: "invalid number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := templateSparkline(test.width, test.height, test.values)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("templateSparkline() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSparklineIsTemplateOnly(t *testing.T) {
	if _, exists := TemplateFuncs()["sparkline"]; !exists {
		t.Fatal("template helper sparkline is unavailable")
	}
	if _, err := Eval(`sparkline(4, 1, [1, 2])`, map[string]any{}); err == nil || !strings.Contains(err.Error(), "unknown name sparkline") {
		t.Fatalf("Expr sparkline() error = %v", err)
	}
}
