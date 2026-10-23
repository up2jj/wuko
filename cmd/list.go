package cmd

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

const workflowListSchemaVersion = 1

type workflowListDocument struct {
	SchemaVersion int                  `json:"schema_version"`
	Workflows     []workflowListSource `json:"workflows"`
}

type workflowListSource struct {
	Name           string            `json:"name"`
	Target         string            `json:"target"`
	Description    string            `json:"description"`
	Scope          string            `json:"scope"`
	Path           string            `json:"path"`
	Invokable      bool              `json:"invokable"`
	HasForm        bool              `json:"has_form"`
	DependsOn      map[string]string `json:"depends_on"`
	PackageVersion string            `json:"package_version"`
	Effective      *bool             `json:"effective,omitempty"`
}

func newListCmd(deps dependencies) *cobra.Command {
	var format string
	var all bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List effective workflows",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("format must be text or json")
			}
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			reporter := diagnosticsFor(command, deps, cwd)
			started := time.Now()
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: started, Message: "listing workflows"})
			sources, warnings, err := workflow.DiscoverForBrowsing(cwd, home, config)
			if err != nil {
				diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(started), Error: err})
				return err
			}
			if err := writeDiscoveryWarnings(command.ErrOrStderr(), warnings); err != nil {
				return err
			}
			if !all {
				sources = slices.DeleteFunc(sources, func(source workflow.Source) bool { return !source.Effective })
			}
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(started), Attributes: []diagnostic.Attribute{diagnostic.Attr("workflows", fmt.Sprint(len(sources)))}})
			if format == "json" {
				if err := json.NewEncoder(command.OutOrStdout()).Encode(newWorkflowListDocument(sources, all)); err != nil {
					return fmt.Errorf("encoding workflow list: %w", err)
				}
				return nil
			}
			for _, source := range sources {
				if all {
					status := "shadowed"
					if source.Effective {
						status = "effective"
					}
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s\t", status); err != nil {
						return err
					}
				}
				if err := writeWorkflowSource(command.OutOrStdout(), source); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "workflow list output format: text or json")
	command.Flags().BoolVar(&all, "all", false, "include shadowed workflows and mark effective sources")
	_ = command.RegisterFlagCompletionFunc("format", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"text", "json"}, cobra.ShellCompDirectiveNoFileComp
	})
	return command
}

func newWorkflowListDocument(sources []workflow.Source, all bool) workflowListDocument {
	workflows := make([]workflowListSource, 0, len(sources))
	for _, source := range sources {
		dependencies := maps.Clone(source.DependsOn)
		if dependencies == nil {
			dependencies = make(map[string]string)
		}
		entry := workflowListSource{
			Name: source.Name, Target: source.Target, Description: source.Description,
			Scope: source.Scope, Path: source.Path, Invokable: source.Invokable,
			HasForm: source.HasForm, DependsOn: dependencies, PackageVersion: source.PackageVersion,
		}
		if all {
			entry.Effective = &source.Effective
		}
		workflows = append(workflows, entry)
	}
	return workflowListDocument{SchemaVersion: workflowListSchemaVersion, Workflows: workflows}
}
