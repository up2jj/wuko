package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
)

func newValidateCmd(deps dependencies) *cobra.Command {
	var variables, variableFiles, environment []string
	command := &cobra.Command{
		Use:   "validate [NAME]",
		Short: "Validate one or all effective workflows",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			reporter := diagnosticsFor(command, deps, cwd)
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusStarted, Message: "validate workflows", Attributes: []diagnostic.Attribute{diagnostic.Attr("run_dir", cwd)}})
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
			var sources []workflow.Source
			discoveryStarted := time.Now()
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: discoveryStarted, Message: "discovering workflows"})
			if len(args) == 1 {
				source, err := workflow.Find(cwd, home, config, args[0])
				if err != nil {
					diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(discoveryStarted), Error: err})
					return err
				}
				sources = []workflow.Source{source}
			} else {
				sources, err = workflow.Discover(cwd, home, config)
				if err != nil {
					diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(discoveryStarted), Error: err})
					return err
				}
			}
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(discoveryStarted), Attributes: []diagnostic.Attribute{diagnostic.Attr("workflows", fmt.Sprint(len(sources)))}})
			for _, source := range sources {
				loader := deps.loader
				if loader == nil {
					loader = workflow.NewLoader(nil)
				}
				definition, err := loader.Load(command.Context(), source.Path, workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Diagnostics: reporter})
				if err != nil {
					return err
				}
				if err := engine.New(deps.registry).Validate(command.Context(), definition, engine.Options{
					Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
					LocalValueDir: filepath.Join(definition.Dir, ".wuko", "values"), GlobalValueDir: filepath.Join(config, "wuko", "values"),
					Diagnostics: reporter,
				}); err != nil {
					return err
				}
				fmt.Fprintf(command.OutOrStdout(), "%s: valid\n", source.Name)
			}
			return nil
		},
	}
	command.Flags().StringArrayVar(&variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
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
