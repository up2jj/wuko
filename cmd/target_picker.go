package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

// Group only the interactive presentation. Discovery and scripted listings keep
// their individual target sources, including all target-specific metadata.
type workflowPickerGroup struct {
	source  workflow.Source
	path    string
	targets []workflow.Source
}

// groupWorkflowPickerSources also returns each group's index by normalized path,
// so callers avoid recomputing paths, which may call os.Getwd.
func groupWorkflowPickerSources(sources []workflow.Source) ([]workflowPickerGroup, map[string]int) {
	var groups []workflowPickerGroup
	indexes := make(map[string]int)
	for _, source := range sources {
		path := workflowPickerSourcePath(source)
		index, exists := indexes[path]
		if !exists {
			index = len(groups)
			indexes[path] = index
			groups = append(groups, workflowPickerGroup{source: source, path: path})
		}
		if source.Target != "" {
			groups[index].targets = append(groups[index].targets, source)
		}
	}
	for index := range groups {
		group := &groups[index]
		if len(group.targets) == 0 {
			continue
		}
		slices.SortFunc(group.targets, func(left, right workflow.Source) int {
			return strings.Compare(left.Target, right.Target)
		})
		names := make([]string, len(group.targets))
		group.source.Target = ""
		group.source.DependsOn = nil
		group.source.HasForm = false
		for index, source := range group.targets {
			names[index] = source.Target
			group.source.HasForm = group.source.HasForm || source.HasForm
		}
		noun := "targets"
		if len(names) == 1 {
			noun = "target"
		}
		summary := fmt.Sprintf("%d %s: %s", len(names), noun, strings.Join(names, ", "))
		group.source.Description = summary
		if description := group.source.WorkflowDescription; description != "" {
			group.source.Description = description + " • " + summary
		}
	}
	return groups, indexes
}

type targetPickerPosition struct {
	target string
	filter string
}

// Navigation belongs to one interactive invocation; persistent history remains
// keyed by workflow source rather than by target.
type workflowPickerNavigation struct {
	selected string
	filter   string
	active   string
	pending  tui.SelectionIntent
	targets  map[string]targetPickerPosition
}

func (navigation *workflowPickerNavigation) reconcile(groups []workflowPickerGroup, indexes map[string]int) {
	for path, position := range navigation.targets {
		// A filter written for a removed target would likely hide the rest.
		index, exists := indexes[path]
		if !exists || !slices.ContainsFunc(groups[index].targets, func(source workflow.Source) bool { return source.Target == position.target }) {
			delete(navigation.targets, path)
		}
	}
	if _, exists := indexes[navigation.selected]; !exists {
		navigation.selected = ""
	}
	if index, exists := indexes[navigation.active]; !exists || len(groups[index].targets) == 0 {
		navigation.active = ""
	}
}

func workflowPickerNeedsTarget(intent tui.SelectionIntent) bool {
	switch intent {
	case tui.SelectionPrimary, tui.SelectionTargets, tui.SelectionUI, tui.SelectionValidate, tui.SelectionTree, tui.SelectionDryRun, tui.SelectionAlternate:
		return true
	default:
		return false
	}
}

func (navigation *workflowPickerNavigation) selectWorkflow(command *cobra.Command, deps dependencies, sources []workflow.Source, state workflowPickerState, mode workflowPickerSort, title, notice string) (tui.Selection, error) {
	groups, indexes := groupWorkflowPickerSources(sortWorkflowSources(sources, state, mode))
	navigation.reconcile(groups, indexes)
	for {
		config := tui.SelectionPickerConfig{Title: title, InitialFilter: navigation.filter, Notice: notice}
		if index, ok := indexes[navigation.active]; ok {
			group := groups[index]
			position := navigation.targets[group.path]
			config.Title = "Targets for " + group.source.Name
			config.InitialFilter = position.filter
			config.AllowBack = true
			config.EnterIntent = navigation.pending
			config.Options = workflowPickerOptions(group.targets, state, group.path+"\x00"+position.target, mode)
			for index := range config.Options {
				config.Options[index].Label = group.targets[index].Target
			}
		} else {
			config.Options = make([]tui.Option, 0, len(groups))
			for _, group := range groups {
				pinned := state.isPinned(group.path)
				option := workflowPickerOptionWithState(group.source, pinned, group.path == navigation.selected)
				option.Actions = workflowPickerActions(group.source, pinned, mode)
				if len(group.targets) > 0 {
					option.Actions = append([]tui.SelectionAction{{Label: "Choose target", Description: "browse this workflow's targets", Intent: tui.SelectionTargets}}, option.Actions...)
				}
				config.Options = append(config.Options, option)
			}
		}
		selection, err := deps.selectWorkflow(command.Context(), command.InOrStdin(), command.OutOrStdout(), config)
		if err != nil {
			return tui.Selection{}, err
		}
		notice = ""
		if selection.Intent == tui.SelectionBack && navigation.active != "" {
			if source, ok := selection.Option.Value.(workflow.Source); ok {
				navigation.targets[navigation.active] = targetPickerPosition{target: source.Target, filter: selection.Filter}
			}
			navigation.active = ""
			continue
		}
		source, ok := selection.Option.Value.(workflow.Source)
		if !ok {
			return tui.Selection{}, fmt.Errorf("workflow selection did not contain a workflow")
		}
		path := workflowPickerSourcePath(source)
		if navigation.active != "" {
			navigation.targets[path] = targetPickerPosition{target: source.Target, filter: selection.Filter}
			if workflowPickerNeedsTarget(selection.Intent) {
				navigation.pending = tui.SelectionPrimary
			}
			return selection, nil
		}
		navigation.selected, navigation.filter = path, selection.Filter
		if index, ok := indexes[path]; ok && len(groups[index].targets) > 0 && workflowPickerNeedsTarget(selection.Intent) {
			navigation.active, navigation.pending = path, selection.Intent
			if navigation.pending == tui.SelectionTargets {
				navigation.pending = tui.SelectionPrimary
			}
			if navigation.targets == nil {
				navigation.targets = make(map[string]targetPickerPosition)
			}
		}
		if navigation.active == "" {
			return selection, nil
		}
	}
}
