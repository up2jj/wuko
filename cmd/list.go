package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/workflow"
)

func newListCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List effective workflows",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			sources, err := workflow.Discover(cwd, home, config)
			if err != nil {
				return err
			}
			for _, source := range sources {
				fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\n", source.Name, source.Description, source.Path)
			}
			return nil
		},
	}
}
