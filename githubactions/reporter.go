// Package githubactions reports Wuko runs through GitHub Actions workflow commands and files.
package githubactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	reporterpkg "github.com/up2jj/wuko/reporter"
)

const aggregateOutput = "wuko_outputs"

// Options identifies the GitHub-owned files and streams used by a workflow step.
type Options struct {
	OutputPath  string
	SummaryPath string
	Workspace   string
	Commands    io.Writer
}

// Reporter translates Wuko lifecycle events into GitHub Actions integration data.
type Reporter struct {
	outputPath  string
	summaryPath string
	workspace   string
	commands    io.Writer

	mu          sync.Mutex
	annotations map[string]struct{}
	annotated   bool
	commandErr  error
	finished    *engine.ProgressEvent
}

// New validates the GitHub environment files before workflow execution begins.
func New(options Options) (*Reporter, error) {
	if options.OutputPath == "" {
		return nil, fmt.Errorf("GITHUB_OUTPUT is required for the github reporter")
	}
	if options.SummaryPath == "" {
		return nil, fmt.Errorf("GITHUB_STEP_SUMMARY is required for the github reporter")
	}
	for name, path := range map[string]string{
		"GITHUB_OUTPUT":       options.OutputPath,
		"GITHUB_STEP_SUMMARY": options.SummaryPath,
	} {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, fmt.Errorf("opening %s file %s: %w", name, path, err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("closing %s file %s: %w", name, path, err)
		}
	}
	commands := options.Commands
	if commands == nil {
		commands = io.Discard
	}
	workspace := options.Workspace
	if workspace != "" {
		absolute, err := filepath.Abs(workspace)
		if err != nil {
			return nil, fmt.Errorf("resolving GitHub workspace %s: %w", workspace, err)
		}
		workspace = filepath.Clean(absolute)
	}
	return &Reporter{
		outputPath: options.OutputPath, summaryPath: options.SummaryPath,
		workspace: workspace, commands: commands, annotations: make(map[string]struct{}),
	}, nil
}

// Progress retains the latest root workflow result. The engine redacts this copy before delivery,
// so Finish prefers it when it describes the same outcome that is being finalized.
func (reporter *Reporter) Progress(event engine.ProgressEvent) {
	if event.Kind != engine.WorkflowFinished || event.Depth != 0 {
		return
	}
	copy := event
	copy.Stats = cloneRunStats(event.Stats)
	reporter.mu.Lock()
	reporter.finished = &copy
	reporter.mu.Unlock()
}

