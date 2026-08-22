package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/githubactions"
	"github.com/up2jj/wuko/tui"
)

type runReporter interface {
	Progress(engine.ProgressEvent)
	Diagnostic(diagnostic.Event)
	Finish(string, *engine.State, error, bool) error
}

type fanoutReporter []runReporter

func (reporters fanoutReporter) Progress(event engine.ProgressEvent) {
	for _, reporter := range reporters {
		reporter.Progress(event)
	}
}

func (reporters fanoutReporter) Diagnostic(event diagnostic.Event) {
	for _, reporter := range reporters {
		reporter.Diagnostic(event)
	}
}

func (reporters fanoutReporter) Finish(workflowName string, state *engine.State, runErr error, dryRun bool) error {
	errorsByReporter := make([]error, 0, len(reporters))
	for _, reporter := range reporters {
		if err := reporter.Finish(workflowName, state, runErr, dryRun); err != nil {
			errorsByReporter = append(errorsByReporter, err)
		}
	}
	return errors.Join(errorsByReporter...)
}

type plainReporter struct {
	progress    *tui.Progress
	diagnostics diagnostic.Reporter
}

func newPlainReporter(command *cobra.Command, deps dependencies, runDir string) *plainReporter {
	return &plainReporter{
		progress:    tui.NewProgress(command.ErrOrStderr(), colorEnabled(command.ErrOrStderr())),
		diagnostics: diagnosticsFor(command, deps, runDir),
	}
}

func (reporter *plainReporter) Progress(event engine.ProgressEvent) {
	reporter.progress.Report(event)
}

func (reporter *plainReporter) Diagnostic(event diagnostic.Event) {
	diagnostic.Emit(reporter.diagnostics, event)
}

func (*plainReporter) Finish(string, *engine.State, error, bool) error { return nil }

func newRunReporters(command *cobra.Command, deps dependencies, runDir string, names []string) (fanoutReporter, error) {
	if len(names) == 0 {
		names = []string{"plain"}
	}
	reporters := make(fanoutReporter, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		switch name {
		case "plain":
			reporters = append(reporters, newPlainReporter(command, deps, runDir))
		case "github":
			workspace := deps.getenv("GITHUB_WORKSPACE")
			if workspace == "" {
				workspace = runDir
			}
			reporter, err := githubactions.New(githubactions.Options{
				OutputPath: deps.getenv("GITHUB_OUTPUT"), SummaryPath: deps.getenv("GITHUB_STEP_SUMMARY"),
				Workspace: workspace, Commands: command.ErrOrStderr(),
			})
			if err != nil {
				return nil, err
			}
			reporters = append(reporters, reporter)
		default:
			return nil, fmt.Errorf("unknown reporter %q; expected plain or github", name)
		}
	}
	return reporters, nil
}

var _ runReporter = (*plainReporter)(nil)
var _ runReporter = (*githubactions.Reporter)(nil)
var _ runReporter = fanoutReporter(nil)
