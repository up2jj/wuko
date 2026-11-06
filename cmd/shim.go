package cmd

import (
	"bytes"
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
		Short: "Create or install commands for workflows or projects",
		Long: "A shim is a small executable script that runs a workflow or opens Wuko in a bound project. " +
			"Create a local shim with create, or install one into ~/.wuko/bin and add that directory to PATH.",
		Args: cobra.NoArgs,
	}
	command.AddCommand(newShimCreateCmd(deps), newShimInstallCmd(deps), newShimListCmd(deps), newShimUninstallCmd(deps))
	return command
}

type shimOptions struct {
	file, name, project string
	byName, force       bool
}

func (options *shimOptions) addFlags(command *cobra.Command, deps dependencies) {
	command.Flags().StringVar(&options.file, "file", "", "pin a workflow file instead of a discovered name")
	command.Flags().StringVar(&options.name, "as", "", "shim command name (default: workflow name plus -TARGET, or project directory name)")
	command.Flags().StringVar(&options.project, "project", "", "bind the full Wuko CLI to a project directory")
	command.Flags().BoolVar(&options.byName, "by-name", false, "resolve NAME by discovery each time the shim runs")
	command.Flags().BoolVar(&options.force, "force", false, "replace an existing Wuko shim")
	markCLIOnly(command, "project")
	command.MarkFlagsMutuallyExclusive("project", "file")
	command.MarkFlagsMutuallyExclusive("project", "by-name")
	_ = command.MarkFlagDirname("project")
	command.ValidArgsFunction = func(command *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if command.Flags().Changed("project") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return workflowCompletion(deps, workflowCompletionOptions{invokableOnly: true})(command, args, toComplete)
	}
}

func (options *shimOptions) resolve(command *cobra.Command, deps dependencies, args []string) (shim.Spec, string, error) {
	var runArgs []string
	if dash := command.ArgsLenAtDash(); dash >= 0 {
		args, runArgs = args[:dash], args[dash:]
	}
	if command.Flags().Changed("project") {
		if options.project == "" || len(args) > 0 || command.ArgsLenAtDash() >= 0 {
			return shim.Spec{}, "", fmt.Errorf("--project requires a directory and cannot be combined with workflow selectors, targets, or arguments after --")
		}
		project := options.project
		if !filepath.IsAbs(project) {
			cwd, err := deps.cwd()
			if err != nil {
				return shim.Spec{}, "", fmt.Errorf("finding current directory: %w", err)
			}
			project = filepath.Join(cwd, project)
		}
		project = filepath.Clean(project)
		if !hasProjectWorkflows(project) {
			return shim.Spec{}, "", fmt.Errorf("project %s must contain a .wuko/workflows directory", project)
		}
		return shim.Spec{Project: project}, filepath.Base(project), nil
	}
	if err := validateShimRunArgs(command, deps, runArgs); err != nil {
		return shim.Spec{}, "", err
	}
	spec, defaultName, err := resolveShimSpec(deps, args, options.file, options.byName)
	if err != nil {
		return shim.Spec{}, "", err
	}
	spec.Args = runArgs
	return spec, defaultName, nil
}

func resolveShimName(name, defaultName string) (string, error) {
	if name == "" {
		name = defaultName
	}
	if name == "" {
		return "", fmt.Errorf("--as is required for a remote workflow")
	}
	if err := shim.ValidateName(name); err != nil {
		return "", fmt.Errorf("%w; choose another name with --as", err)
	}
	return name, nil
}

