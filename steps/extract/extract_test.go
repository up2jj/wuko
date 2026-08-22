package extract

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

func TestFormatExtractsEveryTypeAndMapsVariables(t *testing.T) {
	runner, err := New(map[string]any{
		"text":   "ignored\nRelease wuko build -7 ratio +1.25 enabled true metadata {\"tags\":[\"go\"],\"attempt\":2}\n",
		"format": `Release {name} build {build:integer} ratio {ratio:number} enabled {enabled:boolean} metadata {metadata:json}`,
		"variables": map[string]any{
			"name": "release_name", "build": "build_number",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	wantOutputs := map[string]any{
		"name": "wuko", "build": int64(-7), "ratio": 1.25, "enabled": true,
		"metadata": map[string]any{"tags": []any{"go"}, "attempt": int64(2)},
	}
	wantVariables := map[string]any{"release_name": "wuko", "build_number": int64(-7)}
	if !reflect.DeepEqual(result.Outputs, wantOutputs) || !reflect.DeepEqual(result.Variables, wantVariables) {
		t.Fatalf("result = %#v, want outputs %#v variables %#v", result, wantOutputs, wantVariables)
	}
}

func TestFormatUsesFlexibleWhitespaceAndCRLFLines(t *testing.T) {
	runner, err := New(map[string]any{
		"text":   "skip\r\nRelease\t\t1.2.3   build\t42\r\n",
		"format": "Release {version} build {build:integer}",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": "1.2.3", "build": int64(42)}
	if !reflect.DeepEqual(result.Outputs, want) {
		t.Fatalf("outputs = %#v, want %#v", result.Outputs, want)
	}
}

func TestFormatEscapesLiteralBracesAndBackslash(t *testing.T) {
	runner, err := New(map[string]any{
		"text":   `path={root}\wuko`,
		"format": `path=\{root\}\\{name}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["name"] != "wuko" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestPatternSearchesTextAndDefaultsCapturesToString(t *testing.T) {
	runner, err := New(map[string]any{
		"text":    "prefix version=1.4.2 build=9 suffix",
		"pattern": `version=(?P<version>\S+) build=(?P<build>[0-9]+)`,
		"types":   map[string]any{"build": "integer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": "1.4.2", "build": int64(9)}
	if !reflect.DeepEqual(result.Outputs, want) {
		t.Fatalf("outputs = %#v, want %#v", result.Outputs, want)
	}
}

func TestPatternReadsStringFromWorkflowState(t *testing.T) {
	runner, err := New(map[string]any{
		"from": "steps.build.stdout", "pattern": `artifact=(?P<artifact>\S+)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{Steps: map[string]any{
		"build": map[string]any{"stdout": "artifact=wuko.tar.gz"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["artifact"] != "wuko.tar.gz" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestPatternAllowsEmptyParticipatingStringCapture(t *testing.T) {
	runner, err := New(map[string]any{"text": "value=", "pattern": `value=(?P<value>.*)`})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestJSONNormalizesRuntimeNumbers(t *testing.T) {
	runner, err := New(map[string]any{
		"text":    `payload={"signed":-2,"unsigned":18446744073709551615,"decimal":1.5}`,
		"pattern": `payload=(?P<payload>.+)`, "types": map[string]any{"payload": "json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"signed": int64(-2), "unsigned": uint64(18446744073709551615), "decimal": 1.5}
	if !reflect.DeepEqual(result.Outputs["payload"], want) {
		t.Fatalf("payload = %#v, want %#v", result.Outputs["payload"], want)
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing source", map[string]any{"pattern": `(?P<x>.)`}, "exactly one of text or from"},
		{"both sources", map[string]any{"text": "x", "from": "vars.x", "pattern": `(?P<x>.)`}, "exactly one of text or from"},
		{"empty from", map[string]any{"from": "", "pattern": `(?P<x>.)`}, "from must not be empty"},
		{"invalid from root", map[string]any{"from": "inputs.x", "pattern": `(?P<x>.)`}, "dotted path rooted"},
		{"missing matcher", map[string]any{"text": "x"}, "exactly one of format or pattern"},
		{"both matchers", map[string]any{"text": "x", "format": "{x}", "pattern": `(?P<x>.)`}, "exactly one of format or pattern"},
		{"types with format", map[string]any{"text": "x", "format": "{x}", "types": map[string]any{"x": "string"}}, "only supported with pattern"},
		{"bad regex", map[string]any{"text": "x", "pattern": "["}, "compiling pattern"},
		{"no named captures", map[string]any{"text": "x", "pattern": `(x)`}, "at least one named capture"},
		{"duplicate regex captures", map[string]any{"text": "x", "pattern": `(?P<x>x)|(?P<x>y)`}, "duplicate capture name"},
		{"invalid regex capture", map[string]any{"text": "x", "pattern": `(?P<1x>x)`}, "invalid capture name"},
		{"unknown capture type", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "types": map[string]any{"other": "string"}}, "unknown capture"},
		{"bad type", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "types": map[string]any{"x": "date"}}, "type must be"},
		{"format no placeholder", map[string]any{"text": "x", "format": "literal"}, "at least one placeholder"},
		{"format multiline", map[string]any{"text": "x", "format": "x\n{value}"}, "single line"},
		{"format open brace", map[string]any{"text": "x", "format": "{value"}, "not closed"},
		{"format closing brace", map[string]any{"text": "x", "format": "{value}}"}, "unescaped closing brace"},
		{"format bad escape", map[string]any{"text": "x", "format": `\x{value}`}, "unsupported escape"},
		{"format bad placeholder", map[string]any{"text": "x", "format": "{bad-name}"}, "invalid capture name"},
		{"format duplicate placeholder", map[string]any{"text": "x", "format": "{x}{x}"}, "duplicate capture name"},
		{"invalid variable source", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "variables": map[string]any{"bad-name": "target"}}, "invalid capture name"},
		{"invalid variable target", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "variables": map[string]any{"x": "bad-name"}}, "invalid variable name"},
		{"duplicate variable target", map[string]any{"text": "x", "pattern": `(?P<x>x)(?P<y>y)`, "variables": map[string]any{"x": "same", "y": "same"}}, "duplicate variable target"},
		{"unknown variable capture", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "variables": map[string]any{"other": "target"}}, "unknown capture"},
		{"unknown field", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "extra": true}, "field extra not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("New() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunRejectsInvalidExtraction(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		req  step.Request
		want string
	}{
		{"no match", map[string]any{"text": "other", "pattern": `value=(?P<value>.+)`}, step.Request{}, "found 0 matches"},
		{"multiple regex matches", map[string]any{"text": "x=1 x=2", "pattern": `x=(?P<x>[0-9]+)`}, step.Request{}, "found 2 matches"},
		{"multiple format lines", map[string]any{"text": "x=1\nx=2", "format": "x={x:integer}"}, step.Request{}, "found 2 matches"},
		{"optional group", map[string]any{"text": "name", "pattern": `name(?:=(?P<value>.+))?`}, step.Request{}, "did not participate"},
		{"integer overflow", map[string]any{"text": "9223372036854775808", "pattern": `(?P<x>.+)`, "types": map[string]any{"x": "integer"}}, step.Request{}, "converting capture"},
		{"non finite number", map[string]any{"text": "Inf", "pattern": `(?P<x>.+)`, "types": map[string]any{"x": "number"}}, step.Request{}, "finite"},
		{"invalid boolean", map[string]any{"text": "TRUE", "pattern": `(?P<x>.+)`, "types": map[string]any{"x": "boolean"}}, step.Request{}, "must be true or false"},
		{"invalid json", map[string]any{"text": "{", "pattern": `(?P<x>.+)`, "types": map[string]any{"x": "json"}}, step.Request{}, "converting capture"},
		{"trailing json", map[string]any{"text": "true false", "pattern": `(?P<x>.+)`, "types": map[string]any{"x": "json"}}, step.Request{}, "multiple JSON values"},
		{"missing from", map[string]any{"from": "vars.missing", "pattern": `(?P<x>.+)`}, step.Request{Vars: map[string]any{}}, "resolving input"},
		{"non string from", map[string]any{"from": "vars.value", "pattern": `(?P<x>.+)`}, step.Request{Vars: map[string]any{"value": 42}}, "want string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() result = %#v, error = %v, want %q", result, err, tt.want)
			}
			if len(result.Outputs) != 0 || len(result.Variables) != 0 {
				t.Fatalf("Run() result = %#v", result)
			}
		})
	}
}

func TestTemplatedMatcherAndSourceValidateBeforeRendering(t *testing.T) {
	if _, err := New(map[string]any{
		"from": "{{ .vars.source }}", "format": "{{ .vars.format }}",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]any{
		"text": "value", "pattern": "{{ .vars.pattern }}", "types": map[string]any{"value": "integer"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	runner, err := New(map[string]any{"text": "value=1", "format": "value={value:integer}"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runner.Run(ctx, step.Request{})
	if err != context.Canceled || len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestDocumentationYAMLExamplesDecode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "extract.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```yaml\\n(.*?)```").FindAllSubmatch(data, -1)
	if len(blocks) < 10 {
		t.Fatalf("found %d YAML examples, want at least 10", len(blocks))
	}
	for index, block := range blocks {
		var value any
		if err := yaml.Unmarshal(block[1], &value); err != nil {
			t.Fatalf("YAML block %d: %v", index, err)
		}
	}
}
