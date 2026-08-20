package tui

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/diagnostic"
)

func TestDebugRendersRelativeLocationAndFailure(t *testing.T) {
	runDir := t.TempDir()
	var output bytes.Buffer
	debug := NewDebug(&output, runDir)
	debug.Report(diagnostic.Event{
		Phase: diagnostic.PhaseRender, Status: diagnostic.StatusFailed,
		Location:     diagnostic.Location{Source: filepath.Join(runDir, "workflow.yaml"), Line: 7, Column: 5},
		WorkflowName: "release", StepID: "build", StepType: "shell", Error: errors.New("missing value"),
	})
	got := output.String()
	for _, want := range []string{"[debug +", "workflow.yaml:7:5", "step build (shell)", "render failed", "missing value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestDebugSerializesAttributes(t *testing.T) {
	var output bytes.Buffer
	debug := NewDebug(&output, t.TempDir())
	now := time.Now()
	var group sync.WaitGroup
	for range 10 {
		group.Go(func() {
			debug.Report(diagnostic.Event{Time: now, Phase: diagnostic.PhaseAttempt, Status: diagnostic.StatusStarted, Attributes: []diagnostic.Attribute{diagnostic.Attr("attempt", "1/1")}})
		})
	}
	group.Wait()
	if got := strings.Count(output.String(), "\n"); got != 10 {
		t.Fatalf("line count = %d, want 10", got)
	}
}
