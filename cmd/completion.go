package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/workflow"
	"go.yaml.in/yaml/v3"
)

func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(command *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return command.Root().GenBashCompletion(command.OutOrStdout())
			case "zsh":
				return command.Root().GenZshCompletion(command.OutOrStdout())
			case "fish":
				return command.Root().GenFishCompletion(command.OutOrStdout(), true)
			case "powershell":
				return command.Root().GenPowerShellCompletion(command.OutOrStdout())
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
}

func registerWorkflowFileCompletions(command *cobra.Command) {
	if command.Flags().Lookup("file") != nil {
		_ = command.MarkFlagFilename("file", "yaml", "yml")
	}
	if command.Flags().Lookup("var-file") != nil {
		_ = command.MarkFlagFilename("var-file", "json", "toml")
	}
	for _, child := range command.Commands() {
		registerWorkflowFileCompletions(child)
	}
}

// Completion reads only metadata: it must not consume stdin, resolve actions,
// initialize plugins, or depend on unrelated discovered workflows being valid.
type workflowCompletionOptions struct {
	invokableOnly bool
	formOnly      bool
}

func workflowFileTargetCompletion(deps dependencies, filename string, options workflowCompletionOptions) ([]string, cobra.ShellCompDirective) {
	if !filepath.IsAbs(filename) {
		cwd, err := deps.cwd()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		filename = filepath.Join(cwd, filename)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var metadata struct {
		Description string    `yaml:"description"`
		Invokable   *bool     `yaml:"invokable"`
		Form        yaml.Node `yaml:"form"`
		Targets     map[string]struct {
			Description string    `yaml:"description"`
			Form        yaml.Node `yaml:"form"`
		} `yaml:"targets"`
	}
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	if options.invokableOnly && metadata.Invokable != nil && !*metadata.Invokable {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	values := make([]string, 0, len(metadata.Targets))
	sharedForm := (&workflow.Definition{Form: metadata.Form}).HasForm()
	for name, target := range metadata.Targets {
		if options.formOnly && !sharedForm && !(workflow.TargetDefinition{Form: target.Form}).HasForm() {
			continue
		}
		description := target.Description
		if description == "" {
			description = metadata.Description
		}
		values = append(values, name+"\t"+description)
	}
	slices.Sort(values)
	return values, cobra.ShellCompDirectiveNoFileComp
}

func workflowCompletion(deps dependencies, options workflowCompletionOptions) cobra.CompletionFunc {
	return func(command *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if command != nil {
			filename, _ := command.Flags().GetString("file")
			if filename != "" {
				if filename == "-" || len(args) > 0 {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				return workflowFileTargetCompletion(deps, filename, options)
			}
			pick, _ := command.Flags().GetBool("pick")
			if pick {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
		if len(args) > 1 || (len(args) > 0 && workflow.IsRemoteLocator(args[0])) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cwd, home, config, err := directories(deps)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		sources, err := workflow.DiscoverForCompletion(cwd, home, config)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		if len(args) > 0 {
			values := make([]string, 0)
			seen := make(map[string]struct{})
			for _, source := range sources {
				if source.Name != args[0] || source.Target == "" {
					continue
				}
				if (options.invokableOnly && !source.Invokable) || (options.formOnly && !source.HasForm) {
					continue
				}
				if _, exists := seen[source.Target]; exists {
					continue
				}
				seen[source.Target] = struct{}{}
				values = append(values, source.Target+"\t"+source.Description)
			}
			return values, cobra.ShellCompDirectiveNoFileComp
		}
		values := make([]string, 0, len(sources))
		seen := make(map[string]struct{})
		for _, source := range sources {
			if (options.invokableOnly && !source.Invokable) || (options.formOnly && !source.HasForm) {
				continue
			}
			if _, exists := seen[source.Name]; exists {
				continue
			}
			seen[source.Name] = struct{}{}
			values = append(values, source.Name+"\t"+source.Description)
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
