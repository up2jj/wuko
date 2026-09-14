# Plugin protocol v2

`wuko.plugin/v2` extends [protocol v1](plugin-protocol.md) with two facilities for advanced step
plugins:

- a step can register long-running work with Wuko's managed-service supervisor; and
- an active step request can call a small, declared set of host functions for template validation,
  template rendering, secrets, and workflow-scoped plugin helpers.

All v1 framing, lifecycle, step-result, cleanup, stream-event, cancellation, and 10 MiB frame-limit
rules still apply unless this document changes them. Executors and top-level plugin helpers are
otherwise unchanged.

## Selecting v2

A v2 release declares the protocol in `plugin.json`:

```json
{
  "version": 1,
  "namespace": "http",
  "plugin_version": "0.1.0",
  "protocol": "wuko.plugin/v2",
  "artifacts": []
}
```

The abbreviated `artifacts` value above is illustrative; a publishable manifest must contain the
normal platform entries. Wuko sends the selected protocol during initialization:

```json
{"id":"1","method":"initialize","params":{"protocol":"wuko.plugin/v2","host_version":"0.14.0"}}
```

An installed release pins its protocol through its manifest and installation marker, and the
plugin must return that same protocol. A marker without `protocol` means v1, which is also how a
v1 installation is recorded so that an older Wuko can still read it.

A bare development or `PATH` executable carries no marker, so Wuko negotiates: it offers v2 and
accepts v1 if the plugin answers with it instead. Answering with the protocol you implement costs
one handshake; answering with something Wuko did not offer, or with the wrong namespace, fails the
handshake. Wuko then retries the remaining protocol in a fresh process.

`wuko marketplace plugin init` continues to generate v1 projects. Switching that scaffold to v2
requires a bidirectional reader loop: a service cannot receive cancellation, or receive responses
to its own host calls, if its `step.run` handler blocks the protocol reader.

## Declaring capabilities

A v2 step opts into service behavior and lists every host callback it may invoke:

```json
{
  "protocol": "wuko.plugin/v2",
  "namespace": "http",
  "steps": [
    {
      "type": "http.mock_server",
      "cleanup": true,
      "service": true,
      "host_callbacks": [
        "host.template.validate",
        "host.template.render",
        "host.function.call"
      ]
    }
  ],
  "executors": [],
  "helpers": []
}
```

The only supported callback names are the three shown above. Duplicate or unknown capabilities
make initialization fail. A v1 plugin cannot declare `service` or `host_callbacks`.

## Extended step context

Only v2 `step.validate`, `step.service`, and `step.run` requests receive the extended context. The
v1 context is unchanged.

| Field | Type | Meaning |
| --- | --- | --- |
| `workflow_source` | string | Stable logical source of the workflow or action. |
| `workflow_dir_borrowed` | boolean | The directory belongs to the caller and must not be treated as the plugin package tree. |
| `workflow_timezone` | string | Workflow timezone name. |
| `environment_loaders` | string array | Environment loaders active for the run. |
| `local_value_dir` | string | Workflow-local value storage directory. |
| `global_value_dir` | string | Global value storage directory. |
| `preset_vars` | object | Variables fixed before execution, excluding values written by steps. |
| `bindings` | object | Active control roots such as `foreach`, `matrix`, `error`, and `finally`. |
| `providers` | object | Active provider values keyed by provider name. |
| `helpers` | string array | Exposed workflow-scoped helper names accepted by `host.function.call`. |
| `previous_attempt` | step result or absent | Complete result from the previous failed attempt. |

The ordinary v1 fields (`step_id`, workflow name and directory, run directory, variables, inputs,
environment, step outputs, dependencies, retry counters, and operation ID) remain present.

## Managed services

For a step declared with `"service": true`, Wuko asks for its lifecycle policy before running it:

```json
{"id":"8","method":"step.service","params":{"type":"http.mock_server","with":{"listen":"127.0.0.1:0"},"context":{}}}
```

The result is:

```json
{"id":"8","result":{"kind":"mock_server","keep_alive":false,"fail_fast":true,"exit_on_end":false}}
```

`kind` is the diagnostic label shown for background work. The boolean fields have the same meaning
as native Wuko service options:

- `keep_alive` keeps the owning scope open after foreground work completes;
- `fail_fast` cancels foreground and sibling work if the running service fails; and
- `exit_on_end` ends the scope successfully if the service ends successfully on its own.

Wuko registers the service with its supervisor before waiting for readiness. The subsequent
`step.run` remains active for the entire lifetime of the service. Once startup is committed, the
plugin sends exactly one `ready` event containing the normal step result:

```json
{"id":"9","event":"ready","result":{"outputs":{"ready":true,"url":"http://127.0.0.1:43125"},"variables":{}}}
```

Wuko returns that result from the owning foreground step while request `9` stays in flight. The
plugin may continue sending `stdout` and `stderr` events. When the service eventually ends, it sends
the ordinary final response:

```json
{"id":"9","result":{"outputs":{"ready":true,"url":"http://127.0.0.1:43125"},"variables":{}}}
```

