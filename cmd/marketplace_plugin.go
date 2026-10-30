package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/marketplace"
	pluginpkg "github.com/up2jj/wuko/plugin"
)

const pluginReleaseWorkflowName = "wuko.release.yaml"

func newMarketplacePluginCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Create and manage marketplace plugins"}
	command.AddCommand(newPluginInitCmd(), newMarketplacePluginAddCmd(deps), newMarketplacePluginUpdateCmd(deps), newMarketplacePluginReleaseCmd(deps), newMarketplacePluginRemoveCmd(deps))
	return command
}

func newMarketplacePluginReleaseCmd(deps dependencies) *cobra.Command {
	var description string
	var requestedVersion string
	var skipBuild bool
	command := &cobra.Command{
		Use:     "release SOURCE",
		Short:   "Build, validate, and publish a plugin release",
		Example: "  wuko marketplace plugin release --version 0.1.0 --description \"A simple uppercase step\" ../wuko-plugin-hello\n  wuko marketplace plugin release --skip-build ./completed-bundle",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return releaseMarketplacePlugin(command, deps, args[0], requestedVersion, skipBuild, description, optionProvided(command, "description"))
		},
	}
	command.Flags().StringVar(&description, "description", "", "describe a new plugin or replace its marketplace description")
	command.Flags().StringVar(&requestedVersion, "version", "", "version passed to wuko.release.yaml and required for local builds")
	command.Flags().BoolVar(&skipBuild, "skip-build", false, "publish an already-built local bundle without running wuko.release.yaml")
	return command
}

func newMarketplacePluginRemoveCmd(deps dependencies) *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:     "remove NAMESPACE",
		Short:   "Remove an imported plugin source from the marketplace",
		Example: "  wuko marketplace plugin remove hello --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			namespace := args[0]
			if !pluginNamePattern.MatchString(namespace) {
				return fmt.Errorf("invalid plugin namespace %q", namespace)
			}
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			target := filepath.Join(marketplace.PluginSourceRoot(cwd), namespace)
			info, err := os.Lstat(target)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("marketplace plugin %q is not imported", namespace)
				}
				return fmt.Errorf("checking marketplace plugin %q: %w", namespace, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("marketplace plugin %q source is not a directory", namespace)
			}
			if !yes {
				if deps.isInteractive == nil || !deps.isInteractive(command.InOrStdin()) {
					return fmt.Errorf("plugin removal requires confirmation; rerun with --yes in non-interactive mode")
				}
				if deps.confirm == nil {
					return fmt.Errorf("plugin removal confirmation is unavailable")
				}
				confirmed, err := deps.confirm(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Remove marketplace plugin "+namespace+"?", false)
				if err != nil {
					return fmt.Errorf("confirming plugin removal: %w", err)
				}
				if !confirmed {
					return nil
				}
			}
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("removing marketplace plugin %q: %w", namespace, err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "removed plugin %s\nRun `wuko marketplace build` to remove its published files.\n", namespace)
			return err
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "confirm removal non-interactively")
	markCLIOnly(command, "yes")
	return command
}

func prepareMarketplacePluginRelease(command *cobra.Command, deps dependencies, source, requestedVersion string, skipBuild bool) (string, error) {
	if marketplacePluginReleaseSourceIsRemote(source) || skipBuild {
		return source, nil
	}
	if requestedVersion == "" {
		return "", fmt.Errorf("--version is required when building a local plugin release")
	}
	project, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolving plugin project %s: %w", source, err)
	}
	info, err := os.Lstat(project)
	if err != nil {
		return "", fmt.Errorf("checking plugin project %s: %w", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("plugin project %s must be a directory, not a symlink; use --skip-build for a completed bundle", source)
	}
	workflowPath := filepath.Join(project, pluginReleaseWorkflowName)
	workflowInfo, err := os.Lstat(workflowPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("plugin project %s has no %s; add the conventional release workflow or use --skip-build for a completed bundle", source, pluginReleaseWorkflowName)
		}
		return "", fmt.Errorf("checking plugin release workflow: %w", err)
	}
	if workflowInfo.Mode()&os.ModeSymlink != 0 || !workflowInfo.Mode().IsRegular() {
		return "", fmt.Errorf("plugin release workflow %s must be a regular file, not a symlink", workflowPath)
	}
	outputs, err := runLocalWorkflowForOutputs(command, deps, workflowPath, project, []string{"version=" + requestedVersion})
	if err != nil {
		return "", fmt.Errorf("running %s: %w", pluginReleaseWorkflowName, err)
	}
	bundle, ok := outputs["bundle"].(string)
	if !ok || strings.TrimSpace(bundle) == "" {
		return "", fmt.Errorf("%s must return a non-empty string output named bundle", pluginReleaseWorkflowName)
	}
	resolved, err := resolvePluginReleaseBundle(project, bundle)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(command.OutOrStdout(), "built plugin release with %s\n", pluginReleaseWorkflowName); err != nil {
		return "", err
	}
	return resolved, nil
}

