package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
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
			reporter := diagnosticsFor(command, deps, cwd)
			started := time.Now()
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: started, Message: "listing workflows"})
			sources, err := workflow.Discover(cwd, home, config)
			if err != nil {
				diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(started), Error: err})
				return err
			}
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(started), Attributes: []diagnostic.Attribute{diagnostic.Attr("workflows", fmt.Sprint(len(sources)))}})
			for _, source := range sources {
				fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\t%s\n", source.Name, source.Scope, source.Description, source.Path)
			}
			return nil
		},
	}
}
