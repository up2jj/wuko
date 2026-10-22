package cmd

import (
	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/scriptsdk"
)

// newScriptSDKCmd is a private entry point for the injected Bash functions.
// Disabling flag parsing preserves values such as --help and -n as script data.
func newScriptSDKCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:                "__script-sdk",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(command *cobra.Command, args []string) error {
			return scriptsdk.RunBashClient(command.Context(), deps.getenv(scriptsdk.DescriptorEnvironment), args, command.OutOrStdout())
		},
	}
}
