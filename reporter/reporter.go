// Package reporter defines the lifecycle contract shared by Wuko run reporters.
package reporter

import (
	"context"
	"errors"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
)

// Outcome is the safe, final result exposed to reporters. Outputs contains a deep copy of the
// workflow's declared outputs; execution inputs, variables, environment, and intermediate state
// are deliberately excluded.
type Outcome struct {
	WorkflowName string
	Status       engine.ExecutionStatus
	Stats        engine.RunStats
	Outputs      map[string]any
	Err          error
	DryRun       bool
}

// Reporter receives ordered progress and diagnostic events, then one final outcome. Event calls
// are synchronous and serialized by the engine; implementations must not block indefinitely.
// Implementations that retain events must copy any referenced data.
type Reporter interface {
	Progress(engine.ProgressEvent)
	Diagnostic(diagnostic.Event)
	Finish(context.Context, Outcome) error
}

// Funcs adapts optional callbacks into a Reporter. Its zero value is a no-op reporter.
type Funcs struct {
	ProgressFunc   func(engine.ProgressEvent)
	DiagnosticFunc func(diagnostic.Event)
	FinishFunc     func(context.Context, Outcome) error
}

// Progress calls ProgressFunc when configured.
func (reporter Funcs) Progress(event engine.ProgressEvent) {
	if reporter.ProgressFunc != nil {
		reporter.ProgressFunc(event)
	}
}

// Diagnostic calls DiagnosticFunc when configured.
func (reporter Funcs) Diagnostic(event diagnostic.Event) {
	if reporter.DiagnosticFunc != nil {
		reporter.DiagnosticFunc(event)
	}
}

// Finish calls FinishFunc when configured.
func (reporter Funcs) Finish(ctx context.Context, outcome Outcome) error {
	if reporter.FinishFunc == nil {
		return nil
	}
	return reporter.FinishFunc(ctx, outcome)
}

// Group delivers events to reporters in declaration order. The zero value is ready to use.
type Group []Reporter

// Progress delivers one progress event to every reporter.
func (reporters Group) Progress(event engine.ProgressEvent) {
	for _, reporter := range reporters {
		reporter.Progress(event)
	}
}

// Diagnostic delivers one diagnostic event to every reporter.
func (reporters Group) Diagnostic(event diagnostic.Event) {
	for _, reporter := range reporters {
		reporter.Diagnostic(event)
	}
}

// Finish delivers the final outcome to every reporter and joins their errors.
func (reporters Group) Finish(ctx context.Context, outcome Outcome) error {
	errorsByReporter := make([]error, 0, len(reporters))
	for _, reporter := range reporters {
		if err := reporter.Finish(ctx, outcome); err != nil {
			errorsByReporter = append(errorsByReporter, err)
		}
	}
	return errors.Join(errorsByReporter...)
}

var _ Reporter = Funcs{}
var _ Reporter = Group(nil)
