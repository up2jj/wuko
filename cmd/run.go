package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func newRunCmd(deps dependencies) *cobra.Command {
	var variables, environment []string
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
			if workflowFile != "" && len(args) > 0 {
				return fmt.Errorf("workflow selector and --file cannot be used together")
			}
			if workflowFile == "" && len(args) == 0 {
				return fmt.Errorf("workflow name or --file is required")
			}
			vars, err := parseVars(variables)
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
			var source workflow.Source
			var definition *workflow.Definition
			remoteWorkflow := false
			cleanup := func() {}
			if workflowFile != "" {
				path, err := filepath.Abs(workflowFile)
				if err != nil {
					return fmt.Errorf("resolving workflow file %s: %w", workflowFile, err)
				}
				source = workflow.Source{Path: path}
			} else if workflow.IsRemoteLocator(args[0]) {
				remoteWorkflow = true
				// Remote workflow content is materialized before loading so relative files and
				// workflow metadata behave the same way as for a local workflow file.
				loader := deps.loader
				if loader == nil {
					loader = workflow.NewLoader(nil)
				}
				definition, cleanup, err = loader.LoadRemote(command.Context(), args[0], workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd})
				if err != nil {
					return err
				}
				defer cleanup()
			} else {
				source, err = workflow.Find(cwd, home, config, args[0])
				if err != nil {
					return err
				}
			}
			loader := deps.loader
			if loader == nil {
				loader = workflow.NewLoader(nil)
			}
			if definition == nil {
				definition, err = loader.Load(command.Context(), source.Path, workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd})
				if err != nil {
					return err
				}
			}
			if dryRun {
				fmt.Fprintf(command.OutOrStdout(), "Workflow %s (%s)\n", definition.Name, definition.Path)
			}
			progress := tui.NewProgress(command.ErrOrStderr(), colorEnabled(command.ErrOrStderr()))
			localValueDir := ""
			if !remoteWorkflow {
				localValueDir = filepath.Join(definition.Dir, ".wuko", "values")
			}
			_, err = engine.New(deps.registry).Run(command.Context(), definition, engine.Options{
				Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Stdin: command.InOrStdin(),
				LocalValueDir: localValueDir, GlobalValueDir: filepath.Join(config, "wuko", "values"),
				Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
				Interactive: interactive(command.InOrStdin()), DryRun: dryRun, Progress: progress.Report,
			})
			return err
		},
	}
	command.Flags().StringArrayVar(&variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and print steps without running them")
	command.Flags().StringVar(&workflowFile, "file", "", "run a workflow from a file path")
	command.ValidArgsFunction = workflowCompletion(deps)
	return command
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
