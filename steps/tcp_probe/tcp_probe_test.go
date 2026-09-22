package tcpprobe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{name: "missing hosts", raw: map[string]any{"ports": []any{80}}},
		{name: "empty hosts", raw: map[string]any{"hosts": []any{}, "ports": []any{80}}},
		{name: "blank host", raw: map[string]any{"hosts": []any{" "}, "ports": []any{80}}},
		{name: "duplicate host", raw: map[string]any{"hosts": []any{"localhost", "localhost"}, "ports": []any{80}}},
		{name: "missing ports", raw: map[string]any{"hosts": []any{"localhost"}}},
		{name: "zero port", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{0}}},
		{name: "large port", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{65536}}},
		{name: "duplicate port", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{80, 80}}},
		{name: "non numeric port", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{"http"}}},
		{name: "duplicate port across forms", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{80, "80"}}},
		{name: "duplicate host after trimming", raw: map[string]any{"hosts": []any{"localhost", " localhost"}, "ports": []any{80}}},
		{name: "ports not a list", raw: map[string]any{"hosts": []any{"localhost"}, "ports": 80}},
		{name: "zero timeout", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{80}, "timeout": "0s"}},
		{name: "invalid expectation", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{80}, "expect": "open"}},
		{name: "unknown field", raw: map[string]any{"hosts": []any{"localhost"}, "ports": []any{80}, "unknown": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.raw); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("New(%#v) error = %v", tt.raw, err)
			}
		})
	}
}

func TestRunReturnsOrderedMatrixSummaryAndOwners(t *testing.T) {
	runner := testRunner(t, map[string]any{
		"hosts": []any{"one", "two"}, "ports": []any{4000, 4001}, "owners": true,
	})
	runner.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		switch address {
		case "one:4000":
			return &stubConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4000}}, nil
		case "two:4000":
			return nil, context.DeadlineExceeded
		default:
			return nil, syscall.ECONNREFUSED
		}
	}
	runner.localAddrs = func() (map[netip.Addr]struct{}, error) {
		return map[netip.Addr]struct{}{netip.MustParseAddr("127.0.0.1"): {}}, nil
	}
	runner.lookupOwners = func(_ context.Context, endpoints []listenerEndpoint) ownerLookupResult {
		if len(endpoints) != 1 || endpoints[0].port != 4000 {
			t.Fatalf("endpoints = %#v", endpoints)
		}
		return ownerLookupResult{
			supported: true, complete: true,
			owners: map[listenerEndpoint][]owner{
				endpoints[0]: {{PID: 42, Name: "server", Executable: "/bin/server"}},
			},
		}
	}

	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	// No expect was configured, so there is nothing for these endpoints to match and matched
	// stays null rather than claiming a match that was never asked for.
	if result.Outputs["count"] != 4 || result.Outputs["listening"] != 1 || result.Outputs["free"] != 2 || result.Outputs["indeterminate"] != 1 || result.Outputs["matched"] != nil {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	items := result.Outputs["results"].([]any)
	want := []struct {
		host  string
		port  int
		state string
	}{{"one", 4000, stateListening}, {"one", 4001, stateFree}, {"two", 4000, stateIndeterminate}, {"two", 4001, stateFree}}
	for index, expected := range want {
		item := items[index].(map[string]any)
		if item["host"] != expected.host || item["port"] != expected.port || item["state"] != expected.state {
			t.Fatalf("results[%d] = %#v, want %#v", index, item, expected)
		}
	}
	owners := items[0].(map[string]any)["owners"].([]any)
	if len(owners) != 1 || owners[0].(map[string]any)["pid"] != 42 {
		t.Fatalf("owners = %#v", owners)
	}
	ownership := result.Outputs["ownership"].(map[string]any)
	if ownership["requested"] != true || ownership["supported"] != true || ownership["complete"] != true {
		t.Fatalf("ownership = %#v", ownership)
	}
}

