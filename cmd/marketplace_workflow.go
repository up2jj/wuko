package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

var errMarketplaceWorkflowNotFound = errors.New("marketplace workflow not found")

func newMarketplaceWorkflowCmd(deps dependencies) *cobra.Command {
	command := &cobra.Command{Use: "workflow", Short: "Manage marketplace workflow sources"}
	command.AddCommand(newMarketplaceWorkflowAddCmd(deps), newMarketplaceWorkflowRemoveCmd(deps))
	return command
}

func newMarketplaceWorkflowAddCmd(deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:     "add SOURCE",
		Short:   "Add a workflow package source to the marketplace",
		Example: "  wuko marketplace workflow add ../workflows/release",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			loader := deps.loader
			if loader == nil {
				loader = workflow.NewLoader(nil)
			}
			name, target, err := addMarketplaceWorkflow(cwd, args[0], loader, diagnosticsFor(command, deps, cwd))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "added workflow %s in %s\nRun `wuko marketplace build` to publish it.\n", name, target)
			return err
		},
	}
}

func newMarketplaceWorkflowRemoveCmd(deps dependencies) *cobra.Command {
	var yes bool
	command := &cobra.Command{
		Use:     "remove NAME",
		Short:   "Remove a workflow package source from the marketplace",
		Example: "  wuko marketplace workflow remove release --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name := args[0]
			if !workflow.ValidWorkflowName(name) {
				return fmt.Errorf("invalid workflow name %q", name)
			}
			cwd, err := deps.cwd()
			if err != nil {
				return fmt.Errorf("finding current directory: %w", err)
			}
			loader := deps.loader
			if loader == nil {
				loader = workflow.NewLoader(nil)
			}
			directory, err := findMarketplaceWorkflow(cwd, name, loader, diagnosticsFor(command, deps, cwd))
			if err != nil {
				return err
			}
			if !yes {
				if deps.isInteractive == nil || !deps.isInteractive(command.InOrStdin()) {
					return fmt.Errorf("workflow removal requires confirmation; rerun with --yes in non-interactive mode")
				}
				if deps.confirm == nil {
					return fmt.Errorf("workflow removal confirmation is unavailable")
				}
				confirmed, err := deps.confirm(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Remove marketplace workflow "+name+"?", false)
				if err != nil {
					return fmt.Errorf("confirming workflow removal: %w", err)
				}
				if !confirmed {
					return nil
				}
			}
			if err := os.RemoveAll(directory); err != nil {
				return fmt.Errorf("removing marketplace workflow %q: %w", name, err)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "removed workflow %s\nRun `wuko marketplace build` to remove its published package.\n", name)
			return err
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "confirm removal non-interactively")
	return command
}

func addMarketplaceWorkflow(cwd, source string, loader *workflow.Loader, reporter diagnostic.Reporter) (string, string, error) {
	directory, err := marketplaceWorkflowSourceDirectory(source)
	if err != nil {
		return "", "", err
	}
	manifest, err := workflow.WorkflowPackageManifestPath(directory)
	if err != nil {
		return "", "", err
	}
	definition, err := loader.Decode(manifest, workflow.LoadOptions{RunDir: cwd, Diagnostics: reporter})
	if err != nil {
		return "", "", fmt.Errorf("validating marketplace workflow source: %w", err)
	}
	if !workflow.ValidWorkflowName(definition.Name) {
		return "", "", fmt.Errorf("workflow name %q cannot be used by a marketplace install", definition.Name)
	}
	if previous, findErr := findMarketplaceWorkflow(cwd, definition.Name, loader, reporter); findErr == nil {
		return "", "", fmt.Errorf("marketplace workflow %q already exists in %s", definition.Name, previous)
	} else if !errors.Is(findErr, errMarketplaceWorkflowNotFound) {
		return "", "", findErr
	}
	root := filepath.Join(cwd, ".wuko", "workflows")
	target := filepath.Join(root, definition.Name)
	if _, err := os.Lstat(target); err == nil {
		return "", "", fmt.Errorf("marketplace workflow destination already exists at %s", target)
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("checking marketplace workflow destination %s: %w", target, err)
	}
	stage, err := marketplaceWorkflowStage(cwd, root, definition.Name)
	if err != nil {
		return "", "", err
	}
	removeStage := true
	defer func() {
		if removeStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := workflow.CopyWorkflowPackage(directory, stage); err != nil {
		return "", "", fmt.Errorf("copying marketplace workflow %q: %w", definition.Name, err)
	}
	if err := os.Rename(stage, target); err != nil {
		return "", "", fmt.Errorf("committing marketplace workflow %q: %w", definition.Name, err)
	}
	removeStage = false
	return definition.Name, target, nil
}

func findMarketplaceWorkflow(cwd, name string, loader *workflow.Loader, reporter diagnostic.Reporter) (string, error) {
	root := filepath.Join(cwd, ".wuko", "workflows")
	target := filepath.Join(root, name)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("marketplace workflow %q source is not a directory", name)
		}
		// The conventional directory is only a shortcut: it must be a package root whose manifest
		// declares this name, or is too broken to declare any, so a grouping directory or a package
		// that merely shares the directory name is never mistaken for the requested workflow.
		manifest, err := workflow.WorkflowPackageManifestPath(target)
		if err == nil {
			definition, decodeErr := loader.Decode(manifest, workflow.LoadOptions{RunDir: cwd, Diagnostics: reporter})
			if decodeErr != nil || definition.Name == name {
				return target, nil
			}
		} else if !errors.Is(err, workflow.ErrWorkflowPackageManifestNotFound) {
			return "", fmt.Errorf("checking marketplace workflow %q: %w", name, err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("checking marketplace workflow %q: %w", name, err)
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %q", errMarketplaceWorkflowNotFound, name)
		}
		return "", fmt.Errorf("checking marketplace workflow directory: %w", err)
	}
	packages, err := discoverMarketplacePackages(root)
	if err != nil {
		return "", err
	}
	var found string
	for _, item := range packages {
		definition, err := loader.Decode(item.manifestPath, workflow.LoadOptions{RunDir: cwd, Diagnostics: reporter})
		if err != nil {
			return "", fmt.Errorf("validating marketplace workflow source %s: %w", item.directory, err)
		}
		if definition.Name != name {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("workflow name %q is declared by both %s and %s", name, found, item.directory)
		}
		found = item.directory
	}
	if found == "" {
		return "", fmt.Errorf("%w: %q", errMarketplaceWorkflowNotFound, name)
	}
	return found, nil
}

func marketplaceWorkflowSourceDirectory(source string) (string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("checking workflow source %s: %w", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("workflow source %s must be a directory, not a symlink", source)
	}
	directory, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolving workflow source %s: %w", source, err)
	}
	return filepath.Clean(directory), nil
}

func marketplaceWorkflowStage(cwd, root, name string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("creating marketplace workflow source directory: %w", err)
	}
	// Stage beside the discovery root, not inside it, so a concurrent build never sees a partial
	// copy and an interrupted add cannot leave a package-less directory that breaks discovery.
	stage, err := os.MkdirTemp(filepath.Join(cwd, ".wuko"), ".workflow-add-"+name+"-*")
	if err != nil {
		return "", fmt.Errorf("creating workflow staging directory: %w", err)
	}
	return stage, nil
}
