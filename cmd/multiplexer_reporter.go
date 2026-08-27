package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/multiplexer"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"golang.org/x/term"
)

const multiplexerTitleLimit = 80

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type titleSink interface {
	SetTitle(string) error
}

type reporterTicker interface {
	C() <-chan time.Time
	Stop()
}

type realReporterTicker struct{ *time.Ticker }

func (ticker realReporterTicker) C() <-chan time.Time { return ticker.Ticker.C }

type multiplexerProgressReporter struct {
	sink      titleSink
	newTicker func(time.Duration) reporterTicker

	mu         sync.Mutex
	running    bool
	workflow   string
	detail     string
	index      int
	total      int
	frame      int
	stop       chan struct{}
	done       chan struct{}
	finalTitle string
}

func newMultiplexerProgressReporter(writer io.Writer, getenv func(string) string) reporterpkg.Reporter {
	if getenv == nil {
		return reporterpkg.Funcs{}
	}
	environment := map[string]string{
		"TMUX": getenv("TMUX"), "TMUX_PANE": getenv("TMUX_PANE"),
		"HERDR_ENV": getenv("HERDR_ENV"), "HERDR_PANE_ID": getenv("HERDR_PANE_ID"),
		"HERDR_WORKSPACE_ID": getenv("HERDR_WORKSPACE_ID"),
		"CMUX_SURFACE_ID":    getenv("CMUX_SURFACE_ID"), "CMUX_WORKSPACE_ID": getenv("CMUX_WORKSPACE_ID"),
	}
	if _, active := multiplexer.Detect(environment, multiplexer.ProviderAuto); !active {
		return reporterpkg.Funcs{}
	}
	sink := newOSCTitleSink(writer)
	if sink == nil {
		return reporterpkg.Funcs{}
	}
	return newMultiplexerProgressReporterWithSink(sink, func(interval time.Duration) reporterTicker {
		return realReporterTicker{Ticker: time.NewTicker(interval)}
	})
}

func newMultiplexerProgressReporterWithSink(sink titleSink, newTicker func(time.Duration) reporterTicker) *multiplexerProgressReporter {
	return &multiplexerProgressReporter{sink: sink, newTicker: newTicker}
}

func (reporter *multiplexerProgressReporter) Progress(event engine.ProgressEvent) {
	if event.Kind == engine.WorkflowStarted && event.Depth == 0 {
		reporter.start(event.WorkflowName, event.Total)
		return
	}
	if event.Kind == engine.WorkflowFinished && event.Depth == 0 {
		reporter.stopRun(finalTitle(event.Status, event.WorkflowName, false))
		return
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if !reporter.running {
		return
	}
	switch event.Kind {
	case engine.StepStarted:
		reporter.detail = event.StepID
		if event.Depth == 0 {
			reporter.index, reporter.total = event.Index, event.Total
		}
	case engine.RetryScheduled:
		reporter.detail = fmt.Sprintf("%s retry %d/%d", event.StepID, event.Attempt, event.MaxAttempts)
	case engine.PollScheduled:
		reporter.detail = fmt.Sprintf("%s poll %d", event.StepID, event.Poll)
	case engine.ConcurrentStarted:
		reporter.detail = "concurrent"
	case engine.ControlStarted:
		reporter.detail = event.StepID
		if reporter.detail == "" {
			reporter.detail = event.ControlKind
		}
	}
}

func (*multiplexerProgressReporter) Diagnostic(diagnostic.Event) {}

func (reporter *multiplexerProgressReporter) Finish(_ context.Context, outcome reporterpkg.Outcome) error {
	if outcome.WorkflowName == "" {
		reporter.stopRun("")
		return nil
	}
	reporter.stopRun(finalTitle(outcome.Status, outcome.WorkflowName, outcome.DryRun))
	return nil
}

func (reporter *multiplexerProgressReporter) start(workflow string, total int) {
	reporter.stopRun("")
	reporter.mu.Lock()
	reporter.running = true
	reporter.workflow = workflow
	reporter.detail = ""
	reporter.index = 0
	reporter.total = total
	reporter.frame = 0
	reporter.finalTitle = ""
	reporter.stop = make(chan struct{})
	reporter.done = make(chan struct{})
	stop, done := reporter.stop, reporter.done
	ticker := reporter.newTicker(100 * time.Millisecond)
	reporter.mu.Unlock()
	reporter.writeFrame()
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C():
				reporter.writeFrame()
			case <-stop:
				return
			}
		}
	}()
}

func (reporter *multiplexerProgressReporter) stopRun(title string) {
	reporter.mu.Lock()
	var done chan struct{}
	if reporter.running {
		close(reporter.stop)
		done = reporter.done
		reporter.running = false
	}
	if title != "" && title == reporter.finalTitle {
		title = ""
	}
	reporter.mu.Unlock()
	if done != nil {
		<-done
	}
	if title != "" {
		_ = reporter.sink.SetTitle(sanitizeMultiplexerTitle(title))
		reporter.mu.Lock()
		reporter.finalTitle = title
		reporter.mu.Unlock()
	}
}

func (reporter *multiplexerProgressReporter) writeFrame() {
	reporter.mu.Lock()
	if !reporter.running {
		reporter.mu.Unlock()
		return
	}
	frame := spinnerFrames[reporter.frame%len(spinnerFrames)]
	reporter.frame++
	parts := []string{frame + " " + reporter.workflow}
	if reporter.total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", reporter.index, reporter.total))
	}
	if reporter.detail != "" {
		parts = append(parts, reporter.detail)
	}
	title := sanitizeMultiplexerTitle(strings.Join(parts, " · "))
	reporter.mu.Unlock()
	_ = reporter.sink.SetTitle(title)
}

func finalTitle(status engine.ExecutionStatus, workflow string, dryRun bool) string {
	if dryRun {
		return "◇ " + workflow + " · dry run"
	}
	marker := "✗"
	switch status {
	case engine.StatusSucceeded:
		marker = "✓"
	case engine.StatusCanceled:
		marker = "■"
	case engine.StatusTimedOut:
		marker = "⏱"
	}
	return marker + " " + workflow
}

func sanitizeMultiplexerTitle(value string) string {
	value = strings.TrimSpace(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value))
	runes := []rune(value)
	if len(runes) > multiplexerTitleLimit {
		value = string(runes[:multiplexerTitleLimit-1]) + "…"
	}
	return value
}

type terminalTitleWriter interface {
	io.Writer
	Fd() uintptr
}

type oscTitleSink struct{ writer io.Writer }

func newOSCTitleSink(writer io.Writer) titleSink {
	terminal, ok := writer.(terminalTitleWriter)
	if !ok || !term.IsTerminal(int(terminal.Fd())) {
		return nil
	}
	return oscTitleSink{writer: terminal}
}

func (sink oscTitleSink) SetTitle(title string) error {
	_, err := fmt.Fprintf(sink.writer, "\x1b]2;%s\x1b\\", sanitizeMultiplexerTitle(title))
	return err
}

var _ reporterpkg.Reporter = (*multiplexerProgressReporter)(nil)
