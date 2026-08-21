package control

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestForeachBindings(t *testing.T) {
	iterations, err := Foreach([]string{"linux", "darwin"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Iteration{
		{Index: 0, Bindings: map[string]any{"foreach": map[string]any{"index": 0, "item": "linux"}}},
		{Index: 1, Bindings: map[string]any{"foreach": map[string]any{"index": 1, "item": "darwin"}}},
	}
	if !reflect.DeepEqual(iterations, want) {
		t.Fatalf("iterations = %#v, want %#v", iterations, want)
	}
	if _, err := Foreach(map[string]any{"a": 1}); err == nil {
		t.Fatal("expected map source error")
	}
}

func TestMatrixExpansionOrder(t *testing.T) {
	iterations, err := Matrix([]Axis{
		{Name: "os", Values: []any{"linux", "darwin"}},
		{Name: "go", Values: []any{"1.25", "1.26"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for _, iteration := range iterations {
		got = append(got, iteration.Bindings["matrix"].(map[string]any))
	}
	want := []map[string]any{
		{"os": "linux", "go": "1.25"}, {"os": "linux", "go": "1.26"},
		{"os": "darwin", "go": "1.25"}, {"os": "darwin", "go": "1.26"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matrix = %#v, want %#v", got, want)
	}
	empty, err := Matrix([]Axis{{Name: "os", Values: nil}})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty matrix = %#v, %v", empty, err)
	}
	if _, err := Matrix([]Axis{{Name: "os", Values: []any{"linux"}}, {Name: "os", Values: []any{"darwin"}}}); err == nil {
		t.Fatal("expected duplicate axis error")
	}
}

func TestRunBoundsConcurrencyAndPreservesOrder(t *testing.T) {
	iterations, err := Foreach([]int{0, 1, 2, 3, 4, 5})
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	var maximum atomic.Int32
	outcomes, err := Run(t.Context(), iterations, Policy{MaxConcurrency: 2, FailFast: true}, nil, func(ctx context.Context, iteration Iteration) (int, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		select {
		case <-time.After(time.Duration(len(iterations)-iteration.Index) * time.Millisecond):
			return iteration.Index, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if maximum.Load() > 2 {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
	for i, outcome := range outcomes {
		if outcome.Iteration.Index != i || outcome.Value != i {
			t.Fatalf("outcome %d = %#v", i, outcome)
		}
	}
}

func TestRunFailurePoliciesAndTimeout(t *testing.T) {
	iterations, err := Foreach([]int{0, 1, 2})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := Run(t.Context(), iterations, Policy{MaxConcurrency: 1, FailFast: false}, nil, func(_ context.Context, iteration Iteration) (int, error) {
		if iteration.Index != 1 {
			return iteration.Index, nil
		}
		return 0, errors.New("broken")
	})
	if err == nil || !outcomes[2].Started {
		t.Fatalf("collect-all outcomes = %#v, err = %v", outcomes, err)
	}

	_, err = Run(t.Context(), iterations, Policy{MaxConcurrency: 1, Timeout: time.Millisecond, FailFast: true}, nil, func(ctx context.Context, _ Iteration) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestRunFailFastDoesNotStartQueuedIteration(t *testing.T) {
	iterations, err := Foreach([]int{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := Run(t.Context(), iterations, Policy{MaxConcurrency: 1, FailFast: true}, nil, func(_ context.Context, iteration Iteration) (int, error) {
		if iteration.Index == 0 {
			return 0, errors.New("broken")
		}
		return iteration.Index, nil
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if outcomes[1].Started {
		t.Fatalf("queued iteration started after fail-fast cancellation: %#v", outcomes[1])
	}
}
