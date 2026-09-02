package observe_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/fswatch"
	"github.com/up2jj/wuko/observe"
	"github.com/up2jj/wuko/step"
	tempstep "github.com/up2jj/wuko/steps/temp"
	"github.com/up2jj/wuko/workflow"
)

func TestObserveLaunchesInBackgroundAndExplicitReturnStopsIt(t *testing.T) {
	source := newFilesystemTestSource()
	foregroundRan := false
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
		"foreground": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				foregroundRan = true
				return step.Result{}, nil
			}), nil
		},
	})
	definition := testDefinition(t, "background", observeControl("dev", t.TempDir(), []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}}))
	definition.Steps = append(definition.Steps,
		workflow.Step{ID: "foreground", Type: "foreground", With: map[string]any{}},
		workflow.Step{Return: &workflow.ReturnControl{Outputs: map[string]string{}}},
	)
	state, err := engine.New(registry, withFilesystemTestSource(source)).Run(t.Context(), definition, engine.Options{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !foregroundRan {
		t.Fatal("foreground step did not run")
	}
	launch := state.Steps["dev"].(map[string]any)
	if launch["status"] != "observing" || launch["on_change"] != "restart" {
		t.Fatalf("launch = %#v", launch)
	}
	if !source.isClosed() {
		t.Fatal("filesystem observe source was not closed")
	}
}

func TestObserveRerunsWithDeterministicBinding(t *testing.T) {
	root := t.TempDir()
	source := newFilesystemTestSource()
	bindings := make(chan map[string]any, 4)
	releaseInitial := make(chan struct{})
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				binding := workflow.Clone(request.Bindings["observe"]).(map[string]any)
				bindings <- binding
				if binding["initial"] == true {
					// Deliberately outlives its cancellation: the rerun must not start until
					// both events have been coalesced into the queued batch.
					<-releaseInitial
				}
				return step.Result{}, nil
			}), nil
		},
	})
	control := observeControl("dev", root, []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}})
	control.Observe.Debounce = workflow.Duration(time.Millisecond)
	definition := testDefinition(t, "rerun", control)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	triggers := make(chan string, 4)
	go func() {
		_, err := engine.New(registry, withFilesystemTestSource(source)).Run(ctx, definition, engine.Options{
			RunDir: root,
			Progress: func(event engine.ProgressEvent) {
				if event.Kind == engine.BackgroundTriggered {
					triggers <- event.Action
				}
			},
		})
		done <- err
	}()
	initial := <-bindings
	if initial["initial"] != true || initial["iteration"] != 1 {
		t.Fatalf("initial binding = %#v", initial)
	}
	// Coalescing is what this asserts, so both events have to reach the scheduler before the
	// rerun starts. Holding the first body open and waiting for each event to be handled makes
	// that ordering explicit; racing two sends against a 1ms debounce did not, and lost
	// whenever the machine was busy enough to separate them.
	for _, name := range []string{"b.go", "a.go"} {
		source.events <- fsnotify.Event{Name: filepath.Join(root, name), Op: fsnotify.Write}
		receiveObserveTest(t, triggers)
	}
	close(releaseInitial)
	next := <-bindings
	filesystem := next["filesystem"].(map[string]any)
	paths := filesystem["paths"].([]any)
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Fatalf("binding = %#v", next)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
}

func TestObserveFailureCancelsForeground(t *testing.T) {
	root := t.TempDir()
	source := newFilesystemTestSource()
	foregroundStarted := make(chan struct{})
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
		},
		"block": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				close(foregroundStarted)
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := testDefinition(t, "failure", observeControl("dev", root, []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}}))
	definition.Steps = append(definition.Steps, workflow.Step{ID: "foreground", Type: "block", With: map[string]any{}})
	done := make(chan error, 1)
	go func() {
		_, err := engine.New(registry, withFilesystemTestSource(source)).Run(t.Context(), definition, engine.Options{RunDir: root})
		done <- err
	}()
	<-foregroundStarted
	source.errs <- errors.New("watch overflow")
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "watch overflow") {
		t.Fatalf("run error = %v", err)
	}
}

