package multiplexer

import (
	"context"
	"encoding/json"
	"fmt"
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
	surface := request.Target
	if surface == "" {
		surface = strings.TrimSpace(environment["CMUX_SURFACE_ID"])
	}
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
			pane := "current"
			if request.Target != "" {
				pane = request.Target
			}
			args = []string{"pane", pane, "rename", "--name", title}
		} else if commandAdvertised(help, "rename-tab") {
			args = []string{"rename-tab"}
			if request.Target != "" {
				args = append(args, "--surface", request.Target)
			} else if surface := strings.TrimSpace(environment["CMUX_SURFACE_ID"]); surface != "" {
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
		pane := "current"
		if request.Target != "" {
			pane = request.Target
		}
		args = []string{"pane", pane, "zoom", "--mode", request.Mode}
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
		if request.Target != "" {
			args = append(args, "--surface", request.Target)
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
		args = appendWorkspace(args, request.Target)
		args = append(args, "--", request.Key, request.Value)
	case OperationClearStatus:
		if !commandAdvertised(help, "clear-status") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = appendWorkspace([]string{"clear-status"}, request.Target)
		args = append(args, "--", request.Key)
	case OperationProgress:
		if !commandAdvertised(help, "set-progress") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"set-progress", strconv.FormatFloat(request.Progress, 'f', -1, 64)}
		if request.Label != "" {
			args = append(args, "--label", request.Label)
		}
		args = appendWorkspace(args, request.Target)
	case OperationClearProgress:
		if !commandAdvertised(help, "clear-progress") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = appendWorkspace([]string{"clear-progress"}, request.Target)
	case OperationLog:
		if !commandAdvertised(help, "log") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"log", "--level", request.Level}
		if request.Source != "" {
			args = append(args, "--source", request.Source)
		}
		args = appendWorkspace(args, request.Target)
		args = append(args, "--", request.Message)
	case OperationClearLog:
		if !commandAdvertised(help, "clear-log") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = appendWorkspace([]string{"clear-log"}, request.Target)
	case OperationRead:
		if !commandAdvertised(help, "read-screen") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		if surface == "" {
			return Outcome{}, fmt.Errorf("cmux read requires target when CMUX_SURFACE_ID is not set")
		}
		args = []string{"read-screen", "--surface", surface}
		if request.Scrollback {
			args = append(args, "--scrollback", "--lines", strconv.Itoa(request.Lines))
		}
		result, err := runReadCommand(ctx, adapter.executor, environment, "cmux", args...)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Text: limitedLines(result.Stdout, request)}, nil
	case OperationSendText:
		if !commandAdvertised(help, "send") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		if surface == "" {
			return Outcome{}, fmt.Errorf("cmux send_text requires target when CMUX_SURFACE_ID is not set")
		}
		args = []string{"send", "--surface", surface, "--", escapeCmuxText(request.Text)}
	case OperationSendKeys:
		if !commandAdvertised(help, "send-key") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		if surface == "" {
			return Outcome{}, fmt.Errorf("cmux send_keys requires target when CMUX_SURFACE_ID is not set")
		}
		for _, value := range request.Keys {
			key, err := NormalizeKey(value)
			if err != nil {
				return Outcome{}, err
			}
			if _, err := runCommand(ctx, adapter.executor, environment, "cmux", "send-key", "--surface", surface, "--", key); err != nil {
				return Outcome{}, err
			}
		}
		return Outcome{}, nil
	case OperationSplit:
		if !commandAdvertised(help, "new-split") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		if surface == "" {
			return Outcome{}, fmt.Errorf("cmux split requires target when CMUX_SURFACE_ID is not set")
		}
		result, err := runCommand(ctx, adapter.executor, environment, "cmux", "--json", "--id-format", "refs", "new-split", request.Direction, "--surface", surface)
		if err != nil {
			return Outcome{}, err
		}
		createdTarget, err := cmuxSplitTarget(result.Stdout)
		if err != nil {
			return Outcome{}, err
		}
		createdPane, err := adapter.identifyPane(ctx, environment, createdTarget)
		if err != nil {
			// The split already happened; without its handles the workflow
			// cannot address or close it, so remove it rather than leak it.
			_, _ = runCommand(context.WithoutCancel(ctx), adapter.executor, environment, "cmux", "close-surface", "--surface", createdTarget)
			return Outcome{}, err
		}
		return Outcome{CreatedTarget: createdTarget, CreatedPane: createdPane}, nil
	case OperationResize:
		if !commandAdvertised(help, "resize-pane") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		flags := map[string]string{"left": "-L", "right": "-R", "up": "-U", "down": "-D"}
		args = []string{"resize-pane"}
		if request.Target != "" {
			args = append(args, "--pane", request.Target)
		}
		args = append(args, flags[request.Direction])
		if request.Amount != nil {
			args = append(args, "--amount", amountArgument(request.Amount))
		}
	case OperationFocusPane:
		if !commandAdvertised(help, "focus-pane") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"focus-pane", "--pane", request.Target}
	case OperationCloseSurface:
		if !commandAdvertised(help, "close-surface") {
			return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
		}
		args = []string{"close-surface", "--surface", request.Target}
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation}
	}
	if request.Scope == ScopeTab && request.Operation != OperationTitle && request.Operation != OperationClearTitle {
		return Outcome{}, &UnsupportedError{Provider: ProviderCmux, Operation: request.Operation, Detail: "cmux supports only title and clear_title for tab scope"}
	}
	_, err = runCommand(ctx, adapter.executor, environment, "cmux", args...)
	return Outcome{}, err
}

