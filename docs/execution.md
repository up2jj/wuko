# Execution and composition

[Back to the README](../README.md)

This guide covers the workflow-level features that connect Wuko steps: state, conditions,
concurrency, scheduling, failure policies, file composition, and remote reuse.

## State and execution order

Top-level steps run in declaration order. After a step succeeds, Wuko commits its outputs beneath
`.steps.<id>` and any variables it writes beneath `.vars`. Later sequential steps can consume that
state:

```yaml
steps:
  - id: build
    type: shell
    with: {command: ./build}

  - id: package
    type: shell
    with:
      command: ./package
      args: ["{{ .steps.build.stdout }}"]
```

Templates use `.steps` and `.vars`; Expr conditions use `steps` and `vars`. Schema version 1 has no
dependency graph or `needs` field. Failed and skipped steps commit no state. Concurrent children
share the snapshot from group entry and cannot consume sibling results.

Use the anonymous `return` control to finish the current workflow or composite action successfully
with typed Expr outputs. It may appear in sequential conditional or working-directory scopes, still
runs `finally`, and cannot appear inside concurrent or fan-out bodies. See
[Early successful return](return.md) for the complete contract and examples.

## Conditions

Use an Expr expression returning a boolean:

```yaml
- id: deploy
  type: shell
  if: vars.deploy && steps.tests.exit_code == 0
  with: {command: ./deploy}
```

Guard a consumer when the producer might have been skipped:

```yaml
- id: upload
  type: shell
  if: '"prepare" in steps'
  with:
    command: upload
    args: ["{{ .steps.prepare.path }}"]
```

The guard runs before `with` is rendered. Conditions must be boolean; missing fields and evaluation
errors fail the workflow. Use an anonymous block when several sequential steps share one guard:

```yaml
- if: vars.deploy
  steps:
    - id: build
      type: shell
      with: {command: ./build}
    - id: deploy
      type: shell
      with: {command: ./deploy}
```

The block condition is evaluated once. Its children remain in the surrounding output namespace.

## Scoped working directories

Use `working_directory` when several steps should run from the same existing directory:

```yaml
- working_directory: ./backend
  steps:
    - id: generate
      type: shell
      with: {command: go, args: [generate, ./...]}
    - id: test
      type: shell
      with: {command: go, args: [test, ./...]}
```

`working_directory` is a transparent execution-scope wrapper. It changes `.run.dir` for its
descendants without becoming a step itself. Relative nested paths resolve from the enclosing scoped
directory, and the previous scope is restored automatically when the block ends, including after
failure or cancellation. The target must already be a directory; the block neither creates nor
removes it.

Wuko does not change the process-wide working directory. It passes the scoped directory to child
steps through their execution request, so independently scoped concurrent branches are safe. Child
IDs, outputs, and variables remain in the surrounding namespace, and only executable leaf steps
appear in run statistics and progress.

Use an [executor scope](executors.md) when selected shell steps should run in a persistent Docker
container while the rest of the workflow stays local. Executor scopes share workflow state and
bind-mounted workspace files, restore local execution on exit, and may declare cleanup steps that
run before the executor session closes.

Paths may be absolute or relative and may use templates. The block path is rendered when the block
is entered, using the enclosing `.run.dir` and all state committed by earlier sequential steps:

```yaml
- working_directory: "{{ .vars.project_dir }}"
  steps:
    - working_directory: packages/api
      steps:
        - id: build_api
          type: shell
          with: {command: go, args: [build, ./...]}
```

The inner path above resolves beneath `{{ .vars.project_dir }}`. A step-level
`with.working_directory`, when supported by that step type, still applies only to that one step and
resolves relative to the active scoped `.run.dir`.

Command-backed composite-action sources are resolved while the workflow is loaded. When such an
action is inside a `working_directory` block, the block path must therefore be resolvable from the
initial workflow values; it cannot depend on earlier step outputs or active batch, foreach, or matrix
bindings. HTTPS action sources remain loadable inside runtime-resolved directory blocks, and their
internal steps receive the scoped directory when the action executes.

### Composition with controls

| Composition | Behavior |
| --- | --- |
| With `if` | The fields cannot share one wrapper; place one block inside the other. |
| Around `concurrent` | Every concurrent branch inherits the scoped directory. |
| Directly inside `concurrent` | The block is one atomic branch; its children run sequentially using one concurrency slot and commit together. |
| Around `batch`, `foreach`, or `matrix` | The directory is resolved once, then inherited by every iteration. |
| Inside `batch`, `foreach`, or `matrix` | The directory is resolved per iteration and may use the active `.batch`, `.foreach`, or `.matrix` binding. |
| Nested `working_directory` | Relative paths resolve from the enclosing scoped directory. |
| Composite actions | Internal action steps inherit the caller's active scoped directory. |
| `finally` | The scope behaves normally and is restored when the block finishes. |

Condition several scoped steps by putting the directory block inside an anonymous `if` block:

```yaml
- if: vars.build
  steps:
    - working_directory: ./backend
      steps:
        - id: build
          type: shell
          with: {command: go, args: [build, ./...]}
```

