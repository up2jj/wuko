# Graceful shutdown

Wuko uses context cancellation to stop a running workflow. Cancellation flows from the CLI into
the engine, through workflow controls, and into every active step runner.

## Signal lifecycle

The first `SIGINT` or `SIGTERM` starts graceful shutdown:

1. The root workflow context is canceled.
2. Sequential execution stops before the next step.
3. Concurrent groups stop admitting queued children.
4. Foreach and matrix controls stop admitting queued iterations.
5. Every active child and iteration receives the canceled context.
6. Wuko waits for active work and cleanup to return.

If the workflow or action declares `finally`, Wuko detaches that cleanup list from the first
cancellation so it can run during graceful shutdown. The same 10-second process-wide budget and
second-signal force still apply. See [Finally cleanup](finally.md) for cleanup context, error data,
timeouts, and limitations.

If shutdown completes normally, the workflow is reported as canceled and the CLI exits with
status 1. Send a second signal to stop waiting and force process termination with status 130.
Wuko also forces termination after waiting 10 seconds from the first signal.

The 10-second budget applies to signal-triggered shutdown. A step or group timeout does not by
itself start that process-wide budget.

## Step termination

In-process step runners must observe `ctx.Done()` and return. Go cannot safely terminate an
individual goroutine, so a runner that ignores cancellation can outlive its step or group deadline.
A later `SIGINT` or `SIGTERM` still starts the process-wide 10-second shutdown budget.

On supported Unix platforms, shell and agent commands run in their own process group. When their
context is canceled, Wuko sends `SIGTERM` to the complete process group, waits up to two seconds,
and sends `SIGKILL` if any process remains. A descendant that deliberately starts a new session or
process group is outside the original process group and cannot be terminated by this mechanism.

Cancellation-aware waits, retry delays, polling delays, HTTP requests, Docker operations, and
built-in file or data steps return when their context is canceled.

A shell command running in a [Docker executor scope](executors.md) causes that scope's container to
be forcibly removed when its context is canceled. The scoped `finally` list then runs with the
detached cleanup context; if necessary, Wuko opens a fresh container with the same mounts and
configuration. Bind-mounted state remains available, while container-local state from the canceled
session is gone.

## Workflow controls

External workflow cancellation always takes precedence over `fail_fast`:

| Structure | Queued work after cancellation | Active work | Result commit |
| --- | --- | --- | --- |
| Sequential steps | Later steps do not start | Current step is canceled | Earlier commits are retained internally, but failed runs do not return partial state |
| `concurrent` | Queued children do not execute | All children receive cancellation | No child results from the group are committed |
| `batch` | Queued chunks do not execute | All iterations receive cancellation | No parent aggregate is committed |
| `foreach` | Queued iterations do not execute | All iterations receive cancellation | No parent aggregate is committed |
| `matrix` | Queued combinations do not execute | All iterations receive cancellation | No parent aggregate is committed |

For ordinary execution failures, `fail_fast: true` cancels active siblings and stops queued work;
`fail_fast: false` waits for every child or iteration and reports failures in declaration or
expansion order.

A batch, foreach, or matrix can contain a concurrent group. Cancellation then propagates through the
control, into each active iteration, and into all active children of its nested concurrent group.
Nested fan-out controls and fan-out controls inside concurrent groups are rejected during loading.

## Expansion limits and cancellation

Batch, foreach, and matrix accept `max_iterations`. It defaults to 10,000 and may not exceed the absolute
limit of 1,000,000:

```yaml
steps:
  - id: checks
    matrix:
      axes:
        os: [linux, darwin]
        go: vars.go_versions
      max_iterations: 100
      max_concurrency: 4
      steps:
        - id: test
          type: shell
          with:
            command: ./test-platform
            args: ["{{ .matrix.os }}", "{{ .matrix.go }}"]
```

Wuko rejects an oversized batch chunk count, foreach list, or Cartesian product before allocating
iteration state. Expansion checks cancellation while cloning batch/foreach values and constructing matrix bindings.
Expr does not accept a context during expression evaluation, so cancellation is observed
immediately before and after evaluating `batch.items`, a dynamic `batch.size`, `foreach.items`, or
a dynamic matrix axis, not during that individual expression evaluation.

`wuko tree` and `wuko run --dry-run` display the effective iteration limit without expanding
runtime values.

## Timeouts are cancellation deadlines

A configured `timeout` determines when cancellation begins; it is not a strict upper bound on
observed wall-clock duration. Wuko waits for runner cleanup after the deadline. Process-backed
steps may use up to their additional two-second termination grace period, while an in-process
runner that ignores cancellation can wait indefinitely until process shutdown is forced.

The earliest applicable deadline wins:

- a step timeout covers one attempt;
- retry `max_elapsed_time` covers attempts and retry delays;
- a concurrent timeout covers queueing and all children;
- a batch, foreach, or matrix timeout starts after expression evaluation and expansion, then covers
  queueing, iterations, retries, polling, and nested concurrent groups;
- workflow cancellation covers the complete active execution tree.

## Partial work and idempotency

Cancellation is not transactional. Commands, requests, file changes, container operations, agent
actions, and other external effects that completed before cancellation are not rolled back. A
canceled concurrent group or fan-out control keeps its result commit atomic, so successful sibling
outputs are deliberately unavailable through `steps`.

Terminal progress identifies the partial-work boundary, for example:

```text
■ Matrix checks canceled after 2.1s · 3/12 iterations started · 2 succeeded · 9 not run: context canceled
```

Design externally visible operations to be idempotent when they can run concurrently, be retried,
or be interrupted after taking effect. Use a stable retry `operation_id` as an idempotency key when
the receiving system supports deduplication.

## Runner checklist

Custom Go step runners should:

- check the supplied context before beginning expensive setup;
- select on `ctx.Done()` while blocking or waiting;
- pass the context into HTTP, process, Docker, and storage APIs;
- stop internal goroutines before returning;
- make cleanup bounded and safe to call after partial initialization;
- return `ctx.Err()` when cancellation is the reason execution stopped.
