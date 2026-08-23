package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
)

type recordingReporter struct {
	name   string
	events *[]string
	err    error
}

func (reporter recordingReporter) Progress(engine.ProgressEvent) {
	*reporter.events = append(*reporter.events, reporter.name+":progress")
}

func (reporter recordingReporter) Diagnostic(diagnostic.Event) {
	*reporter.events = append(*reporter.events, reporter.name+":diagnostic")
}

func (reporter recordingReporter) Finish(string, *engine.State, error, bool) error {
	*reporter.events = append(*reporter.events, reporter.name+":finish")
	return reporter.err
}

func TestFanoutReporterPreservesOrderAndJoinsFinishErrors(t *testing.T) {
	var events []string
	firstErr := errors.New("first failed")
	secondErr := errors.New("second failed")
	reporters := fanoutReporter{
		recordingReporter{name: "first", events: &events, err: firstErr},
		recordingReporter{name: "second", events: &events, err: secondErr},
	}
	reporters.Progress(engine.ProgressEvent{})
	reporters.Diagnostic(diagnostic.Event{})
	err := reporters.Finish("check", nil, nil, false)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Finish() error = %v, want both errors", err)
	}
	want := "first:progress,second:progress,first:diagnostic,second:diagnostic,first:finish,second:finish"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestFinishReportersOnceFinalizesOnlyFirstOutcome(t *testing.T) {
	var events []string
	finish := finishReportersOnce(fanoutReporter{recordingReporter{name: "run", events: &events}})
	if err, called := finish("check", nil, errors.New("preparation failed"), false); err != nil || !called {
		t.Fatalf("first finish = (%v, %v), want (nil, true)", err, called)
	}
	if err, called := finish("check", &engine.State{}, nil, false); err != nil || called {
		t.Fatalf("second finish = (%v, %v), want (nil, false)", err, called)
	}
	if got := strings.Join(events, ","); got != "run:finish" {
		t.Fatalf("events = %q, want one finish", got)
	}
}

func TestNewRunReportersDefaultsToPlainAndComposesExplicitReporters(t *testing.T) {
	command := &cobra.Command{}
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	debug := false
	deps := dependencies{debug: &debug, getenv: func(string) string { return "" }}

	defaults, err := newRunReporters(command, deps, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 1 {
		t.Fatalf("default reporters = %d, want 1", len(defaults))
	}
	if _, ok := defaults[0].(*plainReporter); !ok {
		t.Fatalf("default reporter = %T, want *plainReporter", defaults[0])
	}

	output := filepath.Join(t.TempDir(), "output")
	summary := filepath.Join(t.TempDir(), "summary")
	for _, path := range []string{output, summary} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{"GITHUB_OUTPUT": output, "GITHUB_STEP_SUMMARY": summary}
	deps.getenv = func(name string) string { return values[name] }
	composed, err := newRunReporters(command, deps, t.TempDir(), []string{"plain", "github", "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if len(composed) != 2 {
		t.Fatalf("explicit reporters = %d, want two unique reporters", len(composed))
	}
	if _, ok := composed[0].(*plainReporter); !ok {
		t.Fatalf("first reporter = %T, want *plainReporter", composed[0])
	}
}

func TestNewRunReportersRejectsUnknownReporter(t *testing.T) {
	command := &cobra.Command{}
	command.SetErr(new(bytes.Buffer))
	debug := false
	_, err := newRunReporters(command, dependencies{debug: &debug, getenv: func(string) string { return "" }}, t.TempDir(), []string{"otel"})
	if err == nil || !strings.Contains(err.Error(), `unknown reporter "otel"`) {
		t.Fatalf("error = %v, want unknown reporter", err)
	}
}
