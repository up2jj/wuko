package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

func newValidateCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "validate [NAME]",
		Short: "Validate one or all effective workflows",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			var sources []workflow.Source
			if len(args) == 1 {
				source, err := workflow.Find(cwd, home, config, args[0])
				if err != nil {
					return err
				}
				sources = []workflow.Source{source}
			} else {
				sources, err = workflow.Discover(cwd, home, config)
				if err != nil {
					return err
				}
			}
			for _, source := range sources {
				definition, err := workflow.Load(source.Path)
				if err != nil {
					return err
				}
				if err := engine.New(deps.registry).Validate(command.Context(), definition, engine.Options{
					RunDir: cwd, Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
				}); err != nil {
					return err
				}
				fmt.Fprintf(command.OutOrStdout(), "%s: valid\n", source.Name)
			}
			return nil
		},
	}
	command.ValidArgsFunction = workflowCompletion(deps)
	return command
}

func workflowCompletion(deps dependencies) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		cwd, home, config, err := directories(deps)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		sources, err := workflow.Discover(cwd, home, config)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		values := make([]string, 0, len(sources))
		for _, source := range sources {
			values = append(values, source.Name+"\t"+source.Description)
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
