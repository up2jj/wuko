package engine_test

import (
	"errors"
	"net"
	"strconv"
	"syscall"
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	tcpprobestep "github.com/up2jj/wuko/steps/tcp_probe"
	"github.com/up2jj/wuko/workflow"
)

func TestTCPProbeOutputsAreAvailableToLaterSteps(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	definition := &workflow.Definition{
		Version: 1, Name: "tcp-probe",
		Steps: []workflow.Step{
			{ID: "ports", Type: "tcp_probe", With: map[string]any{
				"hosts": []any{"127.0.0.1"}, "ports": []any{port}, "expect": "listening",
			}},
			{ID: "verify", Type: "assert", With: map[string]any{
				"expr":    "steps.ports.count == 1 && steps.ports.listening == 1 && steps.ports.results[0].port == " + strconv.Itoa(port),
				"message": "tcp_probe outputs were not committed",
			}},
		},
	}
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{tcpprobestep.Register, assertstep.Register} {
		if err := register(registry); err != nil {
			t.Fatal(err)
		}
	}
	state, err := engine.New(registry).Run(t.Context(), definition, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Steps["ports"].(map[string]any)["matched"] != true {
		t.Fatalf("state = %#v", state.Steps["ports"])
	}
}
