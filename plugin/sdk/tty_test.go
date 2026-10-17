package sdk

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

type resizeSession struct{}

func (resizeSession) Run(ctx context.Context, command Command) (CommandResult, error) {
	fmt.Fprint(command.Stdout, "ready")
	select {
	case size := <-command.Resize:
		fmt.Fprintf(command.Stdout, "%dx%d", size.Rows, size.Cols)
		return CommandResult{}, nil
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	}
}
func (resizeSession) Close(context.Context) error { return nil }

func TestTTYCommandReceivesResizeNotifications(t *testing.T) {
	plugin, err := New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterExecutor(plugin, "box", ExecutorHandler[map[string]any]{TTY: true, Open: func(context.Context, ExecutorRequest[map[string]any]) (ExecutorSession, error) {
		return resizeSession{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	pluginInput, hostInput := io.Pipe()
	hostOutput, pluginOutput := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = plugin.Serve(ctx, pluginInput, pluginOutput) }()
	frames := bufio.NewScanner(hostOutput)
	send := func(frame map[string]any) {
		data, _ := json.Marshal(frame)
		if _, err := hostInput.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		if !frames.Scan() {
			t.Fatal("plugin closed its output")
		}
		var frame map[string]any
		if err := json.Unmarshal(frames.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	send(map[string]any{"id": "1", "method": "initialize", "params": map[string]any{"protocol": Protocol}})
	next()
	send(map[string]any{"id": "2", "method": "executor.open", "params": map[string]any{"type": "acme.box", "with": map[string]any{}}})
	session := next()["result"].(map[string]any)["session"]
	send(map[string]any{"id": "3", "method": "executor.run", "params": map[string]any{"session": session, "command": "sh", "tty": map[string]any{"rows": 24, "cols": 80}}})
	// A size for another call is dropped instead of failing the protocol.
	send(map[string]any{"method": "resize", "params": map[string]any{"id": "99", "rows": 1, "cols": 1}})
	readData := func(frame map[string]any) string {
		if frame["event"] != "stdout" {
			t.Fatalf("frame = %#v, want stdout", frame)
		}
		data, _ := base64.StdEncoding.DecodeString(frame["data"].(string))
		return string(data)
	}
	if got := readData(next()); got != "ready" {
		t.Fatalf("first output = %q", got)
	}
	send(map[string]any{"method": "resize", "params": map[string]any{"id": "3", "rows": 40, "cols": 100}})
	if got := readData(next()); got != "40x100" {
		t.Fatalf("resized to %q, want 40x100", got)
	}
	if response := next(); response["id"] != "3" || response["error"] != nil {
		t.Fatalf("run response = %#v", response)
	}
}