Use independent directory blocks as concurrent branches when each branch has its own sequential
work:

```yaml
- concurrent:
    max_concurrency: 2
    steps:
      - working_directory: ./backend
        steps:
          - id: generate
            type: shell
            with: {command: go, args: [generate, ./...]}
          - id: backend_tests
            type: shell
            with: {command: go, args: [test, ./...]}
      - working_directory: ./frontend
        steps:
          - id: lint
            type: shell
            with: {command: npm, args: [run, lint]}
```

The backend wrapper is one concurrent branch: `backend_tests` may consume `generate` outputs, and
the branch consumes one concurrency slot. Its state is committed atomically with the other branch
only if the group succeeds.

A directory block outside a fan-out control is resolved once:

```yaml
- working_directory: ./services
  steps:
    - id: test_services
      foreach:
        items: vars.services
        steps:
          - id: test_service
            type: shell
            with: {command: ./test-service, args: ["{{ .foreach.item }}"]}
```

Place it inside the iteration body when each iteration needs a different directory:

```yaml
- id: test_services
  foreach:
    items: vars.services
    steps:
      - working_directory: "services/{{ .foreach.item }}"
        steps:
          - id: test_service
            type: shell
            with: {command: ./test}
```

Matrix bindings work the same way:

```yaml
- id: test_packages
  matrix:
    axes:
      package: [api, worker]
      go: ["1.25", "1.26"]
    steps:
      - working_directory: "packages/{{ .matrix.package }}"
        steps:
          - id: test_package
            type: shell
            with:
              command: go
              args: [test, ./...]
              env: {GOTOOLCHAIN: "go{{ .matrix.go }}"}
```

Composite actions inherit the active scope, and cleanup may establish its own scope:

```yaml
steps:
  - working_directory: ./backend
    steps:
      - id: package
        uses: https://example.com/actions/package-v1.yaml

finally:
  - working_directory: ./backend
    steps:
      - id: cleanup
        type: shell
        with: {command: ./cleanup}
```

Working-directory wrappers preserve the normal nesting rules. They cannot be used to bypass the
restrictions on directly nested conditional, concurrent, batch, foreach, or matrix controls.

## Concurrency

Group independent work with a bounded concurrency level:

```yaml
- concurrent:
    max_concurrency: 3
    timeout: 10m
    fail_fast: true
    steps:
      - id: lint
        type: shell
        with: {command: golangci-lint, args: [run]}
      - id: test
        type: shell
        timeout: 3m
        retry: {max_attempts: 3}
        with: {command: go, args: [test, ./...]}
```

`max_concurrency` defaults to 4 and accepts 1–100. `fail_fast` defaults to true. Child results are
committed in declaration order only after the whole group succeeds. Children cannot read sibling
outputs or write the same variable. Interactive children require pre-supplied values. Directly
nested concurrent groups are not supported.

For repeated blocks, use [batch, foreach, and matrix controls](workflow-control.md).

## Timeouts and retries

`timeout` limits each attempt. `retry` repeats failures with exponential backoff:

```yaml
- id: publish
  type: shell
  timeout: 2m
  retry:
    max_attempts: 4
    initial_delay: 500ms
    backoff_multiplier: 2
    max_delay: 10s
    jitter: 0.2
    max_elapsed_time: 6m
    operation_id: "{{ .vars.release_id }}:publish"
  with: {command: ./publish}
```

Defaults are 3 total attempts, a 1-second initial delay, multiplier 2, 30-second maximum delay, and
20% jitter. `max_elapsed_time` covers attempts and retry delays. Workflow cancellation stops
immediately. Retried operations have at-least-once semantics: Wuko commits state only after success,
but cannot roll back external effects.

Process steps and Lua environment access receive `WUKO_STEP_ATTEMPT`,
`WUKO_STEP_MAX_ATTEMPTS`, and `WUKO_STEP_OPERATION_ID`. Use the operation ID as a receiving
service's idempotency key when supported.

