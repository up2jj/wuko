package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

// stepTerminal lends the user's terminal to one plugin step that declared
// host.terminal.acquire. While it is held, keystrokes reach the plugin through host.input.read,
// size changes are sent as resize notifications for the step's call, and the step's output
// already streams to the terminal. The terminal is restored when the plugin releases it or the
// step ends, whichever comes first.
type stepTerminal struct {
	client  *client
	id      string
	request step.Request

	mu       sync.Mutex
	terminal *process.HostTerminal
	input    *hostInput
	pumped   chan struct{}
}

// terminalUnavailable is the code a plugin sees when the run cannot lend a terminal, so it can
// fall back instead of failing.
const terminalUnavailable = "terminal_unavailable"

func (t *stepTerminal) callbacks(next hostCall) hostCall {
	return func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		switch method {
		case hostTerminalAcquire:
			return t.acquire(raw)
		case "host.terminal.release":
			return struct{}{}, t.release()
		case hostInputRead:
			t.mu.Lock()
			input := t.input
			t.mu.Unlock()
			if input == nil {
				return nil, fmt.Errorf("the terminal is not acquired")
			}
			return input.read(ctx)
		}
		return next(ctx, method, raw)
	}
}

func (t *stepTerminal) acquire(raw json.RawMessage) (any, error) {
	var params struct {
		ParentID string `json:"parent_id"`
		Raw      bool   `json:"raw"`
	}
	if err := decodeFrame(raw, &params); err != nil {
		return nil, fmt.Errorf("decoding terminal request: %w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal != nil {
		return nil, fmt.Errorf("the terminal is already acquired")
	}
	if !t.request.Interactive {
		return nil, &hostCallError{code: terminalUnavailable, err: errors.New("the terminal is unavailable: the run is not interactive")}
	}
	terminal, size, err := process.AcquireTerminal(t.request.Stdin, params.Raw, func(size process.TerminalSize) {
		t.client.notify("resize", map[string]any{"id": t.id, "rows": size.Rows, "cols": size.Cols})
	})
	if err != nil {
		return nil, &hostCallError{code: terminalUnavailable, err: fmt.Errorf("the terminal is unavailable: %w", err)}
	}
	t.terminal, t.input, t.pumped = terminal, newHostInput(), make(chan struct{})
	go func(input *hostInput, pumped chan struct{}) {
		defer close(pumped)
		input.pump(terminal)
	}(t.input, t.pumped)
	return map[string]any{"rows": size.Rows, "cols": size.Cols}, nil
}

func (t *stepTerminal) release() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal == nil {
		return nil
	}
	err := t.terminal.Release()
	t.input.finish()
	<-t.pumped
	err = errors.Join(err, t.terminal.Close())
	t.terminal, t.input, t.pumped = nil, nil, nil
	return err
}