func newShimCreateCmd(deps dependencies) *cobra.Command {
	var options shimOptions
	var output, missingMessage string
	var inline bool
	command := &cobra.Command{
		Use:   "create [NAME|URL|GITHUB] [TARGET] [-- RUN_FLAGS...]",
		Short: "Create a local workflow or project shim",
		Long: "Create an executable shim in the current directory, named after the workflow or project. " +
			"Local sources are pinned to absolute paths; --inline embeds a copy of a local workflow instead. " +
			"Workflow shims run in the caller's working directory, including when invoked from PATH. " +
			"Use --output PATH to choose a filename, or --output - to print the script. " +
			"Arguments after -- are checked wuko run flags baked into workflow shims. " +
			"Creation does not configure PATH.",
		Example: "  wuko shim create check\n" +
			"  wuko shim create --project . --output ./dev\n" +
			"  wuko shim create --file ./release.yaml staging --output ./ship\n" +
			"  wuko shim create check --output -",
		RunE: func(command *cobra.Command, args []string) error {
			spec, defaultName, err := options.resolve(command, deps, args)
			if err != nil {
				return err
			}
			if inline {
				if spec.File == "" {
					return fmt.Errorf("--inline requires a local workflow name or --file; it cannot be combined with --project, --by-name, or remote locators")
				}
				content, err := inlineShimContent(command, deps, spec.File)
				if err != nil {
					return err
				}
				spec.Inline, spec.Content = true, content
			}
			if optionProvided(command, "output") && output == "" {
				return fmt.Errorf("--output requires a filename or - for stdout")
			}
			if output != "" && output != "-" {
				if !filepath.IsAbs(output) {
					cwd, err := deps.cwd()
					if err != nil {
						return fmt.Errorf("finding current directory: %w", err)
					}
					output = filepath.Join(cwd, output)
				}
				if info, err := os.Stat(output); err == nil && info.IsDir() {
					return fmt.Errorf("--output %s is a directory; pass a file path such as %s", output, filepath.Join(output, defaultName))
				}
				// An explicit output filename is also the command name.
				defaultName = filepath.Base(filepath.Clean(output))
				if options.name != "" && options.name != defaultName {
					return fmt.Errorf("--as %q conflicts with output filename %q", options.name, defaultName)
				}
			}
			name, err := resolveShimName(options.name, defaultName)
			if err != nil {
				return err
			}
			spec.MissingMessage = missingMessage
			if output == "-" {
				data, err := shim.Render(name, spec)
				if err != nil {
					return err
				}
				if _, err := command.OutOrStdout().Write(data); err != nil {
					return fmt.Errorf("writing shim to stdout: %w", err)
				}
				return nil
			}
			directory := filepath.Dir(output)
			if output == "" {
				if directory, err = deps.cwd(); err != nil {
					return fmt.Errorf("finding current directory: %w", err)
				}
			}
			created, err := shim.Install(directory, name, spec, options.force)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "created shim %s -> %s\n", created.Path, describeShim(created.Spec))
			return err
		},
	}
	options.addFlags(command, deps)
	command.Flags().BoolVar(&inline, "inline", false, "embed a local workflow in the shim instead of referencing its file")
	command.MarkFlagsMutuallyExclusive("inline", "project")
	command.MarkFlagsMutuallyExclusive("inline", "by-name")
	command.Flags().StringVarP(&output, "output", "o", "", "output file (default: ./NAME); - prints the script to stdout")
	command.Flags().StringVar(&missingMessage, "missing-message", "", "message printed when Wuko is missing (default: installation instructions)")
	markCLIOnly(command, "output", "force")
	return command
}