func marketplacePluginReleaseSourceIsRemote(source string) bool {
	return strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "github:")
}

func resolvePluginReleaseBundle(project, bundle string) (string, error) {
	if filepath.IsAbs(bundle) {
		return "", fmt.Errorf("%s output bundle must be relative to the plugin project", pluginReleaseWorkflowName)
	}
	candidate := filepath.Clean(filepath.Join(project, filepath.FromSlash(bundle)))
	if !pathWithinDirectory(project, candidate) {
		return "", fmt.Errorf("%s output bundle %q escapes the plugin project", pluginReleaseWorkflowName, bundle)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", fmt.Errorf("checking release workflow bundle %s: %w", candidate, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s output bundle %q must identify a directory, not a symlink", pluginReleaseWorkflowName, bundle)
	}
	realProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		return "", fmt.Errorf("resolving plugin project: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolving release workflow bundle: %w", err)
	}
	if !pathWithinDirectory(realProject, realCandidate) {
		return "", fmt.Errorf("%s output bundle %q resolves outside the plugin project", pluginReleaseWorkflowName, bundle)
	}
	return candidate, nil
}

func pathWithinDirectory(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func releaseMarketplacePlugin(command *cobra.Command, deps dependencies, source, requestedVersion string, skipBuild bool, description string, descriptionChanged bool) (resultErr error) {
	if strings.TrimSpace(description) != description {
		return fmt.Errorf("plugin description must not have leading or trailing whitespace")
	}
	if strings.TrimSpace(requestedVersion) != requestedVersion {
		return fmt.Errorf("plugin version must not have leading or trailing whitespace")
	}
	if requestedVersion != "" {
		if _, err := semver.StrictNewVersion(requestedVersion); err != nil {
			return fmt.Errorf("requested plugin version %q is not semantic: %w", requestedVersion, err)
		}
	}
	bundleSource, err := prepareMarketplacePluginRelease(command, deps, source, requestedVersion, skipBuild)
	if err != nil {
		return err
	}
	bundle, err := pluginpkg.FetchBundle(command.Context(), bundleSource, deps.httpClient)
	if err != nil {
		return fmt.Errorf("fetching plugin release: %w", err)
	}
	if requestedVersion != "" && bundle.Manifest.PluginVersion != requestedVersion {
		return fmt.Errorf("plugin bundle version %s does not match requested version %s", bundle.Manifest.PluginVersion, requestedVersion)
	}
	newVersion, err := semver.StrictNewVersion(bundle.Manifest.PluginVersion)
	if err != nil {
		return fmt.Errorf("plugin version %q is not semantic: %w", bundle.Manifest.PluginVersion, err)
	}
	cwd, err := deps.cwd()
	if err != nil {
		return fmt.Errorf("finding current directory: %w", err)
	}
	root := marketplace.PluginSourceRoot(cwd)
	target := filepath.Join(root, bundle.Manifest.Namespace)
	_, statErr := os.Stat(target)
	update := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("checking marketplace plugin %q: %w", bundle.Manifest.Namespace, statErr)
	}
	if !update {
		if !descriptionChanged || description == "" {
			return fmt.Errorf("a new marketplace plugin requires a non-empty --description")
		}
	} else {
		// Only the manifest decides ordering; the previous archives are read and hashed solely when
		// the versions match, so a normal upgrade does not hold two full releases in memory.
		previousManifestData, err := os.ReadFile(filepath.Join(target, "plugin.json"))
		if err != nil {
			return fmt.Errorf("loading previous marketplace plugin %q: %w", bundle.Manifest.Namespace, err)
		}
		previousManifest, err := pluginpkg.ParseManifest(previousManifestData)
		if err != nil {
			return fmt.Errorf("loading previous marketplace plugin %q: %w", bundle.Manifest.Namespace, err)
		}
		previousSource, err := marketplace.ReadPluginSource(target)
		if err != nil {
			return err
		}
		previousVersion, err := semver.StrictNewVersion(previousManifest.PluginVersion)
		if err != nil {
			return fmt.Errorf("previous plugin version %q is not semantic: %w", previousManifest.PluginVersion, err)
		}
		switch newVersion.Compare(previousVersion) {
		case -1:
			return fmt.Errorf("plugin version %s is older than published version %s", bundle.Manifest.PluginVersion, previousManifest.PluginVersion)
		case 0:
			previousBundle, err := pluginpkg.LoadBundle(target)
			if err != nil {
				return fmt.Errorf("loading previous marketplace plugin %q: %w", bundle.Manifest.Namespace, err)
			}
			if pluginpkg.DigestBundle(bundle) != pluginpkg.DigestBundle(previousBundle) {
				return fmt.Errorf("plugin version %s is already published with different contents", bundle.Manifest.PluginVersion)
			}
			if !descriptionChanged || description == previousSource.Description {
				changed, err := publishMarketplaceForPluginRelease(command, deps, cwd)
				if err != nil {
					return err
				}
				state := "already up to date"
				if changed {
					state = "rebuilt"
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "plugin %s %s is already published; marketplace %s\n", bundle.Manifest.Namespace, bundle.Manifest.PluginVersion, state)
				return err
			}
		}
		if !descriptionChanged {
			description = previousSource.Description
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating marketplace plugin directory: %w", err)
	}
	stage, err := os.MkdirTemp(root, "."+bundle.Manifest.Namespace+"-release-*")
	if err != nil {
		return fmt.Errorf("creating plugin release staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	metadata := marketplace.PluginSource{Version: 1, Source: marketplacePluginProvenance(bundle.CanonicalSource), Description: description}
	if err := writeMarketplacePluginBundle(stage, bundle, metadata); err != nil {
		return err
	}
	backup, err := replaceImportedPlugin(stage, target, update)
	if err != nil {
		return fmt.Errorf("committing marketplace plugin %q: %w", bundle.Manifest.Namespace, err)
	}
	committed := false
	defer func() {
		if committed {
			if backup != "" {
				if err := os.RemoveAll(backup); err != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("removing previous plugin backup: %w", err))
				}
			}
			return
		}
		if err := restoreImportedPlugin(target, backup, update); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restoring previous plugin import: %w", err))
		}
	}()
	if _, err := publishMarketplaceForPluginRelease(command, deps, cwd); err != nil {
		return err
	}
	committed = true
	action := "released"
	if update {
		action = "updated"
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "%s plugin %s %s with %d platform artifacts and rebuilt the marketplace\n", action, bundle.Manifest.Namespace, bundle.Manifest.PluginVersion, len(bundle.Manifest.Artifacts))
	return err
}

