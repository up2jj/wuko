package engine

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	storepkg "github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func onceTestRegistry(t *testing.T, runs *atomic.Int64, started chan<- struct{}, release <-chan struct{}, fail *atomic.Bool) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	if err := registry.Register("once_test", func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
			runs.Add(1)
			if started != nil {
				started <- struct{}{}
			}
			if release != nil {
				select {
				case <-release:
				case <-ctx.Done():
					return step.Result{}, ctx.Err()
				}
			}
			if fail != nil && fail.Load() {
				return step.Result{}, errors.New("migration failed")
			}
			return step.Result{
				Outputs:   map[string]any{"value": raw["value"]},
				Variables: map[string]any{"migration_result": raw["value"]},
			}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func onceDefinition(key, onBusy string) *workflow.Definition {
	return &workflow.Definition{
		Version: 1, Name: "migration", Vars: map[string]any{"version": key},
		Steps: []workflow.Step{{
			ID: "migrate",
			Once: &workflow.OnceGroup{
				Key: "schema-{{ .vars.version }}", Scope: "local", OnBusy: onBusy,
				Steps: []workflow.Step{{ID: "apply", Type: "once_test", With: map[string]any{"value": "done"}}},
			},
		}},
	}
}

func onceOptions(root string) Options {
	return Options{
		RunDir: root, LocalValueDir: filepath.Join(root, ".wuko", "values"),
		GlobalValueDir: filepath.Join(root, "global"), Stdout: io.Discard, Stderr: io.Discard,
	}
}

func TestOnceExecutesThenReplaysOutputsAndVariables(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	engine := New(onceTestRegistry(t, &runs, nil, nil, nil))
	root := t.TempDir()
	options := onceOptions(root)
	definition := onceDefinition("v1", workflow.OnceBusyError)

	first, err := engine.Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	assertOnceOutcome(t, first, StatusSucceeded)
	second, err := engine.Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	assertOnceOutcome(t, second, StatusSkipped)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}

	definition.Vars["version"] = "v2"
	third, err := engine.Run(t.Context(), definition, options)
	if err != nil {
		t.Fatal(err)
	}
	assertOnceOutcome(t, third, StatusSucceeded)
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs after key change = %d, want 2", got)
	}
}

func assertOnceOutcome(t *testing.T, state *State, status ExecutionStatus) {
	t.Helper()
	if len(state.Stats.Steps) != 1 || state.Stats.Steps[0].Status != status {
		t.Fatalf("stats = %#v, want %s", state.Stats.Steps, status)
	}
	if _, leaked := state.Steps["apply"]; leaked {
		t.Fatal("private child output leaked into outer steps")
	}
	outcome := state.Steps["migrate"].(map[string]any)
	record := outcome["steps"].(map[string]any)["apply"].(map[string]any)
	if record["status"] != "succeeded" || record["error"] != nil || record["outputs"].(map[string]any)["value"] != "done" {
		t.Fatalf("recorded child = %#v", record)
	}
	if outcome["vars"].(map[string]any)["migration_result"] != "done" || state.Vars["migration_result"] != "done" {
		t.Fatalf("outcome vars = %#v, state vars = %#v", outcome["vars"], state.Vars)
	}
}

func TestOnceFailureIsNotRecorded(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	engine := New(onceTestRegistry(t, &runs, nil, nil, &fail))
	root := t.TempDir()
	definition := onceDefinition("v1", workflow.OnceBusyError)
	if _, err := engine.Run(t.Context(), definition, onceOptions(root)); err == nil || !strings.Contains(err.Error(), "migration failed") {
		t.Fatalf("first run error = %v", err)
	}
	fail.Store(false)
	state, err := engine.Run(t.Context(), definition, onceOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	assertOnceOutcome(t, state, StatusSucceeded)
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2", got)
	}
}

