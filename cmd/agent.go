package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	agentinstaller "github.com/up2jj/wuko/agent"
	"github.com/up2jj/wuko/skills"
)

func newAgentCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "Discover and install coding-agent skills",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newAgentListCmd(deps), newAgentInstallCmd(deps))
	return command
}

func newAgentListCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List supported coding agents available on PATH",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			agents, err := discoverAgents(deps)
			if err != nil {
				return err
			}
			for _, agent := range agents {
				fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\n", agent.Name, agent.Executable, agent.SkillDirectory)
			}
			return nil
		},
	}
}

func newAgentInstallCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "install [AGENT]",
		Short: "Install Wuko skills for an available coding agent",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			agents, err := discoverAgents(deps)
			if err != nil {
				return err
			}
			if len(agents) == 0 {
				return fmt.Errorf("no supported coding agents found on PATH")
			}

			targets := agents
			if len(args) == 1 {
				selected, ok := agentinstaller.Find(agents, args[0])
				if !ok {
					return fmt.Errorf("coding agent %q is not available; run %q to see available agents", args[0], "wuko agent list")
				}
				targets = []agentinstaller.Definition{selected}
			}

			for _, target := range targets {
				installed, err := agentinstaller.Install(skills.Assets, target.SkillDirectory)
				if err != nil {
					return fmt.Errorf("installing skills for %s: %w", target.Name, err)
				}
				fmt.Fprintf(command.OutOrStdout(), "installed %d skills for %s in %s\n", len(installed), target.Name, target.SkillDirectory)
			}
			return nil
		},
	}
	command.ValidArgsFunction = func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		agents, err := discoverAgents(deps)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		values := make([]string, 0, len(agents))
		for _, agent := range agents {
			values = append(values, agent.Name)
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	}
	return command
}

func discoverAgents(deps dependencies) ([]agentinstaller.Definition, error) {
	if deps.homeDir == nil {
		return nil, fmt.Errorf("finding home directory: home directory lookup is unavailable")
	}
	home, err := deps.homeDir()
	if err != nil {
		return nil, fmt.Errorf("finding home directory: %w", err)
	}
	return agentinstaller.Discover(home, deps.agentLookPath), nil
}
