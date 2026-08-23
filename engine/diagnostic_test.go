package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

type attributedTestError struct {
	attributes []diagnostic.Attribute
}

func (attributedTestError) Error() string { return "attributed failure" }

func (err attributedTestError) DiagnosticAttributes() []diagnostic.Attribute {
	return err.attributes
}

func TestTraceStepIncludesWrappedErrorAttributes(t *testing.T) {
	base := attributedTestError{attributes: []diagnostic.Attribute{diagnostic.Attr("health_status", "unhealthy")}}
	wrapped := fmt.Errorf("step failed: %w", base)
	var event diagnostic.Event
	traceStep(Options{Diagnostics: func(reported diagnostic.Event) { event = reported }}, &workflow.Definition{Name: "check"}, workflow.Step{ID: "ready", Type: "docker"}, diagnostic.PhaseAttempt, diagnostic.StatusFailed, time.Time{}, "", wrapped)

	if len(event.Attributes) != 1 || event.Attributes[0].Key != "health_status" || event.Attributes[0].Value != "unhealthy" {
		t.Fatalf("attributes = %#v", event.Attributes)
	}
}