For fixed delays and polling, see the [`wait` step](steps-automation.md#wait).

## Scheduled runs

Add `cron` to keep `wuko run` alive on a five-field schedule, or a six-field schedule with seconds
first. Set `timezone` to an IANA name; otherwise the machine's local timezone is used.

```yaml
version: 1
name: cleanup
cron: "0 0 9 * * *"
timezone: Europe/Warsaw
steps:
  - id: cleanup
    type: shell
    with: {command: ./cleanup}
```

Attempts never overlap, and missed occurrences are not replayed. Wuko reloads and validates the
workflow before every occurrence. A run or reload failure is reported and scheduling continues.
Stop the process with Ctrl+C. Cron descriptors such as `@daily` and embedded `CRON_TZ` prefixes are
not supported.

Use `--once` when another system owns the schedule and Wuko should execute immediately instead of
waiting for cron occurrences:

```sh
wuko run cleanup --once
```

## Run reporters

With no reporter flags, `wuko run` uses the `plain` reporter for terminal progress. Select one or
more reporters explicitly by repeating `--reporter`; events are delivered in declaration order:

```sh
wuko run check --reporter plain --reporter github
```

The `github` reporter requires the `GITHUB_OUTPUT` and `GITHUB_STEP_SUMMARY` files provided to a
GitHub Actions run step. It emits error annotations for located workflow diagnostics, appends a
run-statistics summary, and exports successful workflow return values. String values are preserved;
other values are compact JSON. Each value is available under its declared return name, and the
complete typed map is available as JSON under `wuko_outputs`. Output values are not written for
failed or dry runs. The name `wuko_outputs` is reserved by the GitHub reporter; rename a workflow
return value with that name before enabling the reporter.

Reporters affect presentation and integration only. They do not change interactivity, scheduling,
or exit status; pair the GitHub reporter with `--once` when GitHub owns the schedule. GitHub
environment variables do not enable the reporter implicitly.

## Splitting a workflow across files

`require` inserts steps from another local YAML file at that location:

```yaml
# workflow.yaml
version: 1
name: release
steps:
  - require: steps/prepare.yaml
  - id: publish
    type: shell
    with: {command: ./publish}
```

The required file may be a bare list or wrap the list in `steps`:

```yaml
# steps/prepare.yaml
- id: test
  type: shell
  with: {command: go, args: [test, ./...]}
- id: build
  type: shell
  with: {command: go, args: [build, ./...]}
```

Paths are relative to the file containing `require`, so fragments may require other fragments.
All expanded IDs must be unique. Cycles are rejected. Remote archives can bundle required files;
direct remote YAML files cannot.

## Remote workflows

Run a public HTTPS URL or a GitHub locator:

```sh
wuko run https://example.com/workflows/release.yaml
wuko run github:acme/wuko-workflows
wuko run github:acme/wuko-workflows@main:workflows/release.yaml
```

An HTTPS response may be YAML or a ZIP/tar.gz archive with exactly one root `wuko.yaml` or
`wuko.yml`. GitHub and direct-YAML locators contain only the selected file; use an archive when the
workflow requires companion files. Remote workflows are fetched without authentication and are
not digest-pinned in schema version 1.

## Composite actions

Invoke a Wuko-native action at a step position:

```yaml
- id: build
  uses: https://actions.example.com/v1/build
  sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  with:
    target: linux
```

The URL may return an action manifest or an archive containing one root `action.yml` or
`action.yaml`. Pin immutable releases with the SHA-256 of the exact downloaded bytes. A manifest
declares typed inputs, internal steps, and exported outputs:

```yaml
version: 1
name: build
inputs:
  target: {type: string, required: true}
outputs:
  artifact:
    description: Produced artifact
    value: steps.package.stdout
steps:
  - id: package
    type: shell
    with:
      script: ./scripts/build.sh "$1"
      args: ["{{ .inputs.target }}"]
```

Action inputs may be `string`, `boolean`, `number`, `array`, or `object`. Use `{expr: "..."}` to
pass a typed runtime expression and `{literal: value}` to disambiguate a literal object. Internal
IDs and variables are isolated; only declared outputs appear beneath the caller step ID.

Authenticated tooling can fetch an action through a direct command whose stdout is the manifest
or archive:

```yaml
- id: build
  uses:
    command: gh
    args: [api, repos/acme/wuko-actions/contents/build/action.yml, --header,
      "Accept: application/vnd.github.raw+json"]
  sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  with: {target: linux}
```

Action references are resolved before execution and therefore cannot use prior `.steps` values.
Remote actions cannot invoke another remote action in schema version 1.

## Environment and templates

Environment precedence is step environment, CLI `--env`, workflow environment, then the host
environment. When installed, `direnv` supplies the host environment for `run` and `validate`; its
applicable `.envrc` must already be allowed.

Strings use strict Go templates. Templates can read `.vars`, `.env`, `.steps`, `.workflow`,
`.run`, and action `.inputs`. See [Templates](templates.md) and
[Template, Expr, and Lua functions](template-functions.md).

## Cleanup and cancellation

A workflow or action may have one `finally` list. It runs after main success, failure, timeout, or
cancellation and can inspect structured outcome data:

```yaml
finally:
  - id: release_lock
    type: shell
    timeout: 30s
    with: {command: ./release-lock}
```

See [Finally cleanup](finally.md) for lifecycle and state rules. On the first SIGINT/SIGTERM, Wuko
cancels active work and waits; a second signal forces termination. See
[Graceful shutdown](graceful-shutdown.md) for process and control behavior.

## Inspection and debugging

Inspect without executing:

```sh
wuko validate release
wuko tree release
wuko run release --dry-run
```

Add `--debug` to trace discovery, loading, required-file expansion, action resolution, validation,
and execution:

```sh
wuko run release --debug
wuko tree --file ./workflow.yaml --debug
```

Debug configuration is compact JSON. Fields with sensitive-looking names are redacted and URL
queries are removed, but secrets embedded in ordinary command arguments, scripts, prompts, or
action inputs may still appear. Review debug logs before sharing them.
