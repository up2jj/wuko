package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

type workflowLifecycleConfig struct {
	variables     []string
	variableFiles []string
	environment   []string
	global        bool
	yes           bool
	packages      []string
	reinstall     bool
	storageDir    string
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
	command.Flags().StringArrayVar(&config.packages, "package", nil, "select a marketplace package (repeatable)")
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
	invocation := installInvocation{cwd: cwd, home: home, configDir: configDir, storageDir: config.storageDir, vars: vars, env: env, baseEnv: baseEnv, loader: loader}
	if isHTTPSURL(source) {
		manifest, manifestErr := loader.DiscoverMarketplace(command.Context(), source)
		if manifestErr == nil {
			return installMarketplace(command, deps, source, config, invocation, manifest)
		}
		if !errors.Is(manifestErr, workflow.ErrMarketplaceNotFound) {
			return manifestErr
		}
	}
	if len(config.packages) > 0 {
		return fmt.Errorf("--package can only be used when SOURCE is a marketplace")
	}
	return installSingleWorkflow(command, deps, source, config, invocation, workflowStorageDir(cwd, home, config.global))
}

type installInvocation struct {
	cwd        string
	home       string
	configDir  string
	storageDir string
	vars       map[string]any
	env        map[string]string
	baseEnv    map[string]string
	loader     *workflow.Loader
}

type preparedInstallSource struct {
	definition *workflow.Definition
	data       []byte
	cleanup    func()
}

type preparedMarketplacePackage struct {
	item       workflow.MarketplacePackage
	definition *workflow.Definition
	directory  string
	cleanup    func()
}

func installMarketplace(command *cobra.Command, deps dependencies, source string, config workflowLifecycleConfig, invocation installInvocation, manifest workflow.MarketplaceManifest) error {
	if len(manifest.Packages) == 0 {
		return fmt.Errorf("marketplace %s contains no packages", source)
	}
	selectedSet, err := selectMarketplacePackages(command, deps, manifest, config.packages)
	if err != nil {
		return err
	}
	if selectedSet == nil {
		return nil
	}
	prepared := make([]preparedMarketplacePackage, 0, len(selectedSet))
	defer func() {
		for _, item := range prepared {
			item.cleanup()
		}
	}()
	seenNames := make(map[string]string, len(selectedSet))
	for index, item := range manifest.Packages {
		if _, selected := selectedSet[index]; !selected {
			continue
		}
		loadOptions := workflow.LoadOptions{Vars: invocation.vars, Env: invocation.env, BaseEnv: invocation.baseEnv, RunDir: invocation.cwd, Diagnostics: diagnosticsFor(command, deps, invocation.cwd)}
		definition, directory, cleanup, err := invocation.loader.LoadMarketplacePackage(command.Context(), source, item, loadOptions)
		if err != nil {
			return fmt.Errorf("preparing marketplace package %q: %w", item.Name, err)
		}
		if previous, exists := seenNames[definition.Name]; exists {
			cleanup()
			return fmt.Errorf("marketplace packages %q and %q both define workflow name %q", previous, item.Name, definition.Name)
		}
		seenNames[definition.Name] = item.Name
		prepared = append(prepared, preparedMarketplacePackage{item: item, definition: definition, directory: directory, cleanup: cleanup})
	}
	repositoryName, err := workflow.MarketplaceRepositoryName(source)
	if err != nil {
		return err
	}
	canonicalURL, err := workflow.MarketplaceURL(source)
	if err != nil {
		return err
	}
	root := invocation.storageDir
	if root == "" {
		root = workflowStorageDir(invocation.cwd, invocation.home, config.global)
	}
	repositoryDir, err := prepareMarketplaceDirectory(root, repositoryName, canonicalURL)
	if err != nil {
		return err
	}
	for _, item := range prepared {
		if err := installPreparedMarketplacePackage(command, deps, config, invocation, repositoryDir, canonicalURL, item); err != nil {
			return fmt.Errorf("installing marketplace package %q: %w", item.item.Name, err)
		}
	}
	return nil
}

