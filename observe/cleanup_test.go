package observe

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/fswatch"
	"github.com/up2jj/wuko/workflow"
)

type scriptedSource struct{ events chan any }

func (*scriptedSource) Initial() any { return nil }

func (source *scriptedSource) Next(ctx context.Context) (any, error) {
	select {
	case event := <-source.events:
		return event, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*scriptedSource) NewBatch() Batch          { return &testBatch{} }
func (*scriptedSource) Metadata() map[string]any { return map[string]any{} }
func (*scriptedSource) Close() error             { return nil }

// A zero debounce runs the body as soon as the loop reaches the select again. It is served by a
// closed channel rather than a freshly allocated one, so pin that every observation still lands.
func TestZeroDebounceRunsBodyForEveryObservation(t *testing.T) {
	source := &scriptedSource{events: make(chan any)}
	scheduler := Scheduler{Source: source, SourceType: "test", Debounce: 0, OnChange: workflow.ObserveQueue}
	bodies := make(chan map[string]any, 8)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Run(ctx, engine.BackgroundControlRuntime{
			RunIteration: func(_ context.Context, binding map[string]any) error {
				bodies <- binding
				return nil
			},
			Report: func(engine.BackgroundControlEvent) {},
		})
		done <- err
	}()

	receive := func() map[string]any {
		t.Helper()
		select {
		case binding := <-bodies:
			return binding
		case <-time.After(10 * time.Second):
			t.Fatal("body did not run")
			return nil
		}
	}

	if binding := receive(); binding["initial"] != true || binding["iteration"] != 1 {
		t.Fatalf("initial binding = %#v", binding)
	}
	for run := 2; run <= 4; run++ {
		select {
		case source.events <- map[string]any{"run": run}:
		case <-time.After(10 * time.Second):
			t.Fatal("scheduler stopped reading the source")
		}
		binding := receive()
		if binding["initial"] != false || binding["iteration"] != run {
			t.Fatalf("binding = %#v, want iteration %d", binding, run)
		}
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Run returned no error after cancellation")
	}
}

// Merge now adopts an unseen path's operation set instead of rebuilding it one change at a
// time, so check the union it produces is still the union.
func TestFilesystemBatchMergeUnionsOperations(t *testing.T) {
	older := &filesystemBatch{changes: make(map[string]map[string]bool)}
	newer := &filesystemBatch{changes: make(map[string]map[string]bool)}
	older.Add(fswatch.Change{Path: "a.go", Operations: []string{"create"}})
	older.Add(fswatch.Change{Path: "c.go", Operations: []string{"remove"}})
	newer.Add(fswatch.Change{Path: "a.go", Operations: []string{"modify"}})
	newer.Add(fswatch.Change{Path: "b.go", Operations: []string{"remove", "create"}})
	older.Merge(newer)

	binding := older.Binding()["filesystem"].(map[string]any)
	if paths := binding["paths"].([]any); !reflect.DeepEqual(paths, []any{"a.go", "b.go", "c.go"}) {
		t.Fatalf("paths = %#v", paths)
	}
	want := []any{
		// Operations come back in canonical event order, not insertion order.
		map[string]any{"path": "a.go", "operations": []any{"create", "modify"}},
		map[string]any{"path": "b.go", "operations": []any{"create", "remove"}},
		map[string]any{"path": "c.go", "operations": []any{"remove"}},
	}
	if changes := binding["changes"].([]any); !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
	if older.Empty() {
		t.Fatal("merged batch reports empty")
	}
}
