package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/githook"
	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/workflow"
)

const gitHookStackEnvironment = "WUKO_GIT_HOOK_STACK"

func newGitCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "git", Short: "Integrate Wuko workflows with Git", Args: cobra.NoArgs}
	command.AddCommand(newGitHookCmd(deps))
	return command
}

func newGitHookCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "hook", Short: "Install and run Wuko workflows as Git hooks", Args: cobra.NoArgs}
	command.AddCommand(newGitHookInitCmd(deps), newGitHookInstallCmd(deps), newGitHookUninstallCmd(deps), newGitHookStatusCmd(deps), newGitHookRunCmd(deps))
	return command
}

func newGitHookInitCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "init", Short: "Create a starter Git hook manifest and workflows", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			repository, err := discoverGitRepository(command, deps)
			if err != nil {
				return err
			}
			paths, err := githook.Scaffold(repository)
			if err != nil {
				return err
			}
			for _, path := range paths {
				relative, err := filepath.Rel(repository.Root, path)
				if err != nil {
					relative = path
				}
				fmt.Fprintf(command.OutOrStdout(), "created %s\n", relative)
			}
			fmt.Fprintln(command.OutOrStdout(), "Run `wuko git hook install` after reviewing the generated workflows.")
			return nil
		},
	}
}

func newGitHookInstallCmd(deps dependencies) *cobra.Command {
	var chain bool
	command := &cobra.Command{
		Use: "install", Short: "Install Git hook dispatchers from .wuko/git-hooks.yaml", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			repository, manifest, err := loadGitHookInvocation(command, deps)
			if err != nil {
				return err
			}
			if err := validateGitHookBindings(command, deps, repository, manifest); err != nil {
				return err
			}
			executable, err := deps.executable()
			if err != nil {
				return fmt.Errorf("finding Wuko executable: %w", err)
			}
			executable, err = filepath.Abs(executable)
			if err != nil {
				return fmt.Errorf("resolving Wuko executable: %w", err)
			}
			statuses, err := githook.Install(command.Context(), repository, executable, manifest, chain)
			if err != nil {
				return err
			}
			for _, status := range statuses {
				suffix := ""
				if status.Chained {
					suffix = " (chained)"
				}
				fmt.Fprintf(command.OutOrStdout(), "%s: installed%s at %s\n", status.Name, suffix, status.Path)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&chain, "chain", false, "preserve and run an existing Git hook before Wuko")
	return command
}

func newGitHookUninstallCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{
		Use: "uninstall [HOOK...]", Short: "Uninstall Wuko-managed Git hook dispatchers", Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			for _, name := range args {
				if !githook.Supported(name) {
					return fmt.Errorf("unsupported client-side Git hook %q", name)
				}
			}
			repository, err := discoverGitRepository(command, deps)
			if err != nil {
				return err
			}
			statuses, err := githook.Uninstall(command.Context(), repository, args)
			if err != nil {
				return err
			}
			for _, status := range statuses {
				suffix := ""
				if status.Chained {
					suffix = "; restored preserved hook"
				}
				fmt.Fprintf(command.OutOrStdout(), "%s: uninstalled%s\n", status.Name, suffix)
			}
			return nil
		},
		ValidArgsFunction: gitHookCompletion,
	}
	return command
}

func newGitHookStatusCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show Wuko Git hook installation status", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			repository, err := discoverGitRepository(command, deps)
			if err != nil {
				return err
			}
			manifest, err := githook.LoadManifest(repository.Root)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				manifest = githook.Manifest{Version: 1, Hooks: map[string][]githook.Binding{}}
			}
			statuses, err := githook.Inspect(command.Context(), repository, manifest)
			if err != nil {
				return err
			}
			for _, status := range statuses {
				suffix := ""
				if status.Chained {
					suffix = " (chained)"
				}
				fmt.Fprintf(command.OutOrStdout(), "%s\t%s%s\t%s\n", status.Name, status.State, suffix, status.Path)
			}
			return nil
		},
	}
}

func newGitHookRunCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "run HOOK [GIT_ARGS...]", Short: "Run workflows configured for one Git hook", Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: gitHookCompletion,
		RunE: func(command *cobra.Command, args []string) error {
			name := args[0]
			if !githook.Supported(name) {
				return fmt.Errorf("unsupported client-side Git hook %q", name)
			}
			if hookStackContains(deps.getenv(gitHookStackEnvironment), name) {
				return fmt.Errorf("recursive Git hook %q invocation detected; use Git's native hook bypass when appropriate", name)
			}
			repository, err := discoverGitRepository(command, deps)
			if err != nil {
				return err
			}
			input, err := io.ReadAll(command.InOrStdin())
			if err != nil {
				return fmt.Errorf("reading Git hook stdin: %w", err)
			}
			stack := appendHookStack(deps.getenv(gitHookStackEnvironment), name)
			if preserved, found, err := githook.PreservedPath(command.Context(), repository, name); err != nil {
				return err
			} else if found {
				child := exec.CommandContext(command.Context(), preserved, args[1:]...)
				child.Dir, child.Stdin, child.Stdout, child.Stderr = repository.Root, bytes.NewReader(input), command.OutOrStdout(), command.ErrOrStderr()
				child.Env = environmentWith(os.Environ(), gitHookStackEnvironment, stack)
				if err := child.Run(); err != nil {
					return fmt.Errorf("preserved Git hook %s failed: %w", name, err)
				}
			}
			manifest, err := githook.LoadManifest(repository.Root)
			if err != nil {
				// A dispatcher outlives the worktree state that declared it: checking out a
				// branch without the manifest must not block every Git operation.
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			bindings, ok := manifest.Hooks[name]
			if !ok {
				return nil
			}
			gitContext, err := githook.Context(repository, name, args[1:], string(input))
			if err != nil {
				return err
			}
			runDeps := deps
			runDeps.cwd = func() (string, error) { return repository.Root, nil }
			runDeps.environment = nil
			for _, binding := range bindings {
				selector := []string{binding.Workflow}
				if binding.Target != "" {
					selector = append(selector, binding.Target)
				}
				hookReporter := newGitHookReporter(command, deps, repository.Root, name, binding.Workflow)
				config := runWorkflowConfig{
					once: true, nonInteractive: true,
					environment:     []string{gitHookStackEnvironment + "=" + stack},
					providerValues:  map[string]map[string]any{"git": gitContext},
					defaultReporter: hookReporter,
				}
				if err := runWorkflow(command, runDeps, selector, config); err != nil {
					return hookReporter.failureError(err)
				}
			}
			return nil
		},
	}
}

