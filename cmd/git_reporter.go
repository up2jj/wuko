package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
)

// gitHookReporter keeps successful hook runs quiet while retaining enough structured execution
// detail to turn a rejected Git operation into one concise, actionable error.
type gitHookReporter struct {
	hookName     string
	workflowName string
	runDir       string
	diagnostics  diagnostic.Reporter

	mu        sync.Mutex
	failure   gitHookFailure
	candidate gitHookFailure
}

type gitHookFailure struct {
	workflow string
	step     string
	location diagnostic.Location
	attempts int
	err      error
}

type gitHookRunError struct {
	message string
	cause   error
}

func (err gitHookRunError) Error() string {
	detail := err.cause.Error()
	if end := strings.IndexAny(detail, "\r\n"); end >= 0 {
		detail = detail[:end]
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "workflow execution failed"
	}
	return err.message + ": " + detail
}

func (err gitHookRunError) Unwrap() error { return err.cause }

func newGitHookReporter(command *cobra.Command, deps dependencies, runDir, hookName, workflowName string) *gitHookReporter {
	return &gitHookReporter{
		hookName: hookName, workflowName: workflowName, runDir: runDir,
		diagnostics: diagnosticsFor(command, deps, runDir),
	}
}

func (reporter *gitHookReporter) Progress(event engine.ProgressEvent) {
	if event.Kind != engine.WorkflowFinished || event.Depth != 0 || !failedExecutionStatus(event.Status) {
		return
	}
	stats, found := firstGitHookFailedStep(event.Stats.Steps)
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.failure = gitHookFailure{workflow: event.WorkflowName, err: event.Error}
	if found {
		reporter.failure.step = stats.ID
		reporter.failure.location = stats.Location
		reporter.failure.attempts = len(stats.Attempts)
		reporter.failure.err = stats.Error
	}
}

func (reporter *gitHookReporter) Diagnostic(event diagnostic.Event) {
	reporter.mu.Lock()
	switch {
	case event.Status == diagnostic.StatusFailed && event.StepID != "":
		reporter.candidate = gitHookFailure{
			workflow: event.WorkflowName,
			step:     event.StepID,
			location: event.Location,
			err:      event.Error,
		}
	// A step that recovers - on a retry, say - must not stand in for an unrelated failure that
	// carries no step of its own.
	case (event.Status == diagnostic.StatusSucceeded || event.Status == diagnostic.StatusSkipped) &&
		event.StepID != "" && event.StepID == reporter.candidate.step:
		reporter.candidate = gitHookFailure{}
	}
	reporter.mu.Unlock()
	diagnostic.Emit(reporter.diagnostics, event)
}

func (*gitHookReporter) Finish(context.Context, reporterpkg.Outcome) error { return nil }

func (reporter *gitHookReporter) failureError(runErr error) error {
	reporter.mu.Lock()
	failure := reporter.failure
	if failure.step == "" {
		failure = reporter.candidate
	}
	reporter.mu.Unlock()

	message := fmt.Sprintf("%s: workflow %q failed", reporter.hookName, reporter.workflowName)
	if failure.step != "" {
		if failure.workflow != "" && failure.workflow != reporter.workflowName {
			message += fmt.Sprintf(" in workflow %q", failure.workflow)
		}
		message += fmt.Sprintf(" at step %q", failure.step)
		if location := reporter.formatLocation(failure.location); location != "" {
			message += " (" + location + ")"
		}
		if failure.attempts > 1 {
			message += fmt.Sprintf(" after %d attempts", failure.attempts)
		}
	}
	cause := failure.err
	if cause == nil {
		cause = runErr
	}
	return gitHookRunError{message: message, cause: cause}
}

func (reporter *gitHookReporter) formatLocation(location diagnostic.Location) string {
	if location.Source == "" {
		return ""
	}
	source := location.Source
	if filepath.IsAbs(source) {
		if relative, err := filepath.Rel(reporter.runDir, source); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			source = relative
		}
	}
	if location.Line > 0 {
		source += fmt.Sprintf(":%d", location.Line)
		if location.Column > 0 {
			source += fmt.Sprintf(":%d", location.Column)
		}
	}
	return source
}

func failedExecutionStatus(status engine.ExecutionStatus) bool {
	return status == engine.StatusFailed || status == engine.StatusTimedOut || status == engine.StatusCanceled
}

func firstGitHookFailedStep(steps []engine.StepStats) (engine.StepStats, bool) {
	for _, stats := range steps {
		if !failedExecutionStatus(stats.Status) {
			continue
		}
		for _, iteration := range stats.Iterations {
			if nested, found := firstGitHookFailedStep(iteration.Steps); found {
				return nested, true
			}
		}
		return stats, true
	}
	return engine.StepStats{}, false
}

var _ reporterpkg.Reporter = (*gitHookReporter)(nil)
