package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReturnControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: cached
steps:
  - return:
      outputs:
        artifact: '"dist/app.tar.gz"'
        cached: "true"
    if: vars.cached
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0].Return
	if control == nil || control.Outputs["cached"] != "true" || definition.Steps[0].If != "vars.cached" {
		t.Fatalf("return = %#v, step = %#v", control, definition.Steps[0])
	}
}

func TestReturnSchemaValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing outputs", body: "  - return: {}\n", want: "outputs are required"},
		{name: "non-object outputs", body: "  - return: {outputs: []}\n", want: "outputs must be an object"},
		{name: "non-string expression", body: "  - return: {outputs: {ok: true}}\n", want: "expression string"},
		{name: "empty expression", body: "  - return: {outputs: {ok: ''}}\n", want: "non-empty expression"},
		{name: "invalid name", body: "  - return: {outputs: {'bad-name': 'true'}}\n", want: "invalid return output name"},
		{name: "unknown field", body: "  - return: {outputs: {}, status: success}\n", want: "field status"},
		{name: "duplicate outputs field", body: "  - return:\n      outputs: {}\n      outputs: {ok: 'true'}\n", want: `duplicate return field "outputs"`},
		{name: "mixed id", body: "  - id: done\n    return: {outputs: {}}\n", want: "cannot be combined"},
		{name: "inside concurrent", body: "  - concurrent:\n      steps:\n        - return: {outputs: {}}\n        - {id: work, type: shell}\n", want: "inside concurrent"},
		{name: "inside foreach", body: "  - id: loop\n    foreach:\n      items: vars.items\n      steps:\n        - return: {outputs: {}}\n", want: "inside foreach or matrix"},
		{name: "inside matrix", body: "  - id: loop\n    matrix:\n      axes: {os: [linux]}\n      steps:\n        - return: {outputs: {}}\n", want: "inside foreach or matrix"},
		{name: "inside finally", body: "  - {id: work, type: shell}\nfinally:\n  - return: {outputs: {}}\n", want: "inside finally"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			writeTestFile(t, path, "version: 1\nname: invalid\nvars: {items: []}\nsteps:\n"+test.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReturnCanComeFromRequiredSequentialFragment(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "finish.yaml"), "- return: {outputs: {result: '\"done\"'}}\n")
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: required-return
steps:
  - if: vars.finish
    steps:
      - require: finish.yaml
  - {id: work, type: shell}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Steps[0].Steps[0].Return == nil {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestActionReturnOutputsMustMatchDeclaration(t *testing.T) {
	action := &Action{
		Outputs: map[string]ActionOutput{"result": {Value: `"fallback"`}},
		Steps:   []Step{{Return: &ReturnControl{Outputs: map[string]string{"other": `"value"`}}}},
	}
	err := action.ValidateReturnContracts()
	if err == nil || !strings.Contains(err.Error(), "missing \"result\"") {
		t.Fatalf("error = %v", err)
	}
}
