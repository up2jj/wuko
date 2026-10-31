package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/internal/fsutil"
	"github.com/up2jj/wuko/validation"
)

// ExecutionReportSchemaVersion is the current canonical JSON report schema.
const ExecutionReportSchemaVersion = 2

// ExecutionReport is the stable, safe JSON projection of a completed Wuko invocation.
// It excludes errors, inputs, and environment. Step outputs and variables appear only when the
// JSON writer was asked to include them, redacted and size-limited.
type ExecutionReport struct {
	SchemaVersion    int                      `json:"schema_version"`
	InvocationID     correlation.InvocationID `json:"invocation_id"`
	RunID            correlation.RunID        `json:"run_id,omitempty"`
	Workflow         string                   `json:"workflow,omitempty"`
	Status           engine.ExecutionStatus   `json:"status"`
	DryRun           bool                     `json:"dry_run"`
	Replaced         bool                     `json:"replaced,omitempty"`
	DurationMS       int64                    `json:"duration_ms"`
	FailedStep       string                   `json:"failed_step,omitempty"`
	Stats            ExecutionStats           `json:"stats"`
	Outputs          *map[string]any          `json:"outputs,omitempty"`
	StepOutputs      map[string]any           `json:"step_outputs,omitempty"`
	Vars             map[string]any           `json:"vars,omitempty"`
	ValuesTruncated  bool                     `json:"values_truncated,omitempty"`
	ValidationIssues []validation.SafeIssue   `json:"validation_issues,omitempty"`
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
		Replaced:      outcome.Replaced,
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
	for _, issue := range validation.Issues(outcome.Err) {
		report.ValidationIssues = append(report.ValidationIssues, issue.Safe())
	}
	if status == engine.StatusSucceeded && !outcome.DryRun && !outcome.Replaced && outcome.Err == nil {
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
		if stats.Status.Unsuccessful() {
			return stats.ID
		}
	}
	return ""
}

func milliseconds(duration time.Duration) int64 {
	return max(0, duration.Milliseconds())
}

// JSONWriter writes one compact execution report to a stream when the invocation finishes.
type JSONWriter struct {
	writer  io.Writer
	include ValueInclude
}

// NewJSONWriter constructs a stream-backed execution report writer.
func NewJSONWriter(writer io.Writer, include ValueInclude) (*JSONWriter, error) {
	if writer == nil {
		return nil, fmt.Errorf("execution report writer is required")
	}
	return &JSONWriter{writer: writer, include: include}, nil
}

func (*JSONWriter) Progress(engine.ProgressEvent) {}

func (*JSONWriter) Diagnostic(diagnostic.Event) {}

// Finish writes one compact JSON document followed by a newline.
func (reporter *JSONWriter) Finish(_ context.Context, outcome Outcome) error {
	data, err := json.Marshal(reportWithValues(outcome, reporter.include))
	if err != nil {
		return fmt.Errorf("encoding execution report: %w", err)
	}
	// One Write keeps the document intact for a reader; io.Writer reports any short write.
	if _, err := reporter.writer.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing execution report: %w", err)
	}
	return nil
}

// JSONFile writes one canonical execution report to a local file when the invocation finishes.
type JSONFile struct {
	path    string
	include ValueInclude
}

// NewJSONFile validates a destination for an atomically written execution report.
func NewJSONFile(path string, include ValueInclude) (*JSONFile, error) {
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
	return &JSONFile{path: path, include: include}, nil
}

func (*JSONFile) Progress(engine.ProgressEvent) {}

func (*JSONFile) Diagnostic(diagnostic.Event) {}

// Finish writes an indented report through a mode-0600 temporary sibling and atomic rename.
func (reporter *JSONFile) Finish(_ context.Context, outcome Outcome) error {
	data, err := json.MarshalIndent(reportWithValues(outcome, reporter.include), "", "  ")
	if err != nil {
		return fmt.Errorf("encoding execution report: %w", err)
	}
	data = append(data, '\n')

	if err := fsutil.WriteFileAtomic(reporter.path, data, 0o600); err != nil {
		return fmt.Errorf("replacing execution report: %w", err)
	}
	return nil
}

var _ Reporter = (*JSONFile)(nil)
var _ Reporter = (*JSONWriter)(nil)
