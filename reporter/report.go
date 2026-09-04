package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
)

// ExecutionReportSchemaVersion is the current canonical JSON report schema.
const ExecutionReportSchemaVersion = 1

// ExecutionReport is the stable, safe JSON projection of a completed Wuko invocation.
// It deliberately excludes errors, inputs, variables, environment, and intermediate outputs.
type ExecutionReport struct {
	SchemaVersion int                      `json:"schema_version"`
	InvocationID  correlation.InvocationID `json:"invocation_id"`
	RunID         correlation.RunID        `json:"run_id,omitempty"`
	Workflow      string                   `json:"workflow,omitempty"`
	Status        engine.ExecutionStatus   `json:"status"`
	DryRun        bool                     `json:"dry_run"`
	DurationMS    int64                    `json:"duration_ms"`
	FailedStep    string                   `json:"failed_step,omitempty"`
	Stats         ExecutionStats           `json:"stats"`
	Outputs       *map[string]any          `json:"outputs,omitempty"`
}

// ExecutionStats contains aggregate, non-sensitive statistics for one engine run.
type ExecutionStats struct {
	RunDurationMS int64              `json:"run_duration_ms"`
	Steps         ExecutionStepStats `json:"steps"`
	Attempts      int                `json:"attempts"`
	Retries       int                `json:"retries"`
	RetryWaitMS   int64              `json:"retry_wait_ms"`
	Polls         int                `json:"polls"`
	PollWaitMS    int64              `json:"poll_wait_ms"`
}

// ExecutionStepStats contains aggregate step counts for one engine run. Total is the planned leaf
// step count, so it is not the sum of the remaining fields: steps that never started are counted
// by neither. Succeeded, Skipped, and Canceled are terminal step counts, while Failed also counts
// every step that timed out. TimedOut counts timed-out attempts rather than steps, so it overlaps
// Failed and can exceed the number of steps when a retried step timed out more than once.
type ExecutionStepStats struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
	Canceled  int `json:"canceled"`
	TimedOut  int `json:"timed_out"`
}

// NewExecutionReport converts a reporter outcome into the canonical execution report.
func NewExecutionReport(outcome Outcome) ExecutionReport {
	status := outcome.Status
	if status == "" {
		if outcome.Err != nil {
			status = engine.StatusFailed
		} else {
			status = engine.StatusSucceeded
		}
	}
	report := ExecutionReport{
		SchemaVersion: ExecutionReportSchemaVersion,
		InvocationID:  outcome.InvocationID,
		RunID:         outcome.RunID,
		Workflow:      outcome.WorkflowName,
		Status:        status,
		DryRun:        outcome.DryRun,
		DurationMS:    milliseconds(outcome.Duration),
		FailedStep:    firstFailedStep(outcome.Stats.Steps),
		Stats: ExecutionStats{
			RunDurationMS: milliseconds(outcome.Stats.Duration),
			Steps: ExecutionStepStats{
				Total: outcome.Stats.Total, Succeeded: outcome.Stats.Succeeded,
				Failed: outcome.Stats.Failed, Skipped: outcome.Stats.Skipped,
				Canceled: outcome.Stats.Canceled, TimedOut: outcome.Stats.TimedOut,
			},
			Attempts: outcome.Stats.Attempts, Retries: outcome.Stats.Retries,
			RetryWaitMS: milliseconds(outcome.Stats.RetryWait), Polls: outcome.Stats.Polls,
			PollWaitMS: milliseconds(outcome.Stats.PollWait),
		},
	}
	if status == engine.StatusSucceeded && !outcome.DryRun && outcome.Err == nil {
		outputs := outcome.Outputs
		if outputs == nil {
			outputs = map[string]any{}
		}
		report.Outputs = &outputs
	}
	return report
}

func firstFailedStep(steps []engine.StepStats) string {
	for _, stats := range steps {
		switch stats.Status {
		case engine.StatusFailed, engine.StatusTimedOut, engine.StatusCanceled:
			return stats.ID
		}
	}
	return ""
}

func milliseconds(duration time.Duration) int64 {
	return max(0, duration.Milliseconds())
}

// JSONFile writes one canonical execution report to a local file when the invocation finishes.
type JSONFile struct {
	path string
}

// NewJSONFile validates a destination for an atomically written execution report.
func NewJSONFile(path string) (*JSONFile, error) {
	if path == "" {
		return nil, fmt.Errorf("execution report path is required")
	}
	path = filepath.Clean(path)
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspecting execution report directory %s: %w", directory, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("execution report directory %s is not a directory", directory)
	}
	return &JSONFile{path: path}, nil
}

func (*JSONFile) Progress(engine.ProgressEvent) {}

func (*JSONFile) Diagnostic(diagnostic.Event) {}

// Finish writes an indented report through a mode-0600 temporary sibling and atomic rename.
func (reporter *JSONFile) Finish(_ context.Context, outcome Outcome) error {
	data, err := json.MarshalIndent(NewExecutionReport(outcome), "", "  ")
	if err != nil {
		return fmt.Errorf("encoding execution report: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(reporter.path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(reporter.path)+".*")
	if err != nil {
		return fmt.Errorf("creating temporary execution report: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing temporary execution report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary execution report: %w", err)
	}
	if err := os.Rename(temporaryPath, reporter.path); err != nil {
		return fmt.Errorf("replacing execution report %s: %w", reporter.path, err)
	}
	return nil
}

var _ Reporter = (*JSONFile)(nil)
