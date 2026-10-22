package scriptsdk

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func (b *Bridge) dispatch(ctx context.Context, method string, raw json.RawMessage) (result any, err error) {
	// A faulty third-party helper must fail its call without taking down Wuko.
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = nil, fmt.Errorf("SDK host call panicked")
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch method {
	case "context":
		if err := decode(raw, &struct{}{}); err != nil {
			return nil, fmt.Errorf("decoding context: %w", err)
		}
		return b.contextResult(), nil
	case "output", "set_var":
		var params struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		}
		if err := decode(raw, &params); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", method, err)
		}
		if strings.TrimSpace(params.Name) == "" || len(params.Value) == 0 {
			return nil, fmt.Errorf("%s requires a non-empty name and value", method)
		}
		value, err := wukoexpr.ParseJSON(string(params.Value))
		if err != nil {
			return nil, err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.closing || ctx.Err() != nil {
			return nil, context.Canceled
		}
		if method == "output" {
			b.outputs[params.Name] = value
		} else {
			b.variables[params.Name] = value
		}
		return nil, nil
	case "helper":
		var params struct {
			Name string `json:"name"`
			Args []any  `json:"args"`
		}
		if err := decode(raw, &params); err != nil {
			return nil, fmt.Errorf("decoding helper: %w", err)
		}
		if params.Args != nil {
			data, err := json.Marshal(params.Args)
			if err != nil {
				return nil, err
			}
			value, err := wukoexpr.ParseJSON(string(data))
			if err != nil {
				return nil, err
			}
			params.Args = value.([]any)
		}
		if call := b.request.Helpers[params.Name]; call != nil {
			return call(ctx, params.Args)
		}
		function := templateFuncs()[params.Name]
		if function == nil || params.Name == "secret" {
			return nil, fmt.Errorf("unknown helper %q", params.Name)
		}
		// Bind the callable and every argument as data; never compile caller-supplied
		// function names or source. Expr supplies typed and variadic call checking.
		env := map[string]any{"sdkFunction": function}
		arguments := make([]string, len(params.Args))
		for i, value := range params.Args {
			arguments[i] = fmt.Sprintf("sdkArg%d", i)
			env[arguments[i]] = value
		}
		program, err := expr.Compile("sdkFunction("+strings.Join(arguments, ",")+")", expr.Env(env))
		if err != nil {
			return nil, fmt.Errorf("helper %s: %w", params.Name, err)
		}
		return expr.Run(program, env)
	case "secret":
		var params struct {
			Reference string `json:"reference"`
		}
		if err := decode(raw, &params); err != nil {
			return nil, fmt.Errorf("decoding secret: %w", err)
		}
		return b.request.ResolveSecret(params.Reference)
	case "render":
		var params struct {
			Template string         `json:"template"`
			Extra    map[string]any `json:"extra"`
		}
		if err := decode(raw, &params); err != nil {
			return nil, fmt.Errorf("decoding render: %w", err)
		}
		extra, err := runtimeObject(params.Extra)
		if err != nil {
			return nil, err
		}
		if renderer, ok := b.request.TemplateRenderer.(step.DataTemplateRenderer); ok {
			return renderer.RenderContentWith(params.Template, extra)
		}
		if b.request.TemplateRenderer == nil {
			return nil, fmt.Errorf("template renderer is unavailable")
		}
		if len(params.Extra) > 0 {
			return nil, fmt.Errorf("extra template data is unavailable")
		}
		return b.request.TemplateRenderer.RenderContent(params.Template)
	case "step":
		var params struct {
			Type   string         `json:"type"`
			Config map[string]any `json:"config"`
		}
		if err := decode(raw, &params); err != nil {
			return nil, fmt.Errorf("decoding step: %w", err)
		}
		return b.runStep(ctx, params.Type, params.Config)
	case "run_workflow":
		var params struct {
			Name   string         `json:"name"`
			Target string         `json:"target"`
			Vars   map[string]any `json:"vars"`
		}
		if err := decode(raw, &params); err != nil {
			return nil, fmt.Errorf("decoding workflow: %w", err)
		}
		return b.runStep(ctx, "run_workflow", map[string]any{"workflow": params.Name, "target": params.Target, "vars": params.Vars})
	default:
		return nil, fmt.Errorf("unknown SDK method %q", method)
	}
}

// templateFuncs builds the shared helper table once instead of on every helper call.
var templateFuncs = sync.OnceValue(wukoexpr.TemplateFuncs)

func (b *Bridge) runStep(ctx context.Context, kind string, config map[string]any) (any, error) {
	if strings.TrimSpace(kind) == "" {
		return nil, fmt.Errorf("step type is required")
	}
	if b.request.InlineWorkflows == nil {
		return nil, fmt.Errorf("inline step execution is unavailable")
	}
	config, err := runtimeObject(config)
	if err != nil {
		return nil, err
	}
	// Stable across retries of the same parent operation, unlike the socket path.
	identity := sha256.Sum256([]byte(b.request.OperationID + "\x00" + b.request.StepID))
	id := fmt.Sprintf("sdk_%x_%d", identity[:8], b.sequence.Add(1))
	// Calls use ordinary workflow configuration semantics against the captured
	// parent state. Each child has its own lifecycle and commits no parent state.
	call := step.InlineWorkflowCall{
		Name:    id,
		Steps:   []workflow.Step{{ID: id, Type: kind, With: config}},
		Outputs: map[string]string{"value": "steps." + id},
	}
	if err := b.request.InlineWorkflows.ValidateInlineWorkflow(ctx, call); err != nil {
		return nil, err
	}
	outputs, err := b.request.InlineWorkflows.RunInlineWorkflow(ctx, call)
	if err != nil {
		return nil, err
	}
	return outputs["value"], nil
}

// Convert transport numbers to the native runtime types expected by steps and
// template helpers without rounding integers through float64.
func runtimeObject(object map[string]any) (map[string]any, error) {
	if object == nil {
		return nil, nil
	}
	data, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	value, err := wukoexpr.ParseJSON(string(data))
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}
