package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/process"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func newRunCmd(deps dependencies) *cobra.Command {
	var config runWorkflowConfig
	command := &cobra.Command{
		Use:   "run [NAME|URL|GITHUB] [TARGET]",
		Short: "Run a named or remotely located workflow",
		Example: `  wuko run check
  wuko run deploy staging
  wuko run --file ./deploy.yaml staging
  ./generate-workflow | wuko run --file -
  wuko run check --dry-run
  wuko run check --var 'code="123"'
  wuko run bootstrap --yes`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			return dispatchRun(command, deps, args, config)
		},
	}
	addRunFlags(command, &config)
	command.ValidArgsFunction = workflowCompletion(deps, workflowCompletionOptions{invokableOnly: true})
	return command
}

func dispatchRun(command *cobra.Command, deps dependencies, args []string, config runWorkflowConfig) error {
	if handled, err := runWithResourceLimits(command, deps, args, config); handled {
		return err
	}
	if config.pick {
		// A picker may run several workflows, so it cannot produce one stdout report.
		if config.reportJSON == "-" {
			return fmt.Errorf("--pick cannot be used with --report-json -")
		}
		return runGitHubWorkflowPicker(command, deps, args, config)
	}
	config.replaceProcess = true
	return runWorkflow(command, deps, args, config)
}

