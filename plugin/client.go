package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const maxFrameSize = 10 << 20

type requestFrame struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}
type responseFrame struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}
type wireError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}
type pendingCall struct {
	response chan responseFrame
	event    func(eventFrame)
}
type eventFrame struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	Data  string `json:"data,omitempty"`
}

type client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]pendingCall
	next    atomic.Uint64
	done    chan struct{}
	readErr error
}

func launch(ctx context.Context, path string, stderr io.Writer) (*client, error) {
	cmd := exec.CommandContext(context.WithoutCancel(ctx), path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launching plugin: %w", err)
	}
	c := &client{cmd: cmd, stdin: stdin, pending: make(map[string]pendingCall), done: make(chan struct{})}
	go c.read(stdout)
	return c, nil
}

func (c *client) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxFrameSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		var header struct {
			ID    string `json:"id"`
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &header) != nil || header.ID == "" {
			c.fail(fmt.Errorf("malformed plugin frame"))
			return
		}
		c.mu.Lock()
		pending, ok := c.pending[header.ID]
		if header.Event == "" && ok {
			delete(c.pending, header.ID)
		}
		c.mu.Unlock()
		if !ok {
			c.fail(fmt.Errorf("plugin sent unknown response id %q", header.ID))
			return
		}
		if header.Event != "" {
			var event eventFrame
			if decodeFrame(line, &event) != nil || (event.Event != "stdout" && event.Event != "stderr" && event.Event != "started") {
				c.fail(fmt.Errorf("malformed plugin event"))
				return
			}
			if pending.event != nil {
				pending.event(event)
			}
			continue
		}
		var response responseFrame
		if decodeFrame(line, &response) != nil || (response.Error == nil && response.Result == nil) || (response.Error != nil && response.Result != nil) {
			c.fail(fmt.Errorf("plugin response must contain exactly one of result or error"))
			return
		}
		pending.response <- response
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("reading plugin protocol: %w", err))
	} else {
		c.fail(io.EOF)
	}
}

func decodeFrame(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func (c *client) fail(err error) {
	c.mu.Lock()
	if c.readErr != nil {
		c.mu.Unlock()
		return
	}
	c.readErr = err
	for id, call := range c.pending {
		delete(c.pending, id)
		call.response <- responseFrame{Error: &wireError{Message: err.Error()}}
	}
	close(c.done)
	c.mu.Unlock()
}

func (c *client) call(ctx context.Context, method string, params, result any, event func(eventFrame)) error {
	id := strconv.FormatUint(c.next.Add(1), 10)
	response := make(chan responseFrame, 1)
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return err
	}
	c.pending[id] = pendingCall{response: response, event: event}
	c.mu.Unlock()
	discard := func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }
	data, err := json.Marshal(requestFrame{ID: id, Method: method, Params: params})
	if err != nil {
		discard()
		return err
	}
	if len(data) > maxFrameSize {
		discard()
		return fmt.Errorf("plugin request exceeds 10 MiB")
	}
	c.writeMu.Lock()
	_, err = c.stdin.Write(append(data, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		discard()
		return fmt.Errorf("writing plugin request: %w", err)
	}
	select {
	case frame := <-response:
		if frame.Error != nil {
			return errors.New(frame.Error.Message)
		}
		if result != nil {
			decoder := json.NewDecoder(bytes.NewReader(frame.Result))
			decoder.UseNumber()
			if err := decoder.Decode(result); err != nil {
				return fmt.Errorf("decoding plugin response: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		// Drop the event sink so a plugin that keeps streaming for this id cannot write
		// into writers the canceled caller no longer owns. The entry stays registered so
		// the eventual response is not mistaken for an unknown id.
		c.mu.Lock()
		if entry, ok := c.pending[id]; ok {
			entry.event = nil
			c.pending[id] = entry
		}
		c.mu.Unlock()
		c.notify("cancel", map[string]any{"id": id})
		return ctx.Err()
	}
}

func (c *client) notify(method string, params any) {
	data, _ := json.Marshal(requestFrame{Method: method, Params: params})
	c.writeMu.Lock()
	_, _ = c.stdin.Write(append(data, '\n'))
	c.writeMu.Unlock()
}

func (c *client) close(ctx context.Context) error {
	var result any
	shutdownErr := c.call(ctx, "shutdown", map[string]any{}, &result, nil)
	_ = c.stdin.Close()
	wait := make(chan error, 1)
	go func() { wait <- c.cmd.Wait() }()
	select {
	case err := <-wait:
		return errors.Join(shutdownErr, err)
	case <-ctx.Done():
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-wait:
		return errors.Join(shutdownErr, err)
	case <-timer.C:
		_ = c.cmd.Process.Kill()
		return errors.Join(shutdownErr, <-wait)
	}
}