func (adapter cmuxAdapter) identifyPane(ctx context.Context, environment map[string]string, surface string) (string, error) {
	identify, err := runCommand(ctx, adapter.executor, environment, "cmux", "--json", "--id-format", "refs", "identify", "--surface", surface)
	if err != nil {
		return "", err
	}
	var identified any
	if err := json.Unmarshal([]byte(identify.Stdout), &identified); err != nil {
		return "", fmt.Errorf("parsing cmux identify response: %w", err)
	}
	pane := cmuxIdentifiedPane(identified)
	if pane == "" {
		return "", fmt.Errorf("cmux identify response did not include the created pane handle")
	}
	return pane, nil
}

func appendWorkspace(args []string, workspace string) []string {
	if workspace == "" {
		return args
	}
	return append(args, "--workspace", workspace)
}

func escapeCmuxText(value string) string {
	return strings.ReplaceAll(value, `\`, `\\`)
}

func cmuxSplitTarget(payload string) (string, error) {
	var decoded any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return "", fmt.Errorf("parsing cmux split response: %w", err)
	}
	surface := findCmuxHandle(decoded, "created_surface_ref", "created_surface_id")
	if surface == "" {
		surface = findCmuxHandle(decoded, "surface_ref", "surface_id", "surface", "panel_ref", "panel_id", "panel")
	}
	if surface == "" {
		return "", fmt.Errorf("cmux split response did not include a surface handle")
	}
	return surface, nil
}

func findCmuxHandle(value any, keys ...string) string {
	var visit func(any) string
	visit = func(candidate any) string {
		switch typed := candidate.(type) {
		case map[string]any:
			for _, key := range keys {
				if text, ok := typed[key].(string); ok && text != "" {
					return text
				}
			}
			for _, nested := range typed {
				if result := visit(nested); result != "" {
					return result
				}
			}
		case []any:
			for _, nested := range typed {
				if result := visit(nested); result != "" {
					return result
				}
			}
		}
		return ""
	}
	return visit(value)
}

func cmuxIdentifiedPane(value any) string {
	if envelope, ok := value.(map[string]any); ok {
		if caller, exists := envelope["caller"]; exists {
			if pane := findCmuxHandle(caller, "pane_ref", "pane_id", "pane"); pane != "" {
				return pane
			}
		}
	}
	return findCmuxHandle(value, "pane_ref", "pane_id", "pane")
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