func publishMarketplaceForPluginRelease(command *cobra.Command, deps dependencies, cwd string) (bool, error) {
	build, err := marketplace.BuildManifest(marketplace.DecodeContext(command.Context()), cwd, deps.loader, diagnosticsFor(command, deps, cwd))
	if err != nil {
		return false, fmt.Errorf("building marketplace for plugin release: %w", err)
	}
	defer build.Cleanup()
	if !build.Changed {
		return false, nil
	}
	if err := marketplace.Publish(cwd, filepath.Join(cwd, "manifest.json"), build); err != nil {
		return false, fmt.Errorf("publishing marketplace for plugin release: %w", err)
	}
	return true, nil
}

func restoreImportedPlugin(target, backup string, update bool) error {
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if !update {
		return nil
	}
	if backup == "" {
		return fmt.Errorf("previous plugin backup is missing")
	}
	return os.Rename(backup, target)
}

func newMarketplacePluginAddCmd(deps dependencies) *cobra.Command {
	var description string
	command := &cobra.Command{
		Use:   "add SOURCE",
		Short: "Import a complete plugin release into the marketplace",
		Example: "  wuko marketplace plugin add --description \"A simple uppercase step\" " +
			"../wuko-plugin-hello",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return importMarketplacePlugin(command.Context(), command.OutOrStdout(), deps, args[0], "", description, optionProvided(command, "description"), false)
		},
	}
	command.Flags().StringVar(&description, "description", "", "describe the plugin in the marketplace picker")
	return command
}

