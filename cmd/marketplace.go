package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

func newMarketplaceCmd(deps dependencies) *cobra.Command {
	marketplace := &cobra.Command{
		Use:   "marketplace",
		Short: "Create and build workflow marketplaces",
	}
	marketplace.AddCommand(newMarketplaceInitCmd(deps), newMarketplaceBuildCmd(deps))
	return marketplace
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
			manifest := workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Workflows: []workflow.MarketplaceWorkflow{}}
			if err := writeJSONAtomically(manifestPath, manifest); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "created marketplace manifest in %s\n", manifestPath)
			return err
		},
	}
}

func newMarketplaceBuildCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Discover workflows and rebuild the marketplace manifest",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			manifest, err := buildMarketplaceManifest(cwd, deps.loader, diagnosticsFor(command, deps, cwd))
			if err != nil {
				return err
			}
			manifestPath := filepath.Join(cwd, "manifest.json")
			if err := writeJSONAtomically(manifestPath, manifest); err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "built marketplace manifest with %d workflows in %s\n", len(manifest.Workflows), manifestPath)
			return err
		},
	}
}

func buildMarketplaceManifest(cwd string, loader *workflow.Loader, reporter diagnostic.Reporter) (workflow.MarketplaceManifest, error) {
	if loader == nil {
		loader = workflow.NewLoader(nil)
	}
	workflowRoot := filepath.Join(cwd, ".wuko", "workflows")
	manifest := workflow.MarketplaceManifest{Version: workflow.MarketplaceManifestVersion, Workflows: []workflow.MarketplaceWorkflow{}}
	if _, err := os.Stat(workflowRoot); err != nil {
		if os.IsNotExist(err) {
			return manifest, nil
		}
		return workflow.MarketplaceManifest{}, fmt.Errorf("checking workflow directory %s: %w", workflowRoot, err)
	}
	seenNames := make(map[string]string)
	err := filepath.WalkDir(workflowRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		definition, err := loader.Decode(path, workflow.LoadOptions{RunDir: cwd, Diagnostics: reporter})
		if err != nil {
			return err
		}
		if !workflow.ValidWorkflowName(definition.Name) {
			return fmt.Errorf("workflow %s has name %q, which cannot be used by a marketplace install", path, definition.Name)
		}
		if err := validateStandaloneInstallSource(path, definition); err != nil {
			return err
		}
		if previous, exists := seenNames[definition.Name]; exists {
			return fmt.Errorf("workflow name %q is declared by both %s and %s", definition.Name, previous, path)
		}
		seenNames[definition.Name] = path
		relative, err := filepath.Rel(workflowRoot, path)
		if err != nil {
			return fmt.Errorf("relating workflow path %s: %w", path, err)
		}
		manifest.Workflows = append(manifest.Workflows, workflow.MarketplaceWorkflow{
			Name: definition.Name, Path: filepath.ToSlash(filepath.Join(".wuko", "workflows", relative)), Description: definition.Description,
		})
		return nil
	})
	if err != nil {
		return workflow.MarketplaceManifest{}, fmt.Errorf("building marketplace manifest: %w", err)
	}
	slices.SortStableFunc(manifest.Workflows, func(a, b workflow.MarketplaceWorkflow) int {
		if comparison := strings.Compare(a.Path, b.Path); comparison != 0 {
			return comparison
		}
		return strings.Compare(a.Name, b.Name)
	})
	return manifest, nil
}

func writeJSONAtomically(path string, value any) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	temporary := file.Name()
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	removeTemporary = false
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening marketplace directory: %w", err)
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return fmt.Errorf("syncing marketplace directory: %w", err)
	}
	return nil
}
