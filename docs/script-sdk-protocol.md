# Script SDK bridge protocol v1

This protocol is independent of Node.js and has its own version. A future Python client can use
the same descriptor, endpoint, and methods without changing shell result semantics or the host.
Only the language bootstrap and client convenience API are language-specific.

## Discovery and transport

The language bootstrap reads the file named by `WUKO_SDK_DESCRIPTOR`. The document contains:

```json
{
  "version": 1,
  "socket": "/tmp/wuko-sdk-123/bridge.sock",
  "token": "opaque-per-attempt-token",
  "context": {"args": {}, "vars": {}, "inputs": {}, "steps": {}}
}
```

`context` contains all snapshot properties documented in the [SDK API](script-sdk.md).
Adapters may read it synchronously before executing user code, or request it through `context`.
The descriptor is private to the attempt and must not be logged. Its directory has mode 0700;
the descriptor, adapter files, and Unix socket have mode 0600.

Connect to `socket` and issue HTTP `POST /v1/call` with `Authorization: Bearer <token>` and a
UTF-8 JSON body. Do not use process stdin, stdout, or stderr for protocol traffic.
Each HTTP request carries one call; HTTP handles request/response correlation. Calls can run
concurrently. There is no persistent connection requirement.

```json
{"version":1,"method":"output","params":{"name":"artifact","value":{"path":"dist/app"}}}
```

Successful responses have version 1 and an optional JSON `result` (omitted means null):

```json
{"version":1}
```

Operation errors return HTTP 200 with a structured error instead of a result:

```json
{"version":1,"error":{"code":"host_error","message":"inline step execution is unavailable"}}
```

Codes are `invalid_request`, `host_error`, `canceled`, and `invalid_result`. Authentication
failure returns HTTP 401; an unsupported route or HTTP method returns 404; a closing bridge may
return 503. Connection errors and closure are transport failures, never successful empty results.
Clients must reject unsupported protocol versions. Unknown envelope or parameter fields are
rejected. Request and response bodies are limited to 10 MiB; each bridge runs 64 calls
concurrently and queues the rest until a slot frees or the bridge closes.

## Methods

| Method | Parameters | Result |
| --- | --- | --- |
| `context` | `{}` | Context snapshot, without credentials |
| `output` | `{"name": string, "value": JSON}` | null |
| `set_var` | `{"name": string, "value": JSON}` | null |
| `helper` | `{"name": string, "args": JSON[]}` | Helper value |
| `secret` | `{"reference": string}` | Secret string |
| `render` | `{"template": string, "extra": object}` | Rendered string |
| `step` | `{"type": string, "config": object}` | Step output object |
| `run_workflow` | `{"name": string, "target": string, "vars": object}` | Workflow output object |

Output and variable names must be non-empty. Null values are valid and differ from a missing
`value` parameter. Numbers are decoded without float64 rounding; host runtime values support
signed and unsigned 64-bit integers and finite floating-point numbers. Clients must enforce their
language's narrower limits where necessary, using strings for integers outside those limits.

Writes are staged for the owning shell attempt. Child steps and workflows execute against isolated
state, return only outputs, and complete their lifecycle before responding. The host cancels calls
when their HTTP request disconnects or the attempt ends. Adapters must await responses before
exiting. The bridge is removed after process completion, failure, or cancellation; clients must
never cache a descriptor for another attempt or workflow run.
