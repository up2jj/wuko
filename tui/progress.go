package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/engine"
)

// Progress renders durable execution status lines. It is safe to call from multiple goroutines.
type Progress struct {
	writer io.Writer
	color  bool
	mu     sync.Mutex
}

// NewProgress constructs a renderer for structured engine progress events.
func NewProgress(writer io.Writer, color bool) *Progress {
	if writer == nil {
		writer = io.Discard
	}
	return &Progress{writer: writer, color: color}
}

// Report renders one progress event.
func (progress *Progress) Report(event engine.ProgressEvent) {
	progress.mu.Lock()
	defer progress.mu.Unlock()

	indent := strings.Repeat("  ", event.Depth)
	childIndent := indent + "  "
	switch event.Kind {
	case engine.WorkflowStarted:
		label := "Workflow"
		marker := progress.paint("36", "◆")
		if event.Depth > 0 {
			label = "Action"
			marker = progress.paint("36", "◇")
		}
		fmt.Fprintf(progress.writer, "%s%s %s %s · %s\n", indent, marker, label, event.WorkflowName, count(event.Total, "step"))
	case engine.StepStarted:
		parts := []string{fmt.Sprintf("%s (%s)", event.StepID, event.StepType)}
		if event.MaxAttempts > 1 {
			parts = append(parts, fmt.Sprintf("up to %s", count(event.MaxAttempts, "attempt")))
		}
		if event.Timeout > 0 {
			parts = append(parts, "timeout "+formatDuration(event.Timeout))
		}
		fmt.Fprintf(progress.writer, "%s%s [%d/%d] %s\n", indent, progress.paint("36", "→"), event.Index, event.Total, strings.Join(parts, " · "))
	case engine.ConcurrentStarted:
		parts := []string{count(event.GroupSize, "step"), fmt.Sprintf("max %d concurrent", event.MaxConcurrency)}
		if event.Timeout > 0 {
			parts = append(parts, "timeout "+formatDuration(event.Timeout))
		}
		if event.FailFast {
			parts = append(parts, "fail fast")
		} else {
			parts = append(parts, "wait for all")
		}
		fmt.Fprintf(progress.writer, "%s%s Concurrent · %s\n", indent, progress.paint("36", "⇉"), strings.Join(parts, " · "))
	case engine.ConcurrentFinished:
		line := fmt.Sprintf("%s%s Concurrent %s after %s", indent, progress.statusMarker(event.Status), statusLabel(event.Status), formatDuration(event.Duration))
		if event.Status != engine.StatusSucceeded {
			line += " · " + workSummary(event.Started, event.Succeeded, event.GroupSize, "step")
		}
		if event.Error != nil {
			line += ": " + singleLine(event.Error.Error())
		}
		fmt.Fprintln(progress.writer, line)
	case engine.ControlStarted:
		parts := []string{count(event.Iterations, "iteration"), fmt.Sprintf("max %d concurrent", event.MaxConcurrency)}
		if event.Timeout > 0 {
			parts = append(parts, "timeout "+formatDuration(event.Timeout))
		}
		if event.FailFast {
			parts = append(parts, "fail fast")
		} else {
			parts = append(parts, "wait for all")
		}
		fmt.Fprintf(progress.writer, "%s%s %s %s · %s\n", indent, progress.paint("36", "↻"), controlLabel(event.ControlKind), event.StepID, strings.Join(parts, " · "))
	case engine.ControlFinished:
		line := fmt.Sprintf("%s%s %s %s %s after %s", indent, progress.statusMarker(event.Status), controlLabel(event.ControlKind), event.StepID, statusLabel(event.Status), formatDuration(event.Duration))
		if event.Status != engine.StatusSucceeded {
			line += " · " + workSummary(event.Started, event.Succeeded, event.Iterations, "iteration")
		}
		if event.Error != nil {
			line += ": " + singleLine(event.Error.Error())
		}
		fmt.Fprintln(progress.writer, line)
	case engine.IterationStarted:
		fmt.Fprintf(progress.writer, "%s%s iteration %d/%d started\n", indent, progress.paint("2", "•"), event.Iteration+1, event.Iterations)
	case engine.IterationFinished:
		if event.Status == engine.StatusSucceeded {
			return
		}
		line := fmt.Sprintf("%s%s iteration %d/%d %s after %s", indent, progress.statusMarker(event.Status), event.Iteration+1, event.Iterations, statusLabel(event.Status), formatDuration(event.Duration))
		if event.Error != nil {
			line += ": " + singleLine(event.Error.Error())
		}
		fmt.Fprintln(progress.writer, line)
	case engine.AttemptStarted:
		if event.MaxAttempts > 1 {
			fmt.Fprintf(progress.writer, "%s%s attempt %d/%d started\n", childIndent, progress.paint("2", "•"), event.Attempt, event.MaxAttempts)
		}
	case engine.AttemptFinished:
		if event.Status == engine.StatusSucceeded {
			return
		}
		label := statusLabel(event.Status)
		line := fmt.Sprintf("%s%s attempt %d/%d %s after %s", childIndent, progress.statusMarker(event.Status), event.Attempt, event.MaxAttempts, label, formatDuration(event.Duration))
		if event.Error != nil {
			line += ": " + singleLine(event.Error.Error())
		}
		fmt.Fprintln(progress.writer, line)
	case engine.RetryScheduled:
		fmt.Fprintf(progress.writer, "%s%s retrying with attempt %d/%d in %s\n", childIndent, progress.paint("33", "↻"), event.Attempt, event.MaxAttempts, formatDuration(event.RetryDelay))
	case engine.PollStarted:
		fmt.Fprintf(progress.writer, "%s%s poll %d started\n", childIndent, progress.paint("2", "•"), event.Poll)
	case engine.PollFinished:
		if event.Matched {
			fmt.Fprintf(progress.writer, "%s%s poll %d matched after %s\n", childIndent, progress.paint("32", "✓"), event.Poll, formatDuration(event.Duration))
		}
	case engine.PollScheduled:
		line := fmt.Sprintf("%s%s poll %d did not match · poll %d in %s", childIndent, progress.paint("33", "↻"), event.Poll-1, event.Poll, formatDuration(event.PollDelay))
		if event.Error != nil {
			line += ": " + singleLine(event.Error.Error())
		}
		fmt.Fprintln(progress.writer, line)
	case engine.StepFinished:
		marker := progress.statusMarker(event.Status)
		if event.Status == engine.StatusSkipped {
			fmt.Fprintf(progress.writer, "%s%s [%d/%d] %s skipped\n", indent, marker, event.Index, event.Total, event.StepID)
			return
		}
		line := fmt.Sprintf("%s%s [%d/%d] %s %s after %s", indent, marker, event.Index, event.Total, event.StepID, statusLabel(event.Status), formatDuration(event.Duration))
		if event.Attempt > 1 {
			line += fmt.Sprintf(" · %s", count(event.Attempt, "attempt"))
		}
		fmt.Fprintln(progress.writer, line)
	case engine.WorkflowFinished:
		parts := runSummary(event.Stats)
		label := "Workflow"
		if event.Depth > 0 {
			label = "Action"
		}
		fmt.Fprintf(progress.writer, "%s%s %s %s %s in %s", indent, progress.statusMarker(event.Status), label, event.WorkflowName, statusLabel(event.Status), formatDuration(event.Duration))
		if len(parts) > 0 {
			fmt.Fprintf(progress.writer, " · %s", strings.Join(parts, " · "))
		}
		fmt.Fprintln(progress.writer)
	}
}

