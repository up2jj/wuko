package workflow

import (
	"strconv"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/secret"
)

func secretDiagnostics(reporter diagnostic.Reporter, session *secret.Session) diagnostic.Reporter {
	if reporter == nil || session == nil {
		return reporter
	}
	return func(event diagnostic.Event) {
		event.Message = session.Redact(event.Message)
		event.Error = session.RedactError(event.Error)
		for i := range event.Attributes {
			event.Attributes[i].Value = session.Redact(event.Attributes[i].Value)
		}
		reporter(event)
	}
}

func traceStart(reporter diagnostic.Reporter, phase diagnostic.Phase, location diagnostic.Location, workflowName, stepID, stepType, message string, attributes ...diagnostic.Attribute) time.Time {
	started := time.Now()
	diagnostic.Emit(reporter, diagnostic.Event{
		Phase: phase, Status: diagnostic.StatusStarted, Time: started, Location: location,
		WorkflowName: workflowName, StepID: stepID, StepType: stepType, Message: message, Attributes: attributes,
	})
	return started
}

func traceFinish(reporter diagnostic.Reporter, started time.Time, phase diagnostic.Phase, status diagnostic.Status, location diagnostic.Location, workflowName, stepID, stepType, message string, err error, attributes ...diagnostic.Attribute) {
	diagnostic.Emit(reporter, diagnostic.Event{
		Phase: phase, Status: status, Time: time.Now(), Duration: time.Since(started), Location: location,
		WorkflowName: workflowName, StepID: stepID, StepType: stepType, Message: message, Error: err, Attributes: attributes,
	})
}

func countAttr(key string, value int) diagnostic.Attribute {
	return diagnostic.Attr(key, strconv.Itoa(value))
}
