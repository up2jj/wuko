# Plugin protocol v3 and the Go SDK

[Protocol v4](plugin-protocol-v4.md) extends this contract with execution-environment capabilities.

`wuko.plugin/v3` keeps all v2 framing, lifecycle, service, callback, step, executor, and helper
behavior and adds immutable static composite actions. New Go plugins should use
`github.com/up2jj/wuko/plugin/sdk`; the SDK owns the JSONL transport and uses only the Go standard
library.

## Contents

- [Registering an action](#registering-an-action)
- [Error codes](#error-codes)
- [Step handler requests](#step-handler-requests)
- [Adapting native managed-service steps](#adapting-native-managed-service-steps)
- [Testing Go plugins](#testing-go-plugins)
- [Wire additions](#wire-additions)

## Registering an action

```go
plugin, err := sdk.New("acme")
if err != nil {
	return err
}

err = plugin.RegisterAction("build", sdk.Action{
	Inputs: map[string]sdk.ActionInput{
		"target": {Type: sdk.StringInput, Required: true},
	},
	Outputs: map[string]sdk.ActionOutput{
		"artifact": {Value: "steps.package.stdout"},
	},
	Steps: []sdk.ActionStep{{
		ID:   "package",
		Type: "shell",
		With: map[string]any{
			"command": "printf",
			"args": []any{"dist/app-%s.tar.gz", "{{ .inputs.target }}"},
			"stdout": "capture",
		},
	}},
})
```

`ActionStep` includes ordinary steps and every recursive control accepted by composite actions.
It deliberately has no `uses`, `require`, or `sha256` field, and action templates are inline
strings, so a plugin action cannot reference another action or companion file. Values in `with`
and input defaults may contain arbitrary JSON-compatible nested objects and arrays.

Registration validates the public name and basic action shape, supplies schema `version: 1`, and
stores canonical JSON. Mutating the supplied maps or slices after registration has no effect.
Calling `Serve` freezes action, step, executor, helper, and lifecycle registration.

Workflows invoke the action by its initialized namespace and registered name:

```yaml
- id: build
  uses: plugin:acme/build
  with:
    target: linux
```

Wuko retrieves and validates every advertised action during plugin initialization, before
`plugin.start`, caches it for the plugin process and workflow load, and executes it through the
normal composite-action engine. It may call
built-in steps or steps registered by the same plugin. A plugin action has borrowed-directory
semantics: it has no package directory and cannot read files beside the executable.

## Error codes

A handler may attach a machine-readable code to any error with `sdk.Coded(code, err)`. When the
returned error carries no explicit code, the SDK derives one from the error chain: an error
wrapping `context.DeadlineExceeded` is reported as `timed_out` and one wrapping `context.Canceled`
as `canceled`, so a handler that simply returns `ctx.Err()` after a `cancel` notification needs no
boilerplate. The code is diagnostic: Wuko shows the `message` and does not change step
classification based on it.

## Step handler requests

The Go SDK gives each step lifecycle phase its own request type. Validation handlers receive
`sdk.ValidateRequest`, run handlers receive `sdk.RunRequest`, and service-policy handlers receive
`sdk.ServiceRequest`. Cleanup continues to use `sdk.CleanupRequest`. A service-policy handler has
no host client or output streams because Wuko does not enable callbacks or execution events during
`step.service`.

`sdk.RunRequest.Stdout` and `sdk.RunRequest.Stderr` are always non-nil when the SDK invokes the
handler. `sdk.RunRequest.Ready` reports service startup; calling it for a step without a `Service`
handler returns an error instead of invoking a nil function.

Every dynamic JSON value the SDK hands a handler decodes numbers as `json.Number`, never
`float64`: `with` fields typed `any`, `map[string]any`, or `[]any`, `sdk.StepContext.Vars`,
`PresetVars`, `Inputs`, `Steps`, `Dependencies`, `Bindings`, `Providers`, `sdk.Result.Outputs` and
`Variables`, `plugin.start` configuration, helper arguments, and `Host.CallFunction` results. A
`float64` cannot hold an integer past 2^53, so `with: {id: 9007199254740993}` would otherwise
reach the plugin rounded. Fields with a concrete Go type keep decoding into that type, so declare
`int64` or `float64` where the shape is known and type-switch on `json.Number` only where it is
not. Plugins written against an earlier SDK that assert `value.(float64)` on these values must
switch to `json.Number` and its `Int64`, `Float64`, or `String` methods.

The `Host` methods are also checked against the step's `sdk.WithHostCallbacks` declaration before
the SDK writes a callback frame. An undeclared callback therefore fails locally as well as at the
Wuko boundary. `Host.Secret` requires `sdk.HostFunctionCall`, because secrets use
`host.function.call` on the wire.

## Adapting native managed-service steps

`plugin/sdk` uses only the Go standard library and is the portable API for executable plugins.
Plugins that deliberately reuse Wuko-native `step.Runner` implementations can additionally import
`github.com/up2jj/wuko/plugin/stepadapter`. That package depends on Wuko's step contracts and owns
the conversion between SDK requests and `step.Request`, including host callbacks, readiness,
service shutdown, error classification, and optional cleanup.

```go
plugin, err := sdk.New("acme")
if err != nil {
	return err
}

err = stepadapter.RegisterService(plugin, "server", stepadapter.ServiceRegistration{
	Builder: server.New,
	Cleanup: true,
}, sdk.WithHostCallbacks(
	sdk.HostTemplateValidate,
	sdk.HostTemplateRender,
	sdk.HostFunctionCall,
))
```

The builder has the ordinary `step.Builder` signature. Its runner must implement
`stepadapter.ServiceRunner` by adding a service-policy method to `step.Runner`:

```go
func (runner *Runner) ServiceOptions() (string, step.ServiceOptions) {
	return "server", step.ServiceOptions{KeepAlive: runner.keepAlive, FailFast: true}
}
```

The runner continues to call `request.Services.StartService` exactly as it does in process. The
adapter starts that work immediately, reports the runner's returned result as the plugin readiness
event, keeps the protocol request open for the service lifetime, and stops the service when the
owning request is canceled. Set `Cleanup` only when every runner built by the registration
implements `step.Cleaner`; the adapter then advertises and delegates `step.cleanup`. Existing SDK
options such as `sdk.WithOutputSchema` pass through unchanged.

Host template rendering can reach `secret` only when the step declares `sdk.HostFunctionCall`. In
that case the adapted renderer's `WithoutSecrets()` variant validates templates but refuses to
render them, because the protocol has no secret-free rendering callback; steps that render
less-trusted content should not declare `sdk.HostFunctionCall`.

## Testing Go plugins

The `github.com/up2jj/wuko/plugin/sdk/sdktest` package runs an `sdk.Plugin` through the real JSONL
transport while keeping the test in process. It performs initialization and clean shutdown,
generates request IDs, routes concurrent events, and answers declared host callbacks through typed
fakes:

```go
host := sdktest.Start(t, plugin, sdktest.WithCallbacks(sdktest.Callbacks{
	CallFunction: func(_ context.Context, name string, args []any) (any, error) {
		return fakeFunction(name, args)
	},
}))

call := host.CallStep(sdktest.StepRequest{
	Type:    "acme.server",
	With:    map[string]any{"address": "127.0.0.1:0"},
	Context: sdk.StepContext{StepID: "server"},
})
ready := call.AwaitEvent("ready")
var result sdk.Result
if err := ready.DecodeResult(&result); err != nil {
	t.Fatal(err)
}

call.Cancel()
if response := call.Await(); response.Error != nil {
	t.Fatal(response.Error)
}
```

`StepRequest.Operation` selects the method: it defaults to `StepRun` and also accepts `StepValidate`,
`StepCleanup`, and `StepService`. Awaiting one named event does not discard other events from that
call, so tests may wait for readiness before inspecting earlier stdout or stderr. `Start` registers
`Harness.Close` with the test; calling `Close` explicitly is only necessary when the test needs to
assert shutdown behavior before returning.

## Wire additions

Initialization adds a sorted list of local action names:

```json
{"protocol":"wuko.plugin/v3","namespace":"acme","steps":[],"executors":[],"actions":["build"]}
```

The host retrieves one advertised action with `action.get`:

```json
{"id":"2","method":"action.get","params":{"name":"build"}}
{"id":"2","result":{"action":{"version":1,"name":"build","steps":[]}}}
```

The returned `action` value is a JSON object, not encoded YAML or a byte array. Retrieval must be
pure and must not depend on `plugin.start`. Wuko retrieves every advertised action during release
installation, so malformed actions prevent installation.

Protocols v1 and v2 remain supported unchanged. A v1 or v2 plugin must not advertise `actions`.
