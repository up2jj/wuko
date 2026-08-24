package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

type workflowLifecycleConfig struct {
	variables     []string
	variableFiles []string
	environment   []string
	global        bool
	yes           bool
}

func newInstallCmd(deps dependencies) *cobra.Command {
	var config workflowLifecycleConfig
	command := &cobra.Command{
		Use:   "install URL|GITHUB|FILE",
		Short: "Install a workflow in the local or global workflow directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return installWorkflow(command, deps, args[0], config)
		},
	}
	addWorkflowLifecycleFlags(command, &config)
	return command
}

func newUninstallCmd(deps dependencies) *cobra.Command {
	var config workflowLifecycleConfig
	command := &cobra.Command{
		Use:   "uninstall NAME",
		Short: "Uninstall a workflow from the local or global workflow directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return uninstallWorkflow(command, deps, args[0], config)
		},
	}
	addWorkflowLifecycleFlags(command, &config)
	command.Flags().BoolVarP(&config.yes, "yes", "y", false, "skip uninstall confirmation")
	return command
}

func addWorkflowLifecycleFlags(command *cobra.Command, config *workflowLifecycleConfig) {
	command.Flags().StringArrayVar(&config.variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&config.variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&config.environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().BoolVar(&config.global, "global", false, "use the home-global workflow directory")
}

func installWorkflow(command *cobra.Command, deps dependencies, source string, config workflowLifecycleConfig) error {
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	vars, err := parseVars(command.Context(), cwd, config.variableFiles, config.variables)
	if err != nil {
		return err
	}
	env, err := parseEnv(config.environment)
	if err != nil {
		return err
	}
	baseEnv, err := invocationEnvironment(command, deps, cwd)
	if err != nil {
		return err
	}
	loader := deps.loader
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Diagnostics: diagnosticsFor(command, deps, cwd)}
	sourceDefinition, cleanup, err := decodeInstallSource(command.Context(), loader, source, loadOptions)
	if err != nil {
		return err
	}
	defer cleanup()
	if !workflow.ValidWorkflowName(sourceDefinition.Name) {
		return fmt.Errorf("workflow name %q cannot be used as an installed filename", sourceDefinition.Name)
	}
	data, err := os.ReadFile(sourceDefinition.Path)
	if err != nil {
		return fmt.Errorf("reading workflow source %s: %w", sourceDefinition.Path, err)
	}
	if !workflow.IsRemoteLocator(source) {
		if err := validateStandaloneInstallSource(sourceDefinition.Path, sourceDefinition); err != nil {
			return err
		}
	}

	workflowDir := workflowStorageDir(cwd, home, config.global)
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		return fmt.Errorf("creating workflow directory %s: %w", workflowDir, err)
	}
	target, err := workflowInstallPath(workflowDir, sourceDefinition.Name)
	if err != nil {
		return err
	}
	stage, err := stageWorkflow(workflowDir, sourceDefinition.Name, data)
	if err != nil {
		return err
	}
	defer os.Remove(stage)

	stagedOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: workflowDir, Diagnostics: diagnosticsFor(command, deps, workflowDir)}
	definition, err := loader.Load(command.Context(), stage, stagedOptions)
	if err != nil {
		return fmt.Errorf("preparing workflow for installation: %w", err)
	}
	options := lifecycleEngineOptions(command, deps, definition, vars, env, baseEnv, workflowDir, configDir)
	engineFor := workflowEngine(deps)
	if err := engineFor.Validate(command.Context(), definition, options); err != nil {
		return fmt.Errorf("validating workflow %q: %w", definition.Name, err)
	}
	if _, err := engineFor.RunSteps(command.Context(), definition, definition.Install, options); err != nil {
		return fmt.Errorf("running install hook for workflow %q: %w", definition.Name, err)
	}
	if err := replaceWorkflowFile(stage, target); err != nil {
		return fmt.Errorf("installing workflow %q: %w", definition.Name, err)
	}
	fmt.Fprintf(command.OutOrStdout(), "installed %s in %s\n", definition.Name, target)
	return nil
}