// Diagnostic emits one deduplicated GitHub error annotation for a failed diagnostic.
func (reporter *Reporter) Diagnostic(event diagnostic.Event) {
	if event.Status != diagnostic.StatusFailed {
		return
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.annotate(event)
}

// Finish writes the final annotation, workflow outputs, and job summary.
func (reporter *Reporter) Finish(_ context.Context, outcome reporterpkg.Outcome) error {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()

	if outcome.Err != nil && !reporter.annotated {
		reporter.writeCommand("::error title=Wuko::" + escapeData(singleLine(outcome.Err.Error())) + "\n")
		reporter.annotated = true
	}

	var finishErrors []error
	if outcome.Outputs != nil && outcome.Err == nil && !outcome.DryRun {
		if err := reporter.writeOutputs(outcome.Outputs); err != nil {
			finishErrors = append(finishErrors, err)
		}
	}
	stats := outcome.Stats
	redacted := reporter.finishedMatches(outcome)
	if redacted {
		stats = reporter.finished.Stats
	}
	if err := reporter.writeSummary(outcome, stats, redacted); err != nil {
		finishErrors = append(finishErrors, err)
	}
	if reporter.commandErr != nil {
		finishErrors = append(finishErrors, reporter.commandErr)
	}
	return errors.Join(finishErrors...)
}

func (reporter *Reporter) annotate(event diagnostic.Event) {
	message := strings.TrimSpace(event.Message)
	if event.Error != nil {
		if message == "" {
			message = event.Error.Error()
		} else {
			message += ": " + event.Error.Error()
		}
	}
	if message == "" {
		message = string(event.Phase) + " failed"
	}
	message = singleLine(message)

	properties := []string{"title=" + escapeProperty("Wuko "+string(event.Phase))}
	file, located := reporter.repositoryPath(event.Location.Source)
	if located {
		properties = append(properties, "file="+escapeProperty(file))
		if event.Location.Line > 0 {
			properties = append(properties, fmt.Sprintf("line=%d", event.Location.Line))
		}
		if event.Location.Column > 0 {
			properties = append(properties, fmt.Sprintf("col=%d", event.Location.Column))
		}
	}
	key := strings.Join(properties, ",") + "\x00" + message
	if _, exists := reporter.annotations[key]; exists {
		return
	}
	reporter.annotations[key] = struct{}{}
	reporter.annotated = true
	reporter.writeCommand("::error " + strings.Join(properties, ",") + "::" + escapeData(message) + "\n")
}

func (reporter *Reporter) repositoryPath(source string) (string, bool) {
	if reporter.workspace == "" || source == "" || strings.Contains(source, "://") || strings.HasPrefix(source, "github:") {
		return "", false
	}
	path := source
	if !filepath.IsAbs(path) {
		path = filepath.Join(reporter.workspace, filepath.FromSlash(path))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(reporter.workspace, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func (reporter *Reporter) writeCommand(command string) {
	if reporter.commandErr != nil {
		return
	}
	_, reporter.commandErr = io.WriteString(reporter.commands, command)
	if reporter.commandErr != nil {
		reporter.commandErr = fmt.Errorf("writing GitHub workflow command: %w", reporter.commandErr)
	}
}

func (reporter *Reporter) writeOutputs(outputs map[string]any) error {
	if _, exists := outputs[aggregateOutput]; exists {
		return fmt.Errorf("workflow output %q is reserved by the github reporter", aggregateOutput)
	}
	file, err := os.OpenFile(reporter.outputPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("opening GITHUB_OUTPUT file %s: %w", reporter.outputPath, err)
	}

	names := mapsKeys(outputs)
	slices.Sort(names)
	for _, name := range names {
		value, err := outputValue(outputs[name])
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("encoding GitHub output %s: %w", name, err)
		}
		if err := writeEnvironmentValue(file, name, value); err != nil {
			_ = file.Close()
			return fmt.Errorf("writing GitHub output %s: %w", name, err)
		}
	}
	aggregate, err := json.Marshal(outputs)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("encoding aggregate GitHub outputs: %w", err)
	}
	if err := writeEnvironmentValue(file, aggregateOutput, string(aggregate)); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing aggregate GitHub outputs: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing GITHUB_OUTPUT file %s: %w", reporter.outputPath, err)
	}
	return nil
}

func (reporter *Reporter) finishedMatches(outcome reporterpkg.Outcome) bool {
	return reporter.finished != nil && outcome.RunID != "" &&
		reporter.finished.RunID == outcome.RunID &&
		reporter.finished.WorkflowName == outcome.WorkflowName &&
		reporter.finished.Status == outcome.Status
}

func (reporter *Reporter) writeSummary(outcome reporterpkg.Outcome, stats engine.RunStats, redacted bool) error {
	status := string(outcome.Status)
	if outcome.DryRun {
		status = "validated"
	} else if status == "" && outcome.Err != nil {
		status = "failed"
	} else if status == "" {
		status = "succeeded"
	}

	name := markdownCell(outcome.WorkflowName)
	if name == "" {
		name = "workflow"
	}
	summary := fmt.Sprintf(
		"### Wuko: `%s`\n\n| Status | Duration | Steps | Succeeded | Failed | Skipped |\n| --- | ---: | ---: | ---: | ---: | ---: |\n| %s | %s | %d | %d | %d | %d |\n\n",
		name, status, formatDuration(stats.Duration), stats.Total, stats.Succeeded, stats.Failed, stats.Skipped,
	)
	if len(stats.Steps) > 0 {
		summary += reporter.stepSummary(stats, redacted)
	}
	file, err := os.OpenFile(reporter.summaryPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("opening GITHUB_STEP_SUMMARY file %s: %w", reporter.summaryPath, err)
	}
	if _, err := io.WriteString(file, summary); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing GitHub step summary: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing GITHUB_STEP_SUMMARY file %s: %w", reporter.summaryPath, err)
	}
	return nil
}

// stepSummary renders the recorded steps. Redacted reports whether stats came from the engine's
// redacted progress copy; step error text is only rendered when it did.
func (reporter *Reporter) stepSummary(stats engine.RunStats, redacted bool) string {
	var summary strings.Builder
	summary.WriteString("#### Steps\n\n")
	summary.WriteString("_Rows list the top-level steps the run recorded. The header counts every planned step, and the execution statistics below include the steps nested inside controls._\n\n")
	summary.WriteString("| Step | Status | Duration | Attempts | Retries | Polls |\n")
	summary.WriteString("| --- | --- | ---: | ---: | ---: | ---: |\n")
	for _, step := range stats.Steps {
		attempts := len(step.Attempts)
		fmt.Fprintf(&summary, "| %s | %s | %s | %s | %s | %s |\n",
			markdownCell(html.EscapeString(stepSummaryName(step))), statusLabel(step.Status), formatDuration(step.Duration),
			stepCount(attempts), retryCount(attempts), stepCount(step.Polls))
	}
	summary.WriteString("\n")

	failed := false
	for _, step := range stats.Steps {
		if !unsuccessful(step.Status) {
			continue
		}
		if !failed {
			summary.WriteString("#### Failure details\n\n")
			failed = true
		}
		fmt.Fprintf(&summary, "##### <code>%s</code>\n\n", html.EscapeString(stepSummaryName(step)))
		fmt.Fprintf(&summary, "- Status: %s\n", statusLabel(step.Status))
		fmt.Fprintf(&summary, "- Duration: %s\n", formatDuration(step.Duration))
		if len(step.Attempts) == 0 {
			summary.WriteString("- Attempts: —\n")
			summary.WriteString("- Retries: —\n")
		} else {
			fmt.Fprintf(&summary, "- Attempts: %d\n", len(step.Attempts))
			fmt.Fprintf(&summary, "- Retries: %d\n", max(0, len(step.Attempts)-1))
		}
		if location := reporter.summaryLocation(step.Location); location != "" {
			fmt.Fprintf(&summary, "- Source: <code>%s</code>\n", html.EscapeString(location))
		}
		if step.Error != nil && strings.TrimSpace(step.Error.Error()) != "" {
			if redacted {
				fmt.Fprintf(&summary, "\n<pre>%s</pre>\n", html.EscapeString(step.Error.Error()))
			} else {
				summary.WriteString("- Error: withheld, no redacted progress copy for this run\n")
			}
		}
		summary.WriteString("\n")
	}

	longest := stats.Steps[0]
	for _, step := range stats.Steps[1:] {
		if step.Duration > longest.Duration {
			longest = step
		}
	}
	summary.WriteString("#### Execution statistics\n\n")
	fmt.Fprintf(&summary, "- Attempts: %d\n", stats.Attempts)
	fmt.Fprintf(&summary, "- Retries: %d\n", stats.Retries)
	fmt.Fprintf(&summary, "- Retry wait: %s\n", formatDuration(stats.RetryWait))
	fmt.Fprintf(&summary, "- Polls: %d\n", stats.Polls)
	fmt.Fprintf(&summary, "- Poll wait: %s\n", formatDuration(stats.PollWait))
	fmt.Fprintf(&summary, "- Timeouts: %d\n", stats.TimedOut)
	fmt.Fprintf(&summary, "- Longest step: <code>%s</code> (%s)\n\n", html.EscapeString(stepSummaryName(longest)), formatDuration(longest.Duration))
	return summary.String()
}

