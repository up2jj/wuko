package engine

import (
	"strconv"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

func trace(options Options, event diagnostic.Event) {
	if options.Diagnostics == nil {
		return
	}
	if event.Depth == 0 {
		event.Depth = options.depth
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if options.runtime != nil {
		options.runtime.reportMu.Lock()
		defer options.runtime.reportMu.Unlock()
	}
	options.Diagnostics(event)
}

func traceStep(options Options, definition *workflow.Definition, workflowStep workflow.Step, phase diagnostic.Phase, status diagnostic.Status, started time.Time, message string, err error, attributes ...diagnostic.Attribute) {
	attributes = append(attributes, diagnostic.ErrorAttributes(err)...)
	event := diagnostic.Event{
		Phase: phase, Status: status, Time: time.Now(), WorkflowName: definition.Name,
		StepID: workflowStep.ID, StepType: executionKind(workflowStep), Location: workflowStep.Location,
		Message: message, Error: err, Attributes: attributes,
	}
	if !started.IsZero() {
		event.Duration = time.Since(started)
	}
	trace(options, event)
}

func attemptAttr(attempt, maximum int) diagnostic.Attribute {
	return diagnostic.Attr("attempt", strconv.Itoa(attempt)+"/"+strconv.Itoa(maximum))
}
