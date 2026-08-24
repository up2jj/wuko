# Execution and composition

[Back to the README](../README.md)

This guide covers the workflow-level features that connect Wuko steps: state, conditions,
concurrency, scheduling, failure policies, file composition, and remote reuse.

## Choosing a composition mechanism

Wuko offers three deliberately different ways to compose automation. Choose based on the boundary
you need, not merely where the YAML lives:

| Mechanism | Use it when | Boundary and data flow |
| --- | --- | --- |
| `depends_on` | Another discovered workflow is a prerequisite | Runs the prerequisite first in its own workflow state; only declared typed workflow outputs cross through `.dependencies` |
| `require` | One workflow is too large for one file | Inserts the fragment's steps into the current workflow; steps share the same IDs, variables, environment, state, and execution flow |
| `uses` | A step-sized capability should be reusable behind a stable interface | Invokes a composite action with declared inputs and outputs; internal steps and variables are isolated from the caller |

Use `depends_on` when the producer makes sense as an independently discoverable workflow—for
example, a build workflow that a release workflow must run first. Dependencies form a validated
execution graph, have their own `finally`, and do not expose internal variables or step results:

```yaml
depends_on:
  build: build-artifacts

steps:
  - id: publish
    type: shell
    with:
      command: ./publish.sh
      args: ["{{ .dependencies.build.artifact }}"]
```

Use `require` only to organize one logical workflow across files. It is structural inclusion, not a
call: the inserted steps behave as if they were written at the `require` location and can consume
earlier workflow state directly:

```yaml
steps:
  - id: prepare
    type: shell
    with: {command: ./prepare.sh}
  - require: steps/build.yaml
  - id: publish
    type: shell
    with: {command: ./publish.sh}
```

Use `uses` when callers should pass explicit inputs and receive explicit outputs without seeing the
implementation's internal state. A local action is reusable by multiple workflows but is not
itself discovered or run as a workflow:

```yaml
steps:
  - id: build
    uses: ../actions/build
    with: {target: linux}
  - id: publish
    type: shell
    with:
      command: ./publish.sh
      args: ["{{ .steps.build.artifact }}"]
```

As a quick rule: choose `depends_on` for orchestration between discovered workflows, `require` for
file organization within one workflow, and `uses` for reusable encapsulated behavior. Do not use
`require` to simulate an action interface, or wrap a simple file split in a separate dependency.

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

Templates use `.steps` and `.vars`; Expr conditions use `steps` and `vars`. Steps remain sequential
and have no `needs` field; workflow-level prerequisites are described below. Failed and skipped
steps commit no state. Concurrent children share the snapshot from group entry and cannot consume
sibling results.

Use the anonymous `return` control to finish the current workflow or composite action successfully
with typed Expr outputs. It may appear in sequential conditional or working-directory scopes, still
runs `finally`, and cannot appear inside concurrent or fan-out bodies. See
[Early successful return](return.md) for the complete contract and examples.

## Workflow prerequisites

Use `depends_on` when one discovered workflow must finish successfully before another starts. The
mapping key is a local alias and the value is a workflow name resolved through the normal project
and user discovery order:

```yaml
version: 1
name: release

depends_on:
  build: build-artifacts

steps:
  - id: publish
    type: shell
    with:
      command: ./publish.sh
      args: ["{{ .dependencies.build.artifact }}"]
```

The prerequisite declares the values it exports. Output names and dependency aliases are
identifiers, and output values must match one of `string`, `boolean`, `number`, `array`, or
`object`:

```yaml
version: 1
name: build-artifacts
invokable: false

outputs:
  artifact:
    type: string
    description: Path to the release archive
    value: steps.package.path
  publishable:
    type: boolean
    value: steps.package.exit_code == 0

steps:
  - id: package
    type: shell
    with: {command: ./package.sh}
```

Set `invokable: false` when a producer exists only as a prerequisite. It remains discoverable by
`depends_on` and available to `wuko list`, `wuko validate`, and `wuko tree`, but direct `wuko run`
and `wuko ui` selectors—including file and remote selectors—reject it. Omitting `invokable` keeps
the default directly invokable behavior.

Use `.dependencies.<alias>.<output>` in Go templates and
`dependencies.<alias>.<output>` in Expr:

```yaml
- id: publish
  type: shell
  if: dependencies.build.publishable
  with:
    command: ./publish.sh
    args: ["{{ .dependencies.build.artifact }}"]
```

Dependencies may form chains. If `release` depends on `package`, and `package` depends on `build`,
the execution order is `build`, `package`, `release`. Shared prerequisites in a diamond run once per
root invocation. Independent prerequisites run sequentially in alias order. Cycles, missing
workflows, and references to undeclared outputs fail before execution begins.

Only direct dependency outputs are visible. A workflow that needs a transitive prerequisite's
output must also declare that workflow directly; deduplication prevents a second run. Workflows do
not share variables, step results, environment mutations, cleanup state, or statistics. Invocation
`--var`, `--var-file`, and `--env` overrides are applied independently to every workflow.

