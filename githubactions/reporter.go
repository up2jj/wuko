// Package githubactions reports Wuko runs through GitHub Actions workflow commands and files.
package githubactions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
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

// Progress records the terminal workflow event used to build the job summary.
func (reporter *Reporter) Progress(event engine.ProgressEvent) {
	if event.Kind != engine.WorkflowFinished || event.Depth != 0 {
		return
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	copy := event
	reporter.finished = &copy
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
func (reporter *Reporter) Finish(workflowName string, state *engine.State, runErr error, dryRun bool) error {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()

	if runErr != nil && !reporter.annotated {
		reporter.writeCommand("::error title=Wuko::" + escapeData(singleLine(runErr.Error())) + "\n")
		reporter.annotated = true
	}

	var finishErrors []error
	if state != nil && runErr == nil && !dryRun {
		if err := reporter.writeOutputs(state.Outputs); err != nil {
			finishErrors = append(finishErrors, err)
		}
	}
	if err := reporter.writeSummary(workflowName, state, runErr, dryRun); err != nil {
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

func (reporter *Reporter) writeSummary(workflowName string, state *engine.State, runErr error, dryRun bool) error {
	status := "succeeded"
	if dryRun {
		status = "validated"
	} else if reporter.finished != nil {
		status = string(reporter.finished.Status)
	} else if runErr != nil {
		status = "failed"
	}

	stats := engine.RunStats{}
	duration := time.Duration(0)
	if reporter.finished != nil {
		stats = reporter.finished.Stats
		duration = reporter.finished.Duration
	} else if state != nil {
		stats = state.Stats
		duration = state.Stats.Duration
	}
	name := markdownCell(workflowName)
	if name == "" {
		name = "workflow"
	}
	summary := fmt.Sprintf(
		"### Wuko: `%s`\n\n| Status | Duration | Steps | Succeeded | Failed | Skipped |\n| --- | ---: | ---: | ---: | ---: | ---: |\n| %s | %s | %d | %d | %d | %d |\n\n",
		name, status, formatDuration(duration), stats.Total, stats.Succeeded, stats.Failed, stats.Skipped,
	)
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
