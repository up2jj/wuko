# Defer and finally cleanup

Attach `defer` to a resource-creating step to keep its cleanup beside it. Workflows and composite
actions may also declare one `finally` list for general cleanup. Registered defers run first, then
`finally`, after main execution succeeds, fails, times out, returns early, or is canceled.

```yaml
version: 1
name: deploy
steps:
  - id: acquire_lock
    type: shell
    with: {command: ./acquire-lock}
    defer:
      - id: release_lock
        type: shell
        timeout: 30s
        with: {command: ./release-lock}
  - id: deploy
    type: shell
    with: {command: ./deploy}

finally:
  - id: report
    type: shell
    with: {command: ./report-result}
```

`defer` is a non-empty step list attached to an ordinary `type` or `uses` step. It is registered
only after the owner succeeds and commits its outputs. Skipped, failed, timed-out, and canceled
owners do not register cleanup. Defer groups unwind in reverse owner order, while entries within
one group run in declaration order.

Deferred and `finally` cleanup contains ordinary Wuko steps. They may use conditions, retries,
timeouts, composite actions, concurrent groups, batch, foreach, and matrix subject to their normal
schema and nesting rules.
Workflow cleanup may also use local required step files; action manifests retain their existing
restriction against `require`. Step IDs are unique across `steps`, attached defers, and `finally`.
Attached defer is supported in sequential workflow/action scopes, conditional and working-directory
blocks, and executor bodies. It is rejected in concurrent or fan-out bodies and during cleanup.

## Lifecycle

Wuko loads, resolves, and validates the complete workflow before running it. If that succeeds, the
runtime lifecycle is:

1. Run main `steps` in declaration order until they finish or execution stops.
2. Record the immutable main outcome as `finally.status`.
3. Detach cleanup from the main cancellation and unwind registered defer groups.
4. Run `finally` entries in declaration order.
5. Commit each successful cleanup result before starting the next entry.
6. If a cleanup entry fails, record its error and continue with the remaining entries.
7. Return the main error followed by defer and finally errors in execution order.

A failed main step still stops later main steps. It does not prevent cleanup. A failed cleanup step
does not prevent later cleanup steps, but it does make an otherwise successful workflow fail.
Cleanup never recovers from or suppresses the main error.

The first workflow cancellation is removed from the cleanup context so cleanup can perform useful
work after `SIGINT`, `SIGTERM`, or a parent timeout. Individual cleanup step and control timeouts
still apply. In the CLI, the existing graceful-shutdown budget covers the complete shutdown: a
second signal forces termination, and Wuko also forces termination 10 seconds after the first
signal.

## State available during cleanup

Cleanup uses the same `vars`, `env`, `steps`, `workflow`, `run`, and action `inputs` roots as main
steps. It sees:

- initial variables and environment values;
- outputs and variable writes committed by successful main steps; and
- outputs and variable writes committed by earlier successful cleanup steps.

Failed, timed-out, and canceled steps do not commit their returned outputs or variable writes.
Skipped steps also remain absent from `steps`. Cleanup must guard optional values just like any
other conditional consumer. A failed concurrent group or fan-out control keeps its normal atomic
commit boundary, so successful sibling or iteration results are also unavailable to cleanup.

The `finally` root exists only while cleanup is running:

```yaml
finally:
  - id: report
    type: shell
    if: finally.status != "succeeded"
    with:
      command: ./report-failure
      args: ["{{ .finally.status }}"]
```

`finally.status` is the main phase's immutable terminal status: `succeeded`, `failed`, `timed_out`,
or `canceled`. It does not change when cleanup fails. Check `finally.errors` when a later cleanup
step needs to know whether an earlier cleanup entry failed.

## Structured errors

`finally.errors` is an ordered list. Each record contains:

```yaml
status: failed
message: attempt 1/1 failed: deployment failed
step_id: deploy
step_type: shell
```

