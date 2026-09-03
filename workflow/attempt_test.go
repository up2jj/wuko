package workflow

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// attemptWorkflow wraps one shell step in an attempt control declared by the given fields.
func attemptWorkflow(fields string) string {
	return "version: 1\nname: invalid\nsteps:\n  - id: run\n    attempt:\n" + fields +
		"      steps:\n        - id: work\n          type: shell\n          with: {command: run}\n"
}

func TestLoadAttemptPolicy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: publish
steps:
  - id: publish
    attempt:
      timeout: 2s
      max_attempts: 4
      initial_delay: 250ms
      backoff_multiplier: 1.5
      max_delay: 3s
      jitter: 0.1
      max_elapsed_time: 10s
      operation_id: "release:{{ .vars.version }}"
      when: 'error.exit_code == 75'
      steps:
        - id: work
          type: shell
          with: {command: publish}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0].Attempt
	if control == nil {
		t.Fatal("attempt control is missing")
	}
	if control.Timeout.Literal.Value() != 2*time.Second {
		t.Fatalf("timeout = %v", control.Timeout)
	}
	if control.MaxAttempts.Literal != 4 || control.InitialDelay.Literal.Value() != 250*time.Millisecond ||
		control.BackoffMultiplier.Literal != 1.5 || control.MaxDelay.Literal.Value() != 3*time.Second ||
		control.Jitter.Literal != 0.1 || control.MaxElapsedTime.Literal.Value() != 10*time.Second {
		t.Fatalf("attempt = %#v", control)
	}
	if control.OperationID != "release:{{ .vars.version }}" {
		t.Fatalf("operation ID = %q", control.OperationID)
	}
	if control.When != `error.exit_code == 75` {
		t.Fatalf("when = %q", control.When)
	}
}

func TestAttemptPolicyDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, attemptWorkflow(""))
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0].Attempt
	// max_attempts defaults to 1: attempt is also the spelling for a bare timeout, so the
	// presence of the key must not imply repeating the way the old retry: key did.
	if control.MaxAttempts.Literal != 1 || control.InitialDelay.Literal.Value() != time.Second ||
		control.BackoffMultiplier.Literal != 2 || control.MaxDelay.Literal.Value() != 30*time.Second ||
		control.Jitter.Literal != 0.2 {
		t.Fatalf("attempt defaults = %#v", control)
	}
	if control.Interval.Set() {
		t.Fatalf("interval was defaulted without until: %#v", control.Interval)
	}
}

func TestAttemptPollIntervalDefaultsOnlyWithUntil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, attemptWorkflow("      until: steps.work.done\n      max_elapsed_time: 1m\n"))
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := definition.Steps[0].Attempt.Interval.Literal.Value(); got != 5*time.Second {
		t.Fatalf("interval = %v, want 5s", got)
	}
}

func TestAttemptOptionsAcceptExpressions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, attemptWorkflow("      timeout: vars.budget\n      max_attempts: vars.tries\n      jitter: vars.spread\n"))
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0].Attempt
	if control.Timeout.Expression != "vars.budget" || control.MaxAttempts.Expression != "vars.tries" || control.Jitter.Expression != "vars.spread" {
		t.Fatalf("expressions = %#v", control)
	}
	// A numeric rule cannot be checked at load time for an expression-backed option, so the
	// declaration must still load.
	if control.Timeout.Literal != 0 || control.MaxAttempts.Literal != 0 {
		t.Fatalf("literals were populated from expressions: %#v", control)
	}
}

