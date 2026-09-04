package reporter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
)

func TestNewExecutionReportProjectsSafeSuccess(t *testing.T) {
	outcome := Outcome{
		InvocationID: "invocation", RunID: "run", WorkflowName: "check",
		Status: engine.StatusSucceeded, Duration: 42381 * time.Millisecond,
		Stats: engine.RunStats{
			Duration: 41002 * time.Millisecond, Total: 8, Succeeded: 6, Failed: 1, Skipped: 1,
			Attempts: 9, Retries: 2, RetryWait: time.Second, Polls: 14, PollWait: 7 * time.Second,
		},
		Outputs: map[string]any{"artifact": "dist/app.tar.gz"},
	}
	report := NewExecutionReport(outcome)
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"invocation_id":"invocation","run_id":"run","workflow":"check","status":"succeeded","dry_run":false,"duration_ms":42381,"stats":{"run_duration_ms":41002,"steps":{"total":8,"succeeded":6,"failed":1,"skipped":1,"canceled":0,"timed_out":0},"attempts":9,"retries":2,"retry_wait_ms":1000,"polls":14,"poll_wait_ms":7000},"outputs":{"artifact":"dist/app.tar.gz"}}`
	if string(data) != want {
		t.Fatalf("report = %s, want %s", data, want)
	}
}

func TestNewExecutionReportFailureOmitsUnavailableAndSensitiveData(t *testing.T) {
	outcome := Outcome{
		InvocationID: "invocation", Status: engine.StatusTimedOut, Duration: 3 * time.Second,
		Stats: engine.RunStats{Total: 3, Failed: 1, TimedOut: 1, Steps: []engine.StepStats{
			{ID: "integration", Status: engine.StatusTimedOut, Error: errors.New("secret timeout")},
			{ID: "cleanup", Status: engine.StatusCanceled},
		}},
		Outputs: map[string]any{"partial": "must not escape"}, Err: errors.New("secret run error"),
	}
	data, err := json.Marshal(NewExecutionReport(outcome))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"outputs", "partial", "secret", "run_id", "workflow"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("report = %s, must omit %q", text, forbidden)
		}
	}
	if !strings.Contains(text, `"failed_step":"integration"`) {
		t.Fatalf("report = %s, want first unsuccessful step ID", text)
	}
}

func TestNewExecutionReportDoesNotSubstituteALaterFailedStepID(t *testing.T) {
	report := NewExecutionReport(Outcome{Status: engine.StatusFailed, Err: errors.New("failed"), Stats: engine.RunStats{
		Steps: []engine.StepStats{
			{Status: engine.StatusFailed},
			{ID: "cleanup", Status: engine.StatusFailed},
		},
	}})
	if report.FailedStep != "" {
		t.Fatalf("failed step = %q, want empty ID from first unsuccessful step", report.FailedStep)
	}
}

func TestNewExecutionReportDryRunOmitsPlaceholderOutputs(t *testing.T) {
	report := NewExecutionReport(Outcome{
		InvocationID: "invocation", Status: engine.StatusSucceeded, DryRun: true,
		Outputs: map[string]any{"placeholder": ""},
	})
	if report.Outputs != nil || !report.DryRun || report.Status != engine.StatusSucceeded {
		t.Fatalf("report = %#v, want successful dry run without outputs", report)
	}
}

func TestNewExecutionReportSuccessIncludesEmptyOutputsObject(t *testing.T) {
	report := NewExecutionReport(Outcome{Status: engine.StatusSucceeded})
	if report.Outputs == nil || len(*report.Outputs) != 0 {
		t.Fatalf("outputs = %#v, want present empty object", report.Outputs)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"outputs":{}`) {
		t.Fatalf("report = %s, want empty outputs object", data)
	}
}

func TestNewExecutionReportInfersStatusForExternalCallers(t *testing.T) {
	if got := NewExecutionReport(Outcome{}).Status; got != engine.StatusSucceeded {
		t.Fatalf("empty outcome status = %q, want succeeded", got)
	}
	if got := NewExecutionReport(Outcome{Err: errors.New("failed")}).Status; got != engine.StatusFailed {
		t.Fatalf("failed outcome status = %q, want failed", got)
	}
}

func TestNewExecutionReportPreservesUnsuccessfulStatuses(t *testing.T) {
	for _, status := range []engine.ExecutionStatus{engine.StatusFailed, engine.StatusTimedOut, engine.StatusCanceled} {
		t.Run(string(status), func(t *testing.T) {
			report := NewExecutionReport(Outcome{Status: status, Outputs: map[string]any{"partial": true}})
			if report.Status != status || report.Outputs != nil {
				t.Fatalf("report = %#v, want %q without outputs", report, status)
			}
		})
	}
}

func TestJSONFileAtomicallyOverwritesWithPrivatePrettyReport(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	reporter, err := NewJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.Finish(t.Context(), Outcome{InvocationID: "invocation", Status: engine.StatusFailed, Err: errors.New("broken")}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || !strings.Contains(string(data), "\n  \"schema_version\": 1,") {
		t.Fatalf("report = %q, want indented JSON with trailing newline", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	assertNoTemporaryReports(t, directory, path)
}

func TestNewJSONFileRequiresExistingDirectory(t *testing.T) {
	_, err := NewJSONFile(filepath.Join(t.TempDir(), "missing", "report.json"))
	if err == nil || !strings.Contains(err.Error(), "inspecting execution report directory") {
		t.Fatalf("NewJSONFile() error = %v, want missing directory", err)
	}
}

func TestJSONFilePreservesExistingDestinationWhenEncodingFails(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reporter, err := NewJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = reporter.Finish(t.Context(), Outcome{
		Status: engine.StatusSucceeded, Outputs: map[string]any{"invalid": func() {}},
	})
	if err == nil || !strings.Contains(err.Error(), "encoding execution report") {
		t.Fatalf("Finish() error = %v, want encoding failure", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old" {
		t.Fatalf("destination = %q, want original content", data)
	}
	assertNoTemporaryReports(t, directory, path)
}

func TestJSONFileCleansTemporaryFileWhenRenameFails(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	reporter, err := NewJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = reporter.Finish(t.Context(), Outcome{Status: engine.StatusFailed, Err: errors.New("broken")})
	if err == nil || !strings.Contains(err.Error(), "replacing execution report") {
		t.Fatalf("Finish() error = %v, want rename failure", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("destination changed after rename failure: info=%v err=%v", info, statErr)
	}
	assertNoTemporaryReports(t, directory, path)
}

func assertNoTemporaryReports(t *testing.T, directory, destination string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, "."+filepath.Base(destination)+".*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary reports remain: %v", matches)
	}
}