An error or final response before readiness fails the owning step. Wuko marks the registered job as
an aborted startup so the same failure is not also reported as a background failure. An error after
readiness belongs to the background service and follows `fail_fast` policy.

When the owning scope ends, Wuko sends `cancel` but keeps draining the request for a bounded period:

```json
{"method":"cancel","params":{"id":"9"}}
{"id":"9","error":{"code":"verification_failed","message":"mock server verification failed: expected request was not received"}}
```

This lets a plugin gracefully stop listeners, drain traffic, verify expectations, and return a
more useful shutdown error. Cleanup still runs later through `step.cleanup`, in the same ordering
as cleanup for a native service step.

## Plugin-to-host requests

While `step.validate` or `step.run` is active, the plugin may write a request to Wuko. The request
uses the normal envelope and includes the active host request ID as `parent_id` in `params`:

```json
{"id":"plugin-17","method":"host.template.validate","params":{"parent_id":"9","content":"Hello {{ .vars.name }}"}}
```

Plugin-generated IDs and Wuko-generated IDs share the connection, so use a distinct prefix. Wuko
returns a normal response with the plugin-generated ID:

```json
{"id":"plugin-17","result":{}}
```

Wuko rejects an unknown or completed parent, a callback the step did not declare, malformed
parameters, and oversized frames. Callback handlers run independently of the protocol reader and
all writes in both directions must be serialized. A plugin may issue concurrent callbacks and may
receive unrelated host requests before their responses arrive.

Concurrency is bounded. Wuko runs at most 64 callbacks at a time per plugin process, and caps the
request bytes it holds for them. A callback beyond that is refused immediately rather than queued:

```json
{"id":"plugin-22","error":{"code":"too_many_callbacks","message":"host callback limit reached, retry once an earlier callback completes"}}
```

The refusal is retryable and applies to that callback only; the parent request is unaffected. Wait
for an outstanding callback to complete before reissuing. A plugin that keeps issuing callbacks
while ignoring refusals is treated as a protocol failure and its connection is torn down.

### `host.template.validate`

Validates external template content against the workflow's named templates and function set:

```json
{"id":"plugin-18","method":"host.template.validate","params":{"parent_id":"4","content":"{{ template \"json_error\" . }}"}}
{"id":"plugin-18","result":{}}
```

Use this during `step.validate`. Template errors are returned in the response's `error` object.

### `host.template.render`

Renders external content with the parent request's workflow snapshot. `extra` overlays
request-local roots without changing the frozen workflow data:

```json
{"id":"plugin-19","method":"host.template.render","params":{"parent_id":"9","content":"{{ .request.method }} {{ .vars.target }}","extra":{"request":{"method":"POST"}}}}
{"id":"plugin-19","result":{"value":"POST payments"}}
```

For a service, Wuko snapshots the renderer and workflow roots before startup. Changes made by later
steps therefore cannot race with or alter responses served by an already-running plugin service.
Named templates, providers, bindings, and declared plugin helpers remain available through that
snapshot.

`secret` does not. Rendering executes the workflow's whole function map, so a step that declared
only `host.template.render` could otherwise read every secret the run can resolve by submitting
`{{ secret "op://..." }}` as content -- including through a named template it did not write.
Rendering for such a step runs with `secret` denied, and the render fails with `secret is
unavailable in this template context`. A step that also declares `host.function.call` can resolve
secrets directly, so it keeps `secret` in templates too. Declare `host.function.call` when your
step needs secrets, and leave it off when it does not: the declaration is what the workflow author
inspects.

### `host.function.call`

Calls `secret` or one of the names advertised in the context's `helpers` array:

```json
{"id":"plugin-20","method":"host.function.call","params":{"parent_id":"9","name":"secret","args":["env://API_TOKEN"]}}
{"id":"plugin-20","result":{"value":"resolved-value"}}
```

```json
{"id":"plugin-21","method":"host.function.call","params":{"parent_id":"9","name":"tools_slug","args":["Hello World"]}}
{"id":"plugin-21","result":{"value":"hello-world"}}
```

Arguments and results must be JSON-compatible. Secrets and helper results are scoped to the parent
workflow request and must not be cached across unrelated requests.

## Complete service sequence

The essential ordering is:

```text
Wuko -> plugin  initialize(v2)
plugin -> Wuko initialize result with service/callback declarations
Wuko -> plugin  step.service
plugin -> Wuko service policy
Wuko             registers background job
Wuko -> plugin  step.run
plugin -> Wuko host.template.render(parent_id = step.run id)   [optional, repeatable]
Wuko -> plugin  callback result
plugin -> Wuko ready event with step result                    [exactly once]
Wuko             completes the foreground step
plugin -> Wuko stdout/stderr events and host callbacks         [while running]
Wuko -> plugin  cancel(id = step.run id)                       [when scope ends]
plugin            drains requests, verifies state, closes resources
plugin -> Wuko final result or verification error
Wuko -> plugin  step.cleanup                                   [for successful startup]
```

The plugin's input loop must remain live from `step.run` through the final response. Dispatch the
service body separately, route host responses to their waiting callbacks, honor `cancel`, and use a
single serialized writer for responses, events, and plugin-to-host requests.
