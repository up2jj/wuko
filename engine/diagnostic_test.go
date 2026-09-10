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

func TestReturnDiagnosticIncludesDestination(t *testing.T) {
	definition := testDefinition(t, "picker-return", workflow.Step{
		Return: &workflow.ReturnControl{To: workflow.ReturnDestinationPicker, Outputs: map[string]string{}},
	})
	var returned diagnostic.Event
	_, err := New(newTestRegistry(t, nil)).Run(t.Context(), definition, Options{Diagnostics: func(event diagnostic.Event) {
		if event.Phase == diagnostic.PhaseControl && event.Status == diagnostic.StatusSucceeded {
			returned = event
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(returned.Attributes) != 2 || returned.Attributes[1].Key != "to" || returned.Attributes[1].Value != "picker" {
		t.Fatalf("attributes = %#v", returned.Attributes)
	}
}
