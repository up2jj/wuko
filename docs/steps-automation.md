# Automation steps

[Back to the available steps](../README.md#available-steps)

Automation steps run local programs, coding agents, in-process Lua, or cancellation-aware waits.
Use `timeout` and `retry` on ordinary steps when an operation can hang or fail transiently.

## `process`

Run one long-lived program as a readiness-gated service. The step succeeds when the program is
ready; the program then remains managed by its workflow scope. Wuko prefixes live lines with the
step `label`, stops non-keepalive services when foreground work ends, joins them before `finally`,
and reports an unexpected terminal failure at that join. This follows the useful headless lifecycle
parts of Process Compose while keeping dependencies and pools in Wuko's existing controls.

Exactly one of `command`, `script`, or typed `argv.expr` selects the program. `args`, `shell`,
`working_directory`, `env`, and `user` behave like their `shell` counterparts. `stdout` and
`stderr` accept `inherit` (the default) or `discard`; unbounded service output is not retained in
step state. Discarding both streams is rejected together with log readiness, which reads them. The immutable startup result contains `ready`, `label`, `detached`, and `readiness`; an RPC process also returns `worker_id`.

### 1. Ready on successful spawn

Without a readiness probe, successful spawn is readiness. The following test step starts only after
`api` has spawned, and the service is stopped after the test finishes:

```yaml
version: 1
name: process-spawn
steps:
  - id: api
    type: process
    with: {command: ./bin/api}
  - id: test
    type: shell
    with: {command: curl, args: [-f, http://127.0.0.1:8080/]}
```

### Position and lifetime

Position controls when a service starts; its owning workflow or executor scope controls when it
stops. Services stop before workflow `finally`, while their committed startup outputs remain
available there.

### 2. At the beginning

The service starts first, reaches readiness, and remains active during all later foreground steps:

```yaml
version: 1
name: process-first
steps:
  - id: api
    type: process
    with:
      command: ./bin/api
      readiness: {http: {url: http://127.0.0.1:8080/ready, period: 500ms}}
  - id: integration
    type: shell
    with: {command: go, args: [test, ./integration/...]}
```

### 3. In the middle

Earlier steps finish before the service starts; later steps run while it is active:

```yaml
version: 1
name: process-middle
steps:
  - id: build
    type: shell
    with: {command: go, args: [build, -o, bin/api, ./cmd/api]}
  - id: api
    type: process
    with:
      command: ./bin/api
      readiness: {log: {pattern: "listening on :8080", timeout: 20s}}
  - id: smoke
    type: shell
    with: {command: curl, args: [-f, http://127.0.0.1:8080/]}
```

### 4. At the end: startup smoke test

An ending process is started, checked for readiness, and immediately stopped because no foreground
work follows it:

```yaml
version: 1
name: startup-smoke
steps:
  - id: api_starts
    type: process
    with:
      command: ./bin/api
      readiness: {log: {pattern: ready, timeout: 10s}}
```

### 5. At the end: development server

`keep_alive: true` keeps the scope open after foreground work. This workflow runs until the server
exits or the user interrupts Wuko:

```yaml
version: 1
name: dev-server
steps:
  - id: web
    type: process
    with:
      command: npm
      args: [run, dev]
      keep_alive: true
      readiness: {log: {pattern: "Local:.*http", timeout: 30s}}
```

### Readiness and health

Probe durations use Go duration strings. Exec and HTTP probes default to `initial_delay: 0s`,
`period: 10s`, `timeout: 1s`, `success_threshold: 1`, and `failure_threshold: 3`. Log readiness
defaults to a 30-second timeout and matches a rolling, bounded 1 MiB window across stdout and
stderr, so at least one of those streams must be `inherit`.

### 6. Log readiness

```yaml
version: 1
name: log-ready
steps:
  - id: web
    type: process
    with:
      script: exec ./bin/web --port "$1"
      args: [8080]
      label: web
      readiness:
        log: {pattern: "server ready on .*8080", timeout: 30s}
```

### 7. Exec readiness

```yaml
version: 1
name: exec-ready
steps:
  - id: database
    type: process
    with:
      command: postgres
      args: [-D, .data/postgres]
      readiness:
        exec:
          command: pg_isready
          args: [-h, 127.0.0.1]
          period: 1s
          timeout: 500ms
          failure_threshold: 30
```

### 8. HTTP readiness

```yaml
version: 1
name: http-ready
steps:
  - id: api
    type: process
    with:
      argv: {expr: '["./bin/api", "--port", vars.port]'}
      readiness:
        http:
          url: "http://127.0.0.1:{{ .vars.port }}/ready"
          method: GET
          expected_status: [200, 204]
          period: 500ms
          timeout: 250ms
vars: {port: 8080}
```

### 9. Liveness and restart

Liveness begins after readiness. A failed liveness threshold terminates the current program and
enters the same restart policy as an unexpected exit. `max_restarts: 0` means unlimited.

```yaml
version: 1
name: resilient-worker
steps:
  - id: worker
    type: process
    with:
      command: ./bin/worker
      readiness: {log: {pattern: "waiting for jobs"}}
      liveness:
        exec: {command: ./bin/worker-health, period: 5s, timeout: 1s, failure_threshold: 3}
      restart: {policy: on_failure, backoff: 2s, max_restarts: 5}
```

Restart policies are `never` (default), `on_failure`, and `always`. `allowed_exit_codes` defaults
to `[0]`. Restart eligibility is evaluated before `exit_on_end` or `exit_on_failure`. Lifecycle
shutdown never triggers a restart or exit policy, and neither does a service that fails before it
becomes ready: that failure is the step's own result. A liveness failure stops the service the same
way the lifecycle does, so `shutdown.command` runs before a restart replaces the instance. An
executor that cannot stop a process by cancellation rejects a restart policy that has no
`shutdown.command`, because each restart would otherwise leave the previous instance running.

### Shutdown

Local processes receive `SIGTERM` as a process group, wait up to `shutdown.timeout` (10 seconds by
default), then escalate to `SIGKILL`. Supported configured signals are `SIGTERM`, `SIGINT`,
`SIGHUP`, and `SIGQUIT`. `parent_only` avoids signaling descendants.

Docker-hosted services follow the same rules from inside the container. The Docker API cannot
signal an exec, so a managed service is launched through the session's init shell, which records
the service PID and then `exec`s it, leaving its argv unchanged. Stopping runs a second exec that
signals that PID. This needs a shell as the executor's `init.command`, which is the default; a
session configured with a non-shell init can only stop its services with `shutdown.command`.

### 10. Signal shutdown

```yaml
version: 1
name: signal-shutdown
steps:
  - id: daemon
    type: process
    with:
      command: ./bin/daemon
      readiness: {log: {pattern: ready}}
      shutdown: {signal: SIGINT, timeout: 15s, parent_only: false}
  - id: exercise
    type: shell
    with: {command: ./bin/client, args: [check]}
```

### 11. Shutdown command and detached launcher

A detached launcher must declare a shutdown command. Wuko treats launcher exit as expected, keeps
probing the external daemon, and runs the command while the executor session is still open:

```yaml
version: 1
name: detached-daemon
steps:
  - id: daemon
    type: process
    with:
      command: ./bin/daemonctl
      args: [start, --detach]
      detached: true
      readiness: {exec: {command: ./bin/daemonctl, args: [status], period: 1s}}
      shutdown:
        timeout: 20s
        command: {command: ./bin/daemonctl, args: [stop]}
  - id: use_daemon
    type: shell
    with: {command: ./bin/client}
```

### Dependencies with Wuko constructs

Yes—dependencies stay in Wuko. Sequential order is the simplest ready dependency. For a DAG, put
the steps directly under one `concurrent` group and use `needs`. A process satisfies its edge when
it becomes ready, not when it exits. Use `shell` for a finite prerequisite whose exit must be
awaited. Cross-iteration dependencies are intentionally unsupported.

### 12. Sequential database → API → tests

```yaml
version: 1
name: sequential-stack
steps:
  - id: database
    type: process
    with: {command: ./bin/database, readiness: {log: {pattern: ready}}}
  - id: api
    type: process
    with: {command: ./bin/api, readiness: {http: {url: http://127.0.0.1:8080/ready}}}
  - id: tests
    type: shell
    with: {command: go, args: [test, ./integration/...]}
```

### 13. Concurrent database/cache → API → tests

```yaml
version: 1
name: dependency-stack
steps:
  - concurrent:
      max_concurrency: 3
      steps:
        - id: database
          type: process
          with: {command: ./bin/database, readiness: {log: {pattern: ready}}}
        - id: cache
          type: process
          with: {command: ./bin/cache, readiness: {exec: {command: ./bin/cache-cli, args: [ping]}}}
        - id: api
          type: process
          needs: [database, cache]
          with: {command: ./bin/api, readiness: {http: {url: http://127.0.0.1:8080/ready}}}
        - id: tests
          type: shell
          needs: [api]
          with: {command: go, args: [test, ./integration/...]}
```

### Replicas and pools

One `process` step owns one program. Express replicas with `foreach` over explicit runtime objects
or IDs, and multiple dimensions with `matrix`. The fan-out parent becomes ready only after every
iteration is ready. `max_concurrency` controls startup/readiness concurrency, not final pool size;
started replicas remain active together until their owning scope ends. There is no numeric
`replicas` shorthand or live autoscaling in version 1.

### 14. HTTP replica pool

```yaml
version: 1
name: http-pool
vars: {ports: [8101, 8102, 8103]}
steps:
  - id: replicas
    foreach:
      items: vars.ports
      max_concurrency: 3
      steps:
        - id: api
          type: process
          with:
            command: ./bin/api
            args: ["--port={{ .foreach.item }}"]
            label: "api-{{ .foreach.index }}"
            readiness: {http: {url: "http://127.0.0.1:{{ .foreach.item }}/ready", period: 500ms}}
  - id: tests
    type: shell
    with: {command: ./bin/test-pool}
```

### 15. Shared-queue worker pool

```yaml
version: 1
name: worker-pool
vars: {workers: [worker-a, worker-b, worker-c, worker-d]}
steps:
  - id: pool
    foreach:
      items: vars.workers
      max_concurrency: 2
      steps:
        - id: worker
          type: process
          with:
            command: ./bin/worker
            args: [--queue, shared]
            label: "{{ .foreach.item }}"
            readiness: {log: {pattern: "waiting for jobs"}}
  - id: enqueue
    type: shell
    with: {command: ./bin/enqueue-fixtures}
```

All four workers remain active; `max_concurrency: 2` only starts and readies at most two iterations
at once.

### Calling a process

`rpc: jsonl` turns a managed process into a request/response worker. The worker reads one JSON
request per line from stdin and writes one correlated response per line to stdout. Stdout is
reserved for the protocol and is not printed; application logs belong on stderr. RPC cannot be
combined with `detached` or `stdout: discard`. With log readiness, the readiness message must
therefore be written to stderr.

Requests have this form:

```json
{"id":"call_opaque-id","payload":{"component":"Hello","props":{"name":"Wuko"}}}
```

A worker returns exactly one of:

```json
{"id":"call_opaque-id","result":{"html":"<p>Hello Wuko</p>"}}
{"id":"call_opaque-id","error":{"message":"component was not found"}}
```

Each line is limited to 10 MiB. IDs must be returned unchanged. Malformed messages and unknown
IDs are protocol failures and enter the process restart policy. A restarted instance keeps its
`worker_id` but must pass its readiness probe again before it can receive calls, so calls made
while it restarts wait within their own `timeout`. Calls are never replayed after a request has
been written: one that was routed to a worker in the instant it exited fails with `process RPC
session closed` instead of running twice.

Use `worker` when addressing one process:

```yaml
version: 1
name: render-one
steps:
  - id: renderer
    type: process
    with:
      command: node
      args: [renderer-worker.js]
      rpc: jsonl
      restart: {policy: on_failure, max_restarts: 3}
  - id: render
    type: process_call
    with:
      worker: "{{ .steps.renderer.worker_id }}"
      payload:
        component: ./components/Hello.js
        props: {name: Wuko}
      timeout: 10s
```

`process_call` returns `result` and the selected `worker_id`. `payload` accepts any
YAML/JSON-compatible value; use `payload_expr` instead when the whole payload must come from a
typed expression.

For a pool, collect worker IDs from `foreach` and pass the resulting list as the direct `pool`
expression:

```yaml
version: 1
name: render-pool
vars:
  worker_slots: [0, 1, 2]
  render_request:
    component: ./components/Hello.js
    props: {name: Wuko}
steps:
  - id: renderers
    foreach:
      items: vars.worker_slots
      max_concurrency: 3
      collect: steps.renderer.worker_id
      steps:
        - id: renderer
          type: process
          with:
            command: node
            args: [renderer-worker.js]
            label: "renderer-{{ .foreach.index }}"
            rpc: jsonl
  - id: render
    type: process_call
    with:
      pool: steps.renderers.results
      payload_expr: vars.render_request
```

A pool call fairly selects an available worker and permits one in-flight request per worker. Each
pool rotates independently of every other pool and of single-worker calls. If all workers are busy
or restarting, it waits within `timeout` (30 seconds by default). A worker whose service has ended
leaves the pool, and the remaining workers keep serving calls; the call fails immediately only
when no worker in the pool is left. A timed-out
worker remains reserved until the matching late response arrives or the process restarts, so a
late response cannot satisfy a later call. The opaque `worker_id` remains stable across restarts
of that process step during the run.

### 16. Matrix pool

```yaml
version: 1
name: regional-pool
steps:
  - id: workers
    matrix:
      axes:
        region: [eu, us]
        queue: [critical, default]
      max_concurrency: 4
      steps:
        - id: worker
          type: process
          with:
            command: ./bin/worker
            args: ["--region={{ .matrix.region }}", "--queue={{ .matrix.queue }}"]
            label: "{{ .matrix.region }}-{{ .matrix.queue }}"
            readiness: {log: {pattern: ready}}
  - id: verify
    type: shell
    with: {command: ./bin/check-workers}
```

### 17. Per-replica sidecar → worker

Sequential children inside each iteration express a dependency local to that replica:

```yaml
version: 1
name: sidecar-workers
vars: {workers: [one, two, three]}
steps:
  - id: pool
    foreach:
      items: vars.workers
      max_concurrency: 3
      steps:
        - id: sidecar
          type: process
          with: {command: ./bin/sidecar, args: ["{{ .foreach.item }}"], readiness: {log: {pattern: ready}}}
        - id: worker
          type: process
          with: {command: ./bin/worker, args: ["{{ .foreach.item }}"], readiness: {log: {pattern: ready}}}
  - id: verify
    type: shell
    with: {command: ./bin/check-pool}
```

### 18. Executor-scoped pool

Services stop and join before their executor session closes. Fan-out inside an executor retains the
existing `max_concurrency: 1` restriction: replicas start serially but remain active together.
Docker-hosted services are supervised through the session's init shell, so `shutdown.signal`,
`shutdown.timeout`, and `parent_only` behave as they do locally. A shutdown command remains useful
when a service needs an orderly drain rather than a signal.

```yaml
version: 1
name: executor-workers
vars: {workers: [one, two]}
steps:
  - executor:
      type: docker
      with: {image: alpine:3.22}
    steps:
      - id: pool
        foreach:
          items: vars.workers
          max_concurrency: 1
          steps:
            - id: worker
              type: process
              with:
                command: /app/worker
                args: ["{{ .foreach.item }}"]
                readiness: {log: {pattern: ready}}
                shutdown:
                  command: {command: /app/workerctl, args: [stop, "{{ .foreach.item }}"]}
      - id: verify
        type: shell
        with: {command: /app/check-workers}
```

### Failure behavior

By default, an unexpected exhausted process failure is recorded and reported when services join;
unrelated foreground and sibling work continues. `exit_on_failure: true` cancels the scope
immediately. `exit_on_end: true` ends the scope successfully when an allowed terminal exit remains
after restart evaluation.

### 19. Deferred failure versus fail-fast

```yaml
version: 1
name: failure-policy
steps:
  - id: optional_metrics
    type: process
    with:
      command: ./bin/metrics
      readiness: {log: {pattern: ready}}
      restart: {policy: on_failure, max_restarts: 3, backoff: 1s}
  - id: critical_api
    type: process
    with:
      command: ./bin/api
      readiness: {log: {pattern: ready}}
      exit_on_failure: true
  - id: tests
    type: shell
    with: {command: go, args: [test, ./integration/...]}
```

### 20. Finite prerequisite, then managed process

Use `shell` when success means process completion, then start the managed service:

```yaml
version: 1
name: migrate-and-serve
steps:
  - id: migrations
    type: shell
    with: {command: ./bin/migrate, args: [up]}
  - id: api
    type: process
    with: {command: ./bin/api, readiness: {http: {url: http://127.0.0.1:8080/ready}}}
  - id: smoke
    type: shell
    with: {command: curl, args: [-f, http://127.0.0.1:8080/]}
```

The runnable [process DAG](../examples/process-dag.yaml) and [process pool](../examples/process-pool.yaml)
files supplement these embedded examples; the behavior-defining configurations remain here.

## `shell`

Run an argv command directly or execute inline shell source.

Direct execution avoids implicit shell parsing:

```yaml
- id: status
  type: shell
  with:
    command: git
    args: [status, --short]
```

When a previous step returns a complete argument vector, evaluate it without converting the list
to template text:

```yaml
- id: console
  type: shell
  with:
    argv:
      expr: steps.resolve.argv
    tty: true
```

`argv` contains exactly one `expr` field and cannot be combined with `command`, `script`, `shell`,
or `args`. The expression is compiled during validation and evaluated immediately before the
process starts. It must return a non-empty array or slice whose first item is a non-empty
executable. Strings remain unchanged; booleans and finite numbers are converted to their standard
base-10 text. Nulls, objects, nested lists, and other values are rejected.

The evaluated vector is passed directly to the process API. Spaces, quotes, glob characters, empty
arguments, and shell metacharacters therefore remain ordinary argument data. This behavior is the
same locally and for shell steps inside Docker or devenv executor scopes. It does not change the
separate `docker` step's run-operation schema.

Use `script` when shell syntax is intentional. Values in `args` become `$1`, `$2`, and so on:

```yaml
- id: branch
  type: shell
  with:
    script: |
      set -eu
      git switch -c "$1"
    args: ["{{ .vars.branch }}"]
```

Run as another Unix account when Wuko has permission:

```yaml
- id: identity
  type: shell
  with: {command: id, args: [-un], user: deploy}
```

Live output is forwarded and also captured as `stdout`, `stderr`, and `exit_code`. `user` uses
native process credentials; Wuko does not invoke `sudo` or rewrite `HOME` and `USER`. The boolean
outputs `stdout_truncated` and `stderr_truncated` report whether capture reached its configured
bound.

By default, only exit code 0 succeeds. Set `allowed_exit_codes` to a non-empty list of codes from
0 through 255 when a command uses non-zero statuses as useful observations. The configured list
replaces the default, so include every accepted code explicitly:

```yaml
- id: authorization
  type: shell
  with:
    command: kubectl
    args: [auth, can-i, get, deployments]
    allowed_exit_codes: [0, 1]
    stdout: capture
    stderr: capture
```

An allowed exit commits the usual `exit_code`, `stdout`, `stderr`, `stdout_truncated`, and
`stderr_truncated` outputs for later conditions. Command startup, executor, stream, timeout, and
cancellation errors still fail the step. A disallowed exit retains normal failure and retry
behavior, while an allowed exit completes without retrying.

Control forwarding and capture independently for each process stream with `stdout` and `stderr`:

| Policy | Display live | Return in the output |
| --- | --- | --- |
| `inherit` | Yes | No; the output string is empty |
| `capture` | No | Yes |
| `tee` | Yes | Yes |
| `discard` | No | No; the output string is empty |

Both policies default independently to `tee`, preserving the standard behavior. For example,
capture a large JSON document without printing it while leaving diagnostics and failures visible:

```yaml
- id: deployments
  type: shell
  with:
    command: kubectl
    args: [get, deployments, --all-namespaces, -o, json]
    stdout: capture
    stderr: inherit
```

An omitted `stderr` also defaults to `tee`, so `stdout: capture` alone still displays and captures
stderr. Use `capture_limit` to bound each captured stream independently:

```yaml
with:
  command: generate-manifest
  stdout: capture
  capture_limit: 16MiB
```

Sizes are positive integers followed by `B`, `KiB`, `MiB`, `GiB`, or `TiB`. When a stream exceeds
the limit, Wuko retains its leading bytes without adding a marker, continues draining the process,
and sets its `stdout_truncated` or `stderr_truncated` output to `true`. Streaming continues beyond
the capture limit for `tee`. Without `capture_limit`, non-TTY capture is unlimited.

Set `tty: true` for a local command that needs an interactive terminal, such as a shell, SSH
session, REPL, or terminal UI:

```yaml
- id: console
  type: shell
  with:
    command: /bin/sh
    args: [-i]
    tty: true
```

TTY mode connects the command to the workflow terminal, switches the terminal to raw mode for the
command's lifetime, and follows terminal resizes. The combined terminal stream is forwarded live
and the first 1 MiB is captured as `stdout`; `stderr` is empty and `stdout_truncated` is true when
more output was streamed. This keeps memory bounded for long-running interactive commands.

TTY mode cannot be combined with non-empty `stdin`. Terminal state is restored when the command
succeeds, fails, times out, or is canceled. `stdout`, `stderr`, and `capture_limit` cannot be set
with `tty: true`; TTY output remains a live merged stream with its existing 1 MiB capture.

### Scripted PTY interactions

Use `interactions` to write initial input, wait for prompts, inject dynamic values, and optionally
hand the live PTY to the user without an external `expect` executable:

```yaml
- id: jump
  type: shell
  with:
    tty: true
    argv:
      expr: steps.jump_argv.value
    interactions:
      - expect: 'iex[^\r\n]*>'
        send: Recruitee.Environment.current_env()
        newline: true
        timeout: 30s
    interact: true
```

| Field | Required | Default | Meaning |
| --- | --- | --- | --- |
| `interactions` | No | — | Ordered list of PTY writes and prompt responses, or an object containing `expr`. Requires `tty: true`. |
| `interactions[].send` | Yes | — | Text to write. An empty string is valid. Normal Wuko templates are rendered first. |
| `interactions[].expect` | No | — | Go regular expression matched against raw merged PTY output. Without it, `send` is immediate. |
| `interactions[].newline` | No | `false` | Append carriage return (`\r`) after `send`, equivalent to pressing Enter. |
| `interactions[].timeout` | No | `30s` | Positive bound for this `expect`. It is invalid without `expect`. |
| `interactions[].sensitive` | No | `false` | Redact `send` from diagnostics and suppress PTY echo while injecting it. |
| `interact` | No | `false` | Hand the PTY to the user immediately after the complete scripted sequence. |

The expression form evaluates a typed interaction list at runtime:

```yaml
with:
  command: connect-console
  tty: true
  interactions:
    expr: >-
      steps.console_injection.value
      ? [{
          "expect": steps.select_service.item.console_prompt,
          "send": steps.select_service.item.console_input,
          "newline": true
        }]
      : []
  interact: true
```

`interactions.expr` can use `inputs`, `vars`, `env`, `steps`, `dependencies`, active `batch`,
`foreach`, `matrix`, and `finally` bindings, plus `workflow` and `run`. It must return a list or
array of interaction objects. Each returned object follows the same field and validation rules as
a static interaction; expression-produced strings are used directly and are not rendered as Go
templates afterward.

Static and expression-backed lists may be empty. With `interact: true`, an empty list hands the
PTY to the user immediately. Without `interact`, it runs headlessly without scripted input. Omitting
`interactions` entirely preserves the existing immediate handoff behavior of plain `tty: true`.

Every entry requires `send`, but `expect` is optional. Consecutive send-only entries are written
immediately in declaration order, so startup input does not need an artificial prompt:

```yaml
with:
  command: setup-console
  tty: true
  interactions:
    - {send: select-project, newline: true}
    - {send: enable-feature, newline: true}
```

Immediate and prompt-driven entries may be mixed for login flows. Dynamic sends can use all normal
template roots, including `.inputs`, `.vars`, `.env`, `.steps`, `.dependencies`, active control
bindings, `.workflow`, and `.run`:

```yaml
with:
  command: ssh
  args: [gateway.example]
  tty: true
  interactions:
    - expect: 'Login:'
      send: "{{ .vars.username }}"
      newline: true
    - expect: 'Password:'
      send: "{{ .env.LOGIN_PASSWORD }}"
      newline: true
      sensitive: true
    - expect: 'workspace>'
      send: "use {{ .steps.workspace.value }} {{ .dependencies.build.artifact }}"
      newline: true
  interact: true
```

`send` uses template rendering, not the Expr language used by `argv.expr`. A sensitive send is
absent from debug configuration and Wuko suppresses the PTY line discipline's echo for that write.
A child program that independently prints the value can still expose it and must handle its own
output safely.

Expectations match the raw PTY byte stream. ANSI escapes and carriage returns are not removed, so
patterns should account for them when the target renders styled prompts. A match consumes through
the matched bytes; any bytes after it remain available to the next expectation. Wuko retains at
most 1 MiB of unmatched output for the active expectation.

When `interact: true`, workflow input is withheld during scripting. User control begins immediately
after the final send, including its optional carriage return, has been written. Without `interact`,
the command continues headlessly after scripting. Headless scripted PTYs use a 24×80 size and can
run without a file-backed workflow terminal, including non-interactive and browser-driven runs.
Plain `tty: true` without `interactions` preserves immediate handoff and still requires an
interactive file-backed terminal. Docker executor sessions reject TTY mode; local process wrappers
such as devenv may forward it.

### Live-console appearance

An interactive shell can identify the console by changing the outer terminal's colors and title
for the duration of the user handoff:

```yaml
- id: production_console
  type: shell
  with:
    command: ssh
    args: [production.example.com]
    tty: true
    terminal:
      background: "rgb(30, 30, 46)"
      foreground: white
      title: Production console
```

`terminal` accepts optional `background`, `foreground`, and `title` fields, and at least one must
be set. Colors accept `#RGB`, `#RRGGBB`, and decimal `rgb(r, g, b)` values from 0 through 255.
Names are case-insensitive and include `black`, `silver`, `gray`/`grey`, `white`, `maroon`, `red`,
`purple`, `fuchsia`/`magenta`, `green`, `lime`, `olive`, `yellow`, `navy`, `blue`, `teal`,
`aqua`/`cyan`, and `orange`. Normal Wuko templates are rendered before validation, so appearance
can depend on workflow values. Titles cannot contain terminal control characters.

The appearance is applied immediately before Wuko hands input to the child PTY. With scripted
interactions, this is after the last interaction succeeds and requires `interact: true`. When the
child exits, fails, or is canceled, Wuko resets configured colors to the terminal defaults and
restores the saved window title. Appearance control uses xterm-compatible OSC and CSI sequences;
terminals that do not support them may ignore them. Styling is best effort and never changes the
workflow result. Redirected or non-terminal output is left untouched.

If an expectation is not found, its send and every later interaction are skipped. The step fails
when that interaction's timeout expires, terminates the child process group, and does not hand the
PTY to the user. If the child exits first, failure is immediate. Interaction failures are
operational errors: `allowed_exit_codes` cannot accept them, while a step retry starts the entire
sequence again with fresh state. Step cancellation and outer deadlines take precedence.

| Symptom | Result |
| --- | --- |
| Regex never matches | Fail at that interaction's timeout; do not send or hand off. |
| Child exits before a match | Fail immediately with the interaction index and regex. |
| Rendered regex is invalid | Fail while building the rendered shell runner. |
| More than 1 MiB arrives before a match | Fail with an unmatched-output overflow. |
| `timeout` is used without `expect` | Reject the workflow configuration. |
| `interact: true` has no interactive terminal | Fail before starting the command. |
| Executor cannot create a PTY | Reject TTY execution in that executor. |

## `agent`

Start an external agent process and send its prompt on standard input.

Launch Codex:

```yaml
- id: codex
  type: agent
  with:
    command: codex
    args: [exec, -]
    prompt: "Implement task {{ .vars.task_id }} using {{ .vars.brief }}"
```

Choose between installed agents with conditions:

```yaml
- id: claude
  type: agent
  if: vars.agent == "claude"
  with:
    command: claude
    args: [-p]
    prompt: "Review {{ .steps.brief.value }}"

- id: codex
  type: agent
  if: vars.agent == "codex"
  with:
    command: codex
    args: [exec, -]
    prompt: "Review {{ .steps.brief.value }}"
```

The selected CLI must already be installed and authenticated. Like `shell`, the step streams and
captures `stdout`, `stderr`, and `exit_code`. See
[`examples/clickup-task.yaml`](../examples/clickup-task.yaml) for a complete task-to-agent flow.

## `lua`

Run a Lua file or inline source with typed arguments. Lua source is not templated; pass dynamic
values through `args`.

Run a file:

```yaml
- id: metadata
  type: lua
  with:
    file: ../scripts/metadata.lua
    args:
      task: "{{ .vars.task_name }}"
```

Produce typed outputs and variables inline:

```yaml
- id: metadata
  type: lua
  with:
    source: |
      local token = wuko.env.get("API_TOKEN")
      wuko.output("task", {id = "TASK-1", title = wuko.args.title})
      wuko.set_var("task_id", "TASK-1")
    args:
      title: "{{ .vars.task_name }}"
```

Use an `expr` binding when an argument must keep its runtime type instead of being rendered to a
string:

```yaml
- id: inspect
  type: lua
  with:
    source: |
      for _, deployment in ipairs(wuko.args.inventory) do
        print(deployment.metadata.name)
      end
    args:
      inventory:
        expr: steps.decode_deployments.value
```

Argument expressions use the normal `inputs`, `vars`, `env`, `steps`, `dependencies`, `batch`,
`foreach`, `matrix`, `finally`, `workflow`, and `run` Expr roots.

Use the host API for richer automation:

```lua
local response = wuko.http.request({method = "GET", url = wuko.args.url, timeout = 10})
wuko.fs.write("response.json", response.body)
local result = wuko.exec.run({command = "git", args = {"status", "--short"}})
wuko.output("clean", result.stdout == "")
```

`wuko.exec.run` accepts the same `stdout`, `stderr`, and `capture_limit` options as a shell step:

```lua
local result = wuko.exec.run({
  command = "kubectl",
  args = {"get", "deployments", "--all-namespaces", "-o", "json"},
  stdout = "capture",
  stderr = "inherit",
  capture_limit = "16MiB"
})
local deployments = wuko.json.decode(result.stdout)
```

The result contains `stdout`, `stderr`, `exit_code`, `error`, `stdout_truncated`, and
`stderr_truncated`. The policies and per-stream truncation behavior match `shell`; omitted policies
default independently to `tee`.

The trusted `wuko` API exposes `args`, variables, outputs, environment, JSON, shared helpers,
key-value stores, HTTP, filesystem operations, and direct command execution. It also exposes
snapshot tables for `wuko.inputs`, `wuko.steps`, `wuko.dependencies`, `wuko.workflow`, and
`wuko.run`. Changing these Lua tables does not change workflow state. Outputs may be nil, booleans,
strings, numbers, arrays, or string-keyed objects; cyclic and mixed-key tables are rejected.

## `wait`

Pause for a duration or poll one embedded step until an Expr condition becomes true.

Use a fixed cancellation-aware delay:

```yaml
- id: settle
  type: wait
  with: {duration: 30s}
```

Poll an API immediately and then every five seconds:

```yaml
- id: await_release
  type: wait
  timeout: 5m
  with:
    interval: 5s
    step:
      type: http
      with:
        url: https://api.example.com/releases/42
        response: json
    until: 'error == nil && result.value.status == "ready"'
```

Poll a local command with a different interval:

```yaml
- id: await_socket
  type: wait
  timeout: 1m
  with:
    interval: 1s
    step:
      type: shell
      with: {command: test, args: [-S, /tmp/app.sock]}
    until: error == nil
```

A polling wait requires a top-level timeout. Its expression can use the normal workflow roots plus
`result`, nullable `error`, and the one-based `poll` number. The embedded step accepts only `type`
and `with`; its final successful outputs are published under the wait step's ID.
