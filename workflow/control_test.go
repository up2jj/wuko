package workflow

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadForeachAndMatrixControls(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: fanout
steps:
  - id: deploy
    foreach:
      items: vars.targets
      collect: '{"target": foreach.item, "output": steps.run.stdout}'
      max_iterations: 20
      steps:
        - {id: run, type: shell}
  - id: checks
    matrix:
      axes:
        os: [linux, darwin]
        version: vars.versions
      collect: steps.run.path
      max_concurrency: 2
      timeout: 5m
      fail_fast: false
      steps:
        - {id: run, type: shell}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	foreach := definition.Steps[0].Foreach
	if foreach == nil || foreach.MaxConcurrency != 1 || foreach.MaxIterations != 20 || !foreach.FailFast || foreach.Items != "vars.targets" || foreach.Collect != `{"target": foreach.item, "output": steps.run.stdout}` {
		t.Fatalf("foreach = %#v", foreach)
	}
	matrix := definition.Steps[1].Matrix
	if matrix == nil || matrix.MaxConcurrency != 2 || matrix.MaxIterations != 10_000 || matrix.FailFast || matrix.Timeout.Value() != 5*time.Minute || matrix.Collect != "steps.run.path" {
		t.Fatalf("matrix = %#v", matrix)
	}
	if len(matrix.Axes) != 2 || matrix.Axes[0].Name != "os" || matrix.Axes[1].Expression != "vars.versions" {
		t.Fatalf("axes = %#v", matrix.Axes)
	}
}

func TestLoadBatchControls(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: batch
steps:
  - id: static
    batch:
      items: vars.targets
      size: 2
      collect: steps.run.stdout
      max_iterations: 20
      steps: [{id: run, type: shell}]
  - id: dynamic
    batch:
      items: vars.targets
      size: vars.batch_size
      max_concurrency: 2
      timeout: 5m
      fail_fast: false
      steps: [{id: run, type: shell}]
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	static := definition.Steps[0].Batch
	if static == nil || static.Size.Literal != 2 || static.Size.Expression != "" || static.MaxConcurrency != 1 || static.MaxIterations != 20 || !static.FailFast || static.Collect != "steps.run.stdout" {
		t.Fatalf("static batch = %#v", static)
	}
	dynamic := definition.Steps[1].Batch
	if dynamic == nil || dynamic.Size.Expression != "vars.batch_size" || dynamic.Size.Literal != 0 || dynamic.MaxConcurrency != 2 || dynamic.MaxIterations != 10_000 || dynamic.FailFast || dynamic.Timeout.Value() != 5*time.Minute {
		t.Fatalf("dynamic batch = %#v", dynamic)
	}
	if dynamic.Steps[0].Location.Source != path || dynamic.Steps[0].Location.Line == 0 {
		t.Fatalf("batch child location = %#v", dynamic.Steps[0].Location)
	}
}

func TestControlValidationAndScopedIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing items", body: "  - id: loop\n    foreach:\n      steps: [{id: run, type: shell}]\n", want: "items"},
		{name: "batch missing items", body: "  - id: loop\n    batch:\n      size: 2\n      steps: [{id: run, type: shell}]\n", want: "items"},
		{name: "batch missing size", body: "  - id: loop\n    batch:\n      items: vars.items\n      steps: [{id: run, type: shell}]\n", want: "size is required"},
		{name: "batch zero size", body: "  - id: loop\n    batch:\n      items: vars.items\n      size: 0\n      steps: [{id: run, type: shell}]\n", want: "positive integer"},
		{name: "batch non-scalar size", body: "  - id: loop\n    batch:\n      items: vars.items\n      size: [2]\n      steps: [{id: run, type: shell}]\n", want: "positive integer or expression"},
		{name: "batch empty size expression", body: "  - id: loop\n    batch:\n      items: vars.items\n      size: '  '\n      steps: [{id: run, type: shell}]\n", want: "must not be empty"},
		{name: "no axes", body: "  - id: loop\n    matrix:\n      axes: {}\n      steps: [{id: run, type: shell}]\n", want: "at least one axis"},
		{name: "zero max iterations", body: "  - id: loop\n    foreach:\n      items: vars.items\n      max_iterations: 0\n      steps: [{id: run, type: shell}]\n", want: "max_iterations"},
		{name: "excessive max iterations", body: "  - id: loop\n    matrix:\n      axes: {os: [linux]}\n      max_iterations: 1000001\n      steps: [{id: run, type: shell}]\n", want: "max_iterations"},
		{name: "filter unsupported", body: "  - id: loop\n    matrix:\n      axes: {os: [linux]}\n      exclude: []\n      steps: [{id: run, type: shell}]\n", want: "field exclude"},
		{name: "foreach collect is not a string", body: "  - id: loop\n    foreach:\n      items: vars.items\n      collect: [steps.run.value]\n      steps: [{id: run, type: shell}]\n", want: "collect must be an expression string"},
		{name: "matrix collect is empty", body: "  - id: loop\n    matrix:\n      axes: {os: [linux]}\n      collect: '  '\n      steps: [{id: run, type: shell}]\n", want: "collect must be a non-empty expression"},
		{name: "nested fanout", body: "  - id: outer\n    foreach:\n      items: vars.items\n      steps:\n        - id: inner\n          matrix:\n            axes: {os: [linux]}\n            steps: [{id: run, type: shell}]\n", want: "nested matrix"},
		{name: "nested batch", body: "  - id: outer\n    batch:\n      items: vars.items\n      size: 2\n      steps:\n        - id: inner\n          foreach:\n            items: vars.items\n            steps: [{id: run, type: shell}]\n", want: "nested foreach"},
		{name: "inside concurrent", body: "  - concurrent:\n      steps:\n        - id: loop\n          foreach:\n            items: vars.items\n            steps: [{id: run, type: shell}]\n        - {id: other, type: shell}\n", want: "nested foreach"},
		{name: "outer collision", body: "  - {id: prepare, type: shell}\n  - id: loop\n    foreach:\n      items: vars.items\n      steps: [{id: prepare, type: shell}]\n", want: `duplicate step id "prepare"`},
		{name: "unknown foreach child field", body: "  - id: loop\n    foreach:\n      items: vars.items\n      steps: [{id: run, type: shell, timout: 1s}]\n", want: "field timout"},
		{name: "unknown matrix child field", body: "  - id: loop\n    matrix:\n      axes: {os: [linux]}\n      steps: [{id: run, type: shell, timout: 1s}]\n", want: "field timout"},
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

func TestControlsExpandRequiredStepsAndAllowLocalIDReuse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "child.yaml"), "- {id: run, type: shell}\n")
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: reuse
vars: {items: []}
steps:
  - id: first
    foreach:
      items: vars.items
      steps: [{require: child.yaml}]
  - id: second
    batch:
      items: vars.items
      size: 2
      steps: [{require: child.yaml}]
  - id: third
    matrix:
      axes: {os: [linux]}
      steps: [{id: run, type: shell}]
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Steps[0].Foreach.Steps[0].ID != "run" || definition.Steps[1].Batch.Steps[0].ID != "run" || definition.Steps[2].Matrix.Steps[0].ID != "run" {
		t.Fatalf("definition = %#v", definition)
	}
}