func TestOnceDefaultBusyPolicyErrors(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	engine := New(onceTestRegistry(t, &runs, started, release, nil))
	root := t.TempDir()
	definition := onceDefinition("v1", workflow.OnceBusyError)
	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.Run(t.Context(), definition, onceOptions(root))
		firstDone <- err
	}()
	<-started
	if _, err := engine.Run(t.Context(), definition, onceOptions(root)); err == nil || !strings.Contains(err.Error(), "is busy") {
		t.Fatalf("contended run error = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}

func TestOnceWaitsThenReplays(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	engine := New(onceTestRegistry(t, &runs, started, release, nil))
	root := t.TempDir()
	definition := onceDefinition("v1", workflow.OnceBusyWait)
	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.Run(t.Context(), definition, onceOptions(root))
		firstDone <- err
	}()
	<-started
	secondDone := make(chan struct {
		state *State
		err   error
	}, 1)
	go func() {
		state, err := engine.Run(t.Context(), definition, onceOptions(root))
		secondDone <- struct {
			state *State
			err   error
		}{state, err}
	}()
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	second := <-secondDone
	if second.err != nil {
		t.Fatal(second.err)
	}
	assertOnceOutcome(t, second.state, StatusSkipped)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
}

func TestOnceFalseConditionCreatesNoStore(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	engine := New(onceTestRegistry(t, &runs, nil, nil, nil))
	root := t.TempDir()
	definition := onceDefinition("v1", workflow.OnceBusyError)
	definition.Steps[0].If = "false"
	state, err := engine.Run(t.Context(), definition, onceOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if state.Stats.Steps[0].Status != StatusSkipped || runs.Load() != 0 {
		t.Fatalf("state = %#v, runs = %d", state.Stats.Steps, runs.Load())
	}
	if _, err := filepath.Glob(filepath.Join(root, ".wuko", "values", "once*")); err != nil {
		t.Fatal(err)
	} else if matches, _ := filepath.Glob(filepath.Join(root, ".wuko", "values", "once*")); len(matches) != 0 {
		t.Fatalf("once state created: %v", matches)
	}
}

func TestOnceWaiterTakesOverAfterOwnerFailure(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	registry := step.NewRegistry()
	if err := registry.Register("once_test", func(raw map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) {
			run := runs.Add(1)
			started <- struct{}{}
			<-release
			if run == 1 {
				return step.Result{}, errors.New("owner failed")
			}
			return step.Result{Outputs: map[string]any{"value": raw["value"]}}, nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	engine := New(registry)
	root := t.TempDir()
	definition := onceDefinition("v1", workflow.OnceBusyWait)
	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.Run(t.Context(), definition, onceOptions(root))
		firstDone <- err
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		_, err := engine.Run(t.Context(), definition, onceOptions(root))
		secondDone <- err
	}()
	close(release)
	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "owner failed") {
		t.Fatalf("owner error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2", got)
	}
}

func TestOnceRejectsRecursiveClaim(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	engine := New(onceTestRegistry(t, &runs, nil, nil, nil))
	root := t.TempDir()
	inner := workflow.Step{ID: "inner", Once: &workflow.OnceGroup{
		Key: "same", Scope: "local", OnBusy: workflow.OnceBusyWait,
		Steps: []workflow.Step{{ID: "apply", Type: "once_test", With: map[string]any{"value": "done"}}},
	}}
	definition := &workflow.Definition{Version: 1, Name: "recursive", Steps: []workflow.Step{{
		ID: "outer", Once: &workflow.OnceGroup{
			Key: "same", Scope: "local", OnBusy: workflow.OnceBusyWait, Steps: []workflow.Step{inner},
		},
	}}}
	if _, err := engine.Run(t.Context(), definition, onceOptions(root)); err == nil || !strings.Contains(err.Error(), "recursive claim") {
		t.Fatalf("recursive run error = %v", err)
	}
	if runs.Load() != 0 {
		t.Fatalf("leaf runs = %d, want 0", runs.Load())
	}
}

func TestOnceRejectsMalformedRecord(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	engine := New(onceTestRegistry(t, &runs, nil, nil, nil))
	root := t.TempDir()
	options := onceOptions(root)
	store, err := storepkg.Open(options.LocalValueDir, onceStoreName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(t.Context(), "schema-v1", map[string]any{"version": "unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Run(t.Context(), onceDefinition("v1", workflow.OnceBusyError), options); err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("malformed record error = %v", err)
	}
	if runs.Load() != 0 {
		t.Fatalf("runs = %d, want 0", runs.Load())
	}
}

func TestOnceDryRunShowsBlockWithoutCreatingState(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	root := t.TempDir()
	options := onceOptions(root)
	options.DryRun = true
	var output strings.Builder
	options.Stdout = &output
	if _, err := New(onceTestRegistry(t, &runs, nil, nil, nil)).Run(t.Context(), onceDefinition("v1", workflow.OnceBusyError), options); err != nil {
		t.Fatal(err)
	}
	want := "1. migrate (once schema-{{ .vars.version }}; local; on busy error)\n   1.1 apply (once_test)\n"
	if output.String() != want {
		t.Fatalf("dry-run output = %q, want %q", output.String(), want)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".wuko", "values", "once*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 || runs.Load() != 0 {
		t.Fatalf("state = %v, runs = %d", matches, runs.Load())
	}
}

func TestOnceRejectsRecursiveClaimAcrossAction(t *testing.T) {
	t.Parallel()
	var runs atomic.Int64
	engine := New(onceTestRegistry(t, &runs, nil, nil, nil))
	root := t.TempDir()
	action := &workflow.Action{Version: 1, Name: "inner", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "nested", Once: &workflow.OnceGroup{
			Key: "same", Scope: "local", OnBusy: workflow.OnceBusyWait,
			Steps: []workflow.Step{{ID: "apply", Type: "once_test", With: map[string]any{"value": "done"}}},
		},
	}}}
	definition := &workflow.Definition{Version: 1, Name: "recursive", Dir: t.TempDir(), Steps: []workflow.Step{{
		ID: "outer", Once: &workflow.OnceGroup{
			Key: "same", Scope: "local", OnBusy: workflow.OnceBusyWait,
			Steps: []workflow.Step{{ID: "call", Uses: workflow.ActionSource{Path: "./inner"}, Action: action, With: map[string]any{}}},
		},
	}}}
	// The claim ledger must cross the uses boundary: the flock is held per open file
	// description, so without it the second claim blocks against this process forever.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := engine.Run(ctx, definition, onceOptions(root)); err == nil || !strings.Contains(err.Error(), "recursive claim") {
		t.Fatalf("recursive run error = %v", err)
	}
	if runs.Load() != 0 {
		t.Fatalf("leaf runs = %d, want 0", runs.Load())
	}
}
