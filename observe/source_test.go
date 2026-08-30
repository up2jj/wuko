package observe

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRegistryAcceptsFutureSourceWithoutEngineChanges(t *testing.T) {
	builder := testBuilder{sourceType: "process"}
	registry := NewRegistry(builder)
	if err := registry.Validate("process", map[string]any{"command": "server"}); err != nil {
		t.Fatal(err)
	}
	source, err := registry.Open(t.Context(), "process", OpenRequest{RunDir: t.TempDir(), Config: map[string]any{"command": "server"}})
	if err != nil {
		t.Fatal(err)
	}
	if source.Metadata()["kind"] != "test" {
		t.Fatalf("metadata = %#v", source.Metadata())
	}
	if err := registry.Register(builder); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestHTTPSourceEmitsOnlyChangedResponsesByDefault(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		request := requests.Add(1)
		body := `{"version":1}`
		if request >= 3 {
			body = `{"version":2}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	builder := HTTPBuilder{Client: client}
	source, err := builder.Open(t.Context(), OpenRequest{Config: map[string]any{
		"every": "1ms", "request": map[string]any{"url": "https://example.test/status", "response": "json"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	initial := source.Initial().(map[string]any)
	if initial["status"] != http.StatusOK {
		t.Fatalf("initial = %#v", initial)
	}
	event, err := source.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	value := event.(map[string]any)["value"].(map[string]any)
	if value["version"] != float64(2) || requests.Load() != 3 {
		t.Fatalf("event = %#v after %d requests", event, requests.Load())
	}
}

type testBuilder struct{ sourceType string }

func (builder testBuilder) Type() string          { return builder.sourceType }
func (testBuilder) Validate(map[string]any) error { return nil }
func (testBuilder) Open(context.Context, OpenRequest) (Source, error) {
	return testSource{}, nil
}

type testSource struct{}

func (testSource) Initial() any                          { return nil }
func (testSource) Next(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() }
func (testSource) NewBatch() Batch                       { return &testBatch{} }
func (testSource) Metadata() map[string]any              { return map[string]any{"kind": "test"} }
func (testSource) Close() error                          { return nil }

type testBatch struct{ values []any }

func (batch *testBatch) Add(value any) { batch.values = append(batch.values, value) }
func (batch *testBatch) Merge(other Batch) {
	batch.values = append(batch.values, other.(*testBatch).values...)
}
func (batch *testBatch) Empty() bool { return len(batch.values) == 0 }
func (batch *testBatch) Binding() map[string]any {
	return map[string]any{"test": append([]any(nil), batch.values...)}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
