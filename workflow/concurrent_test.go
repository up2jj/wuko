package workflow

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadConcurrentGroupDefaultsAndPolicies(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: checks
steps:
  - concurrent:
      timeout: 5m
      fail_fast: false
      steps:
        - id: lint
          type: shell
        - id: test
          type: shell
          retry: {max_attempts: 2}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	group := definition.Steps[0].Concurrent
	if group == nil || group.MaxConcurrency != 4 || group.FailFast {
		t.Fatalf("concurrent group = %#v", group)
	}
	if group.Timeout == nil || group.Timeout.Value() != 5*time.Minute {
		t.Fatalf("timeout = %v", group.Timeout)
	}
	if group.Steps[1].Retry == nil || group.Steps[1].Retry.MaxAttempts != 2 {
		t.Fatalf("child retry = %#v", group.Steps[1].Retry)
	}
}

func TestLoadConcurrentGroupDefaultsFailFast(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: checks
steps:
  - concurrent:
      steps:
        - {id: lint, type: shell}
        - {id: test, type: shell}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.Steps[0].Concurrent.FailFast {
		t.Fatal("fail_fast did not default to true")
	}
}

func TestLoadConcurrentGroupNeeds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: checks
steps:
  - concurrent:
      steps:
        - {id: build, type: shell, needs: [lint, test]}
        - {id: lint, type: shell, needs: [deps]}
        - {id: deps, type: shell}
        - {id: test, type: shell, needs: [deps]}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	steps := definition.Steps[0].Concurrent.Steps
	if got := strings.Join(steps[0].Needs, ","); got != "lint,test" {
		t.Fatalf("build needs = %q", got)
	}
	if got := strings.Join(steps[1].Needs, ","); got != "deps" {
		t.Fatalf("lint needs = %q", got)
	}
}

func TestLoadRejectsInvalidConcurrentGroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "one child", body: "      steps:\n        - {id: one, type: shell}\n", want: "at least two steps"},
		{name: "zero limit", body: "      max_concurrency: 0\n      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell}\n", want: "max_concurrency"},
		{name: "zero timeout", body: "      timeout: 0s\n      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell}\n", want: "timeout must be greater"},
		{name: "combined fields", body: "    id: invalid\n    concurrent:\n      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell}\n", want: "cannot be combined"},
		{name: "nested", body: "      steps:\n        - concurrent:\n            steps:\n              - {id: one, type: shell}\n              - {id: two, type: shell}\n        - {id: three, type: shell}\n", want: "nested concurrent"},
		{name: "unknown need", body: "      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell, needs: [missing]}\n", want: `needs unknown sibling "missing"`},
		{name: "self need", body: "      steps:\n        - {id: one, type: shell, needs: [one]}\n        - {id: two, type: shell}\n", want: "cannot need itself"},
		{name: "duplicate need", body: "      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell, needs: [one, one]}\n", want: `duplicate need "one"`},
		{name: "needs cycle", body: "      steps:\n        - {id: one, type: shell, needs: [two]}\n        - {id: two, type: shell, needs: [one]}\n", want: "needs contain a cycle"},
		{name: "unknown field", body: "      unknown: true\n      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell}\n", want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			prefix := "version: 1\nname: invalid\nsteps:\n  - concurrent:\n"
			if test.name == "combined fields" {
				prefix = "version: 1\nname: invalid\nsteps:\n  -\n"
			}
			writeTestFile(t, path, prefix+test.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRejectsNeedsOutsideDirectConcurrentChildren(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "sequential", body: "  - {id: one, type: shell, needs: [other]}\n"},
		{name: "sequential empty", body: "  - {id: one, type: shell, needs: []}\n"},
		{name: "nested branch body", body: "  - concurrent:\n      steps:\n        - id: wrapper\n          once: {key: test, scope: local, steps: [{id: nested, type: shell, needs: [other]}]}\n        - {id: other, type: shell}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.yaml")
			writeTestFile(t, path, "version: 1\nname: invalid\nsteps:\n"+test.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "needs is only supported on direct children of concurrent") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadRejectsNonListNeeds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workflow.yaml")
	writeTestFile(t, path, "version: 1\nname: invalid\nsteps:\n  - concurrent:\n      steps:\n        - {id: one, type: shell}\n        - {id: two, type: shell, needs: one}\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "needs must be a list") {
		t.Fatalf("error = %v", err)
	}
}

func TestConcurrentGroupExpandsRequiredStepsAndSharesIDNamespace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "children.yaml"), `
- id: lint
  type: shell
`)
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: checks
steps:
  - concurrent:
      steps:
        - require: children.yaml
        - {id: test, type: shell, needs: [lint]}
  - {id: package, type: shell}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	children := definition.Steps[0].Concurrent.Steps
	if len(children) != 2 || children[0].ID != "lint" || children[1].ID != "test" || !slices.Equal(children[1].Needs, []string{"lint"}) {
		t.Fatalf("children = %#v", children)
	}

	writeTestFile(t, path, `version: 1
name: duplicate
steps:
  - concurrent:
      steps:
        - {id: lint, type: shell}
        - {id: test, type: shell}
  - {id: lint, type: shell}
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), `duplicate step id "lint"`) {
		t.Fatalf("duplicate error = %v", err)
	}
}