func selectMarketplacePackages(command *cobra.Command, deps dependencies, manifest workflow.MarketplaceManifest, requested []string) (map[int]struct{}, error) {
	if len(requested) > 0 {
		selected := make(map[int]struct{}, len(requested))
		indexes := make(map[string]int, len(manifest.Packages))
		for index, item := range manifest.Packages {
			indexes[item.Name] = index
		}
		for _, name := range requested {
			index, ok := indexes[name]
			if !ok {
				return nil, fmt.Errorf("marketplace package %q was not found", name)
			}
			if _, exists := selected[index]; exists {
				return nil, fmt.Errorf("marketplace package %q was selected more than once", name)
			}
			selected[index] = struct{}{}
		}
		return selected, nil
	}
	if deps.isInteractive == nil || !deps.isInteractive(command.InOrStdin()) {
		return nil, fmt.Errorf("marketplace install requires an interactive terminal or at least one --package flag")
	}
	options := make([]tui.Option, len(manifest.Packages))
	for index, item := range manifest.Packages {
		options[index] = tui.Option{Label: item.Name, Description: marketplacePackageDescription(item), Path: item.Path, Value: item}
	}
	selectMany := deps.selectMany
	if selectMany == nil {
		selectMany = tui.SelectMany
	}
	selectedIndexes, err := selectMany(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Marketplace packages", options)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil
		}
		return nil, fmt.Errorf("selecting marketplace packages: %w", err)
	}
	selected := make(map[int]struct{}, len(selectedIndexes))
	for _, index := range selectedIndexes {
		if index < 0 || index >= len(manifest.Packages) {
			return nil, fmt.Errorf("marketplace picker returned invalid package index %d", index)
		}
		selected[index] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("select at least one marketplace package")
	}
	return selected, nil
}

func installSingleWorkflow(command *cobra.Command, deps dependencies, source string, config workflowLifecycleConfig, invocation installInvocation, workflowDir string) error {
	prepared, err := prepareInstallSource(command, deps, invocation, source)
	if err != nil {
		return err
	}
	defer prepared.cleanup()
	return installPreparedWorkflow(command, deps, config, invocation, workflowDir, prepared)
}

func prepareInstallSource(command *cobra.Command, deps dependencies, invocation installInvocation, source string) (*preparedInstallSource, error) {
	loadOptions := workflow.LoadOptions{Vars: invocation.vars, Env: invocation.env, BaseEnv: invocation.baseEnv, RunDir: invocation.cwd, Diagnostics: diagnosticsFor(command, deps, invocation.cwd)}
	sourceDefinition, cleanup, err := decodeInstallSource(command.Context(), invocation.loader, source, loadOptions)
	if err != nil {
		return nil, err
	}
	if !workflow.ValidWorkflowName(sourceDefinition.Name) {
		cleanup()
		return nil, fmt.Errorf("workflow name %q cannot be used as an installed filename", sourceDefinition.Name)
	}
	data, err := os.ReadFile(sourceDefinition.Path)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("reading workflow source %s: %w", sourceDefinition.Path, err)
	}
	if !workflow.IsRemoteLocator(source) {
		if err := validateStandaloneInstallSource(sourceDefinition.Path, sourceDefinition); err != nil {
			cleanup()
			return nil, err
		}
	}
	return &preparedInstallSource{definition: sourceDefinition, data: data, cleanup: cleanup}, nil
}

func installPreparedWorkflow(command *cobra.Command, deps dependencies, config workflowLifecycleConfig, invocation installInvocation, workflowDir string, prepared *preparedInstallSource) error {
	sourceDefinition := prepared.definition
	data := prepared.data
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

	stagedOptions := workflow.LoadOptions{Vars: invocation.vars, Env: invocation.env, BaseEnv: invocation.baseEnv, RunDir: workflowDir, Diagnostics: diagnosticsFor(command, deps, workflowDir), Lifecycle: true}
	definition, err := invocation.loader.Load(command.Context(), stage, stagedOptions)
	if err != nil {
		return fmt.Errorf("preparing workflow for installation: %w", err)
	}
	options := lifecycleEngineOptions(command, deps, definition, invocation.vars, invocation.env, invocation.baseEnv, workflowDir, invocation.configDir)
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
	_, err = fmt.Fprintf(command.OutOrStdout(), "installed %s in %s\n", definition.Name, target)
	return err
}

type marketplacePackageInstallMarker struct {
	Version        int    `json:"version"`
	Marketplace    string `json:"marketplace"`
	Name           string `json:"name"`
	PackageVersion string `json:"package_version,omitempty"`
	Source         string `json:"source"`
	SHA256         string `json:"sha256"`
}

