package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func newRunCmd(deps dependencies) *cobra.Command {
	var variables, variableFiles, environment []string
	var dryRun bool
	var workflowFile string
	command := &cobra.Command{
		Use:   "run [NAME|URL|GITHUB]",
		Short: "Run a named or remotely located workflow",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			reporter := diagnosticsFor(command, deps, cwd)
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusStarted, Message: "run workflow", Attributes: []diagnostic.Attribute{diagnostic.Attr("run_dir", cwd), diagnostic.Attr("variable_files", fmt.Sprint(len(variableFiles))), diagnostic.Attr("variables", fmt.Sprint(len(variables))), diagnostic.Attr("environment", fmt.Sprint(len(environment)))}})
			if workflowFile != "" && len(args) > 0 {
				return fmt.Errorf("workflow selector and --file cannot be used together")
			}
			if workflowFile == "" && len(args) == 0 {
				return fmt.Errorf("workflow name or --file is required")
			}
			vars, err := parseVars(command.Context(), cwd, variableFiles, variables)
			if err != nil {
				return err
			}
			env, err := parseEnv(environment)
			if err != nil {
				return err
			}
			baseEnv, err := invocationEnvironment(command, deps, cwd)
			if err != nil {
				return err
			}
			var target workflowRunTarget
			if workflowFile != "" {
				path, err := filepath.Abs(workflowFile)
				if err != nil {
					return fmt.Errorf("resolving workflow file %s: %w", workflowFile, err)
				}
				target.path = path
			} else if workflow.IsRemoteLocator(args[0]) {
				target = workflowRunTarget{locator: args[0], remote: true}
			} else {
				discoveryStarted := time.Now()
				diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: discoveryStarted, Message: args[0]})
				source, err := workflow.Find(cwd, home, config, args[0])
				if err != nil {
					diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(discoveryStarted), Error: err})
					return err
				}
				diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(discoveryStarted), Location: diagnostic.Location{Source: source.Path}, Message: source.Name})
				target.path = source.Path
			}
			loader := deps.loader
			if loader == nil {
				loader = workflow.NewLoader(nil)
			}
			loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Diagnostics: reporter}
			definition, cleanup, err := target.load(command.Context(), loader, loadOptions)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(command.OutOrStdout(), "Workflow %s (%s)\n", definition.Name, definition.Path)
			}
			progress := tui.NewProgress(command.ErrOrStderr(), colorEnabled(command.ErrOrStderr()))
			optionsFor := func(definition *workflow.Definition) engine.Options {
				localValueDir := ""
				if !target.remote {
					localValueDir = filepath.Join(definition.Dir, ".wuko", "values")
				}
				return engine.Options{
					Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Stdin: command.InOrStdin(),
					LocalValueDir: localValueDir, GlobalValueDir: filepath.Join(config, "wuko", "values"),
					Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
					Interactive: interactive(command.InOrStdin()), DryRun: dryRun, Progress: progress.Report,
					Diagnostics: reporter,
				}
			}
			execute := func(ctx context.Context, definition *workflow.Definition) error {
				_, err := engine.New(deps.registry).Run(ctx, definition, optionsFor(definition))
				return err
			}
			if dryRun || definition.Cron == "" {
				defer cleanup()
				return execute(command.Context(), definition)
			}

			if err := engine.New(deps.registry).Validate(command.Context(), definition, optionsFor(definition)); err != nil {
				cleanup()
				return err
			}
			runner := scheduledRunner{
				load: func(ctx context.Context) (*workflow.Definition, func(), error) {
					definition, release, err := target.load(ctx, loader, loadOptions)
					if err != nil {
						return nil, func() {}, err
					}
					if err := engine.New(deps.registry).Validate(ctx, definition, optionsFor(definition)); err != nil {
						release()
						return nil, func() {}, err
					}
					return definition, release, nil
				},
				execute: execute, now: deps.now, wait: deps.waitUntil,
				stderr: command.ErrOrStderr(), diagnostics: reporter,
			}
			return runner.run(command.Context(), definition, cleanup)
		},
	}
	command.Flags().StringArrayVar(&variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and print steps without running them")
	command.Flags().StringVar(&workflowFile, "file", "", "run a workflow from a file path")
	command.ValidArgsFunction = workflowCompletion(deps)
	return command
}

type workflowRunTarget struct {
	path    string
	locator string
	remote  bool
}

func (target workflowRunTarget) load(ctx context.Context, loader *workflow.Loader, options workflow.LoadOptions) (*workflow.Definition, func(), error) {
	if target.remote {
		return loader.LoadRemote(ctx, target.locator, options)
	}
	definition, err := loader.Load(ctx, target.path, options)
	return definition, func() {}, err
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
