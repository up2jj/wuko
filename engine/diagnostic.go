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
	if options.secretSession != nil {
		event.Message = options.secretSession.Redact(event.Message)
		event.Error = options.secretSession.RedactError(event.Error)
		for i := range event.Attributes {
			event.Attributes[i].Value = options.secretSession.Redact(event.Attributes[i].Value)
		}
	}
	event.InvocationID = options.InvocationID
	event.RunID = options.runID
	event.ParentRunID = options.parentRunID
	event.ParentStepRunID = options.parentStepRunID
	event.StepRunID = options.stepRunID
	if options.runtime != nil {
		options.runtime.reportMu.Lock()
		defer options.runtime.reportMu.Unlock()
	}
	options.Diagnostics(event)
}

func traceStep(options Options, definition *workflow.Definition, workflowStep workflow.Step, phase diagnostic.Phase, status diagnostic.Status, started time.Time, message string, err error, attributes ...diagnostic.Attribute) {
	attributes = append(attributes, diagnostic.ErrorAttributes(err)...)
	if session := definition.SecretSession(); session != nil {
		err = session.RedactError(err)
		for i := range attributes {
			attributes[i].Value = session.Redact(attributes[i].Value)
		}
	}
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