func installPreparedMarketplacePackage(command *cobra.Command, deps dependencies, config workflowLifecycleConfig, invocation installInvocation, repositoryDir, canonicalURL string, prepared preparedMarketplacePackage) error {
	name := prepared.definition.Name
	if !workflow.ValidWorkflowName(name) {
		return fmt.Errorf("workflow name %q cannot be used as an installed package directory", name)
	}
	target := filepath.Join(repositoryDir, name)
	if info, err := os.Stat(target); err == nil {
		if !config.reinstall {
			return fmt.Errorf("workflow package %q already exists as %s", name, target)
		}
		if !info.IsDir() {
			return fmt.Errorf("workflow package %q exists as a non-directory %s", name, target)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking workflow package %q: %w", name, err)
	}
	stage, err := os.MkdirTemp(repositoryDir, "."+name+"-install-*")
	if err != nil {
		return fmt.Errorf("creating workflow package staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o755); err != nil {
		return fmt.Errorf("setting workflow package staging permissions: %w", err)
	}
	if err := workflow.CopyWorkflowPackage(prepared.directory, stage); err != nil {
		return fmt.Errorf("copying workflow package: %w", err)
	}
	stagedPath := filepath.Join(stage, "wuko.yaml")
	stagedOptions := workflow.LoadOptions{Vars: invocation.vars, Env: invocation.env, BaseEnv: invocation.baseEnv, RunDir: stage, Diagnostics: diagnosticsFor(command, deps, stage), Lifecycle: true}
	definition, err := invocation.loader.Load(command.Context(), stagedPath, stagedOptions)
	if err != nil {
		return fmt.Errorf("preparing workflow package for installation: %w", err)
	}
	options := lifecycleEngineOptions(command, deps, definition, invocation.vars, invocation.env, invocation.baseEnv, stage, invocation.configDir)
	engineFor := workflowEngine(deps)
	if err := engineFor.Validate(command.Context(), definition, options); err != nil {
		return fmt.Errorf("validating workflow package %q: %w", definition.Name, err)
	}
	if _, err := engineFor.RunSteps(command.Context(), definition, definition.Install, options); err != nil {
		return fmt.Errorf("running install hook for workflow package %q: %w", definition.Name, err)
	}
	marker := marketplacePackageInstallMarker{Version: workflow.MarketplaceManifestVersion, Marketplace: canonicalURL, Name: name, PackageVersion: prepared.item.PackageVersion, Source: prepared.item.Source, SHA256: prepared.item.SHA256}
	if err := writeJSONAtomically(filepath.Join(stage, workflow.WorkflowPackageMarkerName), marker); err != nil {
		return fmt.Errorf("writing workflow package marker: %w", err)
	}
	backup, err := replaceMarketplacePackage(stage, target, config.reinstall)
	if err != nil {
		return fmt.Errorf("committing workflow package %q: %w", name, err)
	}
	if backup != "" {
		defer os.RemoveAll(backup)
	}
	message := "installed"
	if config.reinstall {
		message = "reinstalled"
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "%s %s in %s\n", message, name, target)
	return err
}

func replaceMarketplacePackage(stage, target string, replace bool) (string, error) {
	if !replace {
		if err := os.Rename(stage, target); err != nil {
			return "", err
		}
		return "", nil
	}
	backup, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+"-reinstall-backup-*")
	if err != nil {
		return "", fmt.Errorf("creating workflow package backup: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return "", fmt.Errorf("preparing workflow package backup: %w", err)
	}
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("backing up workflow package: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return "", errors.Join(err, fmt.Errorf("restoring workflow package: %w", restoreErr))
		}
		return "", err
	}
	return backup, nil
}

func reinstallMarketplaceWorkflow(command *cobra.Command, deps dependencies, source workflow.Source) error {
	if source.MarketplaceURL == "" || source.PackageDir == "" {
		return fmt.Errorf("workflow %q is not an installed marketplace package", source.Name)
	}
	config := workflowLifecycleConfig{
		packages:   []string{source.Name},
		reinstall:  true,
		storageDir: filepath.Dir(filepath.Dir(source.PackageDir)),
	}
	return installWorkflow(command, deps, source.MarketplaceURL, config)
}

func marketplacePackageDescription(item workflow.MarketplacePackage) string {
	if item.PackageVersion == "" {
		return item.Description
	}
	if item.Description == "" {
		return "package " + item.PackageVersion
	}
	return item.Description + " • package " + item.PackageVersion
}

const marketplaceMarkerName = ".wuko-marketplace.json"

type marketplaceInstallMarker struct {
	Version int    `json:"version"`
	URL     string `json:"url"`
}

func prepareMarketplaceDirectory(root, repositoryName, canonicalURL string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("creating workflow directory %s: %w", root, err)
	}
	directory := filepath.Join(root, repositoryName)
	info, err := os.Stat(directory)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking marketplace directory %s: %w", directory, err)
		}
		if err := os.Mkdir(directory, 0o755); err != nil {
			return "", fmt.Errorf("creating marketplace directory %s: %w", directory, err)
		}
		if err := writeMarketplaceMarker(directory, canonicalURL); err != nil {
			return "", err
		}
		return directory, nil
	}
	if !info.IsDir() {
		return "", fmt.Errorf("marketplace repository path %s is not a directory", directory)
	}
	markerPath := filepath.Join(directory, marketplaceMarkerName)
	markerData, markerErr := os.ReadFile(markerPath)
	if markerErr == nil {
		var marker marketplaceInstallMarker
		decoder := json.NewDecoder(strings.NewReader(string(markerData)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&marker); err != nil {
			return "", fmt.Errorf("reading marketplace marker %s: %w", markerPath, err)
		}
		if marker.Version != workflow.MarketplaceManifestVersion || marker.URL != canonicalURL {
			return "", fmt.Errorf("marketplace directory %s belongs to a different marketplace", directory)
		}
		return directory, nil
	}
	if !os.IsNotExist(markerErr) {
		return "", fmt.Errorf("checking marketplace marker %s: %w", markerPath, markerErr)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("reading marketplace directory %s: %w", directory, err)
	}
	if len(entries) != 0 {
		return "", fmt.Errorf("marketplace directory %s exists without a matching marker; refusing to mix repositories", directory)
	}
	if err := writeMarketplaceMarker(directory, canonicalURL); err != nil {
		return "", err
	}
	return directory, nil
}