func newMarketplacePluginUpdateCmd(deps dependencies) *cobra.Command {
	var description string
	command := &cobra.Command{
		Use:     "update NAMESPACE SOURCE",
		Short:   "Replace an imported marketplace plugin release",
		Example: "  wuko marketplace plugin update hello ../wuko-plugin-hello",
		Args:    cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			if !pluginNamePattern.MatchString(args[0]) {
				return fmt.Errorf("invalid plugin namespace %q", args[0])
			}
			return importMarketplacePlugin(command.Context(), command.OutOrStdout(), deps, args[1], args[0], description, optionProvided(command, "description"), true)
		},
	}
	command.Flags().StringVar(&description, "description", "", "replace the marketplace description, including with an empty value")
	return command
}

func importMarketplacePlugin(ctx context.Context, stdout io.Writer, deps dependencies, source, expectedNamespace, description string, descriptionChanged, update bool) error {
	if strings.TrimSpace(description) != description {
		return fmt.Errorf("plugin description must not have leading or trailing whitespace")
	}
	bundle, err := pluginpkg.FetchBundle(ctx, source, deps.httpClient)
	if err != nil {
		return fmt.Errorf("fetching plugin release: %w", err)
	}
	namespace := bundle.Manifest.Namespace
	if expectedNamespace != "" && namespace != expectedNamespace {
		return fmt.Errorf("plugin update namespace %q conflicts with manifest namespace %q", expectedNamespace, namespace)
	}
	cwd, err := deps.cwd()
	if err != nil {
		return fmt.Errorf("finding current directory: %w", err)
	}
	root := marketplace.PluginSourceRoot(cwd)
	target := filepath.Join(root, namespace)
	info, statErr := os.Stat(target)
	if update {
		if statErr != nil {
			if os.IsNotExist(statErr) {
				return fmt.Errorf("marketplace plugin %q is not imported; use plugin add", namespace)
			}
			return fmt.Errorf("checking marketplace plugin %q: %w", namespace, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("marketplace plugin %q exists as a non-directory", namespace)
		}
		previous, err := marketplace.ReadPluginSource(target)
		if err != nil {
			return err
		}
		if !descriptionChanged {
			description = previous.Description
		}
	} else if statErr == nil {
		return fmt.Errorf("marketplace plugin %q is already imported; use plugin update", namespace)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("checking marketplace plugin %q: %w", namespace, statErr)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating marketplace plugin directory: %w", err)
	}
	stage, err := os.MkdirTemp(root, "."+namespace+"-import-*")
	if err != nil {
		return fmt.Errorf("creating plugin import staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := writeMarketplacePluginBundle(stage, bundle, marketplace.PluginSource{Version: 1, Source: marketplacePluginProvenance(bundle.CanonicalSource), Description: description}); err != nil {
		return err
	}
	backup, err := replaceImportedPlugin(stage, target, update)
	if err != nil {
		return fmt.Errorf("committing marketplace plugin %q: %w", namespace, err)
	}
	if backup != "" {
		defer os.RemoveAll(backup)
	}
	action := "added"
	if update {
		action = "updated"
	}
	_, err = fmt.Fprintf(stdout, "%s plugin %s %s with %d platform artifacts\n", action, namespace, bundle.Manifest.PluginVersion, len(bundle.Manifest.Artifacts))
	return err
}

func marketplacePluginProvenance(source string) string {
	if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "github:") {
		return source
	}
	return "local"
}

func writeMarketplacePluginBundle(directory string, bundle pluginpkg.Bundle, source marketplace.PluginSource) error {
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), bundle.ManifestData, 0o644); err != nil {
		return fmt.Errorf("writing imported plugin manifest: %w", err)
	}
	for _, artifact := range bundle.Manifest.Artifacts {
		target := filepath.Join(directory, filepath.FromSlash(artifact.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("creating imported plugin artifact directory: %w", err)
		}
		if err := os.WriteFile(target, bundle.ArtifactData[artifact.Path], 0o644); err != nil {
			return fmt.Errorf("writing imported plugin artifact %s/%s: %w", artifact.OS, artifact.Arch, err)
		}
	}
	if err := marketplace.WriteJSON(filepath.Join(directory, marketplace.PluginSourceName), source); err != nil {
		return fmt.Errorf("writing marketplace plugin source metadata: %w", err)
	}
	return nil
}

func replaceImportedPlugin(stage, target string, update bool) (string, error) {
	if !update {
		return "", os.Rename(stage, target)
	}
	backup, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+"-update-backup-*")
	if err != nil {
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	if err := os.Rename(target, backup); err != nil {
		return "", err
	}
	if err := os.Rename(stage, target); err != nil {
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return "", errors.Join(err, fmt.Errorf("restoring previous plugin import: %w", restoreErr))
		}
		return "", err
	}
	return backup, nil
}
