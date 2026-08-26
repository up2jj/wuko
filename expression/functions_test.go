package expression

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

func TestEmptyUsesGoTemplateTruthRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "nil", value: nil, want: true},
		{name: "false", value: false, want: true},
		{name: "true", value: true, want: false},
		{name: "zero", value: 0, want: true},
		{name: "number", value: 1, want: false},
		{name: "empty string", value: "", want: true},
		{name: "string", value: "value", want: false},
		{name: "empty slice", value: []string{}, want: true},
		{name: "slice", value: []string{"value"}, want: false},
		{name: "empty map", value: map[string]any{}, want: true},
		{name: "map", value: map[string]any{"key": "value"}, want: false},
		{name: "struct", value: struct{}{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := empty(tt.value); got != tt.want {
				t.Fatalf("empty(%#v) = %t, want %t", tt.value, got, tt.want)
			}
		})
	}
}

func TestCollectionHelpersAreStrictAndDeterministic(t *testing.T) {
	t.Parallel()
	dictionary, err := dict("b", 2, "a", 1)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := keys(dictionary)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"a", "b"}) {
		t.Fatalf("keys = %#v", keys)
	}
	values := []string{"b", "a"}
	sorted, err := sortAlpha(values)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sorted, []string{"a", "b"}) || !reflect.DeepEqual(values, []string{"b", "a"}) {
		t.Fatalf("sorted = %#v, input = %#v", sorted, values)
	}
	if _, err := dict("key"); err == nil {
		t.Fatal("expected odd dict arguments to fail")
	}
	if _, err := dict(1, "value"); err == nil {
		t.Fatal("expected non-string dict key to fail")
	}
	if _, err := sortAlpha([]any{"a", 1}); err == nil {
		t.Fatal("expected non-string list item to fail")
	}
}

func TestExprTypedCollectionHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   any
	}{
		{
			name:   "group by",
			source: `groupBy([{"id": "api", "tier": "backend"}, {"id": "web", "tier": "frontend"}, {"id": "worker", "tier": "backend"}], .tier)`,
			want: map[any][]any{
				"backend": {
					map[string]any{"id": "api", "tier": "backend"},
					map[string]any{"id": "worker", "tier": "backend"},
				},
				"frontend": {map[string]any{"id": "web", "tier": "frontend"}},
			},
		},
		{name: "sort", source: `sort([3, 1, 2])`, want: []any{1, 2, 3}},
		{
			name:   "sort by",
			source: `sortBy([{"name": "api", "priority": 2}, {"name": "web", "priority": 1}], .priority)`,
			want: []any{
				map[string]any{"name": "web", "priority": 1},
				map[string]any{"name": "api", "priority": 2},
			},
		},
		{name: "unique", source: `uniq(["api", "worker", "api"])`, want: []any{"api", "worker"}},
		{name: "flatten", source: `flatten([["api"], [["worker", "web"]]])`, want: []any{"api", "worker", "web"}},
		{
			name:   "index by",
			source: `indexBy([{"id": "api", "port": 8080}, {"id": "web", "port": 3000}], "id")`,
			want: map[string]any{
				"api": map[string]any{"id": "api", "port": 8080},
				"web": map[string]any{"id": "web", "port": 3000},
			},
		},
		{name: "chunk", source: `chunk(["api", "worker", "web"], 2)`, want: [][]any{{"api", "worker"}, {"web"}}},
		{name: "composed pipeline", source: `[3, 1, 2, 3] | uniq() | sort() | chunk(2)`, want: [][]any{{1, 2}, {3}}},
		{name: "empty index", source: `indexBy([], "id")`, want: map[string]any{}},
		{name: "empty chunks", source: `chunk([], 2)`, want: [][]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Eval(tt.source, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("value = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestExprTypedCollectionHelpersAcceptTypedListsWithoutMutation(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3}
	got, err := Eval(`chunk(items, 2)`, map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]any{{1, 2}, {3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("value = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(items, []int{1, 2, 3}) {
		t.Fatalf("input mutated: %#v", items)
	}
	array := [3]string{"api", "worker", "web"}
	got, err = Eval(`chunk(items, 10)`, map[string]any{"items": array})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]any{{"api", "worker", "web"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("value = %#v, want %#v", got, want)
	}

	indexed, err := Eval(`indexBy(items, "id")`, map[string]any{
		"items": []map[string]string{{"id": "api"}, {"id": "worker"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"api":    map[string]string{"id": "api"},
		"worker": map[string]string{"id": "worker"},
	}
	if !reflect.DeepEqual(indexed, want) {
		t.Fatalf("value = %#v, want %#v", indexed, want)
	}
}

func TestExprTypedCollectionHelperErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source string
		want   string
	}{
		{source: `indexBy(nil, "id")`, want: "expected list or array"},
		{source: `indexBy([1], "id")`, want: "item 0"},
		{source: `indexBy([{"name": "api"}], "id")`, want: `has no field "id"`},
		{source: `indexBy([{"id": nil}], "id")`, want: "want string"},
		{source: `indexBy([{"id": 1}], "id")`, want: "want string"},
		{source: `indexBy([{"id": "api"}, {"id": "api"}], "id")`, want: `duplicate index key "api" at item 1`},
		{source: `chunk("api", 2)`, want: "expected list or array"},
		{source: `chunk([], 0)`, want: "chunk size must be at least 1"},
		{source: `chunk([], -1)`, want: "chunk size must be at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			_, err := Eval(tt.source, map[string]any{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestExprOnlyCollectionHelpersAreNotTemplateFunctions(t *testing.T) {
	t.Parallel()
	functions := TemplateFuncs()
	for _, name := range []string{"indexBy", "chunk"} {
		if _, exists := functions[name]; exists {
			t.Fatalf("%s unexpectedly exposed to Go templates", name)
		}
	}
}

func TestSerializationHelpers(t *testing.T) {
	t.Parallel()
	value := map[string]any{"b": 2, "a": 1}
	pretty, err := toJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if pretty != "{\n  \"a\": 1,\n  \"b\": 2\n}" {
		t.Fatalf("pretty JSON = %q", pretty)
	}
	compact, err := toJSONCompact(value)
	if err != nil {
		t.Fatal(err)
	}
	if compact != `{"a":1,"b":2}` {
		t.Fatalf("compact JSON = %q", compact)
	}
	yamlValue, err := toYAML(value)
	if err != nil {
		t.Fatal(err)
	}
	if yamlValue != "a: 1\nb: 2\n" {
		t.Fatalf("YAML = %q", yamlValue)
	}
	if _, err := toJSON(make(chan int)); err == nil {
		t.Fatal("expected unsupported JSON value to fail")
	}
}

func TestTemplateAndExprHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		template   string
		expression string
		want       string
	}{
		{name: "strings", template: `{{ "  HELLO_world  " | trim | lower | replace "_" "-" }}`, expression: `replace(lower(trim("  HELLO_world  ")), "_", "-")`, want: "hello-world"},
		{name: "upper", template: `{{ "wuko" | upper }}`, expression: `upper("wuko")`, want: "WUKO"},
		{name: "trim prefix", template: `{{ "release-v1" | trimPrefix "release-" }}`, expression: `trimPrefix("release-v1", "release-")`, want: "v1"},
		{name: "trim suffix", template: `{{ "release.yaml" | trimSuffix ".yaml" }}`, expression: `trimSuffix("release.yaml", ".yaml")`, want: "release"},
		{name: "has prefix", template: `{{ "release-v1" | hasPrefix "release-" }}`, expression: `string(hasPrefix("release-v1", "release-"))`, want: "true"},
		{name: "has suffix", template: `{{ "release.yaml" | hasSuffix ".yaml" }}`, expression: `string(hasSuffix("release.yaml", ".yaml"))`, want: "true"},
		{name: "split and join", template: `{{ "a,b" | split "," | join ":" }}`, expression: `join(split("a,b", ","), ":")`, want: "a:b"},
		{name: "default", template: `{{ "" | default "fallback" }}`, expression: `default("", "fallback")`, want: "fallback"},
		{name: "coalesce", template: `{{ coalesce "" 0 "value" }}`, expression: `coalesce("", 0, "value")`, want: "value"},
		{name: "required", template: `{{ "value" | required "missing value" }}`, expression: `required("value", "missing value")`, want: "value"},
		{name: "indent", template: `{{ "one\ntwo" | indent 2 }}`, expression: `indent("one\ntwo", 2)`, want: "  one\n  two"},
		{name: "nindent", template: `{{ "one\ntwo" | nindent 2 }}`, expression: `nindent("one\ntwo", 2)`, want: "\n  one\n  two"},
		{name: "join and sort", template: `{{ list "b" "a" | sortAlpha | join "," }}`, expression: `join(sortAlpha(list("b", "a")), ",")`, want: "a,b"},
		{name: "map lookup", template: `{{ dict "answer" 42 | get "answer" }}`, expression: `string(get(dict("answer", 42), "answer"))`, want: "42"},
		{name: "has key", template: `{{ dict "answer" 42 | hasKey "answer" }}`, expression: `string(hasKey(dict("answer", 42), "answer"))`, want: "true"},
		{name: "keys", template: `{{ dict "b" 2 "a" 1 | keys | join "," }}`, expression: `join(keys(dict("b", 2, "a", 1)), ",")`, want: "a,b"},
		{name: "pretty JSON", template: `{{ dict "b" 2 "a" 1 | toJSON }}`, expression: `toJSON(dict("b", 2, "a", 1))`, want: "{\n  \"a\": 1,\n  \"b\": 2\n}"},
		{name: "compact JSON", template: `{{ dict "b" 2 "a" 1 | toJSONCompact }}`, expression: `toJSONCompact(dict("b", 2, "a", 1))`, want: `{"a":1,"b":2}`},
		{name: "YAML", template: `{{ dict "b" 2 "a" 1 | toYAML }}`, expression: `toYAML(dict("b", 2, "a", 1))`, want: "a: 1\nb: 2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTemplate, err := renderTemplate(tt.template)
			if err != nil {
				t.Fatalf("template: %v", err)
			}
			gotExpr, err := Eval(tt.expression, map[string]any{})
			if err != nil {
				t.Fatalf("Expr: %v", err)
			}
			if gotTemplate != tt.want || gotExpr != tt.want {
				t.Fatalf("template = %#v, Expr = %#v, want %q", gotTemplate, gotExpr, tt.want)
			}
		})
	}
}

func TestLanguageSpecificContainsSyntax(t *testing.T) {
	t.Parallel()
	gotTemplate, err := renderTemplate(`{{ "workflow" | contains "flow" }}`)
	if err != nil {
		t.Fatal(err)
	}
	gotExpr, err := Eval(`"workflow" contains "flow"`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if gotTemplate != "true" || gotExpr != true {
		t.Fatalf("template = %#v, Expr = %#v", gotTemplate, gotExpr)
	}
}

func TestRequiredAndIndentErrorsStopEvaluation(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		`required("", "value is required")`,
		`indent("value", -1)`,
		`dict("key")`,
		`sortAlpha(list("value", 1))`,
	} {
		if _, err := Eval(source, map[string]any{}); err == nil {
			t.Fatalf("Eval(%q) succeeded", source)
		}
	}
	for _, source := range []string{
		`{{ "" | required "value is required" }}`,
		`{{ "value" | indent -1 }}`,
		`{{ dict "key" }}`,
		`{{ list "value" 1 | sortAlpha }}`,
	} {
		if _, err := renderTemplate(source); err == nil {
			t.Fatalf("template %q succeeded", source)
		}
	}
}

func renderTemplate(source string) (string, error) {
	tmpl, err := template.New("test").Funcs(TemplateFuncs()).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{}); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func TestRequiredErrorIncludesMessage(t *testing.T) {
	t.Parallel()
	_, err := Eval(`required("", "application is required")`, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "application is required") {
		t.Fatalf("error = %v", err)
	}
}
