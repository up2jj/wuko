package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/up2jj/wuko/shim"
	"github.com/up2jj/wuko/workflow"
)

func newShimCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "shim",
		Short: "Install commands that run one workflow",
		Long: "A shim is a small executable script that runs one workflow under its own command name. " +
			"Shims live in ~/.wuko/bin by default; add that directory to PATH to call them from anywhere.",
		Args: cobra.NoArgs,
	}
	command.AddCommand(newShimInstallCmd(deps), newShimListCmd(deps), newShimUninstallCmd(deps))
	return command
}

func newShimInstallCmd(deps dependencies) *cobra.Command {
	var file, name, dir string
	var byName, force bool
	command := &cobra.Command{
		Use:   "install [NAME|URL|GITHUB] [TARGET] [-- RUN_FLAGS...]",
		Short: "Install a shim that runs a workflow",
		Long: "Install an executable shim for a workflow. A discovered NAME or --file is pinned to its absolute " +
			"path, so the shim works from any directory; --by-name instead resolves NAME from the directory the " +
			"shim runs in. Arguments after -- are wuko run flags baked into the shim; arguments passed to the shim " +
			"follow them.",
		Example: `  wuko shim install check
  wuko shim install deploy staging --as deploy-staging
  wuko shim install --file ./release.yaml
  wuko shim install check --by-name --as wcheck
  wuko shim install lint -- --no-config --var strict=true`,
		RunE: func(command *cobra.Command, args []string) error {
			var runArgs []string
			if dash := command.ArgsLenAtDash(); dash >= 0 {
				args, runArgs = args[:dash], args[dash:]
			}
			if err := validateShimRunArgs(command, deps, runArgs); err != nil {
				return err
			}
			spec, defaultName, err := resolveShimSpec(deps, args, file, byName)
			if err != nil {
				return err
			}
			spec.Args = runArgs
			if name == "" {
				name = defaultName
			}
			if name == "" {
				return fmt.Errorf("--as is required for a remote workflow")
			}
			directory, err := shimDirectory(deps, dir)
			if err != nil {
				return err
			}
			installed, err := shim.Install(directory, name, spec, force)
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "installed shim %s -> %s\n", installed.Path, describeShim(installed.Spec))
			if !onPath(deps, directory) {
				fmt.Fprintf(command.ErrOrStderr(), "%s is not on PATH; add it to run %s by name:\n  export PATH=%q:\"$PATH\"\n", directory, installed.Name, directory)
			}
			return nil
		},
	}
	command.Flags().StringVar(&file, "file", "", "pin a workflow file instead of a discovered name")
	command.Flags().StringVar(&name, "as", "", "shim command name (default: the workflow name, plus -TARGET)")
	command.Flags().StringVar(&dir, "dir", "", "directory to write the shim to (default: ~/.wuko/bin)")
	command.Flags().BoolVar(&byName, "by-name", false, "resolve NAME by discovery each time the shim runs")
	command.Flags().BoolVar(&force, "force", false, "replace an existing shim of the same name")
	command.ValidArgsFunction = workflowCompletion(deps, workflowCompletionOptions{invokableOnly: true})
	return command
}

func newShimListCmd(deps dependencies) *cobra.Command {
	var dir string
	command := &cobra.Command{
		Use:   "list",
		Short: "List installed shims",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			directory, err := shimDirectory(deps, dir)
			if err != nil {
				return err
			}
			shims, err := shim.List(directory)
			if err != nil {
				return err
			}
			for _, installed := range shims {
				line := installed.Name + "\t" + describeShim(installed.Spec)
				if installed.Spec.File != "" {
					if _, err := os.Stat(installed.Spec.File); err != nil {
						line += "\t(workflow file missing)"
					}
				}
				fmt.Fprintln(command.OutOrStdout(), line)
			}
			return nil
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "shim directory (default: ~/.wuko/bin)")
	return command
}