func addRunFlags(command *cobra.Command, config *runWorkflowConfig) {
	command.Flags().StringArrayVar(&config.variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&config.variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&config.environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().BoolVar(&config.dryRun, "dry-run", false, "validate and print steps without running them")
	command.Flags().BoolVar(&config.once, "once", false, "run immediately once, ignoring a declared cron schedule")
	command.Flags().BoolVar(&config.prompts.Yes, "yes", false, "answer every tui_confirm prompt with true")
	command.Flags().BoolVar(&config.prompts.Defaults, "defaults", false, "answer every prompt that declares a default with that default")
	addReporterFlag(command, &config.reporters)
	command.Flags().StringVar(&config.reportJSON, "report-json", "", "write the final execution report as JSON to a path, or - for stdout")
	command.Flags().StringVar(&config.workflowFile, "file", "", "run a workflow from a file path, or - for stdin")
	command.Flags().BoolVar(&config.pick, "pick", false, "select a workflow from a GitHub repository or tree")
	command.Flags().StringVar(&config.cpus, "cpus", "", "limit aggregate local workflow CPU use in cores (for example 1.5)")
	command.Flags().StringVar(&config.memory, "memory", "", "limit aggregate local workflow memory (for example 512MiB)")
}

// Configuration is resolved before argument validation, earlier than runWorkflow's
// reporting setup. Preserve the stdout report contract for failures at that boundary.
func reportEarlyRunFailure(command *cobra.Command, runErr error) error {
	if command.Flags().Lookup("report-json") == nil {
		return runErr
	}
	destination, _ := command.Flags().GetString("report-json")
	if destination != "-" {
		return runErr
	}
	report, err := reporterpkg.NewJSONWriter(command.OutOrStdout())
	if err != nil {
		return errors.Join(runErr, err)
	}
	dryRun, _ := command.Flags().GetBool("dry-run")
	return errors.Join(runErr, report.Finish(context.WithoutCancel(command.Context()), reporterpkg.Outcome{
		InvocationID: correlation.NewInvocationID(), Status: engine.StatusFailed, Err: runErr, DryRun: dryRun,
	}))
}

type runWorkflowConfig struct {
	cpus          string
	memory        string
	variables     []string
	variableFiles []string
	environment   []string
	reporters     []string
	reportJSON    string
	dryRun        bool
	once          bool
	// replaceProcess lets the selected workflow end the invocation by replacing the Wuko
	// process. Only commands whose run is the last thing the invocation does set it.
	replaceProcess  bool
	workflowFile    string
	remoteLocator   string
	pick            bool
	targetName      string
	providerValues  map[string]map[string]any
	nonInteractive  bool
	prompts         step.PromptAnswers
	defaultReporter reporterpkg.Reporter
	returnObserver  *returnDestinationObserver
	resultObserver  func(*engine.State)
	// stepStderr additionally receives the stderr that steps stream, and progressObserver every
	// progress event, so an embedding command can explain a failure in its own error.
	stepStderr       io.Writer
	progressObserver func(engine.ProgressEvent)
}

// runLocalWorkflowForOutputs executes one trusted local workflow with the supplied directory as
// its run directory and returns its declared outputs. It is shared by command flows that use a
// workflow as an implementation detail instead of exposing the general `run` command surface.
func runLocalWorkflowForOutputs(command *cobra.Command, deps dependencies, path, runDir string, variables []string) (map[string]any, error) {
	localDeps := deps
	localDeps.cwd = func() (string, error) { return runDir, nil }
	var outputs map[string]any
	tail := &stderrTail{}
	err := runWorkflow(command, localDeps, nil, runWorkflowConfig{
		variables:    variables,
		workflowFile: path,
		once:         true,
		resultObserver: func(state *engine.State) {
			if state != nil {
				outputs = state.Outputs
			}
		},
		stepStderr: tail,
		progressObserver: func(event engine.ProgressEvent) {
			// Each step or retry starts a fresh tail so a failure shows its own output, not that of
			// an earlier step that succeeded.
			if event.Kind == engine.StepStarted || event.Kind == engine.AttemptStarted {
				tail.Reset()
			}
		},
	})
	if err != nil {
		if lines := tail.Lines(); lines != "" {
			return nil, fmt.Errorf("%w\nlast stderr lines:\n  %s", err, strings.ReplaceAll(lines, "\n", "\n  "))
		}
		return nil, err
	}
	return outputs, nil
}

type returnDestinationObserver struct {
	mutex       sync.Mutex
	destination workflow.ReturnDestination
}

func (observer *returnDestinationObserver) observe(destination workflow.ReturnDestination) {
	if observer == nil {
		return
	}
	observer.mutex.Lock()
	observer.destination = destination
	observer.mutex.Unlock()
}

// reset forgets the previous attempt's destination. A scheduled run reuses one observer across
// occurrences, so a later destinationless return must not read as the earlier attempt's request.
func (observer *returnDestinationObserver) reset() {
	if observer == nil {
		return
	}
	observer.mutex.Lock()
	observer.destination = ""
	observer.mutex.Unlock()
}

func (observer *returnDestinationObserver) requested(destination workflow.ReturnDestination) bool {
	if observer == nil {
		return false
	}
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	return observer.destination == destination
}

func runWorkflow(command *cobra.Command, deps dependencies, args []string, config runWorkflowConfig) (runErr error) {
	initialContext := command.Context()
	defer command.SetContext(initialContext)
	if config.dryRun {
		command.SetContext(workflow.ContextWithPluginLockPolicy(command.Context(), workflow.PluginLockPolicy{ReadOnly: true}))
	}
	workflowOutput := command.OutOrStdout()
	var additionalReporters []reporterpkg.Reporter
	reportsReady := false
	if config.reportJSON == "-" {
		streamReporter, err := reporterpkg.NewJSONWriter(command.OutOrStdout())
		if err != nil {
			return err
		}
		additionalReporters = append(additionalReporters, streamReporter)
		workflowOutput = command.ErrOrStderr()
		// A failure before the reporters exist still owes the caller its one stdout report.
		defer func() {
			if runErr != nil && !reportsReady {
				runErr = errors.Join(runErr, streamReporter.Finish(context.WithoutCancel(command.Context()), reporterpkg.Outcome{
					InvocationID: correlation.NewInvocationID(), Status: engine.StatusFailed, Err: runErr, DryRun: config.dryRun,
				}))
			}
		}()
	}
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	if config.reportJSON != "" && config.reportJSON != "-" {
		path := config.reportJSON
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		fileReporter, err := reporterpkg.NewJSONFile(path)
		if err != nil {
			return err
		}
		additionalReporters = append(additionalReporters, fileReporter)
	}
	if config.progressObserver != nil {
		additionalReporters = append(additionalReporters, reporterpkg.Funcs{ProgressFunc: config.progressObserver})
	}
	stepStderr := command.ErrOrStderr()
	if config.stepStderr != nil {
		stepStderr = io.MultiWriter(stepStderr, config.stepStderr)
	}
	reporters, err := newRunReportersWithDefault(command, deps, cwd, config.reporters, config.defaultReporter, additionalReporters...)
	if err != nil {
		return err
	}
	reportsReady = true
	var workflowName string
	var runState *engine.State
	var attemptErr error
	defer func() {
		if config.resultObserver != nil {
			config.resultObserver(runState)
		}
	}()
	defer func() {
		state := completedRunState(runState, attemptErr, runErr)
		runErr = errors.Join(runErr, reporters.complete(command.Context(), workflowName, state, runErr, config.dryRun))
	}()
	diagnostic.Emit(reporters.Diagnostic, diagnostic.Event{Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusStarted, Message: "run workflow", Attributes: []diagnostic.Attribute{diagnostic.Attr("run_dir", cwd), diagnostic.Attr("variable_files", fmt.Sprint(len(config.variableFiles))), diagnostic.Attr("variables", fmt.Sprint(len(config.variables))), diagnostic.Attr("environment", fmt.Sprint(len(config.environment)))}})
	if config.workflowFile != "" && len(args) > 1 {
		return fmt.Errorf("with --file, pass at most one positional TARGET; usage: wuko run --file PATH [TARGET]")
	}
	if config.workflowFile == "" && len(args) == 0 {
		return fmt.Errorf("workflow name or --file is required; use wuko list to find workflows")
	}
	vars, err := parseVars(command.Context(), cwd, config.variableFiles, config.variables)
	if err != nil {
		return err
	}
	env, err := parseEnv(config.environment)
	if err != nil {
		return err
	}
	invocationEnv, err := invocationEnvironment(command, deps, cwd)
	if err != nil {
		return err
	}
	baseEnv, environmentLoaders := environmentValues(invocationEnv)
	providers, err := invocationProviders(command, deps, baseEnv)
	if err != nil {
		return err
	}
	if providers.Values == nil {
		providers.Values = make(map[string]map[string]any)
	}
	for name, value := range config.providerValues {
		providers.Values[name] = value
	}
	var target workflowRunTarget
	if config.workflowFile != "" {
		target.targetName = config.targetName
		if config.workflowFile == "-" {
			data, err := io.ReadAll(command.InOrStdin())
			if err != nil {
				return fmt.Errorf("reading workflow from stdin: %w", err)
			}
			target.stdinData = data
			target.stdinBaseDir = cwd
			target.fromStdin = true
		} else {
			path, err := filepath.Abs(config.workflowFile)
			if err != nil {
				return fmt.Errorf("resolving workflow file %s: %w", config.workflowFile, err)
			}
			target.path = path
			target.remote = config.remoteLocator != ""
			target.locator = config.remoteLocator
		}
		if len(args) == 1 {
			target.targetName = args[0]
		}
	} else if workflow.IsRemoteLocator(args[0]) {
		target = workflowRunTarget{locator: args[0], remote: true}
		if len(args) == 2 {
			target.targetName = args[1]
		}
	} else {
		discoveryStarted := time.Now()
		diagnostic.Emit(reporters.Diagnostic, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: discoveryStarted, Message: args[0]})
		source, err := workflow.Find(cwd, home, configDir, args[0])
		if err != nil {
			diagnostic.Emit(reporters.Diagnostic, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(discoveryStarted), Error: err})
			return err
		}
		diagnostic.Emit(reporters.Diagnostic, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(discoveryStarted), Location: diagnostic.Location{Source: source.Path}, Message: source.Name})
		target.path = source.Path
		if len(args) == 2 {
			target.targetName = args[1]
		}
	}
	loader := deps.loader
	if loader == nil {
		loader = defaultWorkflowLoader(deps.plugins)
	}
	stdin := command.InOrStdin()
	isInteractive := interactive(stdin)
	if config.nonInteractive {
		stdin = bytes.NewReader(nil)
		isInteractive = false
	}
	if target.fromStdin {
		stdin = nil
		isInteractive = false
	}
	loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, RunDir: cwd, Diagnostics: reporters.Diagnostic, Providers: providers,
		Stdin: stdin, Stdout: workflowOutput, Stderr: command.ErrOrStderr(), Interactive: isInteractive,
		EnsureSecretAuth: true}
	if target.remote {
		command.SetContext(workflow.ContextWithPluginLockPolicy(command.Context(), workflow.PluginLockPolicy{Remote: true}))
	}
	definition, cleanup, err := target.load(command.Context(), loader, loadOptions)
	if err != nil {
		return err
	}
	workflowName = definition.Name
	elevationAllowed := !target.remote && !target.fromStdin
	remoteDefinitions := make(map[string]bool)
	if target.remote {
		remoteDefinitions[definition.Path] = true
	}
	plan, err := resolveDependencyPlan(command.Context(), definition, loader, loadOptions, cwd, home, configDir)
	if err != nil {
		cleanup()
		return err
	}
	// Only the selected workflow of a run that ends with this invocation may replace the
	// process: never a dependency, and never a schedule that must keep running.
	var replaceProcess func(process.Replacement) error
	if config.dryRun {
		replaceProcess = previewProcessReplacement
	} else if replacement := processReplacementFrom(command.Context()); replacement != nil && config.replaceProcess {
		replaceProcess = replacement.request
	}
	rootDefinition, scheduled := plan.Root.Definition, definition.Cron != "" && !config.once && !config.dryRun
	// preflightOptionsFor has no output side effects; preflight runs it for
	// every plan node before execution prints each dry-run header once.
	preflightOptionsFor := func(definition *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
		localValueDir := ""
		if !remoteDefinitions[definition.Path] {
			localValueDir = filepath.Join(definition.Dir, ".wuko", "values")
		}
		return engine.Options{
			InvocationID: reporters.InvocationID(),
			Vars:         vars, Env: env, BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, RunDir: cwd, Stdin: stdin, Providers: providers,
			Dependencies:  dependencies,
			LocalValueDir: localValueDir, GlobalValueDir: filepath.Join(configDir, "wuko", "values"),
			Stdout: workflowOutput, Stderr: stepStderr,
			Interactive: isInteractive, Prompts: config.prompts, DryRun: config.dryRun, Progress: reporters.Progress,
			ElevationAllowed: elevationAllowed && installedWorkflowMayElevate(definition.Path),
			Diagnostics:      reporters.Diagnostic,
			ReplaceProcess:   replacementFor(definition, rootDefinition, scheduled, replaceProcess),
		}
	}
	optionsFor := func(definition *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
		if config.dryRun {
			fmt.Fprintf(workflowOutput, "Workflow %s (%s)\n", definition.Name, workflowDisplaySource(definition))
		}
		return preflightOptionsFor(definition, dependencies)
	}
	engineFor := func() *engine.Engine { return workflowEngine(deps) }
	if err := preflightDependencyPlan(command.Context(), plan, engineFor, preflightOptionsFor); err != nil {
		cleanup()
		return err
	}
	executePlan := func(ctx context.Context, active *workflow.DependencyPlan) error {
		config.returnObserver.reset()
		state, err := executeDependencyPlan(ctx, active, engineFor, func(definition *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
			options := optionsFor(definition, dependencies)
			if definition == active.Root.Definition && config.returnObserver != nil {
				options.OnReturn = config.returnObserver.observe
			}
			return options
		})
		runState, attemptErr = state, err
		return err
	}
	if config.dryRun || config.once || definition.Cron == "" {
		defer cleanup()
		return executePlan(command.Context(), plan)
	}

	plans := map[*workflow.Definition]*workflow.DependencyPlan{definition: plan}
	runner := scheduledRunner{
		load: func(ctx context.Context) (*workflow.Definition, func(), error) {
			definition, release, err := target.load(ctx, loader, loadOptions)
			if err != nil {
				return nil, func() {}, err
			}
			if target.remote {
				remoteDefinitions[definition.Path] = true
			}
			active, err := resolveDependencyPlan(ctx, definition, loader, loadOptions, cwd, home, configDir)
			if err != nil {
				release()
				return nil, func() {}, err
			}
			if err := preflightDependencyPlan(ctx, active, engineFor, preflightOptionsFor); err != nil {
				release()
				return nil, func() {}, err
			}
			plans[definition] = active
			return definition, releaseDependencyPlan(plans, definition, release), nil
		},
		execute: func(ctx context.Context, definition *workflow.Definition) error {
			active := plans[definition]
			delete(plans, definition)
			if active == nil {
				return fmt.Errorf("scheduled workflow %q dependency plan is unavailable", definition.Name)
			}
			return executePlan(ctx, active)
		},
		// A failed occurrence keeps its schedule, exactly as a direct scheduled run does; only
		// the destination the host honors ends the loop.
		stopAfterAttempt: func(error) bool {
			return config.returnObserver.requested(workflow.ReturnDestinationPicker)
		},
		now: deps.now, wait: deps.waitUntil,
		stderr: command.ErrOrStderr(), diagnostics: reporters.Diagnostic,
	}
	return runner.run(command.Context(), definition, releaseDependencyPlan(plans, definition, cleanup))
}

