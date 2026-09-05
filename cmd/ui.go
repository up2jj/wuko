package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/form"
	reporterpkg "github.com/up2jj/wuko/reporter"
	"github.com/up2jj/wuko/webui"
	"github.com/up2jj/wuko/workflow"
)

type uiWorkflowConfig struct {
	variables     []string
	variableFiles []string
	environment   []string
	reporters     []string
	workflowFile  string
	targetName    string
	noOpen        bool
}

func newUICmd(deps dependencies) *cobra.Command {
	var config uiWorkflowConfig
	command := &cobra.Command{
		Use:   "ui [NAME|URL|GITHUB] [TARGET]",
		Short: "Run a workflow through its local browser form",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			return runWorkflowUI(command, deps, args, config)
		},
	}
	command.Flags().StringArrayVar(&config.variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&config.variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&config.environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	addReporterFlag(command, &config.reporters)
	command.Flags().StringVar(&config.workflowFile, "file", "", "run a workflow form from a file path")
	command.Flags().BoolVar(&config.noOpen, "no-open", false, "print the local URL without opening a browser")
	command.ValidArgsFunction = workflowCompletion(deps, true)
	return command
}

func runWorkflowUI(command *cobra.Command, deps dependencies, args []string, config uiWorkflowConfig) (runErr error) {
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	reporters, err := newRunReporters(command, deps, cwd, config.reporters)
	if err != nil {
		return err
	}
	finishReporters := finishReportersOnce(reporters)
	var workflowName string
	defer func() {
		if finishErr, called := finishReporters(command.Context(), workflowName, nil, runErr, false); called {
			runErr = errors.Join(runErr, finishErr)
		}
	}()
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
	invocationEnv, err := invocationEnvironment(command, deps, cwd)
	if err != nil {
		return err
	}
	baseEnv, environmentLoaders := environmentValues(invocationEnv)
	providers, err := invocationProviders(command, deps, baseEnv)
	if err != nil {
		return err
	}
	target, err := resolveUIRunTarget(cwd, home, configDir, args, config.workflowFile)
	if err != nil {
		return err
	}
	if config.workflowFile != "" && len(args) == 0 {
		target.targetName = config.targetName
	}
	loader := deps.loader
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, RunDir: cwd, Diagnostics: reporters.Diagnostic, Providers: providers,
		Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(), Interactive: interactive(command.InOrStdin()),
		EnsureSecretAuth: true}
	definition, cleanup, err := target.decode(command.Context(), loader, loadOptions)
	if err != nil {
		return err
	}
	defer cleanup()
	definition, err = definition.SelectTarget(target.targetName)
	if err != nil {
		return err
	}
	workflowName = definition.Name
	if err := requireDirectlyInvokable(definition); err != nil {
		return err
	}
	if !definition.HasForm() {
		return fmt.Errorf("workflow %q does not declare a form", definition.Name)
	}
	effectiveVars, _, err := workflow.PrepareValues(definition, loadOptions)
	if err != nil {
		return err
	}
	formDefinition, err := form.Decode(&definition.Form, definition.Vars)
	if err != nil {
		return fmt.Errorf("workflow %q: %w", definition.Name, err)
	}
	load := formLoadFunc(command, deps, loader, definition, formDefinition, loadOptions, reporters, cwd, configDir, target.remote)
	execute := func(ctx context.Context, submitted map[string]any, emit func(webui.Progress)) webui.Result {
		activeVars := maps.Clone(vars)
		maps.Copy(activeVars, submitted)
		activeOptions := loadOptions
		activeOptions.Vars = activeVars
		started := time.Now()
		result := webui.Result{WorkflowName: definition.Name, WorkflowDescription: definition.Description, Outputs: map[string]any{}}
		if err := loader.Prepare(ctx, definition, activeOptions); err != nil {
			result.Err = err
			result.Duration = time.Since(started)
			return result
		}
		plan, err := resolveDependencyPlan(ctx, definition, loader, activeOptions, cwd, home, configDir)
		if err != nil {
			result.Err = err
			result.Duration = time.Since(started)
			return result
		}
		browser := browserReporter{stage: "workflow", emit: emit}
		activeReporters := reporters.With(browser)
		remoteDefinitions := map[string]bool{definition.Path: target.remote}
		optionsFor := func(item *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
			localValueDir := ""
			if !remoteDefinitions[item.Path] {
				localValueDir = filepath.Join(item.Dir, ".wuko", "values")
			}
			return engine.Options{
				InvocationID: reporters.InvocationID(),
				Vars:         activeVars, Env: env, BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, Dependencies: dependencies, RunDir: cwd, Providers: providers,
				Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
				Interactive: false, Progress: activeReporters.Progress, Diagnostics: activeReporters.Diagnostic,
				LocalValueDir: localValueDir, GlobalValueDir: filepath.Join(configDir, "wuko", "values"),
			}
		}
		state, engineErr := executeDependencyPlan(ctx, plan, func() *engine.Engine { return workflowEngine(deps) }, optionsFor)
		finishErr, _ := finishReporters(ctx, definition.Name, state, engineErr, false)
		result.Err = errors.Join(engineErr, finishErr)
		result.Duration = time.Since(started)
		if state != nil {
			result.Outputs = workflow.CloneMap(state.Outputs)
			result.Stats = webSummary(state.Stats)
		}
		return result
	}

	openURL := deps.openURL
	if openURL == nil {
		openURL = openBrowser
	}
	return webui.Run(command.Context(), formDefinition, effectiveVars, load, execute, webui.Options{
		OpenURL: openURL, NoOpen: config.noOpen, Output: command.ErrOrStderr(),
	})
}

