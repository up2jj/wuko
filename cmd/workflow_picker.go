package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/up2jj/wuko/keyvalue"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

const (
	workflowPickerStoreName = "picker"
	workflowPickerStateKey  = "state"
	workflowPickerRecentMax = 50
)

type workflowPickerState struct {
	Pinned []string `json:"pinned"`
	Recent []string `json:"recent"`
	Sort   string   `json:"sort,omitempty"`
}

type workflowPickerSort uint8

const (
	workflowPickerSortName workflowPickerSort = iota
	workflowPickerSortRecent
)

func (sort workflowPickerSort) String() string {
	if sort == workflowPickerSortRecent {
		return "recent"
	}
	return "name"
}

func loadWorkflowPickerState(ctx context.Context, configDir string) (workflowPickerState, error) {
	if configDir == "" {
		return workflowPickerState{}, nil
	}
	store, err := keyvalue.Open(filepath.Join(configDir, "wuko", "values"), workflowPickerStoreName)
	if err != nil {
		return workflowPickerState{}, err
	}
	value, found, err := store.Get(ctx, workflowPickerStateKey)
	if err != nil || !found {
		return workflowPickerState{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return workflowPickerState{}, fmt.Errorf("encoding picker state: %w", err)
	}
	var state workflowPickerState
	if err := json.Unmarshal(data, &state); err != nil {
		return workflowPickerState{}, fmt.Errorf("decoding picker state: %w", err)
	}
	state.Pinned = normalizeWorkflowPaths(state.Pinned)
	state.Recent = normalizeWorkflowPaths(state.Recent)
	if len(state.Recent) > workflowPickerRecentMax {
		state.Recent = state.Recent[:workflowPickerRecentMax]
	}
	if state.Sort != "" {
		state.Sort = state.sortMode().String()
	}
	return state, nil
}

func saveWorkflowPickerState(ctx context.Context, configDir string, state workflowPickerState) error {
	if configDir == "" {
		return nil
	}
	store, err := keyvalue.Open(filepath.Join(configDir, "wuko", "values"), workflowPickerStoreName)
	if err != nil {
		return err
	}
	state.Pinned = normalizeWorkflowPaths(state.Pinned)
	state.Recent = normalizeWorkflowPaths(state.Recent)
	if len(state.Recent) > workflowPickerRecentMax {
		state.Recent = state.Recent[:workflowPickerRecentMax]
	}
	state.Sort = state.sortMode().String()
	_, err = store.SetIfDifferent(ctx, workflowPickerStateKey, state)
	return err
}

func (state *workflowPickerState) reconcile(sources []workflow.Source) bool {
	available := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		available[workflowPickerSourcePath(source)] = struct{}{}
	}
	pinned, pinnedChanged := filterWorkflowPaths(state.Pinned, available)
	recent, recentChanged := filterWorkflowPaths(state.Recent, available)
	state.Pinned = pinned
	state.Recent = recent
	sortChanged := state.Sort != "" && state.Sort != state.sortMode().String()
	if sortChanged {
		state.Sort = state.sortMode().String()
	}
	return pinnedChanged || recentChanged || sortChanged
}

func (state workflowPickerState) sortMode() workflowPickerSort {
	if state.Sort == workflowPickerSortRecent.String() {
		return workflowPickerSortRecent
	}
	return workflowPickerSortName
}

func (state *workflowPickerState) togglePinned(path string) {
	path = workflowPickerPath(path)
	for index, pinned := range state.Pinned {
		if pinned == path {
			state.Pinned = append(state.Pinned[:index], state.Pinned[index+1:]...)
			return
		}
	}
	state.Pinned = append(state.Pinned, path)
}

func (state workflowPickerState) isPinned(path string) bool {
	return slices.Contains(state.Pinned, workflowPickerPath(path))
}

func workflowPickerSourcePath(source workflow.Source) string {
	if source.Locator != "" {
		return source.Locator
	}
	return workflowPickerPath(source.Path)
}

func (state *workflowPickerState) markRecent(path string) {
	path = workflowPickerPath(path)
	recent := make([]string, 0, min(len(state.Recent)+1, workflowPickerRecentMax))
	recent = append(recent, path)
	for _, previous := range state.Recent {
		if previous == path || len(recent) == workflowPickerRecentMax {
			continue
		}
		recent = append(recent, previous)
	}
	state.Recent = recent
}

func sortWorkflowSources(sources []workflow.Source, state workflowPickerState, mode workflowPickerSort) []workflow.Source {
	result := slices.Clone(sources)
	recentIndex := make(map[string]int, len(state.Recent))
	for index, path := range state.Recent {
		recentIndex[path] = index
	}
	slices.SortStableFunc(result, func(left, right workflow.Source) int {
		leftPinned, rightPinned := state.isPinned(workflowPickerSourcePath(left)), state.isPinned(workflowPickerSourcePath(right))
		if leftPinned != rightPinned {
			if leftPinned {
				return -1
			}
			return 1
		}
		if mode == workflowPickerSortRecent {
			leftIndex, leftFound := recentIndex[workflowPickerSourcePath(left)]
			rightIndex, rightFound := recentIndex[workflowPickerSourcePath(right)]
			if leftFound != rightFound {
				if leftFound {
					return -1
				}
				return 1
			}
			if leftFound && leftIndex != rightIndex {
				if leftIndex < rightIndex {
					return -1
				}
				return 1
			}
		}
		if comparison := strings.Compare(left.Name, right.Name); comparison != 0 {
			return comparison
		}
		return strings.Compare(workflowPickerSourcePath(left), workflowPickerSourcePath(right))
	})
	return result
}