func TestResolveAttemptEvaluatesAndValidates(t *testing.T) {
	t.Parallel()
	control := AttemptControl{
		Timeout:           AttemptDuration{Expression: "budget"},
		MaxAttempts:       AttemptCount{Expression: "tries"},
		BackoffMultiplier: AttemptFactor{Literal: 1},
	}
	values := map[string]any{"budget": "90s", "tries": 7}
	resolved, err := control.Resolve(func(expression string) (any, error) { return values[expression], nil })
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Timeout != 90*time.Second || !resolved.HasTimeout || resolved.MaxAttempts != 7 {
		t.Fatalf("resolved = %#v", resolved)
	}

	values["tries"] = 0
	if _, err := control.Resolve(func(expression string) (any, error) { return values[expression], nil }); err == nil ||
		!strings.Contains(err.Error(), "max_attempts must be between 1 and 100") {
		t.Fatalf("error = %v", err)
	}

	values["tries"] = 7
	values["budget"] = "not a duration"
	if _, err := control.Resolve(func(expression string) (any, error) { return values[expression], nil }); err == nil ||
		!strings.Contains(err.Error(), "want a duration string") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAttemptHTTPFilters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: http-retry
steps:
  - id: fetch
    attempt:
      max_attempts: 3
      methods: [get, POST]
      statuses: [408, 429, "500-599"]
      steps:
        - id: request
          type: http
          with: {url: https://example.test}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	control := definition.Steps[0].Attempt
	if strings.Join(control.Methods, ",") != "GET,POST" {
		t.Fatalf("methods = %v", control.Methods)
	}
	want := []StatusRange{{From: 408, To: 408}, {From: 429, To: 429}, {From: 500, To: 599}}
	if len(control.Statuses) != len(want) {
		t.Fatalf("statuses = %#v", control.Statuses)
	}
	for i := range want {
		if control.Statuses[i] != want[i] {
			t.Fatalf("statuses = %#v", control.Statuses)
		}
	}
}

func TestLoadRejectsInvalidAttemptPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{name: "zero timeout", fields: "      timeout: 0s\n", want: "timeout must be greater than zero"},
		{name: "numeric timeout", fields: "      timeout: 5\n", want: "must be a duration such as 500ms or an expression string"},
		{name: "attempts", fields: "      max_attempts: 0\n", want: "max_attempts"},
		{name: "negative delay", fields: "      initial_delay: -1s\n", want: "initial_delay"},
		{name: "backoff", fields: "      backoff_multiplier: 0.5\n", want: "backoff_multiplier"},
		{name: "max delay", fields: "      initial_delay: 2s\n      max_delay: 1s\n", want: "max_delay"},
		{name: "jitter", fields: "      jitter: 1.1\n", want: "jitter"},
		{name: "elapsed", fields: "      max_elapsed_time: -1s\n", want: "max_elapsed_time"},
		{name: "blank when", fields: "      when: ''\n", want: "attempt when must not be empty"},
		{name: "non-scalar when", fields: "      when: [true]\n", want: "attempt when must be a boolean expression"},
		{name: "unknown", fields: "      unknown: true\n", want: "field unknown"},
		{name: "interval without until", fields: "      interval: 1s\n", want: "interval requires until"},
		{name: "until without budget", fields: "      until: steps.work.done\n", want: "until requires max_elapsed_time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "workflow.yaml")
			writeTestFile(t, path, attemptWorkflow(test.fields))
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidAttemptShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		workflow string
		want     string
	}{
		{
			name:     "empty body",
			workflow: "version: 1\nname: invalid\nsteps:\n  - id: run\n    attempt: {max_attempts: 2}\n",
			want:     "must contain at least one step",
		},
		{
			name:     "duration with body",
			workflow: "version: 1\nname: invalid\nsteps:\n  - id: run\n    attempt:\n      duration: 1s\n      steps:\n        - {id: work, type: shell, with: {command: run}}\n",
			want:     "duration cannot be combined with other attempt fields",
		},
		{
			name:     "zero duration",
			workflow: "version: 1\nname: invalid\nsteps:\n  - id: run\n    attempt: {duration: 0s}\n",
			want:     "duration must be greater than zero",
		},
		{
			name:     "missing id",
			workflow: "version: 1\nname: invalid\nsteps:\n  - attempt:\n      steps:\n        - {id: work, type: shell, with: {command: run}}\n",
			want:     `step 1 has invalid id ""`,
		},
		{
			name:     "combined with type",
			workflow: "version: 1\nname: invalid\nsteps:\n  - id: run\n    type: shell\n    attempt: {duration: 1s}\n",
			want:     "cannot be combined with ordinary step fields",
		},
		{
			name:     "defer in body",
			workflow: "version: 1\nname: invalid\nsteps:\n  - id: run\n    attempt:\n      steps:\n        - id: work\n          type: shell\n          with: {command: run}\n          defer:\n            - {id: cleanup, type: shell, with: {command: clean}}\n",
			want:     "defer is not supported inside attempt",
		},
		{
			name:     "return in body",
			workflow: "version: 1\nname: invalid\nsteps:\n  - id: run\n    attempt:\n      steps:\n        - return: {outputs: {}}\n",
			want:     "return is not supported inside attempt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "workflow.yaml")
			writeTestFile(t, path, test.workflow)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAttemptBodyKeepsEnclosingIDScope(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	// Outputs are isolated, but ids are not: a body step may not shadow an enclosing id, the
	// same rule once, try/catch, and cancel_on bodies already keep. It keeps diagnostics and
	// step references unambiguous.
	writeTestFile(t, path, `version: 1
name: scoped
steps:
  - id: work
    type: shell
    with: {command: outer}
  - id: guarded
    attempt:
      steps:
        - id: work
          type: shell
          with: {command: inner}
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate step id "work"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsInvalidAttemptHTTPFilters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{name: "invalid method", policy: "      methods: ['BAD METHOD']\n", want: "valid HTTP method"},
		{name: "duplicate method", policy: "      methods: [GET, get]\n", want: "duplicated"},
		{name: "empty methods", policy: "      methods: []\n", want: "at least one"},
		{name: "empty statuses", policy: "      statuses: []\n", want: "at least one"},
		{name: "descending range", policy: "      statuses: ['599-500']\n", want: "ascending"},
		{name: "out of range", policy: "      statuses: [99]\n", want: "between 100 and 599"},
		{name: "overlap", policy: "      statuses: ['500-550', '525-599']\n", want: "overlap"},
		{name: "when with methods", policy: "      when: 'true'\n      methods: [GET]\n", want: "cannot be combined"},
		{name: "when with statuses", policy: "      when: 'true'\n      statuses: [503]\n", want: "cannot be combined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "workflow.yaml")
			writeTestFile(t, path, "version: 1\nname: invalid\nsteps:\n  - id: fetch\n    attempt:\n      max_attempts: 2\n"+test.policy+"      steps:\n        - {id: request, type: http, with: {url: https://example.test}}\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
