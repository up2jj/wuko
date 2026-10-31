package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/up2jj/wuko/cmd"
	"github.com/up2jj/wuko/useroutput"
)

func TestExitCodeDistinguishesCancellationFromFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "success", want: 0},
		{name: "failure", err: errors.New("workflow failed"), want: 1},
		{name: "plain failure", err: fmt.Errorf("workflow: %w", &useroutput.Failure{Message: "fix setup", Code: 2}), want: 2},
		{name: "plain and cleanup failure", err: errors.Join(&useroutput.Failure{Message: "fix setup", Code: 2}, errors.New("cleanup failed")), want: 1},
		{name: "plain and cancellation", err: errors.Join(&useroutput.Failure{Message: "fix setup", Code: 2}, context.Canceled), want: 130},
		{name: "cancellation", err: context.Canceled, want: 130},
		{name: "wrapped cancellation", err: fmt.Errorf("workflow stopped: %w", context.Canceled), want: 130},
		{name: "cleanup after cancellation", err: errors.Join(context.Canceled, errors.New("cleanup failed")), want: 130},
		{name: "forced shutdown", err: fmt.Errorf("signal: %w", cmd.ErrForcedShutdown), want: 130},
		{name: "deadline", err: context.DeadlineExceeded, want: 1},
		{name: "unrelated error text", err: errors.New("context canceled"), want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := exitCode(test.err); got != test.want {
				t.Fatalf("exitCode(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}
