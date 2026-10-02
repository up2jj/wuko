# Plugin protocol v3 and the Go SDK

`wuko.plugin/v3` keeps all v2 framing, lifecycle, service, callback, step, executor, and helper
behavior and adds immutable static composite actions. New Go plugins should use
`github.com/up2jj/wuko/plugin/sdk`; the SDK owns the JSONL transport and uses only the Go standard
library.

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
