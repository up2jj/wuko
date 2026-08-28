package edit

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/up2jj/wuko/step"
)

// editFile runs one edit against a temporary file and returns its new contents.
func editFile(t *testing.T, name, body string, config map[string]any) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config["from"] = map[string]any{"file": name}
	if _, err := newRunner(t, config).Run(t.Context(), step.Request{RunDir: directory}); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTOMLStringsUseTOMLEscapes(t *testing.T) {
	t.Parallel()
	// Go's quoting spells these \v and \x01, neither of which TOML defines, so the
	// file it produced could no longer be parsed at all.
	for _, value := range []string{"a\vb", "a\x01b", "tab\there", "quote\"and\\slash", "del\x7fmark"} {
		rendered, err := renderTOML(value)
		if err != nil {
			t.Fatalf("rendering %q: %v", value, err)
		}
		var document map[string]any
		if err := toml.Unmarshal([]byte("k = "+string(rendered)+"\n"), &document); err != nil {
			t.Fatalf("rendered %q as %s, which TOML rejects: %v", value, rendered, err)
		}
		if document["k"] != value {
			t.Errorf("round trip of %q became %q", value, document["k"])
		}
	}
}

func TestTOMLRejectsInvalidUTF8String(t *testing.T) {
	t.Parallel()
	if _, err := renderTOML("bad\xff"); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error = %v, want a UTF-8 complaint", err)
	}
}

func TestTOMLWholeFloatStaysAFloat(t *testing.T) {
	t.Parallel()
	// "5" would be read back as an integer, quietly changing the value's type.
	got := editFile(t, "config.toml", "k = 2.5\n",
		map[string]any{"operation": "set", "path": "$.k", "expr": "current * 2"})
	if strings.TrimSpace(got) != "k = 5.0" {
		t.Fatalf("document = %q, want k = 5.0", got)
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(got), &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["k"].(float64); !ok {
		t.Fatalf("k reparsed as %T, want float64", document["k"])
	}
}

func TestTOMLNonFiniteFloatsUseTOMLSpelling(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		value float64
		want  string
	}{{math.Inf(1), "inf"}, {math.Inf(-1), "-inf"}, {math.NaN(), "nan"}} {
		if got := tomlFloat(testCase.value); got != testCase.want {
			t.Errorf("tomlFloat(%v) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestTOMLSubTableUnderArrayKeepsItsIndex(t *testing.T) {
	t.Parallel()
	// TOML resolves [items.sub] against the last [[items]], so the editable path is
	// $.items[0].sub.b. Losing the index made that path match nothing.
	got := editFile(t, "config.toml", "[[items]]\na = 1\n[items.sub]\nb = 2\n",
		map[string]any{"operation": "set", "path": "$.items[0].sub.b", "value": 99})
	if !strings.Contains(got, "b = 99") {
		t.Fatalf("document = %q, want b = 99", got)
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(got), &document); err != nil {
		t.Fatal(err)
	}
	items := document["items"].([]any)
	if items[0].(map[string]any)["sub"].(map[string]any)["b"] != int64(99) {
		t.Fatalf("document = %#v", document)
	}
}

func TestTOMLTableHeaderScanSkipsQuotedBrackets(t *testing.T) {
	t.Parallel()
	// Scanning for the first "]" truncated this header, and every edit to the file
	// failed rather than the one key being unusable.
	got := editFile(t, "config.toml", "[\"we]ird\"]\na = 1\n",
		map[string]any{"operation": "set", "path": "$['we]ird'].a", "value": 42})
	if !strings.Contains(got, "a = 42") {
		t.Fatalf("document = %q, want a = 42", got)
	}
}

func TestIdentityEditKeepsAnIntegerTooLargeForFloat64(t *testing.T) {
	t.Parallel()
	// Demoting this to a float changed its digits, and the changed value was then
	// written back, so an expr of just "current" corrupted the document.
	body := "{\"n\": 12345678901234567890}\n"
	got := editFile(t, "config.json", body,
		map[string]any{"operation": "set", "path": "$.n", "expr": "current"})
	if got != body {
		t.Fatalf("document = %q, want it unchanged at %q", got, body)
	}
}

func TestDecodeReportsTheRealSyntaxError(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name, format, document, want string
	}{
		{"json second value", "json", `{"a":1} {"b":2}`, "multiple JSON values are not supported"},
		{"json syntax error", "json", `{"a":1} @@@`, "invalid character '@'"},
		{"yaml second document", "yaml", "a: 1\n---\nb: 2\n", "multiple YAML documents are not supported"},
	} {
		_, err := decodeDocument([]byte(testCase.document), testCase.format)
		if err == nil || !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s: error = %v, want it to mention %q", testCase.name, err, testCase.want)
		}
	}
}
