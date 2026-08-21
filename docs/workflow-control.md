# Workflow controls

Wuko has three controls for scheduling or repeating operations: `concurrent`, `foreach`, and
`matrix`.

- Use `concurrent` for a fixed set of independent steps.
- Use `foreach` when one runtime list determines how many times a block runs.
- Use `matrix` when every combination of several named dimensions must run.

Both `foreach` and `matrix` are logical parent steps. Each iteration receives an isolated copy of
the state that existed before the parent began, runs its child steps in order, and contributes one
record to the parent's ordered result.

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
fan-out parents without expansion. Conditional wrappers may appear inside foreach and matrix
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
      steps:
        - id: deploy
          type: shell
          with:
            command: ./deploy
            args: ["{{ .foreach.item }}"]
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

A successful parent exposes an ordered aggregate:

```yaml
steps.deployments:
  count: 2
  results:
    - index: 0
      item: api
      steps:
        deploy:
          stdout: deployed api
          exit_code: 0
    - index: 1
      item: worker
      steps:
        deploy:
          stdout: deployed worker
          exit_code: 0
```

An empty input list succeeds and exposes `count: 0` and `results: []`.

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
      max_concurrency: 2
      max_iterations: 100
      steps:
        - id: test
          type: shell
          with:
            command: ./test-platform
            args:
              - "{{ .matrix.os }}"
              - "{{ .matrix.go_version }}"
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
    - index: 0
      matrix: {os: linux, go_version: "1.25"}
      steps:
        test: {exit_code: 0}
```

A matrix must declare at least one axis. If any axis is empty, the complete matrix has zero
combinations and succeeds with an empty result. Duplicate axis values are allowed and produce
duplicate combinations.

## Scheduling and failures

Both controls accept the same policies:

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
  allocated when the foreach list or Cartesian product exceeds the configured limit.
- `fail_fast` defaults to `true`. After an iteration fails, Wuko cancels running siblings and does
  not start queued work where possible. With `false`, Wuko runs every iteration and reports all
  failures in expansion order.
- `timeout` is optional and begins after collection expressions and expansion complete. It covers
  queueing, child steps, retries, polling delays, and nested concurrent groups during the execution
  phase. It cancels active work at the deadline and then waits for cleanup, so total wall-clock
  duration can be longer.
- Each child retains its own condition, timeout, retry, polling, and action behavior.

The parent result is committed only after every iteration succeeds. A failure exposes no partial
`steps.<parent-id>` aggregate. Commands, files, HTTP requests, containers, agents, and other
external effects completed before a failure are not rolled back.

Cancellation and fail-fast scheduling stop admitting queued iterations and wait for all active
iterations to return. Terminal progress reports how many iterations started, succeeded, and were
not run. Expansion itself checks cancellation while cloning foreach values and constructing matrix
bindings. Expr does not accept a context during expression evaluation, so cancellation is checked
immediately before and after each collection expression rather than during its evaluation.

See [Graceful shutdown](graceful-shutdown.md) for signal escalation, nested-control propagation,
timeout boundaries, atomic result commits, and partial external effects.

Parallel controls are non-interactive so iterations cannot compete for terminal input. Sequential
controls retain normal interactive behavior. Pre-supplied input, password, and choice variables
remain usable in parallel iterations.

## State and nesting

Each iteration starts from the same pre-parent state. Its child steps run sequentially and may
consume variables and outputs written by earlier children in that iteration. Those variables do
not escape the iteration. The aggregate contains child step outputs, but not iteration-local
variables.

Child IDs are local to a control body, so separate controls may both use an ID such as `build`.
A child ID may not collide with an ID in its enclosing workflow or composite action.

A control body may contain one existing `concurrent` group for a fixed set of independent child
steps. The following are not supported:

- foreach inside foreach or matrix;
- matrix inside matrix or foreach;
- foreach or matrix inside a concurrent group;
- nested concurrent groups;
- conditional blocks directly inside concurrent groups;
- directly nested conditional blocks.

Put dependent work after the inner concurrent group or after the complete control parent.

## Composite actions and required files

Required step fragments can appear inside foreach and matrix bodies. Relative paths continue to
resolve from the file containing the `require` entry.

Composite actions may also be children. Their `with` values and typed input expressions can use
the active foreach or matrix bindings. The `uses` source is resolved while loading the workflow,
before iterations exist, so it cannot reference `.foreach` or `.matrix`. Choose a static action
source and pass the varying value as an action input.

## Conditions, inspection, and dry runs

A parent may have an `if` condition. A skipped parent is absent from `steps`, like any other
skipped step. Child conditions can use the active binding.

`wuko tree` displays the declared control and its child block without expanding runtime values.
Likewise, `wuko run --dry-run` validates collection expressions and child definitions, then prints
each body once. It does not evaluate runtime collections or predict their expansion count.

## Limitations

- Foreach items and dynamic matrix axes must be lists or arrays; map iteration is not supported.
- Matrix is Cartesian-only. GitHub Actions-style `include` and `exclude` rules are not supported.
- Fan-out controls cannot be nested.
- Action sources are static with respect to iteration bindings.
- Expansion is capped by `max_iterations`; choose a lower workflow-specific value when the default
  of 10,000 is larger than expected.
- Parallel iterations cannot prompt interactively, and their stdout or stderr may interleave at
  write boundaries.
- Failed controls do not expose partial aggregate results.
- Wuko cannot roll back external effects from completed or concurrently running iterations.
