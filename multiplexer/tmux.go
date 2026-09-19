package multiplexer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type tmuxAdapter struct{ executor commandExecutor }

func (tmuxAdapter) Provider() Provider { return ProviderTmux }

func (tmuxAdapter) Detect(environment map[string]string) (Target, bool) {
	id := strings.TrimSpace(environment["TMUX_PANE"])
	if id == "" {
		return Target{}, false
	}
	return Target{Provider: ProviderTmux, ID: id}, true
}

func (adapter tmuxAdapter) Execute(ctx context.Context, target Target, request Request, environment map[string]string) (Outcome, error) {
	if request.Scope == ScopeTab {
		return adapter.executeWindow(ctx, target, request, environment)
	}
	id := targetID(target, request.Target)
	switch request.Operation {
	case OperationTitle:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", id, "-T", request.Title)
		return Outcome{}, err
	case OperationClearTitle:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", id, "-T", "")
		return Outcome{}, err
	case OperationZoom:
		target.ID = id
		return Outcome{}, adapter.zoom(ctx, target, request.Mode, environment)
	case OperationNotify:
		message := request.Title
		if request.Body != "" {
			message += ": " + request.Body
		}
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "display-message", "-t", id, "--", message)
		return Outcome{}, err
	case OperationRead:
		args := []string{"capture-pane", "-p", "-t", id}
		if request.Scrollback {
			args = append(args, "-S", "-"+strconv.Itoa(request.Lines))
		}
		result, err := runReadCommand(ctx, adapter.executor, environment, "tmux", args...)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Text: limitedLines(result.Stdout, request)}, nil
	case OperationSendText:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "send-keys", "-t", id, "-l", "--", request.Text)
		return Outcome{}, err
	case OperationSendKeys:
		keys, err := tmuxKeys(request.Keys)
		if err != nil {
			return Outcome{}, err
		}
		args := append([]string{"send-keys", "-t", id, "--"}, keys...)
		_, err = runCommand(ctx, adapter.executor, environment, "tmux", args...)
		return Outcome{}, err
	case OperationSplit:
		flag := "-h"
		if request.Direction == "down" {
			flag = "-v"
		}
		result, err := runCommand(ctx, adapter.executor, environment, "tmux", "split-window", "-P", "-F", "#{pane_id}", "-t", id, flag)
		if err != nil {
			return Outcome{}, err
		}
		created := strings.TrimSpace(result.Stdout)
		if created == "" {
			return Outcome{}, fmt.Errorf("tmux split returned no pane id")
		}
		return Outcome{CreatedTarget: created, CreatedPane: created}, nil
	case OperationResize:
		flags := map[string]string{"left": "-L", "right": "-R", "up": "-U", "down": "-D"}
		args := []string{"resize-pane", "-t", id, flags[request.Direction]}
		if request.Amount != nil {
			args = append(args, amountArgument(request.Amount))
		}
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", args...)
		return Outcome{}, err
	case OperationFocusPane:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", id)
		return Outcome{}, err
	case OperationFocusDirection:
		flags := map[string]string{"left": "-L", "right": "-R", "up": "-U", "down": "-D"}
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", id, flags[request.Direction])
		return Outcome{}, err
	case OperationClosePane:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "kill-pane", "-t", id)
		return Outcome{}, err
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderTmux, Operation: request.Operation}
	}
}

// executeWindow maps tab scope onto the tmux window owning the detected pane.
// Clearing restores tmux's own automatic renaming rather than blanking the
// name, which is what a tmux user means by resetting a window title.
func (adapter tmuxAdapter) executeWindow(ctx context.Context, target Target, request Request, environment map[string]string) (Outcome, error) {
	target.ID = targetID(target, request.Target)
	previous, err := adapter.windowName(ctx, target, environment)
	if err != nil {
		return Outcome{}, err
	}
	switch request.Operation {
	case OperationTitle:
		if _, err := runCommand(ctx, adapter.executor, environment, "tmux", "rename-window", "-t", target.ID, "--", request.Title); err != nil {
			return Outcome{}, err
		}
		return Outcome{PreviousTitle: previous}, nil
	case OperationClearTitle:
		if _, err := runCommand(ctx, adapter.executor, environment, "tmux", "set-window-option", "-t", target.ID, "automatic-rename", "on"); err != nil {
			return Outcome{}, err
		}
		return Outcome{PreviousTitle: previous}, nil
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderTmux, Operation: request.Operation, Detail: "tmux supports only title and clear_title for tab scope"}
	}
}

func tmuxKeys(values []string) ([]string, error) {
	mapping := map[string]string{
		"enter": "Enter", "escape": "Escape", "tab": "Tab", "backspace": "BSpace", "delete": "DC", "insert": "IC",
		"home": "Home", "end": "End", "pageup": "PPage", "pagedown": "NPage", "left": "Left", "right": "Right",
		"up": "Up", "down": "Down", "space": "Space", "shift+tab": "BTab",
	}
	keys := make([]string, 0, len(values))
	for _, value := range values {
		key, err := NormalizeKey(value)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasPrefix(key, "ctrl+"):
			key = "C-" + key[5:]
		case strings.HasPrefix(key, "alt+"):
			key = "M-" + key[4:]
		case strings.HasPrefix(key, "f"):
			key = strings.ToUpper(key[:1]) + key[1:]
		default:
			key = mapping[key]
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (adapter tmuxAdapter) windowName(ctx context.Context, target Target, environment map[string]string) (string, error) {
	result, err := runCommand(ctx, adapter.executor, environment, "tmux", "display-message", "-p", "-t", target.ID, "#W")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(result.Stdout, "\r\n"), nil
}

func (adapter tmuxAdapter) zoom(ctx context.Context, target Target, mode string, environment map[string]string) error {
	if mode == "toggle" {
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "resize-pane", "-t", target.ID, "-Z")
		return err
	}
	result, err := runCommand(ctx, adapter.executor, environment, "tmux", "display-message", "-p", "-t", target.ID, "#{window_zoomed_flag}")
	if err != nil {
		return err
	}
	zoomed := strings.TrimSpace(result.Stdout)
	if zoomed != "0" && zoomed != "1" {
		return fmt.Errorf("reading tmux zoom state: expected 0 or 1, got %q", zoomed)
	}
	want := mode == "on"
	if (zoomed == "1") == want {
		return nil
	}
	_, err = runCommand(ctx, adapter.executor, environment, "tmux", "resize-pane", "-t", target.ID, "-Z")
	return err
}
