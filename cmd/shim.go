package cmd

import (
	"errors"
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
		Short: "Install commands for workflows or projects",
		Long: "A shim is a small executable script that runs a workflow or opens Wuko in a bound project. " +
			"Shims live in ~/.wuko/bin by default; add that directory to PATH to call them from anywhere.",
		Args: cobra.NoArgs,
	}
	command.AddCommand(newShimInstallCmd(deps), newShimListCmd(deps), newShimUninstallCmd(deps))
	return command
}

func newShimInstallCmd(deps dependencies) *cobra.Command {
	var file, name, dir, project, shell string
	var byName, force, noPath bool
	command := &cobra.Command{
		Use:   "install [NAME|URL|GITHUB] [TARGET] [-- RUN_FLAGS...]",
		Short: "Install a workflow or project shim",
		Long: "Install an executable shim for a workflow. A discovered NAME or --file is pinned to its absolute " +
			"path, so the shim works from any directory; --by-name instead resolves NAME from the directory the " +
			"shim runs in. Arguments after -- are wuko run flags baked into the shim; arguments passed to the shim " +
			"follow them. With --project DIR --as NAME, the shim enters the project and forwards the full Wuko CLI. " +
			"Project shims set up PATH automatically for Bash, Zsh, or Fish; use --no-path to manage PATH yourself.",
		Example: `  wuko shim install check
  wuko shim install deploy staging --as deploy-staging
  wuko shim install --file ./release.yaml
  wuko shim install check --by-name --as wcheck
  wuko shim install lint -- --no-config --var strict=true
  wuko shim install --project ./automation --as mytools`,
		RunE: func(command *cobra.Command, args []string) error {
			var runArgs []string
			if dash := command.ArgsLenAtDash(); dash >= 0 {
				args, runArgs = args[:dash], args[dash:]
			}
			if command.Flags().Changed("project") {
				if project == "" || len(args) > 0 || command.ArgsLenAtDash() >= 0 {
					return fmt.Errorf("--project requires a directory and cannot be combined with workflow selectors, targets, or arguments after --")
				}
				return installProjectShim(command, deps, project, name, dir, shell, force, noPath)
			}
			if command.Flags().Changed("shell") || command.Flags().Changed("no-path") {
				return fmt.Errorf("--shell and --no-path require --project")
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
	command.Flags().StringVar(&project, "project", "", "bind the full Wuko CLI to a project directory (requires --as)")
	command.Flags().StringVar(&shell, "shell", "", "shell for project PATH setup: bash, zsh, or fish (default: SHELL)")
	command.Flags().BoolVar(&noPath, "no-path", false, "skip automatic PATH setup for a project shim")
	markCLIOnly(command, "project", "shell", "no-path")
	command.MarkFlagsMutuallyExclusive("shell", "no-path")
	command.MarkFlagsMutuallyExclusive("project", "file")
	command.MarkFlagsMutuallyExclusive("project", "by-name")
	_ = command.MarkFlagDirname("project")
	_ = command.MarkFlagDirname("dir")
	_ = command.RegisterFlagCompletionFunc("shell", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"bash", "zsh", "fish"}, cobra.ShellCompDirectiveNoFileComp
	})
	command.ValidArgsFunction = func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if command.Flags().Changed("project") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return workflowCompletion(deps, workflowCompletionOptions{invokableOnly: true})(command, args, toComplete)
	}
	return command
}

func installProjectShim(command *cobra.Command, deps dependencies, project, name, dir, shell string, force, noPath bool) error {
	if name == "" {
		return fmt.Errorf("--as is required for a project shim")
	}
	cwd, err := deps.cwd()
	if err != nil {
		return fmt.Errorf("finding current directory: %w", err)
	}
	if !filepath.IsAbs(project) {
		project = filepath.Join(cwd, project)
	}
	project = filepath.Clean(project)
	if !hasProjectWorkflows(project) {
		return fmt.Errorf("project %s must contain a .wuko/workflows directory", project)
	}
	directory, err := shimDirectory(deps, dir)
	if err != nil {
		return err
	}
	var setup *shim.PathSetup
	if !noPath {
		home, err := deps.homeDir()
		if err != nil {
			return err
		}
		config, err := deps.configDir()
		if err != nil {
			return err
		}
		if shell == "" {
			shell = deps.getenv("SHELL")
		}
		setup, err = shim.PreparePath(directory, shim.PathOptions{
			HomeDir: home, ConfigDir: config, Shell: shell,
			ZDotDir: deps.getenv("ZDOTDIR"), XDGConfigHome: deps.getenv("XDG_CONFIG_HOME"),
		})
		if err != nil {
			return err
		}
	}
	installed, err := shim.Install(directory, name, shim.Spec{Project: project}, force)
	if err != nil {
		return err
	}
	fmt.Fprintf(command.OutOrStdout(), "installed shim %s -> %s\n", installed.Path, describeShim(installed.Spec))
	if setup != nil {
		if err := setup.Apply(command.Context()); err != nil {
			return fmt.Errorf("shim is installed at %s, but PATH setup failed (retry with --force or uninstall the shim): %w", installed.Path, err)
		}
		fmt.Fprintf(command.OutOrStdout(), "Configured PATH for %s; open a new shell to activate it.\n", filepath.Base(shell))
	}
	if !onPath(deps, directory) {
		fmt.Fprintf(command.ErrOrStderr(), "For this terminal, add %s to PATH or invoke %s directly.\n", directory, installed.Path)
	}
	return nil
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
				if installed.Spec.Project != "" {
					if !hasProjectWorkflows(installed.Spec.Project) {
						line += "\t(project workflow directory missing)"
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
			var removeErr error
			for _, name := range args {
				removed, err := shim.Remove(directory, name)
				if err != nil {
					removeErr = err
					break
				}
				fmt.Fprintf(command.OutOrStdout(), "removed shim %s\n", removed.Path)
			}
			// Clean up PATH even after a failed removal: an earlier name may have been the last shim.
			// Without a config directory no PATH ownership can have been recorded, so there is
			// nothing to clean up.
			config, err := deps.configDir()
			if err != nil {
				return removeErr
			}
			if err := shim.CleanupPath(command.Context(), directory, config); err != nil {
				return errors.Join(removeErr, fmt.Errorf("PATH cleanup failed: %w", err))
			}
			return removeErr
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
		if filepath.IsAbs(dir) {
			return filepath.Clean(dir), nil
		}
		cwd, err := deps.cwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, dir), nil
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, ".wuko", "bin"), nil
}

func hasProjectWorkflows(project string) bool {
	info, err := os.Stat(filepath.Join(project, ".wuko", "workflows"))
	return err == nil && info.IsDir()
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
	if spec.Project != "" {
		return "project " + spec.Project
	}
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