// completedRunState returns the run state that describes how the invocation ended. A scheduled
// workflow keeps running after each occurrence, so state left over from an earlier attempt must
// not stand in for an invocation that ended for an unrelated reason - a broken timer, say. When
// it is discarded the reporter falls back to the terminal progress event, which carries its own
// workflow name and status and is rejected when it does not describe this outcome.
func completedRunState(state *engine.State, attemptErr, runErr error) *engine.State {
	switch {
	case state == nil:
		return nil
	case attemptErr == nil && runErr == nil:
		return state
	case attemptErr != nil && runErr != nil && errors.Is(runErr, attemptErr):
		return state
	default:
		return nil
	}
}

func releaseDependencyPlan(plans map[*workflow.Definition]*workflow.DependencyPlan, definition *workflow.Definition, release func()) func() {
	return func() {
		delete(plans, definition)
		release()
	}
}

type workflowRunTarget struct {
	path         string
	locator      string
	stdinData    []byte
	stdinBaseDir string
	remote       bool
	fromStdin    bool
	targetName   string
}

func (target workflowRunTarget) decode(ctx context.Context, loader *workflow.Loader, options workflow.LoadOptions) (*workflow.Definition, func(), error) {
	if target.remote && target.path != "" {
		definition, err := loader.DecodeMaterializedRemote(ctx, target.path, target.locator, options)
		return definition, func() {}, err
	}
	if target.remote {
		return loader.DecodeRemote(ctx, target.locator, options)
	}
	if target.fromStdin {
		definition, err := loader.DecodeStdinContext(ctx, bytes.NewReader(target.stdinData), target.stdinBaseDir, options)
		return definition, func() {}, err
	}
	definition, err := loader.DecodeContext(ctx, target.path, options)
	return definition, func() {}, err
}

func (target workflowRunTarget) load(ctx context.Context, loader *workflow.Loader, options workflow.LoadOptions) (*workflow.Definition, func(), error) {
	definition, cleanup, err := target.decode(ctx, loader, options)
	if err != nil {
		return nil, func() {}, err
	}
	definition, err = definition.SelectTarget(target.targetName)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := requireDirectlyInvokable(definition); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if err := loader.Prepare(ctx, definition, options); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return definition, cleanup, nil
}

// workflowDisplaySource names the workflow the way its diagnostics do. A workflow read from
// standard input has no file, so its declaration path is a placeholder that must not be printed.
func workflowDisplaySource(definition *workflow.Definition) string {
	if definition.Location.Source != "" {
		return definition.Location.Source
	}
	return definition.Path
}

func requireDirectlyInvokable(definition *workflow.Definition) error {
	if definition.IsInvokable() {
		return nil
	}
	return fmt.Errorf("workflow %q is not directly invokable", definition.Name)
}
