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

func TestPatternMatchAllReturnsTypedRecordsAndVariableLists(t *testing.T) {
	runner, err := New(map[string]any{
		"text":    "query=SELECT 1; rows=1\nquery=SELECT 2; rows=2\n",
		"pattern": `(?m)^query=(?P<query>.+); rows=(?P<rows>[0-9]+)$`,
		"match":   "all",
		"types":   map[string]any{"rows": "integer"},
		"variables": map[string]any{
			"query": "queries", "rows": "row_counts",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	wantMatches := []any{
		map[string]any{"query": "SELECT 1", "rows": int64(1)},
		map[string]any{"query": "SELECT 2", "rows": int64(2)},
	}
	wantVariables := map[string]any{
		"queries": []any{"SELECT 1", "SELECT 2"}, "row_counts": []any{int64(1), int64(2)},
	}
	if !reflect.DeepEqual(result.Outputs["matches"], wantMatches) || result.Outputs["count"] != 2 || !reflect.DeepEqual(result.Variables, wantVariables) {
		t.Fatalf("result = %#v, want matches %#v variables %#v", result, wantMatches, wantVariables)
	}
}

func TestFormatMatchAllAllowsNoMatches(t *testing.T) {
	runner, err := New(map[string]any{
		"text": "ignored", "format": "value={value:integer}", "match": "all",
		"variables": map[string]any{"value": "values"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Outputs["matches"], []any{}) || result.Outputs["count"] != 0 || !reflect.DeepEqual(result.Variables["values"], []any{}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestFormatMatchAllReturnsRecordsInLineOrder(t *testing.T) {
	runner, err := New(map[string]any{
		"text": "value=2\nignored\nvalue=1\n", "format": "value={value:integer}", "match": "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"value": int64(2)}, map[string]any{"value": int64(1)}}
	if !reflect.DeepEqual(result.Outputs["matches"], want) || result.Outputs["count"] != 2 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestFieldsExtractMarkerPayloadRegexValuesAndVariables(t *testing.T) {
	text := "starting\r\n" +
		`WUKO_OUTPUT_V1 {"event":"begin","key":"action_test_binary"}` + "\r\n" +
		"binary=dist/action.test\r\nsize=128\r\n" +
		`WUKO_OUTPUT_V1 {"event":"end","key":"action_test_binary"}` + "\r\n" +
		"warning: unused import\r\nwarning: slow query\r\n"
	runner, err := New(map[string]any{
		"text": text,
		"fields": map[string]any{
			"build_output": map[string]any{"marker": "action_test_binary"},
			"binary_path": map[string]any{
				"marker": "action_test_binary", "regex": `binary=(?P<value>[^\r\n]+)`,
			},
			"binary_size": map[string]any{
				"marker": "action_test_binary", "regex": `size=(?P<value>[0-9]+)`, "type": "integer",
			},
			"warnings": map[string]any{
				"regex": `warning: (?P<value>[^\r\n]+)`, "match": "all",
			},
		},
		"variables": map[string]any{"binary_path": "test_binary", "warnings": "build_warnings"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	wantOutputs := map[string]any{
		"build_output": "binary=dist/action.test\r\nsize=128\r\n",
		"binary_path":  "dist/action.test",
		"binary_size":  int64(128),
		"warnings":     []any{"unused import", "slow query"},
	}
	wantVariables := map[string]any{"test_binary": "dist/action.test", "build_warnings": []any{"unused import", "slow query"}}
	if !reflect.DeepEqual(result.Outputs, wantOutputs) || !reflect.DeepEqual(result.Variables, wantVariables) {
		t.Fatalf("result = %#v, want outputs %#v variables %#v", result, wantOutputs, wantVariables)
	}
}

func TestFieldsMatchAllFlattensRepeatedMarkerMatches(t *testing.T) {
	text := `WUKO_OUTPUT_V1 {"event":"begin","key":"query"}
SQL: SELECT 1;
SQL: SELECT 2;
WUKO_OUTPUT_V1 {"event":"end","key":"query"}
WUKO_OUTPUT_V1 {"event":"begin","key":"query"}
SQL: SELECT 3;
WUKO_OUTPUT_V1 {"event":"end","key":"query"}
`
	runner, err := New(map[string]any{
		"text": text,
		"fields": map[string]any{
			"blocks":  map[string]any{"marker": "query", "match": "all"},
			"queries": map[string]any{"marker": "query", "regex": `SQL: (?P<value>[^\r\n]+)`, "match": "all"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	wantBlocks := []any{"SQL: SELECT 1;\nSQL: SELECT 2;\n", "SQL: SELECT 3;\n"}
	wantQueries := []any{"SELECT 1;", "SELECT 2;", "SELECT 3;"}
	if !reflect.DeepEqual(result.Outputs["blocks"], wantBlocks) || !reflect.DeepEqual(result.Outputs["queries"], wantQueries) {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestFieldsMatchAllAllowsNoMatches(t *testing.T) {
	runner, err := New(map[string]any{
		"text": "clean", "fields": map[string]any{
			"warnings": map[string]any{"regex": `warning: (?P<value>.+)`, "match": "all"},
			"blocks":   map[string]any{"marker": "missing", "match": "all"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Outputs["warnings"], []any{}) || !reflect.DeepEqual(result.Outputs["blocks"], []any{}) {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestFieldsFailAtomicallyWhenOneConversionFails(t *testing.T) {
	runner, err := New(map[string]any{
		"text": "name=wuko count=many", "fields": map[string]any{
			"name":  map[string]any{"regex": `name=(?P<value>\S+)`},
			"count": map[string]any{"regex": `count=(?P<value>\S+)`, "type": "integer"},
		},
		"variables": map[string]any{"name": "name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err == nil || !strings.Contains(err.Error(), `field "count"`) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(result.Outputs) != 0 || len(result.Variables) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestMarkerProtocolRejectsInvalidStreams(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"malformed", "WUKO_OUTPUT_V1 nope\n", "decoding WUKO_OUTPUT_V1 marker"},
		{"unknown field", `WUKO_OUTPUT_V1 {"event":"begin","key":"x","extra":true}` + "\n", "unknown field"},
		{"unknown event", `WUKO_OUTPUT_V1 {"event":"write","key":"x"}` + "\n", "must be begin or end"},
		{"empty key", `WUKO_OUTPUT_V1 {"event":"begin","key":""}` + "\n", "key must not be empty"},
		{"nested", `WUKO_OUTPUT_V1 {"event":"begin","key":"x"}` + "\n" + `WUKO_OUTPUT_V1 {"event":"begin","key":"y"}` + "\n", "inside marker"},
		{"mismatched", `WUKO_OUTPUT_V1 {"event":"begin","key":"x"}` + "\n" + `WUKO_OUTPUT_V1 {"event":"end","key":"y"}` + "\n", "ends marker"},
		{"end without begin", `WUKO_OUTPUT_V1 {"event":"end","key":"x"}` + "\n", "without a matching begin"},
		{"unclosed", `WUKO_OUTPUT_V1 {"event":"begin","key":"x"}` + "\npayload\n", "is not closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := New(map[string]any{
				"text": tt.text, "fields": map[string]any{"value": map[string]any{"marker": "x", "match": "all"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("result = %#v, error = %v, want %q", result, err, tt.want)
			}
			if len(result.Outputs) != 0 || len(result.Variables) != 0 {
				t.Fatalf("result = %#v", result)
			}
		})
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
		{"bad match", map[string]any{"text": "x", "pattern": `(?P<x>.)`, "match": "first"}, "match must be one or all"},
		{"match all shadows matches", map[string]any{"text": "x", "pattern": `(?P<matches>.)`, "match": "all"}, "collides with the match: all output"},
		{"match all shadows count", map[string]any{"text": "x", "pattern": `(?P<count>.)`, "match": "all"}, "collides with the match: all output"},
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
		{"empty fields", map[string]any{"text": "x", "fields": map[string]any{}}, "at least one field"},
		{"fields with pattern", map[string]any{"text": "x", "pattern": `(?P<x>x)`, "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`}}}, "cannot be combined with pattern"},
		{"fields with format", map[string]any{"text": "x", "format": "{x}", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`}}}, "cannot be combined with format"},
		{"fields with types", map[string]any{"text": "x", "types": map[string]any{"x": "string"}, "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`}}}, "cannot be combined with types"},
		{"fields with match", map[string]any{"text": "x", "match": "all", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`}}}, "cannot be combined with match"},
		{"invalid field name", map[string]any{"text": "x", "fields": map[string]any{"bad-name": map[string]any{"regex": `(?P<value>x)`}}}, "invalid field name"},
		{"missing field matcher", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{}}}, "requires marker or regex"},
		{"empty field marker", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"marker": ""}}}, "marker must not be empty"},
		{"empty field regex", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": ""}}}, "regex must not be empty"},
		{"type without regex", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"marker": "x", "type": "json"}}}, "type requires regex"},
		{"bad field type", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`, "type": "date"}}}, "field \"x\" type"},
		{"bad field match", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`, "match": "first"}}}, "match must be one or all"},
		{"bad field regex", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": "["}}}, "compiling regex"},
		{"field regex no value", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(x)`}}}, "exactly one named value"},
		{"field regex wrong name", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(?P<other>x)`}}}, "must be value"},
		{"field regex duplicate value", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)|(?P<value>y)`}}}, "exactly one named value"},
		{"unknown variable field", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`}}, "variables": map[string]any{"other": "target"}}, "unknown field"},
		{"unknown field option", map[string]any{"text": "x", "fields": map[string]any{"x": map[string]any{"regex": `(?P<value>x)`, "trim": true}}}, "field trim not found"},
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
		{"all optional group", map[string]any{"text": "name name=value", "pattern": `name(?:=(?P<value>[^ ]+))?`, "match": "all"}, step.Request{}, "did not participate"},
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
	if _, err := New(map[string]any{
		"text": "value", "fields": map[string]any{
			"value": map[string]any{
				"marker": "{{ .vars.marker }}", "regex": "{{ .vars.regex }}",
				"type": "{{ .vars.type }}", "match": "{{ .vars.match }}",
			},
		},
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
