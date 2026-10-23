package yamlstrict

import (
	"errors"
	"io"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type config struct {
	Name  string `yaml:"name"`
	Count int    `yaml:"count"`
}

func TestDecodeReadsOneStrictDocument(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    config
		wantErr string
		is      error
	}{
		{name: "single document", input: "name: a\ncount: 2\n", want: config{Name: "a", Count: 2}},
		{name: "explicit document markers", input: "---\nname: a\n...\n", want: config{Name: "a"}},
		{name: "unknown field", input: "name: a\ncolor: red\n", wantErr: "field color not found"},
		{name: "second document", input: "name: a\n---\nname: b\n", is: ErrMultipleDocuments},
		{name: "empty trailing document", input: "name: a\n---\n", is: ErrMultipleDocuments},
		{name: "broken second document", input: "name: a\n---\nname: [\n", wantErr: "did not find expected node content"},
		{name: "empty input", input: "", is: io.EOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got config
			err := Decode([]byte(test.input), &got)
			switch {
			case test.is != nil:
				if !errors.Is(err, test.is) {
					t.Fatalf("error = %v, want %v", err, test.is)
				}
			case test.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, test.wantErr)
				}
				if errors.Is(err, ErrMultipleDocuments) {
					t.Fatalf("a syntax error was reported as a second document: %v", err)
				}
			case err != nil:
				t.Fatal(err)
			case got != test.want:
				t.Fatalf("decoded %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestConvertRejectsUnknownFields(t *testing.T) {
	var got config
	if err := Convert(map[string]any{"name": "a", "count": 3}, &got); err != nil {
		t.Fatal(err)
	}
	if got != (config{Name: "a", Count: 3}) {
		t.Fatalf("converted %+v", got)
	}

	err := Convert(map[string]any{"name": "a", "colour": "red"}, &config{})
	if err == nil || !strings.Contains(err.Error(), "field colour not found") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestConvertDecodesANode(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("name: from-node\n"), &node); err != nil {
		t.Fatal(err)
	}
	var got config
	if err := Convert(&node, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "from-node" {
		t.Fatalf("converted %+v", got)
	}
}