func (progress *Progress) statusMarker(status engine.ExecutionStatus) string {
	switch status {
	case engine.StatusSucceeded:
		return progress.paint("32", "✓")
	case engine.StatusSkipped:
		return progress.paint("33", "⊘")
	case engine.StatusTimedOut:
		return progress.paint("35", "⏱")
	case engine.StatusCanceled:
		return progress.paint("33", "■")
	default:
		return progress.paint("31", "✗")
	}
}

func (progress *Progress) paint(code, value string) string {
	if !progress.color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func runSummary(stats engine.RunStats) []string {
	var parts []string
	for _, item := range []struct {
		value int
		name  string
	}{
		{stats.Succeeded, "succeeded"},
		{stats.Failed, "failed"},
		{stats.Skipped, "skipped"},
		{stats.Canceled, "canceled"},
	} {
		if item.value > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.value, item.name))
		}
	}
	terminal := stats.Succeeded + stats.Failed + stats.Skipped + stats.Canceled
	if remaining := stats.Total - terminal; remaining > 0 {
		parts = append(parts, fmt.Sprintf("%d not run", remaining))
	}
	if stats.Attempts > 0 {
		parts = append(parts, count(stats.Attempts, "attempt"))
	}
	if stats.Retries > 0 {
		parts = append(parts, count(stats.Retries, "retry"))
	}
	if stats.TimedOut > 0 {
		parts = append(parts, count(stats.TimedOut, "timeout"))
	}
	if stats.RetryWait > 0 {
		parts = append(parts, formatDuration(stats.RetryWait)+" retry wait")
	}
	if stats.Polls > 0 {
		parts = append(parts, count(stats.Polls, "poll"))
	}
	if stats.PollWait > 0 {
		parts = append(parts, formatDuration(stats.PollWait)+" poll wait")
	}
	return parts
}

func statusLabel(status engine.ExecutionStatus) string {
	switch status {
	case engine.StatusSucceeded:
		return "succeeded"
	case engine.StatusTimedOut:
		return "timed out"
	case engine.StatusCanceled:
		return "canceled"
	case engine.StatusSkipped:
		return "skipped"
	default:
		return "failed"
	}
}

func count(value int, singular string) string {
	word := singular
	if value != 1 {
		if singular == "retry" {
			word = "retries"
		} else {
			word += "s"
		}
	}
	return fmt.Sprintf("%d %s", value, word)
}

func workSummary(started, succeeded, total int, unit string) string {
	parts := []string{fmt.Sprintf("%d/%d %ss started", started, total, unit), fmt.Sprintf("%d succeeded", succeeded)}
	if notRun := total - started; notRun > 0 {
		parts = append(parts, fmt.Sprintf("%d not run", notRun))
	}
	return strings.Join(parts, " · ")
}

func formatDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration < time.Millisecond {
		return "<1ms"
	}
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	if duration < time.Minute {
		return duration.Round(10 * time.Millisecond).String()
	}
	return duration.Round(100 * time.Millisecond).String()
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func controlLabel(kind string) string {
	if kind == "" {
		return "Control"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}