Declared output expressions are evaluated after ordinary steps and `finally` finish successfully.
An early `return` in a workflow with an output contract must supply exactly the declared names and
matching types:

```yaml
outputs:
  artifact: {type: string, value: steps.build.path}
  cached: {type: boolean, value: "false"}

steps:
  - return:
      outputs:
        artifact: steps.restore.path
        cached: "true"
    if: steps.restore.hit
  - id: build
    type: shell
    with: {command: ./build.sh}
```

Dependency values are runtime state. They may be used by step templates, step and control
conditions, controls, and workflow output expressions. They cannot determine top-level `env`, a
composite action `uses` source, or another field resolved while loading the workflow.

`validate` and `tree` resolve and check the complete graph without executing prerequisites and may
inspect a dependency-only workflow directly. `run --dry-run` is still a direct invocation and
rejects a dependency-only root. A prerequisite's own `cron` is ignored when it is invoked by
another workflow. A scheduled root reloads and executes its dependency graph for every occurrence.

`tree` renders the requested workflow as the root and nests the complete transitive chain beneath
`depends_on`. Aliases that differ from their workflow names show both values. In a diamond, the
first occurrence is expanded and later occurrences are marked `shared; shown above`.

```sh
wuko validate release
wuko tree release
wuko run release --dry-run
wuko run release --var target=linux --env MODE=production
```

Runnable chain, diamond, conditional-output, early-return, and scheduling examples live in
[`examples/dependencies/`](../examples/dependencies/).

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

## Git worktrees

Use a named `worktree` block when a group of steps should run against an isolated detached checkout
of the repository containing the active `.run.dir`:

```yaml
- id: apply_fix
  worktree:
    revision: HEAD
    publish:
      branch: "wuko/fix-{{ .vars.issue }}"
    steps:
      - id: edit
        type: shell
        with: {command: ./apply-fix.sh}
      - id: commit
        type: shell
        with:
          command: sh
          args: [-c, "git add -A && git commit -m 'Apply automated fix'"]
```

The block runs `git worktree add --detach` from the repository discovered through `.run.dir`. Its
children inherit the worktree as `.run.dir`, so shell steps, Docker steps, Docker Buildx contexts,
and relative file operations see the checked-out files. The `docker` step must still bind-mount
`{{ .run.dir }}` when the container needs access to the host worktree.

Nested steps may create multiple commits. When `publish.branch` is present, Wuko requires at least
one new commit, refuses to overwrite an existing branch, and creates the result branch at the final
`HEAD` without checking it out. The parent output contains `path`, `revision`, `base_commit`,
`commit`, `branch`, and `published`.

The worktree is removed, and Git worktrees are pruned, when the block exits. Cleanup also runs after
nested failures and cancellation. Attached `defer` cleanup for nested steps runs before the
worktree is removed. Without `publish`, the block is suitable for isolated build and test work.

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
direct remote YAML files cannot. Required steps remain part of the caller: they do not declare
inputs or outputs, receive a separate state, or run independently. Use a composite action instead
when reuse needs an explicit interface, or a workflow dependency when the included work needs a
separate prerequisite state.

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

Invoke a Wuko-native action at a step position. A local reference may name an action directory:

```yaml
- id: build
  uses: ../actions/build
  with:
    target: linux
```

The directory must contain exactly one root `action.yml` or `action.yaml`. A direct manifest path
is also accepted:

```yaml
- id: build
  uses: ../actions/build/action.yaml
  with: {target: linux}
```

Local paths are resolved from the file containing the `uses` declaration. A reference inside a
required step fragment therefore resolves from that fragment's directory, including when the
fragment belongs to a packaged remote workflow. Caller `working_directory` scopes and the process
working directory do not change this path base. `../` traversal is allowed as long as the rendered
reference remains relative.

Local actions execute in place. Their manifest directory is the action root, so internal steps and
file-backed templates can use companion files such as `scripts/build.sh` or
`templates/message.tmpl`. Local action archives and `sha256` are not supported; local actions are
trusted workspace content.

An HTTPS reference fetches an action instead:

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

The available source forms are:

| `uses` form | Resolves from | Companion files | `sha256` |
| --- | --- | --- | --- |
| Relative file or directory | Declaring workflow or fragment | Read from the local action root | Rejected |
| HTTPS URL | Network | Only when the response is an action archive | Optional and recommended |
| Command object | Active load-time run directory | Only when stdout is an action archive | Optional and recommended |

Scalar references may use load-time templates based on workflow variables and environment values.
After rendering they must be either a valid HTTPS URL or a relative local path. Action references
are resolved before execution and therefore cannot use prior `.steps` values or active
`.batch`, `.foreach`, or `.matrix` bindings. Composite actions cannot invoke another composite
action in schema version 1.

An action is not a child workflow: it follows the action manifest schema, receives only declared
inputs, isolates its internal IDs and variables, and exports only declared outputs. Use
`depends_on` to compose discovered workflows and `require` to split one workflow's steps without an
input/output boundary.

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
