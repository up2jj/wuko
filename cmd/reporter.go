package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/githubactions"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/useroutput"
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
	{name: "quiet", new: func(command *cobra.Command, deps dependencies, runDir string) (reporterpkg.Reporter, error) {
		reporter := newPlainReporter(command, deps, runDir)
		reporter.quiet = true
		return reporter, nil
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
	{name: "multiplexer", new: func(command *cobra.Command, deps dependencies, _ string) (reporterpkg.Reporter, error) {
		return newMultiplexerProgressReporter(command.ErrOrStderr(), deps.getenv), nil
	}},
}

// runReporters adapts command completion into the safe public reporter outcome. It records the
// latest root workflow event because Engine.Run returns no State after an execution failure.
type runReporters struct {
	command      *cobra.Command
	deps         dependencies
	runDir       string
	explicit     bool
	additional   reporterpkg.Group
	messages     reporterpkg.Reporter
	selection    string
	presentation workflow.ReporterConfig
	group        reporterpkg.Group
	session      *reporterpkg.Session
	now          func() time.Time
	started      time.Time

	mu       sync.Mutex
	finished *engine.ProgressEvent
	latest   runIdentity
}

type runIdentity struct {
	runID           correlation.RunID
	parentRunID     correlation.RunID
	parentStepRunID correlation.StepRunID
}

func (reporters *runReporters) Progress(event engine.ProgressEvent) {
	reporters.captureProgress(event)
	reporters.activeSession().Progress(event)
}

func (reporters *runReporters) captureProgress(event engine.ProgressEvent) {
	if event.Depth == 0 && event.RunID != "" {
		reporters.mu.Lock()
		reporters.latest = identityFromProgress(event)
		reporters.mu.Unlock()
	}
	if event.Kind == engine.WorkflowFinished && event.Depth == 0 {
		reporters.mu.Lock()
		copy := event
		reporters.finished = &copy
		reporters.mu.Unlock()
	}
}

func (reporters *runReporters) Diagnostic(event diagnostic.Event) {
	reporters.captureDiagnostic(event)
	reporters.activeSession().Diagnostic(event)
}

func (reporters *runReporters) captureDiagnostic(event diagnostic.Event) {
	if event.Depth == 0 && event.RunID != "" {
		reporters.mu.Lock()
		reporters.latest = identityFromDiagnostic(event)
		reporters.mu.Unlock()
	}
}

func (reporters *runReporters) Finish(ctx context.Context, outcome reporterpkg.Outcome) error {
	return reporters.activeSession().Finish(ctx, outcome)
}

func (reporters *runReporters) InvocationID() correlation.InvocationID {
	return reporters.activeSession().InvocationID()
}

func (reporters *runReporters) With(additional ...reporterpkg.Reporter) reporterpkg.Reporter {
	view := reporters.activeSession().With(additional...)
	return reporterpkg.Funcs{
		ProgressFunc: func(event engine.ProgressEvent) {
			reporters.captureProgress(event)
			view.Progress(event)
		},
		DiagnosticFunc: func(event diagnostic.Event) {
			reporters.captureDiagnostic(event)
			view.Diagnostic(event)
		},
	}
}

func (reporters *runReporters) activeSession() *reporterpkg.Session {
	reporters.mu.Lock()
	defer reporters.mu.Unlock()
	if reporters.session == nil {
		reporters.session = reporterpkg.NewSession("", reporters.group...)
	}
	return reporters.session
}

func (reporters *runReporters) complete(ctx context.Context, workflowName string, state *engine.State, runErr error, dryRun bool) error {
	finished := time.Now()
	if reporters.now != nil {
		finished = reporters.now()
	}
	started := reporters.started
	if started.IsZero() || finished.Before(started) {
		started = finished
	}
	outcome := reporterpkg.Outcome{
		Presentation: reporters.presentation,
		WorkflowName: workflowName,
		Status:       outcomeStatus(runErr),
		Duration:     finished.Sub(started),
		Err:          runErr,
		DryRun:       dryRun,
	}
	if state != nil {
		outcome.Stats = state.Stats
		outcome.Outputs = workflow.CloneMap(state.Outputs)
		outcome.Replaced = state.Replaced
		applyStatsIdentity(&outcome, state.Stats)
	}
	reporters.mu.Lock()
	terminal := reporters.finished
	latest := reporters.latest
	reporters.mu.Unlock()
	switch {
	case adoptableFinish(terminal, workflowName, outcome.Status, state):
		outcome.Stats = terminal.Stats
		applyProgressIdentity(&outcome, *terminal)
	case outcome.RunID == "":
		applyRunIdentity(&outcome, latest)
	}
	if terminal != nil && terminal.RunID != "" && terminal.RunID == outcome.RunID {
		outcome.Values = terminal.Values
	}
	return reporters.Finish(context.WithoutCancel(ctx), outcome)
}

// adoptableFinish reports whether the recorded root workflow event describes the run being
// completed. The same reporters observe every root workflow of an invocation - dependencies,
// form loads and earlier scheduled iterations - so an event left over from one of those must not
// stand in for a run that produced no state of its own.
func adoptableFinish(finished *engine.ProgressEvent, workflowName string, status engine.ExecutionStatus, state *engine.State) bool {
	if finished == nil || state != nil {
		return false
	}
	return finished.WorkflowName == workflowName && finished.Status == status
}

func identityFromProgress(event engine.ProgressEvent) runIdentity {
	return runIdentity{runID: event.RunID, parentRunID: event.ParentRunID, parentStepRunID: event.ParentStepRunID}
}

func identityFromDiagnostic(event diagnostic.Event) runIdentity {
	return runIdentity{runID: event.RunID, parentRunID: event.ParentRunID, parentStepRunID: event.ParentStepRunID}
}

func applyProgressIdentity(outcome *reporterpkg.Outcome, event engine.ProgressEvent) {
	applyRunIdentity(outcome, identityFromProgress(event))
}

func applyStatsIdentity(outcome *reporterpkg.Outcome, stats engine.RunStats) {
	applyRunIdentity(outcome, runIdentity{runID: stats.RunID, parentRunID: stats.ParentRunID, parentStepRunID: stats.ParentStepRunID})
}

func applyRunIdentity(outcome *reporterpkg.Outcome, identity runIdentity) {
	outcome.RunID = identity.runID
	outcome.ParentRunID = identity.parentRunID
	outcome.ParentStepRunID = identity.parentStepRunID
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
	quiet       bool
	hideSkipped bool
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
	// The session renders messages once, including when several reporters are selected.
	if event.Kind == engine.MessagePrinted {
		return
	}
	event.Quiet = event.Quiet || reporter.quiet
	event.Presentation.HideSkipped = event.Presentation.HideSkipped || reporter.hideSkipped
	reporter.progress.Report(event)
}

func (reporter *plainReporter) Configure(config workflow.ReporterConfig) {
	reporter.hideSkipped = config.HideSkipped
}

func (reporter *plainReporter) Diagnostic(event diagnostic.Event) {
	diagnostic.Emit(reporter.diagnostics, event)
}

func (*plainReporter) Finish(context.Context, reporterpkg.Outcome) error { return nil }

func addReporterFlag(command *cobra.Command, target *[]string) {
	names := runReporterNames()
	command.Flags().StringArrayVar(target, "reporter", nil, fmt.Sprintf(
		"select a run reporter (%s; repeatable; overrides workflow reporter; defaults to plain)", strings.Join(names, " or "),
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

func newRunReporters(command *cobra.Command, deps dependencies, runDir string, names []string, additional ...reporterpkg.Reporter) (*runReporters, error) {
	return newRunReportersWithDefault(command, deps, runDir, names, nil, additional...)
}

func newRunReportersWithDefault(command *cobra.Command, deps dependencies, runDir string, names []string, fallback reporterpkg.Reporter, additional ...reporterpkg.Reporter) (*runReporters, error) {
	reporters := &runReporters{command: command, deps: deps, runDir: runDir, explicit: len(names) > 0, additional: additional}
	group, selection, err := reporters.build(names, fallback)
	if err != nil {
		return nil, err
	}
	now := deps.now
	if now == nil {
		now = time.Now
	}
	reporters.messages = reporterpkg.Funcs{ProgressFunc: func(event engine.ProgressEvent) {
		if event.Kind == engine.MessagePrinted {
			useroutput.Write(command.ErrOrStderr(), event.Message, colorEnabled(command.ErrOrStderr()))
		}
	}}
	reporters.group, reporters.selection = group, selection
	reporters.session = reporterpkg.NewSession("", append(reporterpkg.Group{reporters.messages}, group...)...)
	reporters.now, reporters.started = now, now()
	return reporters, nil
}

// build creates the named reporters followed by the additional ones, and returns the
// selection that decides run-wide presentation (quiet wins when it is selected).
func (reporters *runReporters) build(names []string, fallback reporterpkg.Reporter) (reporterpkg.Group, string, error) {
	if containsReporter(names, "plain") && containsReporter(names, "quiet") {
		return nil, "", fmt.Errorf("plain and quiet reporters are mutually exclusive")
	}
	group := make(reporterpkg.Group, 0, len(names)+len(reporters.additional)+1)
	if len(names) == 0 {
		if fallback != nil {
			group = append(group, fallback)
		} else {
			names = []string{"plain"}
		}
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		factory, found := runReporterFactoryFor(name)
		if !found {
			return nil, "", unknownReporterError(name)
		}
		created, err := factory(reporters.command, reporters.deps, reporters.runDir)
		if err != nil {
			return nil, "", err
		}
		group = append(group, created)
	}
	group = append(group, reporters.additional...)
	selection := "plain"
	if len(names) > 0 {
		selection = names[0]
	}
	if containsReporter(names, "quiet") {
		selection = "quiet"
	}
	return group, selection, nil
}

func unknownReporterError(name string) error {
	return fmt.Errorf("unknown reporter %q; expected %s", name, strings.Join(runReporterNames(), " or "))
}

func containsReporter(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

// configure binds the selected root workflow without replacing CLI-selected reporters.
func (reporters *runReporters) configure(config workflow.ReporterConfig) error {
	if !reporters.explicit && config.Type != "" {
		group, selection, err := reporters.build([]string{config.Type}, nil)
		if err != nil {
			return err
		}
		reporters.group, reporters.selection = group, selection
		reporters.activeSession().SetReporters(append(reporterpkg.Group{reporters.messages}, reporters.group...)...)
	}
	config.Type = reporters.selection
	reporters.presentation = config
	reporters.group.Configure(config)
	return nil
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
