package workflow

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExecutionPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, `version: 1
name: retry
steps:
  - id: publish
    type: shell
    timeout: 2s
    retry:
      max_attempts: 4
      initial_delay: 250ms
      backoff_multiplier: 1.5
      max_delay: 3s
      jitter: 0.1
      max_elapsed_time: 10s
      operation_id: "release:{{ .vars.version }}"
    with: {command: publish}
`)
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	workflowStep := definition.Steps[0]
	if workflowStep.Timeout == nil || workflowStep.Timeout.Value() != 2*time.Second {
		t.Fatalf("timeout = %v", workflowStep.Timeout)
	}
	policy := workflowStep.Retry
	if policy == nil || policy.MaxAttempts != 4 || policy.InitialDelay.Value() != 250*time.Millisecond || policy.BackoffMultiplier != 1.5 || policy.MaxDelay.Value() != 3*time.Second || policy.Jitter != 0.1 || policy.MaxElapsedTime.Value() != 10*time.Second {
		t.Fatalf("retry = %#v", policy)
	}
	if policy.OperationID != "release:{{ .vars.version }}" {
		t.Fatalf("operation ID = %q", policy.OperationID)
	}
}

func TestRetryPolicyDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	writeTestFile(t, path, "version: 1\nname: defaults\nsteps:\n  - id: run\n    type: shell\n    retry: {}\n    with: {command: run}\n")
	definition, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := definition.Steps[0].Retry
	if policy.MaxAttempts != 3 || policy.InitialDelay.Value() != time.Second || policy.BackoffMultiplier != 2 || policy.MaxDelay.Value() != 30*time.Second || policy.Jitter != 0.2 {
		t.Fatalf("retry defaults = %#v", policy)
	}
}

func TestLoadRejectsInvalidExecutionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{name: "zero timeout", fields: "    timeout: 0s\n", want: "timeout must be greater than zero"},
		{name: "numeric timeout", fields: "    timeout: 5\n", want: "duration must be a string"},
		{name: "attempts", fields: "    retry: {max_attempts: 0}\n", want: "max_attempts"},
		{name: "negative delay", fields: "    retry: {initial_delay: -1s}\n", want: "initial_delay"},
		{name: "backoff", fields: "    retry: {backoff_multiplier: 0.5}\n", want: "backoff_multiplier"},
		{name: "max delay", fields: "    retry: {initial_delay: 2s, max_delay: 1s}\n", want: "max_delay"},
		{name: "jitter", fields: "    retry: {jitter: 1.1}\n", want: "jitter"},
		{name: "elapsed", fields: "    retry: {max_elapsed_time: -1s}\n", want: "max_elapsed_time"},
		{name: "unknown", fields: "    retry: {unknown: true}\n", want: "field unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "workflow.yaml")
			writeTestFile(t, path, "version: 1\nname: invalid\nsteps:\n  - id: run\n    type: shell\n"+test.fields+"    with: {command: run}\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
