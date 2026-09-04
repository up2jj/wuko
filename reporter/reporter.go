// Package reporter defines the lifecycle contract shared by Wuko run reporters.
package reporter

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
)

// Outcome is the safe, final result exposed to reporters. Duration covers the whole reporting
// invocation. Outputs contains a deep copy of the workflow's declared outputs; execution inputs,
// variables, environment, and intermediate state are deliberately excluded.
type Outcome struct {
	InvocationID    correlation.InvocationID
	RunID           correlation.RunID
	ParentRunID     correlation.RunID
	ParentStepRunID correlation.StepRunID
	WorkflowName    string
	Status          engine.ExecutionStatus
	Duration        time.Duration
	Stats           engine.RunStats
	Outputs         map[string]any
	Err             error
	DryRun          bool
}

// Reporter receives ordered progress and diagnostic events, then one final outcome. Engine calls
// are synchronous and serialized within a run; Session extends that ordering across a complete
// reporting invocation. Implementations must not block indefinitely and must copy retained data.
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

// Session correlates and serializes one invocation's progress and diagnostic streams. Its zero
// value is a usable session with a generated invocation ID and no reporters.
type Session struct {
	dispatchMu   sync.Mutex
	identityMu   sync.Mutex
	invocationID correlation.InvocationID
	sequence     correlation.Sequence
	reporters    Group
}

// NewSession constructs a reporting session. A zero invocationID generates a new identity.
func NewSession(invocationID correlation.InvocationID, reporters ...Reporter) *Session {
	return &Session{invocationID: invocationID, reporters: slices.Clone(reporters)}
}

// InvocationID returns the session identity, generating it on first use when necessary.
func (session *Session) InvocationID() correlation.InvocationID {
	session.identityMu.Lock()
	defer session.identityMu.Unlock()
	return session.invocationIDLocked()
}

func (session *Session) invocationIDLocked() correlation.InvocationID {
	if session.invocationID == "" {
		session.invocationID = correlation.NewInvocationID()
	}
	return session.invocationID
}

// Progress stamps and delivers one progress event in invocation order.
func (session *Session) Progress(event engine.ProgressEvent) {
	session.progress(event, nil)
}

func (session *Session) progress(event engine.ProgressEvent, additional Group) {
	session.dispatchMu.Lock()
	defer session.dispatchMu.Unlock()
	session.sequence++
	event.InvocationID = session.InvocationID()
	event.Sequence = session.sequence
	session.reporters.Progress(event)
	additional.Progress(event)
}

// Diagnostic stamps and delivers one diagnostic event in invocation order.
func (session *Session) Diagnostic(event diagnostic.Event) {
	session.diagnostic(event, nil)
}

func (session *Session) diagnostic(event diagnostic.Event, additional Group) {
	session.dispatchMu.Lock()
	defer session.dispatchMu.Unlock()
	session.sequence++
	event.InvocationID = session.InvocationID()
	event.Sequence = session.sequence
	session.reporters.Diagnostic(event)
	additional.Diagnostic(event)
}

// Finish stamps and delivers the final outcome without consuming an event sequence number.
func (session *Session) Finish(ctx context.Context, outcome Outcome) error {
	return session.finish(ctx, outcome, nil)
}

func (session *Session) finish(ctx context.Context, outcome Outcome, additional Group) error {
	session.dispatchMu.Lock()
	defer session.dispatchMu.Unlock()
	outcome.InvocationID = session.InvocationID()
	return errors.Join(session.reporters.Finish(ctx, outcome), additional.Finish(ctx, outcome))
}

var _ Reporter = (*Session)(nil)

// With returns a reporter view that shares this session's identity and sequence while also
// delivering events to additional reporters after the session's reporters.
func (session *Session) With(additional ...Reporter) Reporter {
	return sessionView{session: session, additional: slices.Clone(additional)}
}

type sessionView struct {
	session    *Session
	additional Group
}

func (view sessionView) Progress(event engine.ProgressEvent) {
	view.session.progress(event, view.additional)
}

func (view sessionView) Diagnostic(event diagnostic.Event) {
	view.session.diagnostic(event, view.additional)
}

func (view sessionView) Finish(ctx context.Context, outcome Outcome) error {
	return view.session.finish(ctx, outcome, view.additional)
}

var _ Reporter = sessionView{}
