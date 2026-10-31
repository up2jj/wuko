package print

import (
	"bytes"
	"context"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/step"
)

func TestPrintWritesToStderrWithoutFailingOnErrorLevel(t *testing.T) {
	t.Parallel()
	runner := steptest.Build(t, New, map[string]any{"message": "one\ntwo", "level": "error"})
	var stderr bytes.Buffer
	_, err := runner.Run(t.Context(), step.Request{Stderr: &stderr})
	if err != nil || stderr.String() != "✗ one\n✗ two\n" {
		t.Fatalf("output=%q error=%v", stderr.String(), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stderr.Reset()
	if _, err := runner.Run(ctx, step.Request{Stderr: &stderr}); err != context.Canceled || stderr.Len() != 0 {
		t.Fatalf("canceled output=%q error=%v", stderr.String(), err)
	}
}

func TestPrintRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	for _, raw := range []map[string]any{{}, {"message": " "}, {"message": "hi", "level": "debug"}, {"message": "hi", "unknown": true}} {
		if _, err := New(raw); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
}
