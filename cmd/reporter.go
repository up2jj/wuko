package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/githubactions"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

type runReporterFactory func(*cobra.Command, dependencies, string) (reporterpkg.Reporter, error)

type runReporterDefinition struct {
	name string
	new  runReporterFactory
}

var runReporterCatalog = []runReporterDefinition{
	{name: "plain", new: func(command *cobra.Command, deps dependencies, runDir string) (reporterpkg.Reporter, error) {
		return newPlainReporter(command, deps, runDir), nil
	}},
	{name: "github", new: func(command *cobra.Command, deps dependencies, runDir string) (reporterpkg.Reporter, error) {
		workspace := deps.getenv("GITHUB_WORKSPACE")
		if workspace == "" {
			workspace = runDir
		}
		return githubactions.New(githubactions.Options{
			OutputPath: deps.getenv("GITHUB_OUTPUT"), SummaryPath: deps.getenv("GITHUB_STEP_SUMMARY"),
			Workspace: workspace, Commands: command.ErrOrStderr(),
		})
	}},
}

// runReporters adapts command completion into the safe public reporter outcome. It records the
// latest root workflow event because Engine.Run returns no State after an execution failure.
type runReporters struct {
	group reporterpkg.Group

	mu       sync.Mutex
	finished *engine.ProgressEvent
}

func (reporters *runReporters) Progress(event engine.ProgressEvent) {
	if event.Kind == engine.WorkflowFinished && event.Depth == 0 {
		reporters.mu.Lock()
		copy := event
		reporters.finished = &copy
		reporters.mu.Unlock()
	}
	reporters.group.Progress(event)
}

func (reporters *runReporters) Diagnostic(event diagnostic.Event) {
	reporters.group.Diagnostic(event)
}

func (reporters *runReporters) Finish(ctx context.Context, outcome reporterpkg.Outcome) error {
	return reporters.group.Finish(ctx, outcome)
}

func (reporters *runReporters) complete(ctx context.Context, workflowName string, state *engine.State, runErr error, dryRun bool) error {
	outcome := reporterpkg.Outcome{
		WorkflowName: workflowName,
		Status:       outcomeStatus(runErr),
		Err:          runErr,
		DryRun:       dryRun,
	}
	if state != nil {
		outcome.Stats = state.Stats
		outcome.Outputs = workflow.CloneMap(state.Outputs)
	}
	reporters.mu.Lock()
	finished := reporters.finished
	reporters.mu.Unlock()
	if finished != nil {
		outcome.Status = finished.Status
		outcome.Stats = finished.Stats
	}
	return reporters.Finish(context.WithoutCancel(ctx), outcome)
}

func outcomeStatus(runErr error) engine.ExecutionStatus {
	switch {
	case runErr == nil:
		return engine.StatusSucceeded
	case errors.Is(runErr, context.Canceled):
		return engine.StatusCanceled
	case errors.Is(runErr, context.DeadlineExceeded):
		return engine.StatusTimedOut
	default:
		return engine.StatusFailed
	}
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

func (*plainReporter) Finish(context.Context, reporterpkg.Outcome) error { return nil }

func addReporterFlag(command *cobra.Command, target *[]string) {
	names := runReporterNames()
	command.Flags().StringArrayVar(target, "reporter", nil, fmt.Sprintf(
		"enable a run reporter (%s; repeatable; defaults to plain)", strings.Join(names, " or "),
	))
	_ = command.RegisterFlagCompletionFunc("reporter", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return names, cobra.ShellCompDirectiveNoFileComp
	})
}

func runReporterNames() []string {
	names := make([]string, len(runReporterCatalog))
	for index, definition := range runReporterCatalog {
		names[index] = definition.name
	}
	return names
}

func newRunReporters(command *cobra.Command, deps dependencies, runDir string, names []string) (*runReporters, error) {
	if len(names) == 0 {
		names = []string{"plain"}
	}
	group := make(reporterpkg.Group, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		factory, found := runReporterFactoryFor(name)
		if !found {
			return nil, fmt.Errorf("unknown reporter %q; expected %s", name, strings.Join(runReporterNames(), " or "))
		}
		created, err := factory(command, deps, runDir)
		if err != nil {
			return nil, err
		}
		group = append(group, created)
	}
	return &runReporters{group: group}, nil
}

func runReporterFactoryFor(name string) (runReporterFactory, bool) {
	for _, definition := range runReporterCatalog {
		if definition.name == name {
			return definition.new, true
		}
	}
	return nil, false
}

var _ reporterpkg.Reporter = (*plainReporter)(nil)
var _ reporterpkg.Reporter = (*githubactions.Reporter)(nil)
var _ reporterpkg.Reporter = (*runReporters)(nil)