func finishReportersOnce(reporters *runReporters) func(context.Context, string, *engine.State, error, bool) (error, bool) {
	var once sync.Once
	var finishErr error
	return func(ctx context.Context, workflowName string, state *engine.State, runErr error, dryRun bool) (error, bool) {
		called := false
		once.Do(func() {
			called = true
			finishErr = reporters.complete(ctx, workflowName, state, runErr, dryRun)
		})
		return finishErr, called
	}
}

func resolveUIRunTarget(cwd, home, configDir string, args []string, filename string) (workflowRunTarget, error) {
	if filename != "" {
		path, err := filepath.Abs(filename)
		if err != nil {
			return workflowRunTarget{}, fmt.Errorf("resolving workflow file %s: %w", filename, err)
		}
		target := workflowRunTarget{path: path}
		if len(args) == 1 {
			target.targetName = args[0]
		}
		return target, nil
	}
	if workflow.IsRemoteLocator(args[0]) {
		target := workflowRunTarget{locator: args[0], remote: true}
		if len(args) == 2 {
			target.targetName = args[1]
		}
		return target, nil
	}
	source, err := workflow.Find(cwd, home, configDir, args[0])
	if err != nil {
		return workflowRunTarget{}, err
	}
	target := workflowRunTarget{path: source.Path}
	if len(args) == 2 {
		target.targetName = args[1]
	}
	return target, nil
}

func formLoadFunc(command *cobra.Command, deps dependencies, loader *workflow.Loader, owner *workflow.Definition, declaration *form.Definition, options workflow.LoadOptions, reporters *runReporters, cwd, configDir string, remote bool) webui.LoadFunc {
	if declaration.Load == nil {
		return nil
	}
	return func(ctx context.Context, emit func(webui.Progress)) (map[string]any, error) {
		steps := append([]workflow.Step(nil), declaration.Load.Steps...)
		steps = append(steps, workflow.Step{Return: &workflow.ReturnControl{Outputs: maps.Clone(declaration.Load.Outputs)}})
		definition := &workflow.Definition{
			Version: owner.Version, Name: owner.Name + " form data", Description: owner.Description,
			Templates: owner.Templates, Vars: workflow.CloneMap(owner.Vars), Env: maps.Clone(owner.Env),
			Steps: steps, Finally: append([]workflow.Step(nil), declaration.Load.Finally...),
			Path: owner.Path, Dir: owner.Dir, Location: owner.Location,
		}
		if err := loader.Prepare(ctx, definition, options); err != nil {
			return nil, err
		}
		browser := browserReporter{stage: "form", emit: emit}
		activeReporters := reporters.With(browser)
		localValueDir := ""
		if !remote {
			localValueDir = filepath.Join(definition.Dir, ".wuko", "values")
		}
		state, err := workflowEngine(deps).Run(ctx, definition, engine.Options{
			InvocationID: reporters.InvocationID(),
			Vars:         options.Vars, Env: options.Env, BaseEnv: options.BaseEnv, EnvironmentLoaders: options.EnvironmentLoaders, RunDir: cwd, Providers: options.Providers,
			Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
			Interactive: false, Progress: activeReporters.Progress, Diagnostics: activeReporters.Diagnostic,
			LocalValueDir: localValueDir, GlobalValueDir: filepath.Join(configDir, "wuko", "values"),
		})
		if err != nil {
			return nil, err
		}
		return workflow.CloneMap(state.Outputs), nil
	}
}

type browserReporter struct {
	stage string
	emit  func(webui.Progress)
}

func (reporter browserReporter) Progress(event engine.ProgressEvent) {
	reporter.emit(webui.Progress{
		InvocationID: event.InvocationID, RunID: event.RunID, ParentRunID: event.ParentRunID,
		ParentStepRunID: event.ParentStepRunID, StepRunID: event.StepRunID, Sequence: event.Sequence,
		Stage: reporter.stage, Kind: string(event.Kind), Status: string(event.Status), StepID: event.StepID,
		StepType: event.StepType, Index: event.Index, Total: event.Total, Attempt: event.Attempt,
		Duration: event.Duration.String(),
	})
}

func (browserReporter) Diagnostic(diagnostic.Event) {}
func (browserReporter) Finish(context.Context, reporterpkg.Outcome) error {
	return nil
}

func webSummary(stats engine.RunStats) webui.Summary {
	return webui.Summary{
		Total: stats.Total, Succeeded: stats.Succeeded, Failed: stats.Failed, Skipped: stats.Skipped,
		Canceled: stats.Canceled, TimedOut: stats.TimedOut, Attempts: stats.Attempts, Retries: stats.Retries,
	}
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("opening %s: %w", target, err)
	}
	return nil
}