func loadGitHookInvocation(command *cobra.Command, deps dependencies) (githook.Repository, githook.Manifest, error) {
	repository, err := discoverGitRepository(command, deps)
	if err != nil {
		return githook.Repository{}, githook.Manifest{}, err
	}
	manifest, err := githook.LoadManifest(repository.Root)
	return repository, manifest, err
}

func discoverGitRepository(command *cobra.Command, deps dependencies) (githook.Repository, error) {
	cwd, err := deps.cwd()
	if err != nil {
		return githook.Repository{}, fmt.Errorf("finding current directory: %w", err)
	}
	return githook.Discover(command.Context(), cwd)
}

func validateGitHookBindings(command *cobra.Command, deps dependencies, repository githook.Repository, manifest githook.Manifest) error {
	home, err := deps.homeDir()
	if err != nil {
		return fmt.Errorf("finding home directory: %w", err)
	}
	config, err := deps.configDir()
	if err != nil {
		return fmt.Errorf("finding user config directory: %w", err)
	}
	loader := deps.loader
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	hookDeps := deps
	hookDeps.environment = nil
	invocationEnv, err := invocationEnvironment(command, hookDeps, repository.Root)
	if err != nil {
		return err
	}
	baseEnv, environmentLoaders := environmentValues(invocationEnv)
	providers, err := invocationProviders(command, deps, baseEnv)
	if err != nil {
		return err
	}
	if providers.Schemas == nil {
		providers.Schemas = make(map[string]provider.Schema)
	}
	if providers.Values == nil {
		providers.Values = make(map[string]map[string]any)
	}
	providers.Schemas["git"] = provider.NewGit().Schema()
	providers.Values["git"] = map[string]any{
		"repository": map[string]any{"root": repository.Root, "git_dir": repository.GitDir, "common_dir": repository.CommonDir},
		"hook":       map[string]any{"name": "", "args": []any{}, "stdin": "", "payload": map[string]any{}},
	}
	for _, hookName := range manifest.HookNames() {
		for _, binding := range manifest.Hooks[hookName] {
			source, err := workflow.Find(repository.Root, home, config, binding.Workflow)
			if err != nil {
				return fmt.Errorf("Git hook %s: %w", hookName, err)
			}
			loadOptions := workflow.LoadOptions{
				RunDir: repository.Root, Target: binding.Target, BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, Providers: providers,
				Stdin: bytes.NewReader(nil), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(), Interactive: false,
			}
			definition, err := loader.Load(command.Context(), source.Path, loadOptions)
			if err != nil {
				return fmt.Errorf("Git hook %s workflow %q: %w", hookName, binding.Workflow, err)
			}
			if !definition.IsInvokable() {
				return fmt.Errorf("Git hook %s workflow %q is not directly invokable", hookName, binding.Workflow)
			}
			plan, err := resolveDependencyPlan(command.Context(), definition, loader, loadOptions, repository.Root, home, config)
			if err != nil {
				return fmt.Errorf("Git hook %s workflow %q: %w", hookName, binding.Workflow, err)
			}
			optionsFor := func(definition *workflow.Definition, dependencies map[string]map[string]any) engine.Options {
				return engine.Options{
					BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, Dependencies: dependencies, RunDir: repository.Root, Providers: providers,
					Stdin: bytes.NewReader(nil), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
					LocalValueDir: filepath.Join(definition.Dir, ".wuko", "values"), GlobalValueDir: filepath.Join(config, "wuko", "values"),
				}
			}
			if err := validateDependencyPlan(command.Context(), plan, func() *engine.Engine { return workflowEngine(deps) }, optionsFor); err != nil {
				return fmt.Errorf("Git hook %s workflow %q: %w", hookName, binding.Workflow, err)
			}
		}
	}
	return nil
}

func gitHookCompletion(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return githook.SupportedNames(), cobra.ShellCompDirectiveNoFileComp
}

func hookStackContains(stack, name string) bool {
	for _, item := range strings.Split(stack, ",") {
		if item == name {
			return true
		}
	}
	return false
}

func appendHookStack(stack, name string) string {
	if stack == "" {
		return name
	}
	return stack + "," + name
}

func environmentWith(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
