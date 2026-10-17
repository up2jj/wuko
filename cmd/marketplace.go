package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/marketplace"
	"github.com/up2jj/wuko/workflow"
)

func newMarketplaceCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "marketplace",
		Short: "Create and build workflow and plugin marketplaces",
	}
	command.AddCommand(newMarketplaceInitCmd(deps), newMarketplaceWorkflowCmd(deps), newMarketplacePluginCmd(deps), newMarketplaceBuildCmd(deps))
	return command
}

func newMarketplaceInitCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize a marketplace manifest in the current directory",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			manifestPath := filepath.Join(cwd, "manifest.json")
			if _, err := os.Stat(manifestPath); err == nil {
				return fmt.Errorf("marketplace manifest already exists at %s", manifestPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("checking marketplace manifest %s: %w", manifestPath, err)
			}
			for _, directory := range []string{filepath.Join(cwd, ".wuko", "workflows"), marketplace.PluginSourceRoot(cwd)} {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					return fmt.Errorf("creating marketplace source directory %s: %w", directory, err)
				}
			}
			manifest := workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Packages: []workflow.MarketplacePackage{}, Plugins: []workflow.MarketplacePluginPackage{}}
			if err := marketplace.WriteJSON(manifestPath, manifest); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "created marketplace manifest in %s\n", manifestPath)
			return err
		},
	}
}

func newMarketplaceBuildCmd(deps dependencies) *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:     "build",
		Short:   "Publish workflows and plugins and rebuild the marketplace manifest",
		Example: "  wuko marketplace build\n  wuko marketplace build --check",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			build, err := marketplace.BuildManifest(marketplace.DecodeContext(command.Context()), cwd, deps.loader, diagnosticsFor(command, deps, cwd))
			if err != nil {
				return err
			}
			defer build.Cleanup()
			manifestPath := filepath.Join(cwd, "manifest.json")
			if check {
				if build.Changed {
					return fmt.Errorf("marketplace is not up to date; run wuko marketplace build")
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "marketplace is up to date with %s and %s\n", countMarketplaceItems(len(build.Manifest.Packages), "workflow package"), countMarketplaceItems(len(build.Manifest.Plugins), "plugin"))
				return err
			}
			if build.Changed {
				if err := marketplace.Publish(cwd, manifestPath, build); err != nil {
					return err
				}
			}
			message := "built marketplace manifest"
			if !build.Changed {
				message = "marketplace is already up to date"
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s with %s and %s in %s\n", message, countMarketplaceItems(len(build.Manifest.Packages), "workflow package"), countMarketplaceItems(len(build.Manifest.Plugins), "plugin"), manifestPath)
			return err
		},
	}
	command.Flags().BoolVar(&check, "check", false, "fail when generated marketplace files are not up to date")
	return command
}

// countMarketplaceItems renders a count with its noun, so a marketplace holding
// exactly one plugin does not report "1 plugins".
func countMarketplaceItems(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
