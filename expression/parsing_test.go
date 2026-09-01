package expression

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestParseJSONReturnsRuntimeValues(t *testing.T) {
	t.Parallel()

	got, err := ParseJSON(`{
		"name": "wuko",
		"enabled": true,
		"retries": 3,
		"offset": -2,
		"maximum": 18446744073709551615,
		"ratio": 1.5,
		"exponent": 1e3,
		"items": ["api", null]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "wuko", "enabled": true, "retries": int64(3), "offset": int64(-2),
		"maximum": uint64(math.MaxUint64), "ratio": 1.5, "exponent": 1000.0,
		"items": []any{"api", nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseJSON() = %#v, want %#v", got, want)
	}
}

func TestParseJSONAcceptsScalarsAndWhitespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{name: "null", input: " null \n", want: nil},
		{name: "string", input: ` "wuko" `, want: "wuko"},
		{name: "boolean", input: "\ttrue\r\n", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseJSON(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseJSON() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseJSONRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: " \n\t", want: "JSON input is empty"},
		{name: "malformed", input: `{"name":}`, want: "decoding JSON"},
		{name: "multiple values", input: `{"first":true} {"second":true}`, want: "multiple JSON values"},
		{name: "invalid trailing data", input: `{"first":true} trailing`, want: "invalid data after first JSON value"},
		{name: "integer overflow", input: `18446744073709551616`, want: "out of range"},
		{name: "float overflow", input: `1e400`, want: "converting JSON number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseJSON(tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParseYAMLReturnsRuntimeValues(t *testing.T) {
	t.Parallel()

	got, err := ParseYAML(`
name: wuko
enabled: true
retries: 3
maximum: 18446744073709551615
ratio: 1.5
items:
  - api
  - null
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "wuko", "enabled": true, "retries": int64(3),
		"maximum": uint64(math.MaxUint64), "ratio": 1.5, "items": []any{"api", nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseYAML() = %#v, want %#v", got, want)
	}
}

func TestParseYAMLAcceptsSingleScalarsAndDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{name: "null", input: "---\nnull\n...\n", want: nil},
		{name: "string", input: "wuko\n", want: "wuko"},
		{name: "list", input: "[api, worker]\n", want: []any{"api", "worker"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseYAML(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseYAML() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseYAMLRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "YAML input is empty"},
		{name: "malformed", input: "[one, two", want: "decoding YAML"},
		{name: "multiple documents", input: "name: first\n---\nname: second\n", want: "multiple YAML documents"},
		{name: "empty trailing document", input: "name: first\n---\n", want: "multiple YAML documents"},
		{name: "duplicate keys", input: "name: first\nname: second\n", want: "already defined"},
		{name: "non-string key", input: "1: value\n", want: "mapping key must be a string"},
		{name: "timestamp", input: "created: !!timestamp 2026-09-01T12:00:00Z\n", want: "unsupported parsed value time.Time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseYAML(tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParseHelpersComposeInTemplatesAndExpr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		template   string
		expression string
		want       string
	}{
		{
			name:       "JSON object field",
			template:   `{{ "{\"name\":\"wuko\"}" | parseJSON | get "name" }}`,
			expression: `parseJSON('{"services":[{"name":"wuko"}]}').services[0].name`,
			want:       "wuko",
		},
		{
			name:       "YAML list",
			template:   `{{ "targets:\n  - worker\n  - api\n" | parseYAML | get "targets" | sortAlpha | join "," }}`,
			expression: `join(sortAlpha(parseYAML("targets:\n  - worker\n  - api\n").targets), ",")`,
			want:       "api,worker",
		},
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

func TestParseHelpersRoundTripSerializedValues(t *testing.T) {
	t.Parallel()
	value := map[string]any{"enabled": true, "items": []any{"api", int64(3)}}

	jsonValue, err := ToJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := ParseJSON(jsonValue)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON, value) {
		t.Fatalf("JSON round trip = %#v, want %#v", fromJSON, value)
	}

	yamlValue, err := ToYAML(value)
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, err := ParseYAML(yamlValue)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, value) {
		t.Fatalf("YAML round trip = %#v, want %#v", fromYAML, value)
	}
}