func uninstallWorkflow(command *cobra.Command, deps dependencies, name string, config workflowLifecycleConfig) error {
	if !workflow.ValidWorkflowName(name) {
		return fmt.Errorf("invalid workflow name %q", name)
	}
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	workflowDir := workflowStorageDir(cwd, home, config.global)
	path, err := findWorkflowPath(workflowDir, name)
	if err != nil {
		return err
	}
	vars, err := parseVars(command.Context(), cwd, config.variableFiles, config.variables)
	if err != nil {
		return err
	}
	env, err := parseEnv(config.environment)
	if err != nil {
		return err
	}
	baseEnv, err := invocationEnvironment(command, deps, cwd)
	if err != nil {
		return err
	}
	loader := deps.loader
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: workflowDir, Diagnostics: diagnosticsFor(command, deps, workflowDir)}
	definition, err := loader.Load(command.Context(), path, loadOptions)
	if err != nil {
		return fmt.Errorf("loading workflow %q: %w", name, err)
	}
	options := lifecycleEngineOptions(command, deps, definition, vars, env, baseEnv, workflowDir, configDir)
	engineFor := workflowEngine(deps)
	if err := engineFor.Validate(command.Context(), definition, options); err != nil {
		return fmt.Errorf("validating workflow %q: %w", name, err)
	}

	if !config.yes {
		if deps.isInteractive == nil || !deps.isInteractive(command.InOrStdin()) {
			return fmt.Errorf("uninstall requires confirmation; rerun with --yes in non-interactive mode")
		}
		confirmed, err := deps.confirm(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Uninstall workflow "+name+"?", false)
		if err != nil {
			return fmt.Errorf("confirming uninstall: %w", err)
		}
		if !confirmed {
			return nil
		}
	}
	if _, err := engineFor.RunSteps(command.Context(), definition, definition.Uninstall, options); err != nil {
		return fmt.Errorf("running uninstall hook for workflow %q: %w", name, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing workflow %q: %w", name, err)
	}
	fmt.Fprintf(command.OutOrStdout(), "uninstalled %s from %s\n", name, path)
	return nil
}

func decodeInstallSource(ctx context.Context, loader *workflow.Loader, source string, options workflow.LoadOptions) (*workflow.Definition, func(), error) {
	if workflow.IsRemoteLocator(source) {
		options.RejectRemoteArchives = true
		return loader.DecodeRemote(ctx, source, options)
	}
	path, err := filepath.Abs(source)
	if err != nil {
		return nil, func() {}, fmt.Errorf("resolving workflow source %s: %w", source, err)
	}
	definition, err := loader.Decode(path, options)
	if err != nil {
		return nil, func() {}, err
	}
	return definition, func() {}, nil
}

func validateStandaloneInstallSource(path string, definition *workflow.Definition) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading workflow source %s: %w", path, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("checking workflow source %s: %w", path, err)
	}
	if yamlNodeHasKey(&document, "require") {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use required step files", path)
	}
	for name, template := range definition.Templates {
		if template.File != "" {
			return fmt.Errorf("installing local workflow %s: standalone workflows cannot use file-backed template %q", path, name)
		}
	}
	if reference := localActionReference(definition.Steps); reference != "" {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use local action %q", path, reference)
	}
	if reference := localActionReference(definition.Finally); reference != "" {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use local action %q", path, reference)
	}
	if reference := localActionReference(definition.Install); reference != "" {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use local action %q", path, reference)
	}
	if reference := localActionReference(definition.Uninstall); reference != "" {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use local action %q", path, reference)
	}
	if script := relativeShellScript(definition.Install); script != "" {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use relative lifecycle script %q", path, script)
	}
	if script := relativeShellScript(definition.Uninstall); script != "" {
		return fmt.Errorf("installing local workflow %s: standalone workflows cannot use relative lifecycle script %q", path, script)
	}
	return nil
}

