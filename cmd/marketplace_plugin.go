package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/workflow"
)

const marketplacePluginSourceName = ".wuko-marketplace-source.json"

// marketplacePluginSourceRoot resolves the directory holding imported plugin releases. It must not
// be the local plugin installation root, because an imported release contains no executable and
// would make local plugin discovery fail for that namespace.
func marketplacePluginSourceRoot(cwd string) string {
	return filepath.Join(cwd, filepath.FromSlash(workflow.MarketplacePluginSourceDir))
}

type marketplacePluginSource struct {
	Version     int    `json:"version"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

func newMarketplacePluginCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Create and manage marketplace plugins"}
	command.AddCommand(newPluginInitCmd(), newMarketplacePluginAddCmd(deps), newMarketplacePluginUpdateCmd(deps))
	return command
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
			return importMarketplacePlugin(command.Context(), command.OutOrStdout(), deps, args[0], "", description, command.Flags().Changed("description"), false)
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
			return importMarketplacePlugin(command.Context(), command.OutOrStdout(), deps, args[1], args[0], description, command.Flags().Changed("description"), true)
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
	root := marketplacePluginSourceRoot(cwd)
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
		previous, err := readMarketplacePluginSource(target)
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
	if err := writeMarketplacePluginBundle(stage, bundle, marketplacePluginSource{Version: 1, Source: marketplacePluginProvenance(bundle.CanonicalSource), Description: description}); err != nil {
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

func writeMarketplacePluginBundle(directory string, bundle pluginpkg.Bundle, source marketplacePluginSource) error {
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
	if err := writeJSONAtomically(filepath.Join(directory, marketplacePluginSourceName), source); err != nil {
		return fmt.Errorf("writing marketplace plugin source metadata: %w", err)
	}
	return nil
}

func readMarketplacePluginSource(directory string) (marketplacePluginSource, error) {
	filename := filepath.Join(directory, marketplacePluginSourceName)
	data, err := os.ReadFile(filename)
	if err != nil {
		return marketplacePluginSource{}, fmt.Errorf("reading marketplace plugin source metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var source marketplacePluginSource
	if err := decoder.Decode(&source); err != nil {
		return marketplacePluginSource{}, fmt.Errorf("decoding marketplace plugin source metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return marketplacePluginSource{}, fmt.Errorf("decoding marketplace plugin source metadata: multiple JSON values are not supported")
	} else if !errors.Is(err, io.EOF) {
		return marketplacePluginSource{}, fmt.Errorf("decoding marketplace plugin source metadata: %w", err)
	}
	if source.Version != 1 || source.Source == "" || strings.TrimSpace(source.Description) != source.Description {
		return marketplacePluginSource{}, fmt.Errorf("invalid marketplace plugin source metadata")
	}
	return source, nil
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
