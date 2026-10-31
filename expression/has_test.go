package expression

import (
	"bytes"
	"testing"
	"text/template"
)

func TestHasUsesExprEquality(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		list, needle any
		want         bool
		invalid      bool
	}{
		{name: "hit", list: []string{"a", "b"}, needle: "a", want: true},
		{name: "miss", list: []string{"a", "b"}, needle: "c"},
		{name: "array", list: [2]int{1, 2}, needle: 2.0, want: true},
		{name: "numeric", list: []any{1.0}, needle: 1, want: true},
		{name: "no string coercion", list: []any{1}, needle: "1"},
		{name: "object", list: []any{map[string]any{"a": 1}}, needle: map[string]any{"a": 1}, want: true},
		{name: "nested list", list: []any{[]any{1, 2}}, needle: []any{1, 2}, want: true},
		{name: "nil", list: nil, needle: 1},
		{name: "empty", list: []any{}, needle: nil},
		{name: "nil element", list: []any{nil}, needle: nil, want: true},
		{name: "string", list: "abc", needle: "a", invalid: true},
		{name: "map", list: map[string]any{"a": 1}, needle: "a", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Has(tt.list, tt.needle)
			if (err != nil) != tt.invalid || got != tt.want {
				t.Fatalf("Has() = %v, %v; want %v, invalid=%v", got, err, tt.want, tt.invalid)
			}
		})
	}
}

func TestHasAdapters(t *testing.T) {
	t.Parallel()
	for _, source := range []string{`{{ has "a" (list "a" "b") }}`, `{{ list "a" "b" | has "a" }}`, `{{ if has 1 (list 1.0) }}true{{ end }}`} {
		parsed, err := template.New("has").Funcs(TemplateFuncs()).Parse(source)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := parsed.Execute(&out, nil); err != nil {
			t.Fatal(err)
		}
		if out.String() != "true" {
			t.Fatalf("%s = %s", source, out.String())
		}
	}
	got, err := Eval(`has([1.0, 2], 1)`, map[string]any{})
	if err != nil || got != true {
		t.Fatalf("Expr has = %v, %v", got, err)
	}
}