func workflowPickerOptions(sources []workflow.Source, state workflowPickerState, selectedPath string, mode workflowPickerSort) []tui.Option {
	options := make([]tui.Option, len(sources))
	for index, source := range sources {
		path := workflowPickerSourcePath(source)
		pinned := state.isPinned(path)
		options[index] = workflowPickerOptionWithState(source, pinned, workflowPickerSelectionKey(source) == selectedPath)
		options[index].Actions = workflowPickerActions(source, pinned, mode)
	}
	return options
}

func workflowPickerActions(source workflow.Source, pinned bool, mode workflowPickerSort) []tui.SelectionAction {
	pinLabel := "Pin workflow"
	pinDescription := "keep this workflow above unpinned workflows"
	if pinned {
		pinLabel = "Unpin workflow"
		pinDescription = "remove this workflow from pinned workflows"
	}
	sortLabel := "Sort by recent"
	if mode == workflowPickerSortRecent {
		sortLabel = "Sort by name"
	}
	actions := []tui.SelectionAction{
		{Label: "Run", Description: "execute this workflow", Intent: tui.SelectionPrimary},
		{Label: "Open form", Description: "configure and run in the browser", Intent: tui.SelectionUI, Disabled: !source.HasForm, DisabledReason: "workflow does not declare a form"},
		{Label: "Validate", Description: "validate workflow and dependencies", Intent: tui.SelectionValidate},
		{Label: "Show tree", Description: "inspect the execution structure", Intent: tui.SelectionTree},
		{Label: "Dry run", Description: "resolve and print steps without executing them", Intent: tui.SelectionDryRun},
		{Label: "Print command", Description: "print a reproducible wuko run command", Intent: tui.SelectionAlternate},
	}
	if source.Locator == "" {
		actions = append(actions, tui.SelectionAction{Label: "Edit workflow", Description: "open in $VISUAL or $EDITOR", Intent: tui.SelectionEditor})
	}
	actions = append(actions,
		tui.SelectionAction{Label: pinLabel, Description: pinDescription, Intent: tui.SelectionTogglePin},
		tui.SelectionAction{Label: sortLabel, Description: "change the picker ordering", Intent: tui.SelectionToggleSort},
	)
	if source.MarketplaceURL != "" {
		actions = append(actions,
			tui.SelectionAction{Label: "Open marketplace", Description: "open the originating marketplace", Intent: tui.SelectionMarketplace},
			tui.SelectionAction{Label: "Reinstall", Description: "reinstall the marketplace package", Intent: tui.SelectionReinstall},
		)
	}
	return actions
}

func workflowPickerOptionWithState(source workflow.Source, pinned, selected bool) tui.Option {
	description := source.Description
	if description == "" {
		description = "(no description)"
	}
	parts := []string{source.Scope, description}
	if source.PackageVersion != "" {
		parts = append(parts, "package "+source.PackageVersion)
	}
	label := source.Name
	if source.Target != "" {
		label += " " + source.Target
		parts = append(parts, "target: "+source.Target)
	}
	if source.HasForm {
		parts = append(parts, "form")
	}
	if dependencies := workflowDependencySummary(source.DependsOn); dependencies != "" {
		parts = append(parts, dependencies)
	}
	if pinned {
		parts = append(parts, "[pinned]")
	}
	displayPath := source.Path
	if source.Locator != "" {
		displayPath = source.Locator
	}
	return tui.Option{Label: label, Description: strings.Join(parts, " • "), Path: displayPath, URL: source.MarketplaceURL, Value: source, Default: selected}
}

func workflowPickerSelectionKey(source workflow.Source) string {
	return workflowPickerSourcePath(source) + "\x00" + source.Target
}

func workflowDisplayName(source workflow.Source) string {
	if source.Target == "" {
		return source.Name
	}
	return source.Name + " " + source.Target
}

func normalizeWorkflowPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = workflowPickerPath(path)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func filterWorkflowPaths(paths []string, available map[string]struct{}) ([]string, bool) {
	normalized := normalizeWorkflowPaths(paths)
	filtered := make([]string, 0, len(normalized))
	for _, path := range normalized {
		// GitHub locators belong to the `run --pick` picker, which shares this state; local
		// discovery cannot see them, so their absence is not evidence they are stale.
		if _, exists := available[path]; exists || strings.HasPrefix(path, "github:") {
			filtered = append(filtered, path)
		}
	}
	return filtered, !slices.Equal(paths, filtered)
}

func workflowPickerPath(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "github:") {
		return path
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func openWorkflowEditor(getenv func(string) string) func(context.Context, io.Reader, io.Writer, io.Writer, string) error {
	return func(ctx context.Context, input io.Reader, output, errorOutput io.Writer, path string) error {
		spec := strings.TrimSpace(getenv("VISUAL"))
		if spec == "" {
			spec = strings.TrimSpace(getenv("EDITOR"))
		}
		if spec == "" {
			if runtime.GOOS == "windows" {
				spec = "notepad"
			} else {
				spec = "vi"
			}
		}
		parts := strings.Fields(spec)
		if len(parts) == 0 {
			return fmt.Errorf("editor command is empty")
		}
		args := append(slices.Clone(parts[1:]), path)
		command := exec.CommandContext(ctx, parts[0], args...)
		command.Stdin = input
		command.Stdout = output
		command.Stderr = errorOutput
		if err := command.Run(); err != nil {
			return fmt.Errorf("opening %s in editor: %w", path, err)
		}
		return nil
	}
}
