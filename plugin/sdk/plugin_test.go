package sdk

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRegisteredStepRunProvidesStreamsAndSafeReadiness(t *testing.T) {
	plugin, err := New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterStep(plugin, "work", StepHandler[struct{}]{
		Run: func(_ context.Context, request RunRequest[struct{}]) (Result, error) {
			if request.Stdout == nil || request.Stderr == nil {
				return Result{}, io.ErrClosedPipe
			}
			if _, err := request.Stdout.Write([]byte("stdout")); err != nil {
				return Result{}, err
			}
			if _, err := request.Stderr.Write([]byte("stderr")); err != nil {
				return Result{}, err
			}
			return Result{}, request.Ready(Result{})
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = plugin.steps["work"].run(t.Context(), nil, StepContext{}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "readiness is unavailable") {
		t.Fatalf("run error = %v", err)
	}
}
