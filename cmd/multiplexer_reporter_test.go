package cmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
)

type fakeTitleSink struct {
	mu     sync.Mutex
	titles []string
	wrote  chan string
}

func (sink *fakeTitleSink) SetTitle(title string) error {
	sink.mu.Lock()
	sink.titles = append(sink.titles, title)
	sink.mu.Unlock()
	if sink.wrote != nil {
		sink.wrote <- title
	}
	return nil
}

type fakeReporterTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func (ticker *fakeReporterTicker) C() <-chan time.Time { return ticker.ticks }
func (ticker *fakeReporterTicker) Stop()               { ticker.once.Do(func() { close(ticker.stopped) }) }

func TestMultiplexerReporterAnimatesProgressAndLeavesFinalStatus(t *testing.T) {
	sink := &fakeTitleSink{wrote: make(chan string, 8)}
	ticker := &fakeReporterTicker{ticks: make(chan time.Time, 1), stopped: make(chan struct{})}
	reporter := newMultiplexerProgressReporterWithSink(sink, func(time.Duration) reporterTicker { return ticker })

	reporter.Progress(engine.ProgressEvent{Kind: engine.WorkflowStarted, WorkflowName: "release", Total: 8})
	if title := <-sink.wrote; title != "⠋ release · 0/8" {
		t.Fatalf("initial title = %q", title)
	}
	reporter.Progress(engine.ProgressEvent{Kind: engine.StepStarted, WorkflowName: "release", StepID: "test", Index: 3, Total: 8})
	ticker.ticks <- time.Now()
	if title := <-sink.wrote; title != "⠙ release · 3/8 · test" {
		t.Fatalf("animated title = %q", title)
	}
	reporter.Progress(engine.ProgressEvent{Kind: engine.WorkflowFinished, WorkflowName: "release", Status: engine.StatusSucceeded})
	if title := <-sink.wrote; title != "✓ release" {
		t.Fatalf("final title = %q", title)
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("ticker was not stopped")
	}
	if err := reporter.Finish(t.Context(), reporterpkg.Outcome{WorkflowName: "release", Status: engine.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-sink.wrote:
		t.Fatalf("duplicate final title = %q", duplicate)
	default:
	}
}

func TestMultiplexerReporterFormatsRetryPollAndDryRun(t *testing.T) {
	tests := []struct {
		event engine.ProgressEvent
		want  string
	}{
		{event: engine.ProgressEvent{Kind: engine.RetryScheduled, StepID: "test", Attempt: 2, MaxAttempts: 3}, want: "test retry 2/3"},
		{event: engine.ProgressEvent{Kind: engine.PollScheduled, StepID: "wait", Poll: 4}, want: "wait poll 4"},
		{event: engine.ProgressEvent{Kind: engine.ControlStarted, StepID: "matrix"}, want: "matrix"},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			sink := &fakeTitleSink{wrote: make(chan string, 8)}
			ticker := &fakeReporterTicker{ticks: make(chan time.Time, 1), stopped: make(chan struct{})}
			reporter := newMultiplexerProgressReporterWithSink(sink, func(time.Duration) reporterTicker { return ticker })
			reporter.Progress(engine.ProgressEvent{Kind: engine.WorkflowStarted, WorkflowName: "release", Total: 5})
			<-sink.wrote
			reporter.Progress(test.event)
			ticker.ticks <- time.Now()
			if title := <-sink.wrote; !strings.Contains(title, test.want) {
				t.Fatalf("title = %q, want %q", title, test.want)
			}
			reporter.stopRun("")
		})
	}

	sink := &fakeTitleSink{wrote: make(chan string, 1)}
	reporter := newMultiplexerProgressReporterWithSink(sink, func(time.Duration) reporterTicker {
		return &fakeReporterTicker{ticks: make(chan time.Time), stopped: make(chan struct{})}
	})
	if err := reporter.Finish(context.Background(), reporterpkg.Outcome{WorkflowName: "release", Status: engine.StatusSucceeded, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if title := <-sink.wrote; title != "◇ release · dry run" {
		t.Fatalf("dry-run title = %q", title)
	}
}

func TestSanitizeMultiplexerTitleRemovesControlsAndLimitsRunes(t *testing.T) {
	value := sanitizeMultiplexerTitle("  bad\x1b\n" + strings.Repeat("界", 100))
	if strings.ContainsAny(value, "\x1b\n") {
		t.Fatalf("title contains controls: %q", value)
	}
	if length := len([]rune(value)); length != multiplexerTitleLimit {
		t.Fatalf("title length = %d", length)
	}
}
