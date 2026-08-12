package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

func newRunCmd(deps dependencies) *cobra.Command {
	var variables, environment []string
	var dryRun bool
	command := &cobra.Command{
		Use:   "run NAME",
		Short: "Run a named workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			source, err := workflow.Find(cwd, home, config, args[0])
			if err != nil {
				return err
			}
			vars, err := parseVars(variables)
			if err != nil {
				return err
			}
			env, err := parseEnv(environment)
			if err != nil {
				return err
			}
			loader := deps.loader
			if loader == nil {
				loader = workflow.NewLoader(nil)
			}
			definition, err := loader.Load(command.Context(), source.Path, workflow.LoadOptions{Vars: vars, Env: env, RunDir: cwd})
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(command.OutOrStdout(), "Workflow %s (%s)\n", definition.Name, definition.Path)
			}
			_, err = engine.New(deps.registry).Run(command.Context(), definition, engine.Options{
				Vars: vars, Env: env, RunDir: cwd, Stdin: command.InOrStdin(),
				Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
				Interactive: interactive(command.InOrStdin()), DryRun: dryRun,
			})
			return err
		},
	}
	command.Flags().StringArrayVar(&variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and print steps without running them")
	command.ValidArgsFunction = workflowCompletion(deps)
	return command
}
