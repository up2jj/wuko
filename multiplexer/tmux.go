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

func (adapter tmuxAdapter) Execute(ctx context.Context, target Target, request Request, environment map[string]string) error {
	switch request.Operation {
	case OperationTitle:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", target.ID, "-T", request.Title)
		return err
	case OperationClearTitle:
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "select-pane", "-t", target.ID, "-T", "")
		return err
	case OperationZoom:
		return adapter.zoom(ctx, target, request.Mode, environment)
	case OperationNotify:
		message := request.Title
		if request.Body != "" {
			message += ": " + request.Body
		}
		_, err := runCommand(ctx, adapter.executor, environment, "tmux", "display-message", "-t", target.ID, message)
		return err
	default:
		return &UnsupportedError{Provider: ProviderTmux, Operation: request.Operation}
	}
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
