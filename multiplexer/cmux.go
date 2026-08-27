package multiplexer

import (
	"context"
	"strconv"
	"strings"
)

type cmuxAdapter struct{ executor commandExecutor }

func (cmuxAdapter) Provider() Provider { return ProviderCmux }

func (cmuxAdapter) Detect(environment map[string]string) (Target, bool) {
	id := strings.TrimSpace(environment["CMUX_SURFACE_ID"])
	workspace := strings.TrimSpace(environment["CMUX_WORKSPACE_ID"])
	if id == "" && workspace == "" {
		return Target{}, false
	}
	if id == "" {
		id = workspace
	}
	return Target{Provider: ProviderCmux, ID: id, Workspace: workspace}, true
}

func (adapter cmuxAdapter) Execute(ctx context.Context, target Target, request Request, environment map[string]string) (Outcome, error) {
	help, err := adapter.help(ctx, environment)
	if err != nil {
		return Outcome{}, err
	}
	nounFirst := strings.Contains(help, "pane <selector>")
	var args []string
	switch request.Operation {
	case OperationTitle, OperationClearTitle:
		title := request.Title
		if request.Operation == OperationClearTitle {
			title = ""
		}
		// cmux labels a tab through rename-tab and a pane through the
		// noun-first pane command, so tab scope needs rename-tab even on a
		// release that also has pane rename.
		if request.Scope == ScopeTab && !commandAdvertised(help, "rename-tab") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation, Detail: "installed CLI has no rename-tab command"}
		}
		if nounFirst && request.Scope != ScopeTab {
			args = []string{"pane", "current", "rename", "--name", title}
		} else if commandAdvertised(help, "rename-tab") {
			args = []string{"rename-tab"}
			if surface := strings.TrimSpace(environment["CMUX_SURFACE_ID"]); surface != "" {
				args = append(args, "--surface", surface)
			} else if target.Workspace != "" {
				args = append(args, "--workspace", target.Workspace)
			}
			args = append(args, "--title", title)
		} else {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation, Detail: "installed CLI has no pane or tab rename command"}
		}
	case OperationZoom:
		if !nounFirst {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation, Detail: "installed CLI has no pane zoom command"}
		}
		args = []string{"pane", "current", "zoom", "--mode", request.Mode}
	case OperationNotify:
		if nounFirst {
			args = []string{"notification", "create", "--title", request.Title}
		} else if commandAdvertised(help, "notify") {
			args = []string{"notify", "--title", request.Title}
		} else {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		if request.Body != "" {
			args = append(args, "--body", request.Body)
		}
	case OperationStatus:
		if !commandAdvertised(help, "set-status") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation, Detail: "installed CLI does not advertise sidebar status commands"}
		}
		args = []string{"set-status"}
		if request.Icon != "" {
			args = append(args, "--icon", request.Icon)
		}
		if request.Color != "" {
			args = append(args, "--color", request.Color)
		}
		if request.Priority != nil {
			args = append(args, "--priority", strconv.Itoa(*request.Priority))
		}
		args = append(args, "--", request.Key, request.Value)
	case OperationClearStatus:
		if !commandAdvertised(help, "clear-status") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"clear-status", "--", request.Key}
	case OperationProgress:
		if !commandAdvertised(help, "set-progress") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"set-progress", strconv.FormatFloat(request.Progress, 'f', -1, 64)}
		if request.Label != "" {
			args = append(args, "--label", request.Label)
		}
	case OperationClearProgress:
		if !commandAdvertised(help, "clear-progress") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"clear-progress"}
	case OperationLog:
		if !commandAdvertised(help, "log") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"log", "--level", request.Level}
		if request.Source != "" {
			args = append(args, "--source", request.Source)
		}
		args = append(args, "--", request.Message)
	case OperationClearLog:
		if !commandAdvertised(help, "clear-log") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"clear-log"}
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
	}
	if request.Scope == ScopeTab && request.Operation != OperationTitle && request.Operation != OperationClearTitle {
		return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation, Detail: "cmux supports only title and clear_title for tab scope"}
	}
	_, err = runCommand(ctx, adapter.executor, environment, "cmux", args...)
	return Outcome{}, err
}

func (adapter cmuxAdapter) help(ctx context.Context, environment map[string]string) (string, error) {
	result, err := runCommand(ctx, adapter.executor, environment, "cmux", "--help")
	if err != nil {
		return "", err
	}
	return result.Stdout + result.Stderr, nil
}

func commandAdvertised(help, command string) bool {
	for line := range strings.Lines(help) {
		line = strings.TrimSpace(line)
		if line == command || strings.HasPrefix(line, command+" ") {
			return true
		}
	}
	return false
}