func newShimUninstallCmd(deps dependencies) *cobra.Command {
	var dir string
	command := &cobra.Command{
		Use:   "uninstall SHIM...",
		Short: "Remove installed shims",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			directory, err := shimDirectory(deps, dir)
			if err != nil {
				return err
			}
			for _, name := range args {
				removed, err := shim.Remove(directory, name)
				if err != nil {
					return err
				}
				fmt.Fprintf(command.OutOrStdout(), "removed shim %s\n", removed.Path)
			}
			return nil
		},
	}
	command.Flags().StringVar(&dir, "dir", "", "shim directory (default: ~/.wuko/bin)")
	command.ValidArgsFunction = func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		directory, err := shimDirectory(deps, dir)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		shims, err := shim.List(directory)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var names []string
		for _, installed := range shims {
			if !slices.Contains(args, installed.Name) {
				names = append(names, installed.Name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return command
}

// resolveShimSpec turns install arguments into what the shim runs and its default name.
func resolveShimSpec(deps dependencies, args []string, file string, byName bool) (shim.Spec, string, error) {
	if file != "" {
		if byName {
			return shim.Spec{}, "", fmt.Errorf("--by-name cannot be used with --file")
		}
		if len(args) > 1 {
			return shim.Spec{}, "", fmt.Errorf("with --file, pass at most one positional TARGET")
		}
		cwd, err := deps.cwd()
		if err != nil {
			return shim.Spec{}, "", fmt.Errorf("finding current directory: %w", err)
		}
		path := file
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if info, err := os.Stat(path); err != nil {
			return shim.Spec{}, "", fmt.Errorf("checking workflow file: %w", err)
		} else if !info.Mode().IsRegular() {
			return shim.Spec{}, "", fmt.Errorf("workflow file %s is not a regular file", path)
		}
		spec := shim.Spec{File: path, Target: optionalArg(args, 0)}
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		return spec, shimName(stem, spec.Target), nil
	}
	if len(args) == 0 {
		return shim.Spec{}, "", fmt.Errorf("workflow name, remote locator, or --file is required; use wuko list to find workflows")
	}
	if len(args) > 2 {
		return shim.Spec{}, "", fmt.Errorf("pass a workflow and at most one TARGET; put wuko run flags after --")
	}
	target := optionalArg(args, 1)
	if workflow.IsRemoteLocator(args[0]) {
		if byName {
			return shim.Spec{}, "", fmt.Errorf("--by-name cannot be used with a remote locator")
		}
		// A locator has no short name to default to.
		return shim.Spec{Locator: args[0], Target: target}, "", nil
	}
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return shim.Spec{}, "", err
	}
	source, err := workflow.Find(cwd, home, configDir, args[0])
	if err != nil {
		return shim.Spec{}, "", err
	}
	if !source.Invokable {
		return shim.Spec{}, "", fmt.Errorf("workflow %q is not invokable; it runs only as a depends_on prerequisite", args[0])
	}
	spec := shim.Spec{File: source.Path, Target: target}
	if byName {
		spec = shim.Spec{Workflow: args[0], Target: target}
	}
	return spec, shimName(args[0], target), nil
}

// validateShimRunArgs parses baked flags with the run command so a typo fails at install
// rather than every time the shim runs.
func validateShimRunArgs(command *cobra.Command, deps dependencies, runArgs []string) error {
	if len(runArgs) == 0 {
		return nil
	}
	run := newRunCmd(deps)
	// Global flags are declared again rather than shared, so parsing cannot change the values
	// of the command that is running.
	command.Root().PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		switch flag.Value.Type() {
		case "bool":
			run.Flags().BoolP(flag.Name, flag.Shorthand, false, flag.Usage)
		case "stringArray":
			run.Flags().StringArrayP(flag.Name, flag.Shorthand, nil, flag.Usage)
		default:
			run.Flags().StringP(flag.Name, flag.Shorthand, "", flag.Usage)
		}
	})
	if err := run.ParseFlags(runArgs); err != nil {
		return fmt.Errorf("invalid wuko run flags after --: %w", err)
	}
	if extra := run.Flags().Args(); len(extra) > 0 {
		return fmt.Errorf("only wuko run flags may follow --, got %q; pass TARGET before --", extra[0])
	}
	return nil
}

func shimDirectory(deps dependencies, dir string) (string, error) {
	if dir != "" {
		return filepath.Abs(dir)
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, ".wuko", "bin"), nil
}

func onPath(deps dependencies, dir string) bool {
	getenv := deps.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, entry := range filepath.SplitList(getenv("PATH")) {
		if entry != "" && filepath.Clean(entry) == dir {
			return true
		}
	}
	return false
}

func shimName(workflowName, target string) string {
	if target == "" {
		return workflowName
	}
	return workflowName + "-" + target
}

func describeShim(spec shim.Spec) string {
	parts := []string{spec.Source()}
	if spec.Target != "" {
		parts = append(parts, spec.Target)
	}
	parts = append(parts, spec.Args...)
	return strings.Join(parts, " ")
}

func optionalArg(args []string, index int) string {
	if index < len(args) {
		return args[index]
	}
	return ""
}
