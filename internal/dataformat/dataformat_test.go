package dataformat

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDecodeYAMLStrictnessAndLocations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, text string
		strict     bool
		want       any
		wantErr    string
	}{
		{name: "strict empty", text: "", strict: true, want: nil},
		{name: "lenient empty", text: "", wantErr: "line 1, column 1: EOF"},
		{name: "strict custom tag", text: "a: !custom b\n", strict: true, wantErr: `line 1: unsupported YAML tag "!custom"`},
		{name: "lenient custom tag", text: "a: !custom b\n", want: map[string]any{"a": "b"}},
		{name: "strict invalid UTF-8", text: "a: \xff\n", strict: true, wantErr: "YAML must be UTF-8"},
		{name: "syntax error", text: "a: 1\nb: [\n", strict: true, wantErr: "line 2, column 5: yaml: line 2"},
		{name: "second document", text: "a: 1\n---\nb: 2\n", strict: true, wantErr: "line 3, column 1: multiple YAML documents"},
		{name: "duplicate key", text: "a: 1\na: 2\n", strict: true, wantErr: "already defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := DecodeYAML(test.text, test.strict)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("value mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDecodeTOMLReportsLocations(t *testing.T) {
	t.Parallel()
	got, err := DecodeTOML("name = 'wuko'\n[server]\nport = 8080\n")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]any{"name": "wuko", "server": map[string]any{"port": int64(8080)}}, got); diff != "" {
		t.Fatalf("value mismatch (-want +got):\n%s", diff)
	}
	if _, err := DecodeTOML("a = 1\na = 2\n"); err == nil || !strings.Contains(err.Error(), "line 2, column 1") {
		t.Fatalf("duplicate key error = %v", err)
	}
}
