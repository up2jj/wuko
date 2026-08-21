// Package diagnostic defines structured, opt-in workflow debugging events.
package diagnostic

import "time"

// Phase identifies one operation in the workflow lifecycle.
type Phase string

const (
	PhaseInvocation     Phase = "invocation"
	PhaseDiscovery      Phase = "discovery"
	PhaseSelection      Phase = "selection"
	PhaseLoad           Phase = "load"
	PhaseDecode         Phase = "decode"
	PhaseRequire        Phase = "require"
	PhaseValues         Phase = "values"
	PhaseActionResolve  Phase = "action resolve"
	PhaseActionFetch    Phase = "action fetch"
	PhaseActionChecksum Phase = "action checksum"
	PhaseActionDecode   Phase = "action decode"
	PhaseWorkflowFetch  Phase = "workflow fetch"
	PhaseValidation     Phase = "validation"
	PhaseCondition      Phase = "condition"
	PhaseRender         Phase = "render"
	PhaseRunner         Phase = "runner"
	PhaseActionInputs   Phase = "action inputs"
	PhaseActionOutputs  Phase = "action outputs"
	PhaseAttempt        Phase = "attempt"
	PhaseRetry          Phase = "retry"
	PhasePoll           Phase = "poll"
	PhaseConcurrent     Phase = "concurrent"
	PhaseCommit         Phase = "commit"
)

// Status describes the lifecycle state of a diagnostic event.
type Status string

const (
	StatusStarted   Status = "started"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusSkipped   Status = "skipped"
	StatusDetail    Status = "detail"
)

// Location identifies a logical YAML source position. Source is a local path or sanitized remote
// locator; it is deliberately independent of temporary materialization paths.
type Location struct {
	Source string
	Line   int
	Column int
}

// Attribute is a preformatted, safe diagnostic value.
type Attribute struct {
	Key   string
	Value string
}

// Event is one synchronous diagnostic record.
type Event struct {
	Phase        Phase
	Status       Status
	Time         time.Time
	Duration     time.Duration
	Depth        int
	WorkflowName string
	StepID       string
	StepType     string
	Location     Location
	Message      string
	Attributes   []Attribute
	Error        error
}

// Reporter receives diagnostic events synchronously. Callers that retain events must copy them.
type Reporter func(Event)

// Emit delivers an event when reporting is enabled and supplies a timestamp when absent.
func Emit(reporter Reporter, event Event) {
	if reporter == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	reporter(event)
}

// Attr constructs a string-valued diagnostic attribute.
func Attr(key, value string) Attribute { return Attribute{Key: key, Value: value} }