func TestObserveHTTPSourceFeedsGenericBinding(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: engineRoundTripFunc(func(*http.Request) (*http.Response, error) {
		version := requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"version":%d}`, version))),
		}, nil
	})}
	bindings := make(chan map[string]any, 3)
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				bindings <- workflow.Clone(request.Bindings["observe"]).(map[string]any)
				return step.Result{}, nil
			}), nil
		},
	})
	control := workflow.Step{ID: "api", Observe: &workflow.ObserveGroup{
		Source: workflow.ObserveSource{Type: "http", With: map[string]any{
			"every": "1ms", "trigger": "always",
			"request": map[string]any{"url": "https://example.test/status", "response": "json"},
		}},
		Debounce: workflow.Duration(time.Millisecond),
		Steps:    []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	sources := observe.NewRegistry(observe.HTTPBuilder{Client: client})
	go func() {
		_, err := engine.New(registry, engine.WithBackgroundControl(observe.NewControl(sources))).Run(ctx, testDefinition(t, "http-observe", control), engine.Options{RunDir: t.TempDir()})
		done <- err
	}()
	initial := receiveObserveTest(t, bindings)
	if initial["source"] != "http" || initial["initial"] != true {
		t.Fatalf("initial binding = %#v", initial)
	}
	next := receiveObserveTest(t, bindings)
	response := next["http"].(map[string]any)
	if response["status"] != http.StatusOK || next["initial"] != false {
		t.Fatalf("next binding = %#v", next)
	}
	cancel()
	if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
}

func TestObserveChangePolicies(t *testing.T) {
	for _, policy := range []string{workflow.ObserveRestart, workflow.ObserveQueue, workflow.ObserveSkip} {
		t.Run(policy, func(t *testing.T) {
			root := t.TempDir()
			source := newFilesystemTestSource()
			started := make(chan int, 4)
			releaseInitial := make(chan struct{})
			actions := make(chan string, 4)
			finished := make(chan int, 4)
			var runs atomic.Int32
			var active atomic.Int32
			var overlapped atomic.Bool
			registry := newTestRegistry(t, map[string]step.Builder{
				"body": func(map[string]any) (step.Runner, error) {
					return observeRunnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
						run := int(runs.Add(1))
						if active.Add(1) != 1 {
							overlapped.Store(true)
						}
						defer active.Add(-1)
						started <- run
						if run == 1 {
							select {
							case <-releaseInitial:
							case <-ctx.Done():
								return step.Result{}, ctx.Err()
							}
						}
						return step.Result{}, nil
					}), nil
				},
			})
			control := observeControl("dev", root, []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}})
			control.Observe.Debounce = workflow.Duration(time.Millisecond)
			control.Observe.OnChange = policy
			definition := testDefinition(t, "policy", control)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := engine.New(registry, withFilesystemTestSource(source)).Run(ctx, definition, engine.Options{
					RunDir: root,
					Progress: func(event engine.ProgressEvent) {
						if event.Kind == engine.BackgroundTriggered {
							actions <- event.Action
						}
						if event.Kind == engine.IterationFinished {
							finished <- event.Iteration
						}
					},
				})
				done <- err
			}()

			if run := receiveObserveTest(t, started); run != 1 {
				t.Fatalf("initial run = %d", run)
			}
			source.events <- fsnotify.Event{Name: filepath.Join(root, "first.go"), Op: fsnotify.Write}
			if action := receiveObserveTest(t, actions); action != policy {
				t.Fatalf("action = %q, want %q", action, policy)
			}

			switch policy {
			case workflow.ObserveRestart:
				if run := receiveObserveTest(t, started); run != 2 {
					t.Fatalf("replacement run = %d", run)
				}
			case workflow.ObserveQueue:
				close(releaseInitial)
				if run := receiveObserveTest(t, started); run != 2 {
					t.Fatalf("queued run = %d", run)
				}
			case workflow.ObserveSkip:
				close(releaseInitial)
				if iteration := receiveObserveTest(t, finished); iteration != 0 {
					t.Fatalf("finished iteration = %d", iteration)
				}
				source.events <- fsnotify.Event{Name: filepath.Join(root, "second.go"), Op: fsnotify.Write}
				if run := receiveObserveTest(t, started); run != 2 {
					t.Fatalf("post-idle run = %d", run)
				}
			}

			cancel()
			if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("run error = %v", err)
			}
			if overlapped.Load() {
				t.Fatal("observe bodies overlapped")
			}
		})
	}
}

func receiveObserveTest[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for observe test event")
		var zero T
		return zero
	}
}

func testDefinition(t *testing.T, name string, steps ...workflow.Step) *workflow.Definition {
	t.Helper()
	return &workflow.Definition{Version: 1, Name: name, Dir: t.TempDir(), Steps: steps}
}

func newTestRegistry(t *testing.T, builders map[string]step.Builder) *step.Registry {
	t.Helper()
	registry := step.NewRegistry()
	for name, builder := range builders {
		if err := registry.Register(name, builder); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func observeControl(id, root string, body []workflow.Step) workflow.Step {
	return workflow.Step{ID: id, Observe: &workflow.ObserveGroup{
		Source: workflow.ObserveSource{Type: "filesystem", With: map[string]any{"root": root, "paths": []any{"**/*.go"}}},
		Steps:  body,
	}}
}

func withFilesystemTestSource(source fswatch.Source) engine.Option {
	registry := observe.NewRegistry(observe.FilesystemBuilder{Factory: func() (fswatch.Source, error) { return source, nil }})
	return engine.WithBackgroundControl(observe.NewControl(registry))
}

type observeRunnerFunc func(context.Context, step.Request) (step.Result, error)

func (runner observeRunnerFunc) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return runner(ctx, request)
}

type filesystemTestSource struct {
	events chan fsnotify.Event
	errs   chan error
	mu     sync.Mutex
	closed bool
}

func newFilesystemTestSource() *filesystemTestSource {
	return &filesystemTestSource{events: make(chan fsnotify.Event, 16), errs: make(chan error, 4)}
}
func (source *filesystemTestSource) Add(string) error { return nil }
func (source *filesystemTestSource) Close() error {
	source.mu.Lock()
	source.closed = true
	source.mu.Unlock()
	return nil
}
func (source *filesystemTestSource) EventChannel() <-chan fsnotify.Event { return source.events }
func (source *filesystemTestSource) ErrorChannel() <-chan error          { return source.errs }
func (source *filesystemTestSource) isClosed() bool {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.closed
}

type engineRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip engineRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestObserveReleasesManagedResourcesEachIteration(t *testing.T) {
	root := t.TempDir()
	source := newFilesystemTestSource()
	paths := make(chan string, 4)
	registry := newTestRegistry(t, map[string]step.Builder{
		"record": func(raw map[string]any) (step.Runner, error) {
			path, _ := raw["path"].(string)
			return observeRunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				paths <- path
				return step.Result{}, nil
			}), nil
		},
	})
	if err := tempstep.Register(registry); err != nil {
		t.Fatal(err)
	}
	control := observeControl("dev", root, []workflow.Step{
		{ID: "workspace", Type: "temp", With: map[string]any{"kind": "directory"}},
		{ID: "record", Type: "record", With: map[string]any{"path": "{{ .steps.workspace.path }}"}},
	})
	control.Observe.Debounce = workflow.Duration(time.Millisecond)
	finished := make(chan int, 4)
	controlFinished := make(chan engine.ProgressEvent, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := engine.New(registry, withFilesystemTestSource(source)).Run(ctx, testDefinition(t, "managed", control), engine.Options{
			RunDir: root,
			Progress: func(event engine.ProgressEvent) {
				switch event.Kind {
				case engine.IterationFinished:
					finished <- event.Iteration
				case engine.ControlFinished:
					controlFinished <- event
				}
			},
		})
		done <- err
	}()

	first := receiveObserveTest(t, paths)
	receiveObserveTest(t, finished)
	if _, err := os.Lstat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace %q outlived its iteration: %v", first, err)
	}
	source.events <- fsnotify.Event{Name: filepath.Join(root, "a.go"), Op: fsnotify.Write}
	second := receiveObserveTest(t, paths)
	if second == first {
		t.Fatalf("second iteration reused workspace %q", first)
	}
	receiveObserveTest(t, finished)
	if _, err := os.Lstat(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace %q outlived its iteration: %v", second, err)
	}

	cancel()
	if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
	summary := receiveObserveTest(t, controlFinished)
	if summary.Iterations != 2 || summary.Succeeded != 2 {
		t.Fatalf("control summary reported %d/%d succeeded", summary.Succeeded, summary.Iterations)
	}
}

func TestObserveReportsIterationInterruptedByShutdown(t *testing.T) {
	root := t.TempDir()
	source := newFilesystemTestSource()
	running := make(chan struct{})
	registry := newTestRegistry(t, map[string]step.Builder{
		"block": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(ctx context.Context, _ step.Request) (step.Result, error) {
				close(running)
				<-ctx.Done()
				return step.Result{}, ctx.Err()
			}), nil
		},
	})
	definition := testDefinition(t, "shutdown", observeControl("dev", root, []workflow.Step{{ID: "body", Type: "block", With: map[string]any{}}}))
	finished := make(chan engine.ProgressEvent, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := engine.New(registry, withFilesystemTestSource(source)).Run(ctx, definition, engine.Options{
			RunDir: root,
			Progress: func(event engine.ProgressEvent) {
				if event.Kind == engine.IterationFinished {
					finished <- event
				}
			},
		})
		done <- err
	}()

	<-running
	cancel()
	if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
	event := receiveObserveTest(t, finished)
	if event.Iteration != 0 || event.Status != engine.StatusCanceled {
		t.Fatalf("iteration %d finished with status %q", event.Iteration, event.Status)
	}
}

func TestObserveNamedTemplateReferencesBinding(t *testing.T) {
	root := t.TempDir()
	source := newFilesystemTestSource()
	rendered := make(chan string, 2)
	registry := newTestRegistry(t, map[string]step.Builder{
		"record": func(raw map[string]any) (step.Runner, error) {
			value, _ := raw["value"].(string)
			return observeRunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				rendered <- value
				return step.Result{}, nil
			}), nil
		},
	})
	body := []workflow.Step{{ID: "body", Type: "record", With: map[string]any{"value": `{{ template "changes" . }}`}}}
	definition := testDefinition(t, "templates", observeControl("dev", root, body))
	definition.Templates = map[string]workflow.TemplateDefinition{"changes": {Inline: "{{ len .observe.filesystem.paths }} changed"}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := engine.New(registry, withFilesystemTestSource(source)).Run(ctx, definition, engine.Options{RunDir: root})
		done <- err
	}()

	select {
	case value := <-rendered:
		if value != "0 changed" {
			t.Fatalf("rendered = %q", value)
		}
	case err := <-done:
		t.Fatalf("run ended before the observe body ran: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the observe body")
	}
	cancel()
	if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
}

func TestObserveShellSourceUsesWorkflowEnvironment(t *testing.T) {
	bindings := make(chan map[string]any, 3)
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(_ context.Context, request step.Request) (step.Result, error) {
				bindings <- workflow.Clone(request.Bindings["observe"]).(map[string]any)
				return step.Result{}, nil
			}), nil
		},
	})
	control := workflow.Step{ID: "revision", Observe: &workflow.ObserveGroup{
		Source: workflow.ObserveSource{Type: "shell", With: map[string]any{
			"command": "sh", "args": []any{"-c", `printf '%s' "$GREETING"`},
			"every": "1ms", "trigger": "always",
		}},
		Debounce: workflow.Duration(time.Millisecond),
		Steps:    []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}},
	}}
	definition := testDefinition(t, "shell-observe", control)
	definition.Env = workflow.Environment{"GREETING": "hello"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := engine.New(registry, engine.WithBackgroundControl(observe.NewControl(nil))).Run(ctx, definition, engine.Options{RunDir: t.TempDir()})
		done <- err
	}()

	initial := receiveObserveTest(t, bindings)
	if initial["source"] != "shell" || initial["initial"] != true {
		t.Fatalf("initial binding = %#v", initial)
	}
	observation := initial["shell"].(map[string]any)
	if observation["value"] != "hello" || observation["exit_code"] != 0 {
		t.Fatalf("observation = %#v", observation)
	}
	if next := receiveObserveTest(t, bindings); next["initial"] != false {
		t.Fatalf("next binding = %#v", next)
	}
	cancel()
	if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
}

func TestObserveContinuesAfterToleratedSourceFailure(t *testing.T) {
	root := t.TempDir()
	source := newFilesystemTestSource()
	runs := make(chan int, 4)
	var started atomic.Int32
	registry := newTestRegistry(t, map[string]step.Builder{
		"body": func(map[string]any) (step.Runner, error) {
			return observeRunnerFunc(func(context.Context, step.Request) (step.Result, error) {
				runs <- int(started.Add(1))
				return step.Result{}, nil
			}), nil
		},
	})
	control := observeControl("dev", root, []workflow.Step{{ID: "body", Type: "body", With: map[string]any{}}})
	control.Observe.Debounce = workflow.Duration(time.Millisecond)
	control.Observe.OnError = workflow.ObserveContinue
	failures := make(chan engine.ProgressEvent, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := engine.New(registry, withFilesystemTestSource(source)).Run(ctx, testDefinition(t, "tolerant", control), engine.Options{
			RunDir: root,
			Progress: func(event engine.ProgressEvent) {
				if event.Kind == engine.BackgroundSourceFailed {
					failures <- event
				}
			},
		})
		done <- err
	}()

	if run := receiveObserveTest(t, runs); run != 1 {
		t.Fatalf("initial run = %d", run)
	}
	source.errs <- errors.New("watch overflow")
	failure := receiveObserveTest(t, failures)
	if failure.Error == nil || !strings.Contains(failure.Error.Error(), "watch overflow") || failure.Action != workflow.ObserveContinue {
		t.Fatalf("failure event = %#v", failure)
	}

	// Observation survives the failure: a later change still runs the body.
	source.events <- fsnotify.Event{Name: filepath.Join(root, "a.go"), Op: fsnotify.Write}
	if run := receiveObserveTest(t, runs); run != 2 {
		t.Fatalf("run after tolerated failure = %d", run)
	}
	cancel()
	if err := receiveObserveTest(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}
}
