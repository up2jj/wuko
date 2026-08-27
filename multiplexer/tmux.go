package multiplexer

import (
	"context"
	"fmt"
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
	switch request.Operation {
	case OperationTitle:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", target.ID, "-T", request.Title)
		return Outcome{}, err
	case OperationClearTitle:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", target.ID, "-T", "")
		return Outcome{}, err
	case OperationZoom:
		return Outcome{}, adapter.zoom(ctx, target, request.Mode, environment)
	case OperationNotify:
		message := request.Title
		if request.Body != "" {
			message += ": " + request.Body
		}
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "display-message", "-t", target.ID, "--", message)
		return Outcome{}, err
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderTmux, Operation: request.Operation}
	}
}

// executeWindow maps tab scope onto the tmux window owning the detected pane.
// Clearing restores tmux's own automatic renaming rather than blanking the
// name, which is what a tmux user means by resetting a window title.
func (adapter tmuxAdapter) executeWindow(ctx context.Context, target Target, request Request, environment map[string]string) (Outcome, error) {
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
