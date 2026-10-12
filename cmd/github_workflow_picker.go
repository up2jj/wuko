package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

func runGitHubWorkflowPicker(command *cobra.Command, deps dependencies, args []string, config runWorkflowConfig) error {
	if config.workflowFile != "" {
		return fmt.Errorf("--pick and --file cannot be used together")
	}
	if len(args) != 1 {
		return fmt.Errorf("--pick requires exactly one GitHub repository or tree locator")
	}
	_, recognized, err := workflow.ParseGitHubWorkflowLocator(args[0])
	if err != nil {
		return err
	}
	if !recognized {
		return fmt.Errorf("--pick requires a GitHub repository or tree locator")
	}
	cwd, _, configDir, err := directories(deps)
	if err != nil {
		return err
	}
	invocationEnv, err := invocationEnvironment(command, deps, cwd)
	if err != nil {
		return err
	}
	baseEnv, environmentLoaders := environmentValues(invocationEnv)
	providers, err := invocationProviders(command, deps, baseEnv)
	if err != nil {
		return err
	}
	loader := deps.loader
	if loader == nil {
		loader = defaultWorkflowLoader(deps.plugins)
	}
	options := workflow.LoadOptions{
		BaseEnv: baseEnv, EnvironmentLoaders: environmentLoaders, RunDir: cwd, Providers: providers,
		Diagnostics: diagnosticsFor(command, deps, cwd), Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
		Interactive: interactive(command.InOrStdin()),
	}
	sources, cleanup, err := loader.DiscoverGitHubWorkflows(command.Context(), args[0], options)
	if err != nil {
		return err
	}
	defer cleanup()
	discovered := len(sources)
	sources = slices.DeleteFunc(sources, func(source workflow.Source) bool { return !source.Invokable })
	if !deps.isInteractive(command.InOrStdin()) {
		for _, source := range sources {
			target := ""
			if source.Target != "" {
				target = " " + shellQuote(source.Target)
			}
			fmt.Fprintf(command.OutOrStdout(), "wuko run %s%s\n", shellQuote(source.Locator), target)
		}
		return nil
	}
	if len(sources) == 0 {
		if discovered > 0 {
			fmt.Fprint(command.OutOrStdout(), noInvokableWorkflowsHelp)
			return nil
		}
		fmt.Fprintln(command.OutOrStdout(), "No workflows found in the selected GitHub repository path.")
		return nil
	}
	state, stateErr := loadWorkflowPickerState(command.Context(), configDir)
	if stateErr != nil {
		fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot load workflow picker state: %v\n", stateErr)
	}
	sortMode := state.sortMode()
	selected, filter, notice := "", "", ""
	resumeAfterReturn := func(observer *returnDestinationObserver, runErr error) bool {
		if !observer.requested(workflow.ReturnDestinationPicker) || errors.Is(runErr, context.Canceled) || command.Context().Err() != nil {
			return false
		}
		if runErr != nil {
			notice = "workflow failed: " + runErr.Error()
		}
		if deps.plugins != nil {
			resetCtx, cancel := context.WithTimeout(context.WithoutCancel(command.Context()), 10*time.Second)
			resetErr := deps.plugins.Reset(resetCtx, pluginStopReason(runErr))
			cancel()
			if resetErr != nil {
				if notice != "" {
					notice += "; "
				}
				notice += "plugin teardown failed: " + resetErr.Error()
			}
		}
		return true
	}
	for {
		ordered := sortWorkflowSources(sources, state, sortMode)
		selection, err := deps.selectWorkflow(command.Context(), command.InOrStdin(), command.OutOrStdout(), tui.SelectionPickerConfig{
			Title: "GitHub workflows (sort: " + sortMode.String() + ")", Options: workflowPickerOptions(ordered, state, selected, sortMode), InitialFilter: filter, Notice: notice,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		filter, notice = selection.Filter, ""
		source, ok := selection.Option.Value.(workflow.Source)
		if !ok {
			return fmt.Errorf("workflow selection did not contain a workflow")
		}
		selected = workflowPickerSelectionKey(source)
		switch selection.Intent {
		case tui.SelectionPrimary:
			observer := &returnDestinationObserver{}
			active := config
			active.pick, active.workflowFile, active.remoteLocator, active.targetName, active.returnObserver = false, source.Path, source.Locator, source.Target, observer
			err := runWorkflow(command, deps, nil, active)
			if err == nil {
				state.markRecent(workflowPickerSourcePath(source))
				if saveErr := saveWorkflowPickerState(command.Context(), configDir, state); saveErr != nil {
					fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", saveErr)
				}
			}
			if resumeAfterReturn(observer, err) {
				continue
			}
			return err
		case tui.SelectionUI:
			observer := &returnDestinationObserver{}
			err := runWorkflowUI(command, deps, nil, uiWorkflowConfig{
				variables: config.variables, variableFiles: config.variableFiles, environment: config.environment, reporters: config.reporters,
				workflowFile: source.Path, remoteLocator: source.Locator, targetName: source.Target, returnObserver: observer,
			})
			if resumeAfterReturn(observer, err) {
				continue
			}
			return err
		case tui.SelectionValidate, tui.SelectionTree, tui.SelectionDryRun:
			title, content, failed := runWorkflowPickerInspection(command, deps, source, selection.Intent)
			if err := deps.viewText(command.Context(), command.InOrStdin(), command.OutOrStdout(), tui.TextViewerConfig{Title: title, Content: content, Failed: failed}); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		case tui.SelectionTogglePin:
			state.togglePinned(workflowPickerSourcePath(source))
			if err := saveWorkflowPickerState(command.Context(), configDir, state); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", err)
			}
		case tui.SelectionToggleSort:
			if sortMode == workflowPickerSortName {
				sortMode = workflowPickerSortRecent
			} else {
				sortMode = workflowPickerSortName
			}
			state.Sort = sortMode.String()
			if err := saveWorkflowPickerState(command.Context(), configDir, state); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", err)
			}
		default:
			target := ""
			if source.Target != "" {
				target = " " + shellQuote(source.Target)
			}
			fmt.Fprintf(command.OutOrStdout(), "wuko run %s%s\n", shellQuote(source.Locator), target)
			return nil
		}
	}
}
