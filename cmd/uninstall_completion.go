package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/workflow"
)

func uninstallWorkflowCompletion(deps dependencies) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cwd, home, _, err := directories(deps)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		global, _ := command.Flags().GetBool("global")
		scope := "local"
		if global {
			scope = "global"
		}
		sources, err := workflow.DiscoverDirectoryForCompletion(workflowStorageDir(cwd, home, global), scope)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var values []string
		seen := make(map[string]bool)
		for _, source := range sources {
			if seen[source.Name] || !strings.HasPrefix(source.Name, prefix) {
				continue
			}
			seen[source.Name] = true
			values = append(values, source.Name+"\t"+source.Description)
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

func uninstallPluginCompletion(deps dependencies) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cwd, home, _, err := directories(deps)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		root := filepath.Join(cwd, ".wuko", "plugins")
		if global, _ := command.Flags().GetBool("global"); global {
			root = filepath.Join(home, ".wuko", "plugins")
		}
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var values []string
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() || !pluginNamePattern.MatchString(name) || !strings.HasPrefix(name, prefix) {
				continue
			}
			// Validation reads the marker and executable; it never launches the plugin.
			if _, err := pluginpkg.ValidateInstallation(filepath.Join(root, name), name); err == nil {
				values = append(values, name)
			}
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
