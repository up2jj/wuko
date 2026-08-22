# Workflow controls

Wuko has four controls for scheduling or repeating operations: `concurrent`, `batch`, `foreach`,
and `matrix`.

- Use `concurrent` for a fixed set of independent steps.
- Use `batch` when each block should receive a fixed-size chunk of one runtime list.
- Use `foreach` when one runtime list determines how many times a block runs.
- Use `matrix` when every combination of several named dimensions must run.

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
the active batch, foreach, or matrix bindings. The `uses` source is resolved while loading the workflow,
before iterations exist, so it cannot reference `.batch`, `.foreach`, or `.matrix`. Choose a static action
source and pass the varying value as an action input.

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