func TestExpectationFailureCarriesObservation(t *testing.T) {
	runner := testRunner(t, map[string]any{
		"hosts": []any{"127.0.0.1"}, "ports": []any{4000, 4001}, "expect": stateFree,
	})
	runner.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		if strings.HasSuffix(address, ":4000") {
			return &stubConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4000}}, nil
		}
		return nil, context.DeadlineExceeded
	}

	result, err := runner.Run(t.Context(), step.Request{})
	if err == nil || result.Outputs["matched"] != false {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	var observation step.ObservationError
	if !errors.As(err, &observation) || !observation.ObservationAvailable() {
		t.Fatalf("error = %T %v, want observation", err, err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:4000 (listening)") || !strings.Contains(err.Error(), "127.0.0.1:4001 (indeterminate)") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunHonorsConcurrencyLimit(t *testing.T) {
	ports := make([]any, probeLimit+1)
	for index := range ports {
		ports[index] = index + 1
	}
	runner := testRunner(t, map[string]any{"hosts": []any{"127.0.0.1"}, "ports": ports})
	started := make(chan struct{}, len(ports))
	release := make(chan struct{})
	var mu sync.Mutex
	active, maximum := 0, 0
	runner.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		mu.Lock()
		active++
		maximum = max(maximum, active)
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		return nil, syscall.ECONNREFUSED
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(t.Context(), step.Request{})
		done <- err
	}()
	for range probeLimit {
		<-started
	}
	select {
	case <-started:
		t.Fatal("probe exceeded concurrency limit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != probeLimit {
		t.Fatalf("maximum concurrency = %d, want %d", maximum, probeLimit)
	}
}

func TestRealLoopbackListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	runner := testRunner(t, map[string]any{"hosts": []any{"127.0.0.1"}, "ports": []any{port}})
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	item := result.Outputs["results"].([]any)[0].(map[string]any)
	if item["state"] != stateListening || item["address"] != "127.0.0.1" {
		t.Fatalf("result = %#v", item)
	}
}

// A port is the field most likely to come from a variable, so it has to survive validation
// while it is still a template and be parsed once the engine has resolved it.
func TestPortsAcceptTemplates(t *testing.T) {
	runner := testRunner(t, map[string]any{
		"hosts": []any{"{{ .vars.host }}"}, "ports": []any{"{{ .vars.port }}", 4001},
	})
	if _, err := runner.validate(true); err == nil {
		t.Fatal("an unresolved port must not reach the dialer")
	}

	runner.config.Hosts = []string{" 127.0.0.1 "}
	runner.config.Ports = portList{"4000", "4001"}
	probes, err := runner.validate(true)
	if err != nil {
		t.Fatal(err)
	}
	// Surrounding whitespace is trimmed rather than dialed as part of the host.
	if len(probes.hosts) != 1 || probes.hosts[0] != "127.0.0.1" {
		t.Fatalf("hosts = %#v", probes.hosts)
	}
	if len(probes.ports) != 2 || probes.ports[0] != 4000 || probes.ports[1] != 4001 {
		t.Fatalf("ports = %#v", probes.ports)
	}
}

// A template that is still a template when the step runs never becomes a port, and failing
// there is better than dialing whatever the text happens to parse as.
func TestPortsRejectUnresolvedTemplatesAtRun(t *testing.T) {
	runner := testRunner(t, map[string]any{
		"hosts": []any{"localhost"}, "ports": []any{"{{ .vars.port }}", "{{ .vars.other }}"},
	})
	if _, err := runner.validate(true); err == nil {
		t.Fatal("validate accepted an unresolved port")
	}
}

// Every endpoint handed to the lookup answered a TCP handshake moments earlier, so an empty
// owner list is a blind spot rather than an unowned port and must not read as complete.
func TestRunReportsUnexplainedListenersAsIncomplete(t *testing.T) {
	runner := testRunner(t, map[string]any{
		"hosts": []any{"127.0.0.1"}, "ports": []any{4000}, "owners": true,
	})
	runner.dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		return &stubConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4000}}, nil
	}
	runner.localAddrs = func() (map[netip.Addr]struct{}, error) {
		return map[netip.Addr]struct{}{netip.MustParseAddr("127.0.0.1"): {}}, nil
	}
	runner.lookupOwners = func(context.Context, []listenerEndpoint) ownerLookupResult {
		return ownerLookupResult{supported: true, complete: true, hint: "12 processes were not readable by this user"}
	}

	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	ownership := result.Outputs["ownership"].(map[string]any)
	if ownership["complete"] != false {
		t.Fatalf("ownership = %#v", ownership)
	}
	detail, _ := ownership["detail"].(string)
	if !strings.Contains(detail, "127.0.0.1:4000") || !strings.Contains(detail, "not readable") {
		t.Fatalf("detail = %q", detail)
	}
}

func testRunner(t *testing.T, raw map[string]any) *Runner {
	t.Helper()
	built, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return built.(*Runner)
}

type stubConn struct {
	remote net.Addr
}

func (*stubConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*stubConn) Write(data []byte) (int, error)   { return len(data), nil }
func (*stubConn) Close() error                     { return nil }
func (*stubConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (conn *stubConn) RemoteAddr() net.Addr        { return conn.remote }
func (*stubConn) SetDeadline(time.Time) error      { return nil }
func (*stubConn) SetReadDeadline(time.Time) error  { return nil }
func (*stubConn) SetWriteDeadline(time.Time) error { return nil }
