package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/diagnostic"
)

// Debug renders durable, human-readable diagnostic lifecycle lines.
type Debug struct {
	writer  io.Writer
	runDir  string
	started time.Time
	mu      sync.Mutex
}

// NewDebug constructs a reporter whose local locations are relative to runDir when possible.
func NewDebug(writer io.Writer, runDir string) *Debug {
	if writer == nil {
		writer = io.Discard
	}
	return &Debug{writer: writer, runDir: filepath.Clean(runDir), started: time.Now()}
}

// Report renders one diagnostic event. It is safe to call from multiple goroutines.
func (debug *Debug) Report(event diagnostic.Event) {
	debug.mu.Lock()
	defer debug.mu.Unlock()

	when := event.Time
	if when.IsZero() {
		when = time.Now()
	}
	elapsed := when.Sub(debug.started)
	if elapsed < 0 {
		elapsed = 0
	}
	parts := []string{fmt.Sprintf("[debug +%s]", formatDuration(elapsed))}
	if location := debug.location(event.Location); location != "" {
		parts = append(parts, location)
	}
	if event.StepID != "" {
		step := "step " + event.StepID
		if event.StepType != "" {
			step += " (" + event.StepType + ")"
		}
		parts = append(parts, step)
	} else if event.WorkflowName != "" {
		parts = append(parts, "workflow "+event.WorkflowName)
	}
	phase := string(event.Phase)
	if event.Status != "" && event.Status != diagnostic.StatusDetail {
		phase += " " + string(event.Status)
	}
	if phase != "" {
		parts = append(parts, phase)
	}
	if event.Duration > 0 {
		parts = append(parts, "after "+formatDuration(event.Duration))
	}
	if event.Message != "" {
		parts = append(parts, singleLine(event.Message))
	}
	for _, attribute := range event.Attributes {
		parts = append(parts, attribute.Key+"="+escapeLine(attribute.Value))
	}
	if event.Error != nil {
		parts = append(parts, singleLine(event.Error.Error()))
	}
	indent := strings.Repeat("  ", event.Depth)
	fmt.Fprintln(debug.writer, indent+strings.Join(parts, " · "))
}

func escapeLine(value string) string {
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, "\t", `\t`)
}

func (debug *Debug) location(location diagnostic.Location) string {
	if location.Source == "" {
		return ""
	}
	source := location.Source
	if filepath.IsAbs(source) && debug.runDir != "." && debug.runDir != "" {
		if relative, err := filepath.Rel(debug.runDir, source); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			source = relative
		}
	}
	if location.Line > 0 {
		source += fmt.Sprintf(":%d", location.Line)
		if location.Column > 0 {
			source += fmt.Sprintf(":%d", location.Column)
		}
	}
	return source
}
