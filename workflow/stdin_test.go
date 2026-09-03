package workflow

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoaderDecodeStdinUsesBaseDirectoryAndLogicalLocations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "templates", "message.tmpl"), "hello {{ .vars.name }}")
	writeTestFile(t, filepath.Join(root, "fragments", "build.yaml"), `steps:
  - id: build
    uses: ./actions/build
`)
	writeTestFile(t, filepath.Join(root, "fragments", "actions", "build", "action.yaml"), `version: 1
name: build
steps:
  - id: run
    type: shell
`)
	data := `version: 1
name: streamed
templates:
  message:
    file: templates/message.tmpl
vars: {name: wuko}
steps:
  - require: fragments/build.yaml
`

	loader := NewLoader(nil)
	definition, err := loader.DecodeStdin(strings.NewReader(data), root, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if definition.Path != filepath.Join(root, "-") || definition.Dir != root {
		t.Fatalf("path = %q, dir = %q", definition.Path, definition.Dir)
	}
	if definition.Location.Source != "stdin" {
		t.Fatalf("definition source = %q", definition.Location.Source)
	}
	if got := definition.Templates["message"].Body; got != "hello {{ .vars.name }}" {
		t.Fatalf("template body = %q", got)
	}
	if len(definition.Steps) != 1 || definition.Steps[0].Location.Source != "stdin::fragments/build.yaml" {
		t.Fatalf("steps = %#v", definition.Steps)
	}
	if err := loader.Prepare(t.Context(), definition, LoadOptions{RunDir: root}); err != nil {
		t.Fatal(err)
	}
	if definition.Steps[0].Action == nil || definition.Steps[0].Action.Location.Source != "stdin::fragments/actions/build/action.yaml" {
		t.Fatalf("action = %#v", definition.Steps[0].Action)
	}
}

func TestLoaderDecodeStdinReportsLogicalSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "invalid", data: "version: [", want: "decoding workflow stdin"},
		{name: "multiple documents", data: "version: 1\nname: first\nsteps: [{id: run, type: shell}]\n---\n{}\n", want: "multiple YAML documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := NewLoader(nil).DecodeStdin(strings.NewReader(test.data), root, LoadOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), root) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoaderDecodeStdinNamesLogicalSourceInRequireErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := "version: 1\nname: streamed\nsteps:\n  - require: /absolute.yaml\n"
	_, err := NewLoader(nil).DecodeStdin(strings.NewReader(data), root, LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "step 1 in stdin:") || strings.Contains(err.Error(), filepath.Join(root, "-")) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderDecodeStdinReportsReadFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("broken pipe")
	_, err := NewLoader(nil).DecodeStdin(errorReader{err: want}, t.TempDir(), LoadOptions{})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "reading workflow from stdin") {
		t.Fatalf("error = %v", err)
	}
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

var _ io.Reader = errorReader{}
