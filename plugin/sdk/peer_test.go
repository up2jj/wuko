package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedWriter holds the first write whose payload contains hold until the test releases it,
// which puts a reply that is mid-flight into a known state rather than a racing one.
type gatedWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	hold   string
	held   chan struct{}
	gate   chan struct{}
	once   sync.Once
}

func (writer *gatedWriter) Write(data []byte) (int, error) {
	if writer.hold != "" && strings.Contains(string(data), writer.hold) {
		writer.once.Do(func() { close(writer.held) })
		<-writer.gate
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.Write(data)
}

func (writer *gatedWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

// The host waits for a shutdown reply before considering the plugin closed, so serve must not
// return - and let Serve's caller exit the process - while that reply is still being written.
// A cancel arriving in that window is exactly when it is most tempting to.
func TestShutdownReplyOutlivesRootCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	output := &gatedWriter{hold: `"id":"stop"`, held: make(chan struct{}), gate: make(chan struct{})}
	running := make(chan struct{})
	dispatch := func(requestCtx context.Context, request wireRequest, _ *peer) (any, error) {
		close(running)
		<-requestCtx.Done()
		return map[string]any{}, nil
	}
	served := make(chan error, 1)
	go func() { served <- serve(ctx, reader, output, dispatch) }()

	if _, err := writer.Write([]byte(`{"id":"work","method":"step.run"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	<-running
	if _, err := writer.Write([]byte(`{"id":"stop","method":"shutdown"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// The shutdown reply has reached the transport, so the race this guards is open.
	<-output.held

	cancel()
	select {
	case <-served:
		t.Fatal("serve returned while the shutdown reply was still being written")
	case <-time.After(100 * time.Millisecond):
	}
	close(output.gate)
	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Fatalf("serve error = %v, want context.Canceled", err)
	}
	if !strings.Contains(output.String(), `"id":"stop"`) {
		t.Fatalf("shutdown reply was never written: %q", output.String())
	}
}

// A frame at exactly the size a writer is allowed to emit must be readable: the cap bounds the
// JSON, and a scanner sized to the cap alone rejects that frame once its newline is counted.
func TestLargestPermittedFrameIsReadable(t *testing.T) {
	build := func(padding int) []byte {
		data, err := json.Marshal(wireRequest{ID: "big", Method: "step.run", Params: mustJSON(map[string]any{"value": strings.Repeat("x", padding)})})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	frame := build(maxProtocolFrame - len(build(0)))
	if len(frame) != maxProtocolFrame {
		t.Fatalf("frame = %d bytes, want exactly %d", len(frame), maxProtocolFrame)
	}
	if err := (&peer{out: io.Discard}).write(json.RawMessage(frame)); err != nil {
		t.Fatalf("a frame this size is rejected by the writer, so the test is not measuring the reader: %v", err)
	}

	reader, writer := io.Pipe()
	received := make(chan string, 1)
	dispatch := func(context.Context, wireRequest, *peer) (any, error) {
		received <- "dispatched"
		return map[string]any{}, nil
	}
	served := make(chan error, 1)
	go func() { served <- serve(context.Background(), reader, io.Discard, dispatch) }()
	// A reader that gives up on this frame stops consuming it, and the pipe blocks the writer
	// for good; writing from a goroutine keeps that a legible failure rather than a hung test.
	written := make(chan error, 1)
	go func() {
		_, err := writer.Write(append(frame, '\n'))
		written <- err
	}()
	select {
	case <-received:
	case <-time.After(10 * time.Second):
		select {
		case err := <-written:
			t.Fatalf("the largest permitted frame was never dispatched, write returned %v", err)
		default:
			t.Fatal("the largest permitted frame was never dispatched: the reader stopped consuming it")
		}
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if err := <-served; err != nil {
		t.Fatalf("serve error = %v", err)
	}
}
