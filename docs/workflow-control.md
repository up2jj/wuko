# Workflow controls

Wuko has eight controls for scheduling, repeating, recovering, or monitoring operations:
`concurrent`, `batch`, `foreach`, `matrix`, `loop`, `try`/`catch`, `cancel_on`, and `observe`.

- Use `concurrent` for a fixed bounded DAG; omit `needs` when every child is independent.
- Use `batch` when each block should receive a fixed-size chunk of one runtime list.
- Use `foreach` when one runtime list determines how many times a block runs.
- Use `matrix` when every combination of several named dimensions must run.
- Use `loop` when a sequential block should repeat until a runtime expression becomes true.
- Use `try`/`catch` when a failed sequential operation needs an explicit recovery path.
- Use `cancel_on` when a sequential body should stop as soon as one named monitor finishes.
- Use `observe` for a background loop driven by filesystem, HTTP, or future event sources.

## Observe

`observe` opens and validates its selected source, publishes a ready result, and continues the
foreground workflow while its body runs in the background. After the foreground sequence succeeds,
Wuko waits for every active observer until interruption. Background jobs are always joined before
workflow `finally` and managed-resource cleanup begin.

```yaml
steps:
  - id: go_tests
    observe:
      source:
        type: filesystem
        with:
          root: .
          paths: ["**/*.go"]
          events: [create, modify, rename, remove]
      debounce: 300ms
      on_change: restart
      steps:
        - id: test
          type: shell
          with: {command: go, args: [test, ./...]}

  - id: server
    type: shell
    with: {command: ./server}
```

