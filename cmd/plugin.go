package cmd

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

const scaffoldSDKFallbackVersion = "v0.16.0"

var (
	pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// releaseVersionPattern accepts only versions a published tag can carry. Anything looser lets a
	// source build pin the generated plugin to a module version that was never published: `go build`
	// stamps an untagged checkout with a pseudo-version (v0.16.1-0.20260928150208-c48a8da14c1a),
	// `git describe` yields v0.16.0-3-gabc1234, and goreleaser snapshots yield v0.16.1-next. None of
	// those resolve, so the generated go.mod would fail on the user's first build.
	releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:alpha|beta|rc)(?:[.-]?[0-9]+)?)?$`)
)

// scaffoldAssets holds the plugin starter tree written by `wuko plugin init`. Every template is
// stored with a .tmpl suffix so this module never tries to compile the generated plugin's code.
//
//go:embed scaffold
var scaffoldAssets embed.FS

func newPluginCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Install and manage executable plugins"}
	command.AddCommand(newPluginInstallCmd(deps), newPluginUninstallCmd(deps))
	return command
}

func newPluginInstallCmd(deps dependencies) *cobra.Command {
	var global, reinstall bool
	var packages []string
	command := &cobra.Command{Use: "install SOURCE", Short: "Install a plugin release or selected marketplace plugins", Example: "  wuko plugin install --package acme https://github.com/acme/wuko-marketplace\n  wuko plugin install --global --reinstall --package acme https://github.com/acme/wuko-marketplace", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		return installPlugin(command, deps, args[0], global, reinstall, packages)
	}}
	command.Flags().BoolVar(&global, "global", false, "install in the user plugin directory")
	command.Flags().BoolVar(&reinstall, "reinstall", false, "replace an existing installation")
	command.Flags().StringArrayVar(&packages, "package", nil, "select a marketplace plugin namespace (repeatable)")
	return command
}

func installPlugin(command *cobra.Command, deps dependencies, source string, global, reinstall bool, requested []string) error {
	cwd, home, _, err := directories(deps)
	if err != nil {
		return err
	}
	root := filepath.Join(cwd, ".wuko", "plugins")
	if global {
		root = filepath.Join(home, ".wuko", "plugins")
	}
	if isHTTPSURL(source) {
		manifest, manifestErr := deps.loader.DiscoverMarketplace(command.Context(), source)
		if manifestErr == nil {
			return installPluginMarketplace(command, deps, source, root, reinstall, requested, manifest)
		}
		if !errors.Is(manifestErr, workflow.ErrMarketplaceNotFound) {
			return manifestErr
		}
	}
	if len(requested) > 0 {
		return fmt.Errorf("--package can only be used when SOURCE is a marketplace")
	}
	return installPluginRelease(command, deps, source, "", "", "", root, reinstall)
}

func installPluginMarketplace(command *cobra.Command, deps dependencies, source, root string, reinstall bool, requested []string, manifest workflow.MarketplaceManifest) error {
	if len(manifest.Plugins) == 0 {
		return fmt.Errorf("marketplace %s contains no plugins", marketplaceDisplaySource(source))
	}
	selected, err := selectMarketplacePlugins(command, deps, manifest.Plugins, requested)
	if err != nil || selected == nil {
		return err
	}
	requests, err := marketplaceInstallRequests(source, manifest.Plugins, selected)
	if err != nil {
		return err
	}
	markers, err := pluginpkg.InstallMarketplaceBatch(command.Context(), requests, root, reinstall, deps.httpClient, command.ErrOrStderr())
	// A non-empty marker list means publication committed, so any error beside it comes from
	// post-commit cleanup. Reporting the installations first keeps the user from believing a
	// successful transaction was rolled back.
	if writeErr := reportInstalledPlugins(command, root, markers); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	if err != nil {
		if len(markers) > 0 {
			return fmt.Errorf("cleaning up after marketplace install: %w", err)
		}
		return fmt.Errorf("installing marketplace plugins: %w", err)
	}
	return nil
}

// marketplaceDisplaySource renders a marketplace source for an error message. MarketplaceURL
// drops the query string, so a source carrying a token in it is not echoed back to the terminal.
func marketplaceDisplaySource(source string) string {
	display, err := workflow.MarketplaceURL(source)
	if err != nil {
		return "HTTPS marketplace"
	}
	return display
}

func marketplaceInstallRequests(source string, plugins []workflow.MarketplacePluginPackage, selected map[int]struct{}) ([]pluginpkg.MarketplaceInstallRequest, error) {
	requests := make([]pluginpkg.MarketplaceInstallRequest, 0, len(selected))
	for index, item := range plugins {
		if _, ok := selected[index]; !ok {
			continue
		}
		resolved, err := workflow.ResolveMarketplacePlugin(source, item)
		if err != nil {
			return nil, fmt.Errorf("resolving marketplace plugin %q: %w", item.Namespace, err)
		}
		requests = append(requests, pluginpkg.MarketplaceInstallRequest{
			Source:         resolved,
			ManifestDigest: item.SHA256,
			Namespace:      item.Namespace,
			PluginVersion:  item.PluginVersion,
		})
	}
	return requests, nil
}

func reportInstalledPlugins(command *cobra.Command, root string, markers []pluginpkg.InstallationMarker) error {
	for _, marker := range markers {
		location := filepath.Join(root, marker.Namespace)
		if _, err := fmt.Fprintf(command.OutOrStdout(), "Installed plugin %s %s in %s\n", marker.Namespace, marker.PluginVersion, location); err != nil {
			return err
		}
	}
	return nil
}

func installPluginRelease(command *cobra.Command, deps dependencies, source, expectedDigest, expectedNamespace, expectedVersion, root string, reinstall bool) error {
	var marker pluginpkg.InstallationMarker
	var err error
	if expectedNamespace == "" {
		marker, err = pluginpkg.InstallPinned(command.Context(), source, expectedDigest, root, reinstall, deps.httpClient, command.ErrOrStderr())
	} else {
		marker, err = pluginpkg.InstallMarketplace(command.Context(), source, expectedDigest, expectedNamespace, expectedVersion, root, reinstall, deps.httpClient, command.ErrOrStderr())
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Installed plugin %s %s in %s\n", marker.Namespace, marker.PluginVersion, filepath.Join(root, marker.Namespace))
	return err
}

func selectMarketplacePlugins(command *cobra.Command, deps dependencies, plugins []workflow.MarketplacePluginPackage, requested []string) (map[int]struct{}, error) {
	if len(requested) > 0 {
		indexes := make(map[string]int, len(plugins))
		for index, item := range plugins {
			indexes[item.Namespace] = index
		}
		selected := make(map[int]struct{}, len(requested))
		for _, namespace := range requested {
			index, ok := indexes[namespace]
			if !ok {
				return nil, fmt.Errorf("marketplace plugin %q was not found", namespace)
			}
			if !marketplacePluginSupportsCurrentPlatform(plugins[index]) {
				return nil, fmt.Errorf("marketplace plugin %q does not support %s/%s", namespace, runtime.GOOS, runtime.GOARCH)
			}
			if _, exists := selected[index]; exists {
				return nil, fmt.Errorf("marketplace plugin %q was selected more than once", namespace)
			}
			selected[index] = struct{}{}
		}
		return selected, nil
	}
	if deps.isInteractive == nil || !deps.isInteractive(command.InOrStdin()) {
		return nil, fmt.Errorf("marketplace plugin install requires an interactive terminal or at least one --package flag")
	}
	var options []tui.Option
	var indexes []int
	for index, item := range plugins {
		if !marketplacePluginSupportsCurrentPlatform(item) {
			continue
		}
		options = append(options, tui.Option{Label: item.Namespace, Description: marketplacePluginDescription(item), Path: item.Path, Value: item})
		indexes = append(indexes, index)
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("marketplace contains no plugins for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	selectMany := deps.selectMany
	if selectMany == nil {
		selectMany = tui.SelectMany
	}
	chosen, err := selectMany(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Marketplace plugins", options)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil
		}
		return nil, fmt.Errorf("selecting marketplace plugins: %w", err)
	}
	selected := make(map[int]struct{}, len(chosen))
	for _, optionIndex := range chosen {
		if optionIndex < 0 || optionIndex >= len(indexes) {
			return nil, fmt.Errorf("marketplace picker returned invalid plugin index %d", optionIndex)
		}
		selected[indexes[optionIndex]] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("select at least one marketplace plugin")
	}
	return selected, nil
}

func marketplacePluginSupportsCurrentPlatform(item workflow.MarketplacePluginPackage) bool {
	return slices.ContainsFunc(item.Platforms, func(platform workflow.MarketplacePlatform) bool {
		return platform.OS == runtime.GOOS && platform.Arch == runtime.GOARCH
	})
}

func marketplacePluginDescription(item workflow.MarketplacePluginPackage) string {
	version := "plugin " + item.PluginVersion
	if item.Description == "" {
		return version
	}
	return item.Description + " • " + version
}

func newPluginUninstallCmd(deps dependencies) *cobra.Command {
	var global, yes bool
	command := &cobra.Command{Use: "uninstall NAMESPACE", Short: "Uninstall a validated plugin installation", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		namespace := args[0]
		if !pluginNamePattern.MatchString(namespace) {
			return fmt.Errorf("invalid plugin namespace %q", namespace)
		}
		cwd, home, _, err := directories(deps)
		if err != nil {
			return err
		}
		root := filepath.Join(cwd, ".wuko", "plugins")
		if global {
			root = filepath.Join(home, ".wuko", "plugins")
		}
		directory := filepath.Join(root, namespace)
		if _, err := pluginpkg.ValidateInstallation(directory, namespace); err != nil {
			return err
		}
		if !yes {
			if !deps.isInteractive(command.InOrStdin()) {
				return fmt.Errorf("refusing non-interactive uninstall without --yes")
			}
			confirmed, err := deps.confirm(command.Context(), command.InOrStdin(), command.OutOrStdout(), fmt.Sprintf("Uninstall plugin %s?", namespace), false)
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
		}
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("removing plugin %s: %w", namespace, err)
		}
		fmt.Fprintf(command.OutOrStdout(), "Uninstalled plugin %s\n", namespace)
		return nil
	}}
	command.Flags().BoolVar(&global, "global", false, "use the user plugin directory")
	command.Flags().BoolVar(&yes, "yes", false, "confirm uninstall non-interactively")
	return command
}

func newPluginInitCmd() *cobra.Command {
	return &cobra.Command{Use: "init NAMESPACE [DIRECTORY]", Short: "Create a standalone Go executable plugin", Example: "  wuko marketplace plugin init hello\n  wuko marketplace plugin init hello ./plugins/hello", Args: cobra.RangeArgs(1, 2), RunE: func(command *cobra.Command, args []string) error {
		namespace := args[0]
		if !pluginNamePattern.MatchString(namespace) {
			return fmt.Errorf("invalid plugin namespace %q", namespace)
		}
		directory := "wuko-plugin-" + namespace
		if len(args) == 2 {
			directory = args[1]
		}
		if !filepath.IsAbs(directory) {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			directory = filepath.Join(cwd, directory)
		}
		if err := writeGoPluginScaffold(directory, namespace); err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "Initialized Go plugin %s in %s\nRun `go mod tidy` there to resolve the pinned Wuko SDK before building.\n", namespace, directory)
		return nil
	}}
}

func writeGoPluginScaffold(directory, namespace string) error {
	if entries, err := os.ReadDir(directory); err == nil && len(entries) > 0 {
		return fmt.Errorf("directory %s is not empty", directory)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(directory, "examples"), 0755); err != nil {
		return err
	}
	files, err := scaffoldFiles()
	if err != nil {
		return err
	}
	for name, content := range files {
		content = strings.ReplaceAll(content, "{{NS}}", namespace)
		content = strings.ReplaceAll(content, "{{PROTOCOL}}", pluginpkg.Protocol)
		content = strings.ReplaceAll(content, "{{SDK_VERSION}}", scaffoldSDKVersion())
		target := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func scaffoldSDKVersion() string {
	buildVersion := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		buildVersion = info.Main.Version
	}
	return resolveScaffoldSDKVersion(buildVersion, version)
}

func resolveScaffoldSDKVersion(buildVersion, cliVersion string) string {
	for _, candidate := range []string{buildVersion, cliVersion} {
		if releaseVersionPattern.MatchString(candidate) {
			return candidate
		}
	}
	return scaffoldSDKFallbackVersion
}

// scaffoldFiles returns the plugin starter tree, keyed by the path each file takes in the
// generated project. The sources live under scaffold/ as real files rather than string literals
// so they stay gofmt-clean and reviewable; TestScaffoldIsFormatted holds that.
func scaffoldFiles() (map[string]string, error) {
	files := make(map[string]string)
	err := fs.WalkDir(scaffoldAssets, "scaffold", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := scaffoldAssets.ReadFile(path)
		if err != nil {
			return err
		}
		files[scaffoldTargetPath(path)] = string(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading plugin scaffold: %w", err)
	}
	return files, nil
}

// scaffoldTargetPath maps an embedded template to its path in the generated project. The .tmpl
// suffix keeps the templates out of this module's build, and the dot- prefix carries a leading
// dot that go:embed would otherwise skip.
func scaffoldTargetPath(path string) string {
	target := strings.TrimSuffix(strings.TrimPrefix(path, "scaffold/"), ".tmpl")
	directory, name := filepath.Split(target)
	return directory + strings.Replace(name, "dot-", ".", 1)
}
