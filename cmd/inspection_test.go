package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/validation"
)

func TestInspectionCommandsLoadFilesAndStdinWithoutExecuting(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"validate", "tree"} {
		for _, filename := range []string{"draft.yaml", "-", "./-"} {
			t.Run(name+"/"+filename, func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"fragments/actions/record", ".wuko/workflows"} {
					testfs.MkdirAll(t, filepath.Join(root, dir), 0o755)
				}
				// An unrelated malformed workflow must not prevent explicit inspection.
				testfs.Write(t, filepath.Join(root, ".wuko/workflows/broken.yaml"), "version: [")
				testfs.Write(t, filepath.Join(root, "fragments/steps.yaml"), "- id: selected\n  uses: ./actions/record\n")
				marker := filepath.Join(root, "executed")
				action := "version: 1\nname: record\nsteps:\n  - id: touch\n    type: shell\n    with:\n      command: touch\n      args: [" + marker + "]\n"
				testfs.Write(t, filepath.Join(root, "fragments/actions/record/action.yaml"), action)
				data := `version: 1
name: inspected
invokable: false
targets:
  staging:
    steps:
      - require: fragments/steps.yaml
  production:
    steps:
      - id: unselected
        type: shell
        with: {script: "{{ .vars.missing }}"}
`
				var output bytes.Buffer
				deps := inspectionTestDependencies(t, root, &output)
				if filename == "-" {
					deps.stdin = &failAfterEOFReader{reader: strings.NewReader(data)}
					deps.isInteractive = func(io.Reader) bool { return true }
				} else {
					testfs.Write(t, filepath.Join(root, filename), data)
					deps.stdin = strings.NewReader("not workflow YAML")
				}
				command := newRootCmd(deps)
				command.SetArgs([]string{name, "--file", filename, "staging"})
				if err := command.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				if name == "validate" && output.String() != "inspected (staging): valid\n" {
					t.Fatalf("validation output = %q", output.String())
				}
				if name == "tree" && (!strings.Contains(output.String(), "selected") || strings.Contains(output.String(), "unselected")) {
					t.Fatalf("tree output = %q", output.String())
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("inspection executed workflow: marker stat = %v", err)
				}
			})
		}
	}
}

func TestValidateFileJSONReportsLogicalSources(t *testing.T) {
	t.Parallel()
	for _, filename := range []string{"draft.yaml", "-"} {
		for _, valid := range []bool{true, false} {
			label := "invalid"
			if valid {
				label = "valid"
			}
			t.Run(filename+"/"+label, func(t *testing.T) {
				root := t.TempDir()
				data := "version: 1\nname: inspected\nsteps:\n  - id: example\n    type: shell\n    with: {command: echo}\n"
				if !valid {
					data = strings.ReplaceAll(data, "command: echo", "commnad: echo")
				}
				var output bytes.Buffer
				deps := inspectionTestDependencies(t, root, &output)
				wantSource := filepath.Join(root, filename)
				if filename == "-" {
					deps.stdin = strings.NewReader(data)
					wantSource = "stdin"
				} else {
					testfs.Write(t, wantSource, data)
				}
				command := newRootCmd(deps)
				command.SetArgs([]string{"validate", "--file", filename, "--format", "json"})
				err := command.ExecuteContext(t.Context())
				if (err == nil) != valid {
					t.Fatalf("ExecuteContext() error = %v, valid = %v", err, valid)
				}
				var document validation.Document
				if err := json.Unmarshal(output.Bytes(), &document); err != nil {
					t.Fatalf("decoding %q: %v", output.String(), err)
				}
				if document.Valid != valid || (len(document.Issues) == 0) != valid {
					t.Fatalf("document = %#v", document)
				}
				for _, issue := range document.Issues {
					if issue.Span.Source != wantSource || issue.Span.Line == 0 {
						t.Fatalf("issue source = %#v, want %q and line", issue.Span, wantSource)
					}
				}
			})
		}
	}
}

func TestInspectionStdinValidatesNamedDependencies(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"validate", "tree"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".wuko", "workflows", "prepare.yaml")
			testfs.MkdirAll(t, filepath.Dir(path), 0o755)
			testfs.Write(t, path, "version: 1\nname: prepare\ninvokable: false\nsteps:\n  - id: invalid\n    type: shell\n    with: {commnad: echo}\n")
			var output bytes.Buffer
			deps := inspectionTestDependencies(t, root, &output)
			deps.stdin = strings.NewReader("version: 1\nname: streamed\ndepends_on: {build: prepare}\nsteps: [{return: {outputs: {}}}]\n")
			command := newRootCmd(deps)
			command.SetArgs([]string{name, "--file", "-"})
			err := command.ExecuteContext(t.Context())
			if err == nil {
				t.Fatal("expected prerequisite validation failure")
			}
			issues := validation.Issues(err)
			if len(issues) == 0 || issues[0].Span.Source != path {
				t.Fatalf("prerequisite issues = %#v, error = %v", issues, err)
			}
		})
	}
}

func TestInspectionCommandsReportInputAndTargetErrors(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"validate", "tree"} {
		for _, test := range []struct {
			name, input, want string
			args              []string
		}{
			{name: "selector conflict", input: "unread", args: []string{"--file", "-", "deploy", "staging"}, want: "with --file, pass at most one positional TARGET; usage: wuko " + name + " --file PATH [TARGET]"},
			{name: "malformed stdin", input: "version: [", args: []string{"--file", "-"}, want: "yaml: line"},
			{name: "missing file", args: []string{"--file", "missing.yaml"}, want: "missing.yaml"},
			{name: "stdin read error", args: []string{"--file", "-"}, want: "reading workflow from stdin"},
			{name: "missing target", input: "version: 1\nname: deploy\ntargets: {staging: {steps: [{return: {outputs: {}}}]}}", args: []string{"--file", "-"}, want: "requires a target"},
			{name: "unknown target", input: "version: 1\nname: deploy\ntargets: {staging: {steps: [{return: {outputs: {}}}]}}", args: []string{"--file", "-", "missing"}, want: "has no target"},
			{name: "stdin elevation", input: "version: 1\nname: elevated\nsteps: [{id: root, type: shell, with: {command: id, elevated: true}}]", args: []string{"--file", "-"}, want: "trusted local"},
		} {
			t.Run(name+"/"+test.name, func(t *testing.T) {
				var output bytes.Buffer
				deps := inspectionTestDependencies(t, t.TempDir(), &output)
				input := strings.NewReader(test.input)
				deps.stdin = input
				if test.name == "stdin read error" {
					deps.stdin = &failAfterEOFReader{exhausted: true}
				}
				command := newRootCmd(deps)
				command.SetArgs(append([]string{name}, test.args...))
				err := command.ExecuteContext(t.Context())
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("ExecuteContext() error = %v, want %q", err, test.want)
				}
				if test.name == "selector conflict" && input.Len() != len(test.input) {
					t.Fatal("invalid arguments consumed stdin")
				}
				if test.name == "malformed stdin" {
					issues := validation.Issues(err)
					if len(issues) == 0 || issues[0].Span.Source != "stdin" {
						t.Fatalf("malformed stdin issues = %#v", issues)
					}
				}
			})
		}
	}
}

func inspectionTestDependencies(t *testing.T, root string, output io.Writer) dependencies {
	t.Helper()
	deps := listTestDependencies(root, output)
	deps.registry = step.NewRegistry()
	if err := shell.Register(deps.registry); err != nil {
		t.Fatal(err)
	}
	return deps
}