`id`, `source.type`, and a non-empty `steps` body are required. `debounce` defaults to `300ms`, and
`on_change` defaults to `restart`; an explicit `0s` disables debounce. The `filesystem` source
requires `paths`, defaults `root` to `.` and events to `create`, `modify`, `rename`, and `remove`,
and otherwise matches the one-shot [`type: watch`](steps-system.md#watch) step.

The body runs immediately once, then receives an `.observe` binding on each trigger. Every source
provides `initial`, the one-based `iteration`, and `source`. Filesystem runs additionally provide
`.observe.filesystem.paths` and sorted `.observe.filesystem.changes` records containing `path` and
`operations`. Each run starts from a private snapshot taken when the control was declared; later
foreground outputs and previous body outputs are deliberately unavailable.

HTTP observation polls a response and triggers when it changes by default:

```yaml
- id: api_health
  observe:
    source:
      type: http
      with:
        every: 5s
        timeout: 10s
        trigger: change       # change | always
        request:
          url: https://example.test/health
          response: json      # text | json
    on_change: queue
    steps:
      - id: report
        type: shell
        with:
          command: ./report-health
          args: ['{{ .observe.http.status }}']
```

The HTTP source performs its first request during synchronous readiness and exposes that response
to the initial run under `.observe.http`. Later polls emit only changed responses unless `trigger`
is `always`. HTTP status failures remain observable responses with an `error` field; transport,
timeout, oversized-body, and decoding failures are fatal source errors.

- `restart` cancels and joins the active body before starting a replacement.
- `queue` coalesces changes into at most one pending run.
- `skip` ignores triggers received while the body is active.

Body failures are reported but do not stop observation. Fatal source failures cancel the foreground
workflow and sibling observers. A workflow `return` deliberately cancels and joins observers
without turning that shutdown into a failure. The immutable launch result at `steps.<id>` contains
`status: observing`, source type and readiness metadata, `debounce`, and `on_change`; it is never
mutated from a background goroutine.

Version 1 permits the control only in the main sequential workflow flow, including transparent
`env` and `working_directory` scopes. It is rejected in actions, lifecycle hooks, cleanup,
executors, concurrent or fan-out bodies, and nested observers. Use workflow `finally` for shutdown
work; it runs after the observer body and its child processes have stopped.

## Try and catch

`try`/`catch` is an ordinary named control step. Both blocks are required and each contains a
non-empty `steps` list. The parent `id` is also required: it gives the complete, stable result one
path under `steps.<id>` instead of exposing child IDs at the outer workflow level.

```yaml
version: 1
name: recover-deployment
steps:
  - id: deployment
    try:
      steps:
        - id: deploy
          type: shell
          with: {script: ./deploy.sh}
    catch:
      steps:
        - id: rollback
          type: shell
          with: {script: ./rollback.sh}
        - id: report
          type: http
          with:
            url: https://example.com/deployment-failures
            body: '{{ .error.step }}: {{ .error.message }}'

  - id: notify_success
    type: http
    if: steps.deployment.recovered == false
    with: {url: https://example.com/deployments}
```

The `try` steps run sequentially. If all of them succeed, `catch` is recorded as skipped and
ordinary execution continues. If a try child fails or reaches its own timeout, Wuko stops the rest
of `try`, makes the structured `error` root available only while `catch` runs, and executes every
top-level catch entry. A failed or timed-out catch entry does not prevent later catch entries from
running. If at least one catch entry runs and every entry that ran succeeds, the failure is
recovered and ordinary execution continues; otherwise the parent fails after all catch entries have
been attempted. A catch whose entries are all skipped by their own `if:` recovers nothing: the
`catch` phase is recorded as skipped and the original failure stands.

`error` contains `status`, `message`, `step`, `type`, and an ordered `errors` list. Each list entry
contains the same four fields. Prefer `status`, `step`, and `type` in conditions; `message` is
informational and may change. Templates and the typed expression environments of steps such as
`assert`, `set`, `shell`, and Lua expose the same root. A `wait` step's `until` expression retains
its existing local `error` meaning for the most recent poll.

Parent cancellation and an expired parent deadline bypass `catch`; they are not recoverable
failures. Registered defers owned by successful try or catch children still run on a detached
cleanup context. These control-local defers run after catch, before the parent result is published.
Cleanup is best effort, and any cleanup failure fails the parent. Nested `try`/`catch` and `return`
are rejected inside the control. `try`/`catch` is also rejected inside `cancel_on`; a `cancel_on`
may appear inside `try` or `catch`, subject to its own restrictions. Other children keep the
restrictions of the enclosing scope.

### Try/catch outcome

On an ordinary success, the stable parent result is:

```yaml
steps.deployment:
  ok: true
  recovered: false
  status: succeeded
  error: null
  try:
    status: succeeded
    error: null
    steps:
      deploy:
        status: succeeded
        error: null
        outputs: {stdout: "deployed\n", exit_code: 0}
    vars: {}
  catch:
    status: skipped
    error: null
    steps:
      rollback: {status: skipped, error: null, outputs: null}
      report: {status: skipped, error: null, outputs: null}
    vars: {}
  cleanup: {status: skipped, error: null, steps: {}, vars: {}}
  vars: {}
```

After `deploy` fails and both recovery entries succeed, the parent still succeeds, with the
original failure retained under `try.error`:

```yaml
steps.deployment:
  ok: true
  recovered: true
  status: succeeded
  error: null
  try:
    status: failed
    error:
      status: failed
      message: 'workflow "recover-deployment" step "deploy" (shell): attempt 1/1 failed: exit status 1'
      step: deploy
      type: shell
      errors:
        - {status: failed, message: 'attempt 1/1 failed: exit status 1', step: deploy, type: shell}
    steps:
      deploy: {status: failed, error: 'attempt 1/1 failed: exit status 1', outputs: null}
    vars: {}
  catch:
    status: succeeded
    error: null
    steps:
      rollback: {status: succeeded, error: null, outputs: {}}
      report: {status: succeeded, error: null, outputs: {status: 200}}
    vars: {}
  cleanup: {status: skipped, error: null, steps: {}, vars: {}}
  vars: {}
```

Successful child variable writes are committed atomically with the parent and are also copied into
the phase-local and final `vars` records. Child outputs are read through paths such as
`steps.deployment.try.steps.deploy.outputs` and
`steps.deployment.catch.steps.rollback.outputs`. If catch or local cleanup fails, the parent result
and its variable writes are not published at all; the workflow fails normally. A failure captured
as data by `cancel_on` does not trigger catch because the `cancel_on` parent succeeded—follow it
with an `assert` when that recorded outcome should become a recoverable failure.

## Cancel on

`cancel_on` is one named logical step. It starts its sequential body and every monitor concurrently
from isolated copies of the state that existed before the control. The first participant that
finishes wins, except that a monitor whose steps were all skipped never triggers. A body whose steps
were all skipped still ends the race. Wuko cancels and joins the other participants, then records the
complete outcome under `steps.<cancel_on-id>` instead of merging child state into the workflow.

```yaml
version: 1
name: monitored-deployment
steps:
  - id: deployment_watch
    cancel_on:
      monitors:
        - id: deployment_finished
          type: wait
          timeout: 30m
          with:
            interval: 5s
            step:
              type: http
              with:
                url: https://example.com/status
            until: result.status == 200

      steps:
        - id: prepare
          type: lua
          with:
            source: |
              wuko.set_var("artifact", "dist/app.tar")

        - id: deploy
          type: shell
          with:
            command: ./deploy
```

The parent ID and every monitor ID are required and must be valid and unique. A control must contain
at least one monitor and one body step. Multiple monitors are ordinary declarations in the
`monitors` list; all of them race the body and one another. A monitor may be a concrete step, action,
conditional or working-directory block, worktree, executor, `concurrent`, `batch`, `foreach`,
`matrix`, or `loop`. The monitor ID also labels declarations such as `concurrent` that are normally
anonymous. Conditions, retries, timeouts, templates, and owned cleanup retain their ordinary
behavior. Monitor branches have no shared interactive standard input.

Nested `cancel_on`, `return`, `require`, and declared `defer` are rejected anywhere inside the
control. The control races its own participants, so it is rejected inside an executor block, where
every branch would share the one open session. Elsewhere its participants keep the restrictions of
the scope the control sits in: a `cancel_on` inside a `concurrent` group or a `foreach` body may
contain only what that scope already allows. Child outputs and variables are isolated: they do not become top-level `.steps` or `.vars`
entries. Read them through the parent output instead.

### Outcome shape

If the body wins after both body steps succeed, the output has this shape. This example assumes the
shell step captured its standard output.

```yaml
steps.deployment_watch:
  ok: true
  triggered: false
  status: succeeded
  error: null
  winner:
    monitor: ""
    kind: body
  steps:
    prepare:
      status: succeeded
      error: null
      outputs: {}
    deploy:
      status: succeeded
      error: null
      outputs:
        stdout: "deployed\n"
        exit_code: 0
  vars:
    artifact: dist/app.tar
  monitors:
    deployment_finished:
      kind: wait
      status: canceled
      error: null
      outputs: null
      steps: {}
      vars: {}
  result: null
```

If `deployment_finished` wins after `prepare` commits but while `deploy` is running, the body keeps
the completed variable and step record. The canceled step has a null error because cancellation was
caused by winner selection, and only successful declarations publish non-null outputs.

```yaml
steps.deployment_watch:
  ok: true
  triggered: true
  status: succeeded
  error: null
  winner:
    monitor: deployment_finished
    kind: wait
  steps:
    prepare:
      status: succeeded
      error: null
      outputs: {}
    deploy:
      status: canceled
      error: null
      outputs: null
  vars:
    artifact: dist/app.tar
  monitors:
    deployment_finished:
      kind: wait
      status: succeeded
      error: null
      outputs:
        status: 200
        headers: {}
        body: "ready\n"
      steps: {}
      vars: {}
  result: null
```

If the monitor wins before `prepare` starts, every known unstarted body declaration is recorded as
`skipped`; no body variable exists.

```yaml
steps.deployment_watch:
  ok: true
  triggered: true
  status: succeeded
  error: null
  winner:
    monitor: deployment_finished
    kind: wait
  steps:
    prepare:
      status: skipped
      error: null
      outputs: null
    deploy:
      status: skipped
      error: null
      outputs: null
  vars: {}
  monitors:
    deployment_finished:
      kind: wait
      status: succeeded
      error: null
      outputs:
        status: 200
        headers: {}
        body: "ready\n"
      steps: {}
      vars: {}
  result: null
```

`winner.monitor` is empty when the body wins and is the required monitor ID otherwise.
`winner.kind` is `body` or the winning monitor's executable kind, such as `wait`, `foreach`, or
`concurrent`. `triggered` is equivalent to `winner.monitor != ""`. The top-level `ok`, `status`, and
`error` describe the winner, not every participant. Body declarations are under `steps`, body
variables are under `vars`, and monitor records are keyed by ID under `monitors`. Each step record
has `status`, nullable `error`, and nullable `outputs`; statuses are `succeeded`, `failed`,
`timed_out`, `canceled`, or `skipped`.

Read the recorded outcome with ordinary conditions and templates:

```yaml
if: steps.deployment_watch.status == "succeeded"
```

```yaml
if: steps.deployment_watch.steps.deploy.status == "succeeded"
```

```yaml
if: steps.deployment_watch.monitors.deployment_finished.status == "succeeded"
```

```yaml
"{{ .steps.deployment_watch.vars.artifact }}"
```

A body or monitor failure and a participant timeout still win the race. They are captured as
`ok: false`, `status: failed` or `timed_out`, and a non-null `error`; the `cancel_on` parent itself
completes successfully so later steps can inspect that outcome. Parent cancellation, validation
errors, internal engine errors, and collection errors fail the parent normally.

### Collection

Add `collect` when a later step needs a smaller typed summary:

```yaml
- id: deployment_watch
  cancel_on:
    monitors:
      - id: deployment_finished
        type: wait
        with: {duration: 5s}
    steps:
      - id: prepare
        type: lua
        with:
          source: |
            wuko.set_var("artifact", "dist/app.tar")
      - id: deploy
        type: shell
        with: {command: ./deploy}
    collect: |
      {
        "deployed": steps.deploy.status == "succeeded",
        "artifact": vars.artifact,
        "monitor": cancel_on.winner.monitor
      }
```

Collection runs once after every participant stops. Its Expr environment exposes the namespaced
`steps`, `vars`, `monitors`, and `cancel_on` outcome roots plus ordinary workflow roots. The typed
value is stored at `steps.deployment_watch.result`. Without `collect`, `result` is always null; the
full body and monitor records remain available either way.

## Loop

`loop` repeats its child steps sequentially, evaluates `until` after each iteration, and supports
an optional delay, timeout, and maximum iteration limit. Child outputs remain available in `steps`
and the loop output contains `iterations`, `count`, and `last`.

```yaml
- id: wait_for_ci
  loop:
    until: steps.poll.terminal
    delay: 10s
    timeout: 30m
    max_iterations: 180
    steps:
      - id: poll
        type: github_actions
        with:
          workflow: ci.yml
          head_sha: "{{ .vars.head_sha }}"
```

`batch`, `foreach`, and `matrix` are logical parent steps. Each iteration receives an isolated copy of
the state that existed before the parent began and runs its child steps in order. The optional
`collect` expression controls which value each completed iteration contributes to the parent's
ordered result.

An anonymous `if` wrapper can guard a sequential block containing any of these controls:

```yaml
- if: vars.enabled
  steps:
    - {id: prepare, type: shell}
    - concurrent:
        steps:
          - {id: lint, type: shell}
          - {id: test, type: shell}
```

The condition is evaluated once. The wrapper adds no ID, state, output, or step-count entry of its
own. A false condition reports its ordinary children and concurrent leaves as skipped and skips
fan-out parents without expansion. Conditional wrappers may appear inside batch, foreach, and matrix
bodies, but not directly inside a concurrent group; wrappers cannot be directly nested.

## Foreach

`foreach.items` is an Expr expression evaluated when execution reaches the parent. It may read
inputs, variables, environment values, and outputs from earlier steps. The result must be a list
or array.

```yaml
version: 1
name: deploy-targets
vars:
  targets: [api, worker]
steps:
  - id: deployments
    foreach:
      items: vars.targets
      collect: '{"target": foreach.item, "url": steps.deploy.url}'
      steps:
        - id: deploy
          type: lua
          with:
            args: {target: "{{ .foreach.item }}"}
            source: |
              wuko.output("url", "https://example.com/" .. wuko.args.target)
```

Inside the block, templates use `.foreach.item` and `.foreach.index`. Conditions and other Expr
fields omit the leading dot:

```yaml
- id: checked-deployments
  foreach:
    items: vars.targets
    steps:
      - id: deploy
        type: shell
        if: foreach.index < 10
        with:
          command: ./deploy
          args: ["{{ .foreach.item }}"]
```

Indexes are zero-based. Lua steps can read the same values from `wuko.foreach.item` and
`wuko.foreach.index`.

A successful parent with `collect` exposes one evaluated value per iteration in expansion order:

```yaml
steps.deployments:
  count: 2
  results:
    - {target: api, url: "https://example.com/api"}
    - {target: worker, url: "https://example.com/worker"}
```

`collect` is a typed Expr expression evaluated against each iteration's final state after every
iteration succeeds. It can read `steps`, iteration-local `vars`, `inputs`, `env`, `run`, and the
active `batch`, `foreach`, or `matrix` binding. It may return null, a scalar, a list, or an object. Nested
lists remain nested.

Iteration-local variables can be exported deliberately even though they do not otherwise escape:

```yaml
- id: generated
  foreach:
    items: vars.targets
    collect: vars.artifact
    steps:
      - id: prepare
        type: lua
        with:
          args: {target: "{{ .foreach.item }}"}
          source: |
            wuko.set_var("artifact", "dist/" .. wuko.args.target .. ".tgz")
```

Without `collect`, a successful parent exposes only its expansion count:

```yaml
steps.deployments:
  count: 2
```

An empty input list succeeds. With `collect` it exposes `count: 0` and `results: []`; without
`collect` it exposes only `count: 0`.

## Batch

`batch.items` is an Expr expression whose result must be a list or array. `batch.size` accepts a
positive YAML integer or a non-empty Expr string evaluated when execution reaches the parent.
Items retain their input order, and the final chunk is shorter when the list does not divide
evenly.

```yaml
version: 1
name: deploy-batches
vars:
  targets: [api, worker, web, cron, admin]
steps:
  - id: deployments
    batch:
      items: vars.targets
      size: 2
      steps:
        - id: deploy
          type: shell
          with:
            command: ./deploy-batch
            args: ['{{ .batch.items | toJSONCompact }}']
```

This runs three blocks with `[api, worker]`, `[web, cron]`, and `[admin]`. Templates use
`.batch.items` and the zero-based `.batch.index`; Expr fields omit the leading dot. Conditions can
therefore select individual chunks:

```yaml
- id: checked
  batch:
    items: vars.targets
    size: 2
    steps:
      - id: deploy
        type: shell
        if: batch.index < 10
        with:
          command: ./deploy-batch
          args: ['{{ .batch.items | toJSONCompact }}']
```

Use an expression when the size is runtime data. It may read inputs, variables, environment
values, workflow/run metadata, and outputs committed by earlier steps:

```yaml
vars:
  targets: [api, worker, web, cron]
  batch_size: 2
steps:
  - id: deployments
    batch:
      items: vars.targets
      size: vars.batch_size
      steps:
        - id: deploy
          type: shell
          with:
            command: ./deploy-batch
            args: ['{{ .batch.items | toJSONCompact }}']
```

An expression such as `size: 'vars.worker_count * 2'` is also valid. Its result must be positive,
exactly integral, and within the platform `int` range. Zero, negative, fractional, non-numeric,
and overflowing results fail the parent during expansion before any child starts. The `batch`
binding does not exist while `items` or `size` is being evaluated.

`collect` evaluates once per completed chunk and preserves expansion order even when chunks run
concurrently:

```yaml
- id: processed
  batch:
    items: vars.records
    size: 100
    collect: |
      {
        "batch": batch.index,
        "items": batch.items,
        "stdout": steps.process.stdout
      }
    max_concurrency: 4
    max_iterations: 100
    timeout: 15m
    fail_fast: false
    steps:
      - id: process
        type: shell
        with:
          command: ./process-records
          args: ['{{ .batch.items | toJSONCompact }}']
```

The parent exposes the chunk count and ordered collected values:

```yaml
steps.processed:
  count: 3
  results:
    - {batch: 0, items: [...], stdout: "..."}
    - {batch: 1, items: [...], stdout: "..."}
    - {batch: 2, items: [...], stdout: "..."}
```

Lua steps use `wuko.batch.index` and `wuko.batch.items`:

```yaml
- id: transformed
  batch:
    items: vars.values
    size: 10
    collect: steps.transform.values
    steps:
      - id: transform
        type: lua
        with:
          source: |
            wuko.output("values", {
              index = wuko.batch.index,
              items = wuko.batch.items,
            })
```

An empty source succeeds with `count: 0` and, when `collect` is present, `results: []`.
`max_iterations` counts chunks rather than source items.

## Matrix

A matrix generates the Cartesian product of its axes. Axis names must be Wuko identifiers. An
axis can be an inline YAML list or an Expr whose runtime result is a list or array.

```yaml
version: 1
name: platform-tests
vars:
  go_versions: ["1.25", "1.26"]
steps:
  - id: checks
    matrix:
      axes:
        os: [linux, darwin]
        go_version: vars.go_versions
      collect: steps.build.path
      max_concurrency: 2
      max_iterations: 100
      steps:
        - id: build
          type: lua
          with:
            args:
              os: "{{ .matrix.os }}"
              go_version: "{{ .matrix.go_version }}"
            source: |
              wuko.output("path", "dist/app-" .. wuko.args.os .. "-" .. wuko.args.go_version)
```

Axis declaration order is significant and preserved. The rightmost axis changes fastest, so the
example expands in this order:

1. `{os: linux, go_version: "1.25"}`
2. `{os: linux, go_version: "1.26"}`
3. `{os: darwin, go_version: "1.25"}`
4. `{os: darwin, go_version: "1.26"}`

Templates use `.matrix.<axis>`, Expr uses `matrix.<axis>`, and Lua uses
`wuko.matrix.<axis>`. Successful results retain the same expansion order even when iterations
finish in a different order:

```yaml
steps.checks:
  count: 4
  results:
    - dist/app-linux-1.25
    - dist/app-linux-1.26
    - dist/app-darwin-1.25
    - dist/app-darwin-1.26
```

Collect an object when later steps also need the matrix coordinates:

```yaml
collect: '{"os": matrix.os, "version": matrix.go_version, "artifact": steps.build.path}'
```

A matrix must declare at least one axis. If any axis is empty, the complete matrix has zero
combinations and succeeds with `count: 0` plus `results: []` when `collect` is present. Duplicate
axis values are allowed and produce duplicate combinations.

## Scheduling and failures

All three fan-out controls accept the same policies:

```yaml
- id: work
  foreach:
    items: vars.items
    max_concurrency: 4
    max_iterations: 1000
    timeout: 15m
    fail_fast: false
    steps:
      - id: process
        type: shell
        with: {command: ./process}
```

- `max_concurrency` defaults to `1` and must be from 1 through 100. One preserves item order for
  external effects; larger values allow iterations to overlap.
- `max_iterations` defaults to `10,000`, may be lowered for a workflow-specific safety bound, and
  cannot exceed the absolute limit of `1,000,000`. Expansion fails before iteration state is
  allocated when the batch chunk count, foreach list, or Cartesian product exceeds the configured limit.
- `fail_fast` defaults to `true`. After an iteration fails, Wuko cancels running siblings and does
  not start queued work where possible. With `false`, Wuko runs every iteration and reports all
  failures in expansion order.
- `timeout` is optional and begins after fan-out input expressions and expansion complete. It covers
  queueing, child steps, retries, polling delays, and nested concurrent groups during the execution
  phase. It cancels active work at the deadline and then waits for cleanup, so total wall-clock
  duration can be longer.
- Each child retains its own condition, timeout, retry, polling, and action behavior.

The parent result is committed only after every iteration and every collection evaluation
succeeds. A failure exposes no partial `steps.<parent-id>` aggregate. Commands, files, HTTP
requests, containers, agents, and other external effects completed before a failure are not
rolled back.

Collection runs after all child work. Expressions are evaluated sequentially in expansion order;
the first error fails the parent with its zero-based iteration index. For example, this fails when
`inspect` was skipped and therefore published no output:

```yaml
collect: steps.inspect.value
```

Returning `nil` explicitly is valid and adds a null entry. Other collected values must be
YAML/JSON-compatible.

Cancellation and fail-fast scheduling stop admitting queued iterations and wait for all active
iterations to return. Terminal progress reports how many iterations started, succeeded, and were
not run. Expansion itself checks cancellation while cloning batch/foreach values and constructing
matrix bindings. Expr does not accept a context during expression evaluation, so cancellation is checked
immediately before and after each fan-out input or `collect` expression rather than during its
evaluation.

See [Graceful shutdown](graceful-shutdown.md) for signal escalation, nested-control propagation,
timeout boundaries, atomic result commits, and partial external effects.

Parallel controls are non-interactive so iterations cannot compete for terminal input. Sequential
controls retain normal interactive behavior. Pre-supplied `tui_input`, `tui_password`, and
`tui_choice` variables remain usable in parallel iterations. An optional `tui_choice` without a
supplied variable resolves to no selection.
## State and nesting

Each iteration starts from the same pre-parent state. Its child steps run sequentially and may
consume variables and outputs written by earlier children in that iteration. Those variables do
not escape the iteration except through an explicit `collect` expression.

Child IDs are local to a control body, so separate controls may both use an ID such as `build`.
A child ID may not collide with an ID in its enclosing workflow or composite action.

A control body may contain one existing `concurrent` group for a fixed set of independent child
steps. The following are not supported:

- batch, foreach, or matrix inside another fan-out control;
- batch, foreach, or matrix inside a concurrent group;
- nested concurrent groups;
- conditional blocks directly inside concurrent groups;
- directly nested conditional blocks.

Put dependent work after the inner concurrent group or after the complete control parent.

An early [`return`](return.md) may follow a completed concurrent, batch, foreach, or matrix control and
consume its committed outputs. It cannot appear inside a concurrent branch or fan-out body because
parallel branches or iterations could race to publish conflicting workflow results. A composite
action used by a branch or iteration may return internally; that finishes only the individual
action invocation.

## Composite actions and required files

Required step fragments can appear inside batch, foreach, and matrix bodies. Relative paths continue to
resolve from the file containing the `require` entry.

Composite actions may also be children. Their `with` values and typed input expressions can use
the active batch, foreach, or matrix bindings. Every `uses` source is resolved while loading the
workflow, before iterations exist, so it cannot reference `.batch`, `.foreach`, or `.matrix`.
Choose a static action source and pass the varying value as an action input. A local action path in
a required fragment resolves from the fragment containing the `uses`, not from the top-level
workflow or an active `working_directory`.

## Conditions, inspection, and dry runs

A parent may have an `if` condition. A skipped parent is absent from `steps`, like any other
skipped step. Child conditions can use the active binding.

`wuko tree` displays the declared control and its child block without expanding runtime values.
Likewise, `wuko run --dry-run` validates collection expressions and child definitions, then prints
each body once. It does not evaluate runtime collections or predict their expansion count.

## Limitations

- Batch/foreach items and dynamic matrix axes must be lists or arrays; map iteration is not supported.
- Matrix is Cartesian-only. GitHub Actions-style `include` and `exclude` rules are not supported.
- Fan-out controls cannot be nested.
- Action sources are static with respect to iteration bindings.
- Expansion is capped by `max_iterations`; choose a lower workflow-specific value when the default
  of 10,000 is larger than expected.
- Parallel iterations cannot prompt interactively, and their stdout or stderr may interleave at
  write boundaries.
- Failed controls do not expose partial aggregate results.
- Wuko cannot roll back external effects from completed or concurrently running iterations.