func writeMarketplaceMarker(directory, canonicalURL string) error {
	return writeJSONAtomically(filepath.Join(directory, marketplaceMarkerName), marketplaceInstallMarker{Version: workflow.MarketplaceManifestVersion, URL: canonicalURL})
}

func isHTTPSURL(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func uninstallWorkflow(command *cobra.Command, deps dependencies, name string, config workflowLifecycleConfig) error {
	if !workflow.ValidWorkflowSelector(name) {
		return fmt.Errorf("invalid workflow name %q", name)
	}
	cwd, home, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	workflowDir := workflowStorageDir(cwd, home, config.global)
	loader := deps.loader
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	source, err := workflow.FindInDirectory(workflowDir, name)
	if err != nil {
		return err
	}
	path := source.Path
	removePath := path
	runDir := filepath.Dir(path)
	if source.PackageDir != "" {
		removePath = source.PackageDir
		runDir = source.PackageDir
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
	loadOptions := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: runDir, Diagnostics: diagnosticsFor(command, deps, runDir), Lifecycle: true}
	definition, err := loader.Load(command.Context(), path, loadOptions)
	if err != nil {
		return fmt.Errorf("loading workflow %q: %w", name, err)
	}
	options := lifecycleEngineOptions(command, deps, definition, vars, env, baseEnv, runDir, configDir)
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
	if err := os.RemoveAll(removePath); err != nil {
		return fmt.Errorf("removing workflow %q: %w", name, err)
	}
	fmt.Fprintf(command.OutOrStdout(), "uninstalled %s from %s\n", name, removePath)
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
	for name, target := range definition.Targets {
		if reference := localActionReference(target.Steps); reference != "" {
			return fmt.Errorf("installing local workflow %s: target %q cannot use local action %q", path, name, reference)
		}
		if reference := localActionReference(target.Finally); reference != "" {
			return fmt.Errorf("installing local workflow %s: target %q cannot use local action %q", path, name, reference)
		}
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
	for name, target := range definition.Targets {
		if script := relativeShellScript(target.Steps); script != "" {
			return fmt.Errorf("installing local workflow %s: target %q cannot use relative shell script %q", path, name, script)
		}
		if script := relativeShellScript(target.Finally); script != "" {
			return fmt.Errorf("installing local workflow %s: target %q cannot use relative shell script %q", path, name, script)
		}
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
