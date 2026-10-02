package stepadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/provider"
	"github.com/up2jj/wuko/step"
)

func stepRequest(ctx context.Context, value sdk.StepContext, host sdk.Host, secretsReachable bool, stdout, stderr io.Writer, services step.ServiceLauncher) step.Request {
	helpers := make(helper.Set, len(value.Helpers))
	for _, name := range value.Helpers {
		helpers[name] = func(callCtx context.Context, args []any) (any, error) {
			result, err := host.CallFunction(callCtx, name, args)
			return normalizeValue(result), err
		}
	}
	renderer := &hostRenderer{ctx: ctx, host: host, secretsReachable: secretsReachable}
	request := step.Request{
		StepID: value.StepID, WorkflowName: value.WorkflowName, WorkflowSource: value.WorkflowSource,
		WorkflowDir: value.WorkflowDir, WorkflowDirBorrowed: value.WorkflowDirBorrowed, WorkflowTimezone: value.WorkflowTimezone,
		RunDir: value.RunDir, EnvironmentLoaders: slices.Clone(value.EnvironmentLoaders),
		LocalValueDir: value.LocalValueDir, GlobalValueDir: value.GlobalValueDir,
		Vars: normalizeMap(value.Vars), PresetVars: normalizeMap(value.PresetVars), Inputs: normalizeMap(value.Inputs),
		Env: maps.Clone(value.Env), Steps: normalizeMap(value.Steps), Dependencies: normalizeNestedMap(value.Dependencies),
		Bindings: normalizeMap(value.Bindings), Providers: provider.Set{Values: providerValues(value.Providers)},
		Stdout: stdout, Stderr: stderr, Attempt: value.Attempt, MaxAttempts: value.MaxAttempts, OperationID: value.OperationID,
		TemplateRenderer: renderer, Helpers: helpers, HelperContext: ctx, Services: services,
	}
	request.Secret = func(reference string) (string, error) { return host.Secret(ctx, reference) }
	if value.PreviousAttempt != nil {
		request.PreviousAttempt = &step.Result{
			Outputs: normalizeMap(value.PreviousAttempt.Outputs), Variables: normalizeMap(value.PreviousAttempt.Variables),
		}
	}
	return request
}

func providerValues(values map[string]any) map[string]map[string]any {
	result := make(map[string]map[string]any, len(values))
	for name, value := range values {
		if fields, ok := value.(map[string]any); ok {
			result[name] = normalizeMap(fields)
		}
	}
	return result
}

func normalizeMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(values))
	for name, value := range values {
		result[name] = normalizeValue(value)
	}
	return result
}

func normalizeNestedMap(values map[string]map[string]any) map[string]map[string]any {
	if values == nil {
		return map[string]map[string]any{}
	}
	result := make(map[string]map[string]any, len(values))
	for name, value := range values {
		result[name] = normalizeMap(value)
	}
	return result
}

func normalizeValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return normalizeNumber(typed)
	case map[string]any:
		return normalizeMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeValue(item)
		}
		return result
	default:
		return value
	}
}

func normalizeNumber(value json.Number) any {
	text := string(value)
	if strings.ContainsAny(text, ".eE") {
		if number, err := strconv.ParseFloat(text, 64); err == nil {
			return number
		}
		return value
	}
	if number, err := strconv.ParseInt(text, 10, 64); err == nil {
		if strconv.IntSize == 64 || number >= -1<<31 && number <= 1<<31-1 {
			return int(number)
		}
		return number
	}
	if number, err := strconv.ParseUint(text, 10, 64); err == nil {
		if strconv.IntSize == 64 && number <= 1<<63-1 || strconv.IntSize == 32 && number <= 1<<31-1 {
			return int(number)
		}
		return number
	}
	return value
}

type hostRenderer struct {
	ctx              context.Context
	host             sdk.Host
	secretsReachable bool
}

func (renderer *hostRenderer) Validate(value string) error { return renderer.ValidateContent(value) }

func (renderer *hostRenderer) Render(value string) (string, error) {
	return renderer.RenderContent(value)
}

func (renderer *hostRenderer) ValidateContent(value string) error {
	return renderer.host.ValidateTemplate(renderer.ctx, value)
}

func (renderer *hostRenderer) RenderContent(value string) (string, error) {
	return renderer.RenderContentWith(value, nil)
}

func (renderer *hostRenderer) RenderWith(value string, extra map[string]any) (string, error) {
	return renderer.RenderContentWith(value, extra)
}

func (renderer *hostRenderer) RenderContentWith(value string, extra map[string]any) (string, error) {
	return renderer.host.RenderTemplate(renderer.ctx, value, extra)
}

func (renderer *hostRenderer) Snapshot() step.DataTemplateRenderer { return renderer }

// WithoutSecrets returns the renderer unchanged when the host already denies secret, which it
// does for steps that did not declare host.function.call. Otherwise host rendering would reach
// secret and the protocol has no secret-free variant, so rendering fails closed.
func (renderer *hostRenderer) WithoutSecrets() step.DataTemplateRenderer {
	if !renderer.secretsReachable {
		return renderer
	}
	return secretFreeRenderer{hostRenderer: renderer}
}

var errSecretFreeRendering = errors.New("secret-free template rendering is unavailable to plugin steps that declare host.function.call")

// secretFreeRenderer validates through the host but refuses to render, because rendering would
// run with the host's secret helper enabled.
type secretFreeRenderer struct {
	*hostRenderer
}

func (secretFreeRenderer) Render(string) (string, error) { return "", errSecretFreeRendering }

func (secretFreeRenderer) RenderContent(string) (string, error) { return "", errSecretFreeRendering }

func (secretFreeRenderer) RenderWith(string, map[string]any) (string, error) {
	return "", errSecretFreeRendering
}

func (secretFreeRenderer) RenderContentWith(string, map[string]any) (string, error) {
	return "", errSecretFreeRendering
}

func (renderer secretFreeRenderer) Snapshot() step.DataTemplateRenderer { return renderer }

func (renderer secretFreeRenderer) WithoutSecrets() step.DataTemplateRenderer { return renderer }
