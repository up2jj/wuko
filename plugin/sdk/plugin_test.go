package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestDispatchPreservesDynamicJSONNumbers(t *testing.T) {
	const rawNumber = "9007199254740993"
	want := json.Number(rawNumber)
	check := func(value any) error {
		if value != want {
			return fmt.Errorf("value = %#v (%T), want json.Number(%q)", value, value, rawNumber)
		}
		return nil
	}

	plugin, err := New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetLifecycle(Lifecycle{Start: func(_ context.Context, with map[string]any) error {
		return check(with["large"])
	}}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.RegisterHelper("number", func(_ context.Context, args []any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("args = %#v, want one argument", args)
		}
		return nil, check(args[0])
	}); err != nil {
		t.Fatal(err)
	}
	type dynamicConfig struct {
		Payload map[string]any `json:"payload"`
	}
	if err := RegisterStep(plugin, "work", StepHandler[dynamicConfig]{
		Validate: func(_ context.Context, request ValidateRequest[dynamicConfig]) error {
			if err := check(request.Config.Payload["large"]); err != nil {
				return err
			}
			return check(request.Context.Vars["large"])
		},
		Run: func(context.Context, RunRequest[dynamicConfig]) (Result, error) {
			return Result{}, nil
		},
		Cleanup: func(_ context.Context, request CleanupRequest[dynamicConfig]) error {
			if err := check(request.Config.Payload["large"]); err != nil {
				return err
			}
			return check(request.Result.Outputs["large"])
		},
	}); err != nil {
		t.Fatal(err)
	}

	with := `{"payload":{"large":` + rawNumber + `}}`
	requests := []wireRequest{
		{Method: "plugin.start", Params: json.RawMessage(`{"with":{"large":` + rawNumber + `}}`)},
		{Method: "helper.call", Params: json.RawMessage(`{"name":"number","args":[` + rawNumber + `]}`)},
		{Method: "step.validate", Params: json.RawMessage(`{"type":"acme.work","with":` + with + `,"context":{"vars":{"large":` + rawNumber + `}}}`)},
		{Method: "step.cleanup", Params: json.RawMessage(`{"type":"acme.work","with":` + with + `,"result":{"outputs":{"large":` + rawNumber + `}}}`)},
	}
	for _, request := range requests {
		if _, err := plugin.dispatch(t.Context(), request, nil); err != nil {
			t.Fatalf("%s: %v", request.Method, err)
		}
	}

	var typed struct {
		Value int64 `json:"value"`
	}
	if err := decodeParams(json.RawMessage(`{"value":`+rawNumber+`}`), &typed); err != nil {
		t.Fatal(err)
	}
	if typed.Value != 9007199254740993 {
		t.Fatalf("typed value = %d", typed.Value)
	}
}

func TestDecodeJSONRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{`{`, `{}` + `{}`} {
		t.Run(raw, func(t *testing.T) {
			var value any
			if err := decodeJSON([]byte(raw), &value); err == nil {
				t.Fatal("decodeJSON returned nil error")
			}
		})
	}
}

func TestRegisteredStepRunProvidesStreamsAndSafeReadiness(t *testing.T) {
	plugin, err := New("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterStep(plugin, "work", StepHandler[struct{}]{
		Run: func(_ context.Context, request RunRequest[struct{}]) (Result, error) {
			if request.Stdout == nil || request.Stderr == nil {
				return Result{}, io.ErrClosedPipe
			}
			if _, err := request.Stdout.Write([]byte("stdout")); err != nil {
				return Result{}, err
			}
			if _, err := request.Stderr.Write([]byte("stderr")); err != nil {
				return Result{}, err
			}
			return Result{}, request.Ready(Result{})
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = plugin.steps["work"].run(t.Context(), nil, StepContext{}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "readiness is unavailable") {
		t.Fatalf("run error = %v", err)
	}
}
