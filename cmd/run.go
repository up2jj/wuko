package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

func newRunCmd(deps dependencies) *cobra.Command {
	var config runWorkflowConfig
	command := &cobra.Command{
		Use:   "run [NAME|URL|GITHUB] [TARGET]",
		Short: "Run a named or remotely located workflow",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			return runWorkflow(command, deps, args, config)
		},
	}
	command.Flags().StringArrayVar(&config.variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&config.variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&config.environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().BoolVar(&config.dryRun, "dry-run", false, "validate and print steps without running them")
	command.Flags().BoolVar(&config.once, "once", false, "run immediately once, ignoring a declared cron schedule")
	addReporterFlag(command, &config.reporters)
	command.Flags().StringVar(&config.workflowFile, "file", "", "run a workflow from a file path")
	command.ValidArgsFunction = workflowCompletion(deps, true)
	return command
}

type runWorkflowConfig struct {
	variables     []string
	variableFiles []string
	environment   []string
	reporters     []string
	dryRun        bool
	once          bool
	workflowFile  string
	targetName    string
}

func runWorkflow(command *cobra.Command, deps dependencies, args []string, config runWorkflowConfig) (runErr error) {
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	reporters, err := newRunReporters(command, deps, cwd, config.reporters)
	if err != nil {
		return err
	}
	var workflowName string
	var runState *engine.State
	defer func() {
		runErr = errors.Join(runErr, reporters.complete(command.Context(), workflowName, runState, runErr, config.dryRun))
	}()
	diagnostic.Emit(reporters.Diagnostic, diagnostic.Event{Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusStarted, Message: "run workflow", Attributes: []diagnostic.Attribute{diagnostic.Attr("run_dir", cwd), diagnostic.Attr("variable_files", fmt.Sprint(len(config.variableFiles))), diagnostic.Attr("variables", fmt.Sprint(len(config.variables))), diagnostic.Attr("environment", fmt.Sprint(len(config.environment)))}})
	if config.workflowFile != "" && len(args) > 1 {
		return fmt.Errorf("workflow selector and --file cannot be used together")
	}
	if config.workflowFile == "" && len(args) == 0 {
		return fmt.Errorf("workflow name or --file is required")
	}
	vars, err := parseVars(command.Context(), cwd, config.variableFiles, config.variables)
	if err != nil {
		return err
	}
	env, err := parseEnv(config.environment)
	if err != nil {
		return err
	}
	baseEnv, err := invocationEnvironment(command, deps, cwd)
	if err != nil {
		return err
	}
	var target workflowRunTarget
	if config.workflowFile != "" {
		path, err := filepath.Abs(config.workflowFile)
		if err != nil {
			return fmt.Errorf("resolving workflow file %s: %w", config.workflowFile, err)
		}
		target.path = path
		target.targetName = config.targetName
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
		loader = workflow.NewLoader(nil)
	}
	loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Diagnostics: reporters.Diagnostic}
	definition, cleanup, err := target.load(command.Context(), loader, loadOptions)
	if err != nil {
		return err
	}
	workflowName = definition.Name
	remoteDefinitions := make(map[string]bool)
	if target.remote {
		remoteDefinitions[definition.Path] = true
	}
	plan, err := resolveDependencyPlan(command.Context(), definition, loader, loadOptions, cwd, home, configDir)
	if err != nil {
		cleanup()
		return err
	}
	optionsFor := func(definition *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
		localValueDir := ""
		if !remoteDefinitions[definition.Path] {
			localValueDir = filepath.Join(definition.Dir, ".wuko", "values")
		}
		if config.dryRun {
			fmt.Fprintf(command.OutOrStdout(), "Workflow %s (%s)\n", definition.Name, definition.Path)
		}
		return engine.Options{
			Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Stdin: command.InOrStdin(),
			Dependencies:  dependencies,
			LocalValueDir: localValueDir, GlobalValueDir: filepath.Join(configDir, "wuko", "values"),
			Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
			Interactive: interactive(command.InOrStdin()), DryRun: config.dryRun, Progress: reporters.Progress,
			Diagnostics: reporters.Diagnostic,
		}
	}
	engineFor := func() *engine.Engine { return workflowEngine(deps) }
	executePlan := func(ctx context.Context, active *workflow.DependencyPlan) error {
		state, err := executeDependencyPlan(ctx, active, engineFor, optionsFor)
		runState = state
		return err
	}
	if config.dryRun || config.once || definition.Cron == "" {
		defer cleanup()
		return executePlan(command.Context(), plan)
	}

	if err := validateDependencyPlan(command.Context(), plan, engineFor, optionsFor); err != nil {
		cleanup()
		return err
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
			if err := validateDependencyPlan(ctx, active, engineFor, optionsFor); err != nil {
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
		}, now: deps.now, wait: deps.waitUntil,
		stderr: command.ErrOrStderr(), diagnostics: reporters.Diagnostic,
	}
	return runner.run(command.Context(), definition, releaseDependencyPlan(plans, definition, cleanup))
}

func releaseDependencyPlan(plans map[*workflow.Definition]*workflow.DependencyPlan, definition *workflow.Definition, release func()) func() {
	return func() {
		delete(plans, definition)
		release()
	}
}

type workflowRunTarget struct {
	path       string
	locator    string
	remote     bool
	targetName string
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

func requireDirectlyInvokable(definition *workflow.Definition) error {
	if definition.IsInvokable() {
		return nil
	}
	return fmt.Errorf("workflow %q is not directly invokable", definition.Name)
}

func invocationEnvironment(command *cobra.Command, deps dependencies, cwd string) (map[string]string, error) {
	if deps.environment == nil {
		return nil, nil
	}
	environment, err := deps.environment(command.Context(), cwd)
	if err != nil {
		return nil, err
	}
	return environment, nil
}
