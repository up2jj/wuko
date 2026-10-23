package cmd

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/workflow"
)

func TestTUIDateCLIAndFormUseSuppliedValue(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "date.yaml")
	source := `version: 1
name: cli-date
vars:
  layout: "02.01.2006"
  earliest: "01.10.2026"
steps:
  - id: date
    type: tui_date
    with:
      variable: release_date
      message: Release date
      format: "{{ .vars.layout }}"
      min: "{{ .vars.earliest }}"
      max: "31.12.2026"
  - id: settings
    type: tui_form
    with:
      title: Schedule
      fields:
        - id: due
          type: date
          label: Due date
          format: "{{ .vars.layout }}"
          min: "{{ .vars.earliest }}"
          value: {expr: vars.release_date}
  - id: verify
    type: assert
    with:
      expr: steps.date.value == "23.10.2026" && vars.release_date == steps.settings.due
      message: Date step and form publish the same formatted value
  - return:
      outputs:
        due: steps.settings.due
`
	testfs.WriteFile(t, file, []byte(source), 0o600)
	var output bytes.Buffer
	command := newRootCmd(dependencies{stdin: strings.NewReader(""), stdout: &output, stderr: io.Discard, cwd: func() (string, error) { return root, nil }, homeDir: func() (string, error) { return root, nil }, configDir: func() (string, error) { return root, nil }, registry: formRegistry(t), getenv: func(string) string { return "" }})
	command.SetArgs([]string{"run", "--file", file, "--var", `release_date="23.10.2026"`})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestTUIDateReferenceValidationAndConcurrentInputs(t *testing.T) {
	t.Parallel()
	date := workflow.Step{ID: "date", Type: "tui_date", With: map[string]any{"variable": "due", "message": "Due date"}}
	for _, test := range []struct{ reference, want string }{
		{"steps.date.value", ""}, {"vars.due", ""}, {"steps.date.typo", `has no output "typo"`},
	} {
		definition := formDefinition(t, date, workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{"date": test.reference}}})
		err := engine.New(formRegistry(t)).Validate(t.Context(), definition, engine.Options{})
		if test.want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("error = %v, want %s", err, test.want)
		}
	}
	definition := formDefinition(t, workflow.Step{Concurrent: &workflow.ConcurrentGroup{Steps: []workflow.Step{date, {ID: "other", Type: "assert", With: map[string]any{"expr": "true", "message": "Other step succeeds"}}}, MaxConcurrency: 2}})
	for _, supplied := range []bool{false, true} {
		vars := map[string]any{}
		if supplied {
			vars["due"] = "+2d"
		}
		state, err := engine.New(formRegistry(t)).Run(t.Context(), definition, engine.Options{Vars: vars, Interactive: true, Stdin: strings.NewReader(""), Stdout: io.Discard})
		if supplied {
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := state.Vars["due"].(string); !ok || len(value) != 10 || strings.HasPrefix(value, "+") {
				t.Fatalf("date was not resolved: %v", state.Vars)
			}
		} else if err == nil || !strings.Contains(err.Error(), "supply it with --var") {
			t.Fatalf("error = %v", err)
		}
	}
}