`step_id` and `step_type` are empty when an error cannot be attributed to a specific step, such as
cancellation between sequential steps. Concurrent and fan-out failures can add multiple records in
deterministic declaration or expansion order.

At the start of cleanup, the list describes terminal main failures. When a deferred or final
cleanup entry fails, its records are appended before Wuko evaluates the next cleanup entry.
Conditions should select stable metadata rather than matching `message`, whose wording is
informational and may change.

```yaml
finally:
  - id: remove_failed_deployment
    type: shell
    if: >-
      finally.status == "failed" &&
      any(finally.errors, {.step_id == "deploy" && .status == "failed"})
    with: {command: ./remove-deployment}

  - id: report_cleanup_failure
    type: shell
    if: any(finally.errors, {.step_id == "remove_failed_deployment"})
    with: {command: ./report-cleanup-failure}
```

Templates can inspect the same records:

```yaml
with:
  script: |
    {{- range .finally.errors }}
    printf '%s: %s\n' '{{ .step_id }}' '{{ .status }}'
    {{- end }}
```

Error messages may contain operational details. Avoid printing `.message` into externally visible
logs, especially when a command or remote service could have included sensitive response text.

Lua steps receive the root as `wuko.finally`:

```lua
if wuko.finally.status ~= "succeeded" then
  local failure = wuko.finally.errors[1]
  wuko.output("failed_step", failure.step_id)
end
```

## Composite actions and retries

Action manifests use the same syntax:

```yaml
version: 1
name: temporary-environment
outputs:
  log:
    value: steps.collect_log.stdout
steps:
  - id: start
    type: shell
    with: {command: ./start-environment}
finally:
  - id: collect_log
    type: shell
    with: {command: ./collect-log}
  - id: stop
    type: shell
    with: {command: ./stop-environment}
```

Action defer and finally cleanup run once for each attempt of the caller's `uses` step. A cleanup
failure fails that attempt, so an outer retry can replay the action's main steps and cleanup. Action
outputs are evaluated only after cleanup succeeds; successful cleanup outputs can therefore feed
an action output expression.

Cleanup should be idempotent because retries, cancellation, and partial external effects can cause
it to run after partially completed work. Prefer cleanup commands that tolerate an already-removed
resource.

## Timeouts, controls, and inspection

Every cleanup step retains its ordinary `timeout` and `retry` behavior. Concurrent, batch, foreach,
and matrix cleanup entries retain their existing group timeout, `fail_fast`, concurrency, interaction,
nesting, and iteration limits. A failed concurrent or fan-out cleanup entry does not commit its
aggregate result, but Wuko continues to the next top-level cleanup entry.

`wuko validate` validates attached defers and final sections. `wuko tree` and `wuko run --dry-run`
display each defer beneath its owner and show `finally` as a separate branch. Validation and dry-run
never execute cleanup.

## Limitations

- Cleanup does not run for YAML decoding, workflow loading, action resolution, template setup, or
  validation failures because runtime execution has not begun.
- Failed, timed-out, canceled, and skipped steps expose no uncommitted outputs or variable writes.
- `finally` cannot recover from or suppress the main error.
- A workflow or action supports one top-level `finally` list. An
  [executor block](executors.md#cleanup-and-persistence) may additionally have a scoped `finally`
  list that runs inside its executor before the session closes. Attached defers in an executor run
  before that list and before the session closes. There is no recursive defer, ordinary per-step
  `finally`, multiple-clause selection, or `catch` construct.
- Error messages are unstable informational text and should not be used as condition keys.
- There is no block-level cleanup timeout. Bound cleanup with step and control timeouts. CLI cleanup
  may still be forcibly terminated by the shutdown budget or a second signal.
- External effects are not transactional and are not rolled back automatically.
- Action cleanup runs per caller attempt, so retrying after cleanup failure can repeat successful
  main action effects.
- Cleanup controls retain all existing nesting, concurrency, non-interactive, and iteration
  limitations.