func yamlNodeHasKey(node *yaml.Node, key string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key || yamlNodeHasKey(node.Content[i+1], key) {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if yamlNodeHasKey(child, key) {
			return true
		}
	}
	return false
}

func localActionReference(steps []workflow.Step) string {
	for _, workflowStep := range steps {
		if workflowStep.Uses.Path != "" {
			return workflowStep.Uses.Path
		}
		for _, child := range workflowStep.ChildSequences() {
			if reference := localActionReference(child.Steps); reference != "" {
				return reference
			}
		}
	}
	return ""
}

func relativeShellScript(steps []workflow.Step) string {
	for _, workflowStep := range steps {
		if workflowStep.Type == "shell" {
			if script, ok := workflowStep.With["script"].(string); ok {
				script = strings.TrimSpace(script)
				if isStandaloneShellScriptReference(script) {
					return script
				}
			}
		}
		for _, child := range workflowStep.ChildSequences() {
			if script := relativeShellScript(child.Steps); script != "" {
				return script
			}
		}
	}
	return ""
}

func isStandaloneShellScriptReference(script string) bool {
	if strings.HasPrefix(script, "./") || strings.HasPrefix(script, "../") || filepath.IsAbs(script) {
		return true
	}
	return !strings.ContainsAny(script, " \t\r\n;|&<>$`'\"") && filepath.Ext(script) != ""
}

func workflowStorageDir(cwd, home string, global bool) string {
	if global {
		return filepath.Join(home, ".wuko", "workflows")
	}
	return filepath.Join(cwd, ".wuko", "workflows")
}

func workflowInstallPath(directory, name string) (string, error) {
	target := filepath.Join(directory, name+".yaml")
	legacy := filepath.Join(directory, name+".yml")
	if _, err := os.Stat(legacy); err == nil {
		return "", fmt.Errorf("workflow %q already exists as %s", name, legacy)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking workflow %q: %w", name, err)
	}
	return target, nil
}

func findWorkflowPath(directory, name string) (string, error) {
	paths := []string{filepath.Join(directory, name+".yaml"), filepath.Join(directory, name+".yml")}
	found := ""
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			if found != "" {
				return "", fmt.Errorf("workflow %q is declared twice in %s", name, directory)
			}
			found = path
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking workflow %q: %w", name, err)
		}
	}
	if found == "" {
		return "", fmt.Errorf("workflow %q not found in %s", name, directory)
	}
	return found, nil
}

func stageWorkflow(directory, name string, data []byte) (string, error) {
	file, err := os.CreateTemp(directory, "."+name+"-install-*")
	if err != nil {
		return "", fmt.Errorf("creating workflow staging file: %w", err)
	}
	path := file.Name()
	cleanup := func() {
		file.Close()
		_ = os.Remove(path)
	}
	if err := file.Chmod(0o644); err != nil {
		cleanup()
		return "", fmt.Errorf("setting workflow staging permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return "", fmt.Errorf("writing workflow staging file: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("syncing workflow staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("closing workflow staging file: %w", err)
	}
	return path, nil
}

func replaceWorkflowFile(stage, target string) error {
	if err := os.Rename(stage, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("opening workflow directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("syncing workflow directory: %w", err)
	}
	return nil
}

func lifecycleEngineOptions(command *cobra.Command, deps dependencies, definition *workflow.Definition, vars map[string]any, env, baseEnv map[string]string, runDir, configDir string) engine.Options {
	return engine.Options{
		Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: runDir,
		LocalValueDir: filepath.Join(definition.Dir, ".wuko", "values"), GlobalValueDir: filepath.Join(configDir, "wuko", "values"),
		Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
		Interactive: deps.isInteractive != nil && deps.isInteractive(command.InOrStdin()),
		Diagnostics: diagnosticsFor(command, deps, runDir),
	}
}