func newShimInstallCmd(deps dependencies) *cobra.Command {
	var options shimOptions
	var dir, shell string
	var noPath bool
	command := &cobra.Command{
		Use:   "install [NAME|URL|GITHUB] [TARGET] [-- RUN_FLAGS...]",
		Short: "Install a workflow or project shim",
		Long: "Install an executable shim for a workflow. A discovered NAME or --file is pinned to its absolute " +
			"path, so the shim works from any directory; --by-name instead resolves NAME from the directory the " +
			"shim runs in. Arguments after -- are wuko run flags baked into the shim; arguments passed to the shim " +
			"follow them. With --project DIR, the shim is named after the directory, enters the project, and forwards the full Wuko CLI. " +
			"Project shims set up PATH automatically for Bash, Zsh, or Fish; use --no-path to manage PATH yourself.",
		Example: `  wuko shim install check
  wuko shim install deploy staging --as deploy-staging
  wuko shim install --file ./release.yaml
  wuko shim install check --by-name --as wcheck
  wuko shim install lint -- --no-config --var strict=true
  wuko shim install --project ./automation --as mytools`,
		RunE: func(command *cobra.Command, args []string) error {
			if !command.Flags().Changed("project") && (command.Flags().Changed("shell") || command.Flags().Changed("no-path")) {
				return fmt.Errorf("--shell and --no-path require --project")
			}
			spec, defaultName, err := options.resolve(command, deps, args)
			if err != nil {
				return err
			}
			name, err := resolveShimName(options.name, defaultName)
			if err != nil {
				return err
			}
			if spec.Project != "" {
				return installProjectShim(command, deps, spec, name, dir, shell, options.force, noPath)
			}
			directory, err := shimDirectory(deps, dir)
			if err != nil {
				return err
			}
			installed, err := shim.Install(directory, name, spec, options.force)
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
	options.addFlags(command, deps)
	command.Flags().StringVar(&dir, "dir", "", "directory to write the shim to (default: ~/.wuko/bin)")
	command.Flags().StringVar(&shell, "shell", "", "shell for project PATH setup: bash, zsh, or fish (default: SHELL)")
	command.Flags().BoolVar(&noPath, "no-path", false, "skip automatic PATH setup for a project shim")
	markCLIOnly(command, "project", "shell", "no-path")
	command.MarkFlagsMutuallyExclusive("shell", "no-path")
	_ = command.MarkFlagDirname("dir")
	_ = command.RegisterFlagCompletionFunc("shell", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"bash", "zsh", "fish"}, cobra.ShellCompDirectiveNoFileComp
	})
	return command
}

func installProjectShim(command *cobra.Command, deps dependencies, spec shim.Spec, name, dir, shell string, force, noPath bool) error {
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
	installed, err := shim.Install(directory, name, spec, force)
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
				if installed.Err != nil {
					fmt.Fprintf(command.OutOrStdout(), "%s\t(invalid: %v)\n", installed.Name, installed.Err)
					continue
				}
				line := installed.Name + "\t" + describeShim(installed.Spec)
				if installed.Spec.File != "" && !installed.Spec.Inline {
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

// inlineShimContent reads the workflow to embed, unwrapping an inline shim used as the
// source. The workflow must decode and be standalone: the shim carries no other files, so a
// broken workflow or a local reference fails at creation rather than wherever the shim runs.
func inlineShimContent(command *cobra.Command, deps dependencies, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading inline workflow: %w", err)
	}
	if content, ok, err := shim.InlineWorkflow(data); err != nil {
		return "", fmt.Errorf("reading inline workflow %s: %w", path, err)
	} else if ok {
		data = content
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", fmt.Errorf("cannot inline an empty workflow")
	}
	cwd, err := deps.cwd()
	if err != nil {
		return "", fmt.Errorf("finding current directory: %w", err)
	}
	loader := deps.loader
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	definition, err := loader.DecodeContext(command.Context(), path, workflow.LoadOptions{RunDir: cwd})
	if err != nil {
		return "", fmt.Errorf("cannot inline %s: %w", path, err)
	}
	if err := workflow.ValidateStandalone(data, definition); err != nil {
		return "", fmt.Errorf("cannot inline %s: %w; reference the workflow file instead of using --inline", path, err)
	}
	return string(data), nil
}

// resolveShimSpec turns workflow selector arguments into what the shim runs and its default name.
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

// validateShimRunArgs parses baked flags with the run command so a typo fails at generation
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
	if spec.Inline {
		parts[0] = "inline " + parts[0]
	}
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
