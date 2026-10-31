package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/useroutput"
	"github.com/up2jj/wuko/validation"
)

// gitHookReporter prints a final result for each top-level check and retains enough structured
// execution detail to turn a rejected Git operation into one concise, actionable error.
type gitHookReporter struct {
	hookName     string
	workflowName string
	runDir       string
	diagnostics  diagnostic.Reporter
	output       io.Writer

	mu        sync.Mutex
	failure   gitHookFailure
	candidate gitHookFailure
	// concurrentGroups counts running top-level concurrent groups. Workflow validation rejects
	// nested concurrent groups, so their branches always report at depth one.
	concurrentGroups int
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
		output:      command.ErrOrStderr(),
	}
}

func (reporter *gitHookReporter) Progress(event engine.ProgressEvent) {
	// The engine holds its report lock during this callback, so the line is written only after
	// releasing ours: a slow stderr reader then stalls nothing but the write itself.
	if line := reporter.record(event); line != "" {
		io.WriteString(reporter.output, line)
	}
}

// record updates the reporter's state for event and returns the check line to print, if any.
func (reporter *gitHookReporter) record(event engine.ProgressEvent) (line string) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	// Concurrent groups are transparent: their individual checks still get rows. Bodies of
	// repeated controls and child workflows instead contribute to their enclosing check.
	if event.ParentRunID == "" {
		switch {
		case event.Depth == 0 && event.Kind == engine.ConcurrentStarted:
			reporter.concurrentGroups++
		case event.Depth == 0 && event.Kind == engine.ConcurrentFinished:
			reporter.concurrentGroups--
		case event.Kind == engine.StepFinished && (event.Depth == 0 || event.Depth == 1 && reporter.concurrentGroups > 0):
			line = reporter.checkLine(event)
		}
	}
	if event.Kind != engine.WorkflowFinished || event.Depth != 0 || !event.Status.Unsuccessful() {
		return line
	}
	stats, found := firstGitHookFailedStep(event.Stats.Steps)
	reporter.failure = gitHookFailure{workflow: event.WorkflowName, err: event.Error}
	if found {
		reporter.failure.step = stats.ID
		reporter.failure.location = stats.Location
		reporter.failure.attempts = len(stats.Attempts)
		reporter.failure.err = stats.Error
	}
	return line
}

func (reporter *gitHookReporter) checkLine(event engine.ProgressEvent) string {
	if event.Hidden() || useroutput.OnlyPlain(event.Error) {
		return ""
	}
	var status string
	switch event.Status {
	case engine.StatusSucceeded:
		status = "Passed"
	case engine.StatusFailed:
		status = "Failed"
	case engine.StatusSkipped:
		status = "Skipped"
	case engine.StatusTimedOut:
		status = "Timed out"
	case engine.StatusCanceled:
		status = "Canceled"
	default:
		return ""
	}
	// Label checks by the workflow's declared name; the hook selector may be a path or target.
	workflow := event.WorkflowName
	if workflow == "" {
		workflow = reporter.workflowName
	}
	// Measure terminal columns, not runes, and clip long labels so every status stays aligned.
	const lineWidth, minDots = 72, 3
	label := ansi.Truncate(workflow+" / "+event.StepID, lineWidth-minDots-len(status), "…")
	dots := strings.Repeat(".", lineWidth-ansi.StringWidth(label)-len(status))
	return label + dots + status + "\n"
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
	if issues := validation.Issues(cause); len(issues) != 0 {
		message += fmt.Sprintf(": %s", issues[0].Message)
		if len(issues) > 1 {
			message += fmt.Sprintf(" (and %d more validation issues)", len(issues)-1)
		}
		return errors.New(message)
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

func firstGitHookFailedStep(steps []engine.StepStats) (engine.StepStats, bool) {
	for _, stats := range steps {
		if !stats.Status.Unsuccessful() {
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