func (reporter *Reporter) summaryLocation(location diagnostic.Location) string {
	path, ok := reporter.repositoryPath(location.Source)
	if !ok {
		return ""
	}
	if location.Line > 0 {
		return fmt.Sprintf("%s:%d", path, location.Line)
	}
	return path
}

func stepSummaryName(step engine.StepStats) string {
	if step.ID != "" {
		return step.ID
	}
	if step.Type != "" {
		return step.Type
	}
	return fmt.Sprintf("step %d", step.Index)
}

func statusLabel(status engine.ExecutionStatus) string {
	switch status {
	case engine.StatusSucceeded:
		return "✓ succeeded"
	case engine.StatusTimedOut:
		return "⏱ timed out"
	case engine.StatusCanceled:
		return "■ canceled"
	case engine.StatusSkipped:
		return "⊘ skipped"
	default:
		return "✗ failed"
	}
}

func unsuccessful(status engine.ExecutionStatus) bool {
	return status == engine.StatusFailed || status == engine.StatusTimedOut || status == engine.StatusCanceled
}

func stepCount(value int) string {
	if value == 0 {
		return "—"
	}
	return fmt.Sprint(value)
}

func retryCount(attempts int) string {
	if attempts == 0 {
		return "—"
	}
	return fmt.Sprint(max(0, attempts-1))
}

func cloneRunStats(stats engine.RunStats) engine.RunStats {
	stats.Steps = append([]engine.StepStats(nil), stats.Steps...)
	for index := range stats.Steps {
		stats.Steps[index] = cloneStepStats(stats.Steps[index])
	}
	return stats
}

func cloneStepStats(stats engine.StepStats) engine.StepStats {
	stats.Attempts = append([]engine.AttemptStats(nil), stats.Attempts...)
	stats.Iterations = append([]engine.IterationStats(nil), stats.Iterations...)
	for index := range stats.Iterations {
		stats.Iterations[index].Steps = append([]engine.StepStats(nil), stats.Iterations[index].Steps...)
		for stepIndex := range stats.Iterations[index].Steps {
			stats.Iterations[index].Steps[stepIndex] = cloneStepStats(stats.Iterations[index].Steps[stepIndex])
		}
	}
	return stats
}

func outputValue(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func writeEnvironmentValue(writer io.Writer, name, value string) error {
	delimiter := "WUKO_EOF"
	for containsLine(value, delimiter) {
		delimiter += "_"
	}
	_, err := fmt.Fprintf(writer, "%s<<%s\n%s\n%s\n", name, delimiter, value, delimiter)
	return err
}

func containsLine(value, target string) bool {
	for line := range strings.Lines(value) {
		line = strings.TrimSuffix(line, "\n")
		if strings.TrimSuffix(line, "\r") == target {
			return true
		}
	}
	return value == target
}

func mapsKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func escapeData(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "\r", "%0D")
	return strings.ReplaceAll(value, "\n", "%0A")
}

func escapeProperty(value string) string {
	value = escapeData(value)
	value = strings.ReplaceAll(value, ":", "%3A")
	return strings.ReplaceAll(value, ",", "%2C")
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func markdownCell(value string) string {
	value = singleLine(value)
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.ReplaceAll(value, "`", "\\`")
}

func formatDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	if value < time.Millisecond {
		return "<1ms"
	}
	return value.Round(time.Millisecond).String()
}
