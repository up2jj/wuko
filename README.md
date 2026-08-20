# Wuko

Wuko is a trusted workflow runner for everyday development tasks. Local workflows are YAML files
composed from independently registered Go step packages. The built-in steps collect text and
password input, select one or more choices, import typed variables, run Lua automation, execute
commands or inline shell, and launch an external agent such as Codex.

## Features

- Discover local and global workflows from an interactive terminal picker or with `wuko list`.
- Define strict, versioned YAML workflows with variables, environment values, Go templates, and
  conditional steps.
- Run steps sequentially or concurrently, with retries, timeouts, dry runs, execution trees, live
  progress, and run statistics.
- Use built-in input, password, choice, confirm, set, `import_vars`, HTTP, file, key-value, Lua,
  shell, agent, and Docker steps.
- Split workflows across local files with `require`, or reuse remote workflows and composite
  actions from HTTPS URLs and GitHub locators.
- Integrate with `direnv`, import JSON or TOML variable files, and pass values explicitly with
  `--var` and `--env`.
- Automate development tasks locally while keeping workflow code visible and reviewable.

## Table of Contents

- [Features](#features)
- [Install](#install)
- [Agent skill installation](#agent-skill-installation)
- [GitHub Actions comparison](#github-actions-comparison)
- [Workflow discovery](#workflow-discovery)
  - [ClickUp task agent example](#clickup-task-agent-example)
  - [Remote workflows](#remote-workflows)
- [Workflow schema](#workflow-schema)
  - [Splitting steps across files](#splitting-steps-across-files)
  - [Conditional steps](#conditional-steps)
  - [Concurrent steps](#concurrent-steps)
  - [Retries and execution timeouts](#retries-and-execution-timeouts)
  - [Execution progress and statistics](#execution-progress-and-statistics)
  - [Debug tracing](#debug-tracing)
  - [Remote composite actions](#remote-composite-actions)
  - [Available steps](#available-steps)
    - [Input](#input)
    - [Password](#password)
    - [Choice](#choice)
    - [Confirm](#confirm)
    - [Set](#set)
    - [Import variables](#import-variables)
    - [HTTP](#http)
    - [File](#file)
    - [Key-value stores](#key-value-stores)
    - [Lua](#lua)
    - [Shell and agent](#shell-and-agent)
    - [Docker](#docker)
- [Trust model](#trust-model)

## Install

### Homebrew

On macOS, install the latest released cask from the Wuko Homebrew tap:

```sh
brew install --cask up2jj/tap/wuko
```

Upgrade an existing installation with:

```sh
brew upgrade --cask wuko
```

### Go

Wuko requires Go 1.26 or newer. Install the latest version with:

```sh
go install github.com/up2jj/wuko@latest
```

During development:

```sh
just build
just test
```

Run `just hooks` once to install the prek pre-commit and pre-push hooks. Use
`prek run --all-files` to run every hook manually, and `just snapshot` to build
local release archives without publishing them.

## Agent skill installation

Wuko discovers supported coding-agent CLIs available on `PATH` and can install its bundled skills
for them. Use `wuko agent list` to see the discovered agents and their skill directories. Install
skills for one agent with `wuko agent install claude`, or install them for every discovered agent
with `wuko agent install`.

Claude skills are installed under `~/.claude/skills/`; Codex skills are installed under
`~/.agents/skills/`. Installation is repeatable and replaces the bundled skill files in those
directories.

## GitHub Actions comparison

Wuko and [GitHub Actions](https://docs.github.com/en/actions) both describe automation as
workflows made up of steps, but they target different environments:

| | Wuko | GitHub Actions |
| --- | --- | --- |
| Primary use | Repeatable development workflows run locally | Hosted CI/CD triggered by GitHub events or manual dispatch |
| Runtime | The machine where the `wuko` command is run | GitHub-hosted or self-hosted runners |
| Definition format | Wuko's strict, versioned YAML schema | GitHub Actions workflow syntax under `.github/workflows/` |
| Built-in operations | Interactive prompts, typed values, HTTP, files, Lua, shell, agents, Docker, and key-value stores | Jobs, runners, marketplace actions, matrices, artifacts, and GitHub integrations |
| Secrets and context | Explicit CLI/environment values and the host environment | GitHub-provided contexts, secrets, variables, and permissions |

Wuko is **not compatible with GitHub Actions**. A GitHub Actions workflow cannot be run directly
by Wuko, and Wuko workflow files cannot be used as GitHub Actions workflows. In particular, Wuko
does not implement GitHub Actions events, job syntax, runner labels, marketplace actions, or
GitHub Actions expressions. Choose Wuko for local, interactive development automation; choose
GitHub Actions for repository-hosted CI/CD. They can still be used together—for example, a GitHub
Actions job can install Wuko and invoke a Wuko workflow as a command—but the workflow definitions
remain separate.

## Workflow discovery

`wuko` opens an interactive workflow picker when connected to a terminal. The picker includes
every local and global workflow, supports typing to filter, and supports arrow-key navigation.
Press Enter to print the matching `wuko run` command; press Esc to cancel. A shadowed workflow is
printed with `wuko run --file PATH` so the selected definition remains unambiguous.

When output is redirected or otherwise non-interactive, bare `wuko` prints all workflows as
tab-separated name, scope, description, and path fields.

`wuko run NAME` looks for `NAME.yaml` and `NAME.yml` in this order:

1. `.wuko/workflows/` in the current directory and each parent, nearest first.
2. `~/.wuko/workflows/`.
3. The platform user config directory under `wuko/workflows/`.

The first definition wins. Declaring both extensions for the same name in one directory is an
error. Referenced Lua files are relative to the workflow file; commands and Lua host operations
default to the directory from which Wuko was invoked.

```sh
wuko
wuko list
wuko validate start-task
wuko validate
wuko run start-task
wuko run --file ./path/to/start-task.yaml
wuko run https://example.com/workflows/release.yaml
wuko run github:acme/wuko-workflows@v1.2.3:release.yaml
wuko run start-task --var-file defaults.toml --var-file local.json
wuko run start-task --var 'reviewers=["alice","bob"]' --env CLICKUP_TOKEN=secret
wuko run start-task --dry-run
wuko tree start-task
wuko tree --file ./path/to/start-task.yaml
```

`--var-file` imports the top-level object from a JSON file or root table from a TOML file. The flag
is repeatable: files merge from left to right, replacing entire top-level variables rather than
recursively merging nested objects. Relative paths resolve from the invocation directory.
Workflow `vars` provide the defaults, variable files override them, and `--var` overrides every
file value. `--var` attempts JSON decoding and otherwise stores a string; `--env` always stores a
string. `run`, `validate`, and `tree` accept all three flags, including when values are needed to
resolve a remote action reference.

Variable files use Viper's configuration decoding. Imported keys, including nested keys, are
case-insensitive and normalized to lowercase; dotted keys have Viper's nested-key meaning. The
JSON or TOML document itself is the variable object—do not wrap it in a `vars` field. Other Viper
formats are not currently accepted, but can be added through the shared variable-file loader.

For complete file-shape, precedence, nesting, Lua, and runtime-step semantics, see
[Variable imports](docs/variable-imports.md).

`wuko list` shows the effective workflows and labels each one as `local` or `global`. Bare `wuko`
also includes shadowed definitions from other scopes.

For the complete discovery rules and command behavior, see
[Workflow discovery](docs/workflow-discovery.md).

### ClickUp task agent example

[`examples/clickup-task.yaml`](examples/clickup-task.yaml) is a complete task-start workflow. It
asks for a ClickUp task ID, downloads its Markdown description, creates a task branch, and launches
either Claude Code or Codex with a prepopulated implementation prompt.

Set a ClickUp personal API token in `CLICKUP_TOKEN`. Native task IDs work without any other ClickUp
configuration. For a custom task ID, also set `CLICKUP_TEAM_ID` to the numeric Workspace ID; the
workflow then sends ClickUp's `custom_task_ids` and `team_id` query parameters. The selected agent
CLI must already be installed and authenticated. Run the workflow from the repository root.

```sh
export CLICKUP_TOKEN=pk_...
# Required only for custom task IDs:
export CLICKUP_TEAM_ID=123456

wuko run --file ./examples/clickup-task.yaml
```

The task brief is written to `.wuko/context/<task-id>.md`, which this repository ignores. The
branch is named `<task-id>_<lowercase-task-name-slug>`. Before creating it, the workflow rejects a
dirty working tree, an invalid generated name, or an existing local or remote branch. It does not
reuse ClickUp MCP authentication because Wuko performs the HTTP request before starting an agent;
the API token is used only in the request's `Authorization` header.

`wuko tree NAME` prints the workflow's steps as a tree. Remote composite actions are expanded to
show their internal steps, and conditional steps include their `if` expression. Like `run`, `tree`
accepts `--file`, HTTPS and GitHub workflow locators, and repeatable `--var-file`, `--var`, and
`--env` flags for resolving templated action references.

```text
release
├── test (shell)
└── build (uses https://actions.example.com/build/v1)
    ├── compile (shell)
    └── package (shell) if: inputs.package
```

### Remote workflows

`wuko run` accepts a public HTTPS URL or a GitHub shorthand in the form
`github:owner/repo[@ref][:path]`. A bare GitHub locator uses the repository's default branch and
`wuko.yaml` at the repository root. For example:

```sh
wuko run github:acme/wuko-workflows
wuko run github:acme/wuko-workflows@main:workflows/release.yaml
```

HTTPS sources may return a YAML workflow directly or a ZIP/tar.gz archive. Archives must contain
exactly one root-level `wuko.yaml` or `wuko.yml`; companion files are available relative to that
workflow. Direct YAML URLs and GitHub file locators contain only the selected workflow file, so
workflows needing companion files should use an HTTPS archive. Remote workflows are downloaded
without authentication, materialized in a temporary directory, and removed after the run. Remote
workflow bytes are not pinned by a digest in this version.

## Workflow schema

Every workflow is strict and versioned:

```yaml
version: 1
name: example
description: Example workflow
vars:
  greeting: Hello
env:
  API_TOKEN: "{{ .env.API_TOKEN }}"
steps: []
```

Strings use Go templates with `missingkey=error`. Template roots are `.vars`, `.env`, `.steps`,
`.workflow.name`, `.workflow.dir`, and `.run.dir`. Lua source itself is not templated; use its
typed `args` instead.

Environment precedence is step environment, CLI `--env`, workflow environment, then the host
environment. Environment values are not shown by dry-run output.

### Splitting steps across files

Use a `require` entry to insert steps from another local YAML file at that position. Paths must be
relative and are resolved from the file containing the entry, so step files can require other step
files:

```yaml
# workflow.yaml
version: 1
name: release
steps:
  - require: steps/prepare.yaml
  - id: publish
    type: shell
    with:
      command: ./publish
```

The required file may be a bare step list:

```yaml
# steps/prepare.yaml
- id: test
  type: shell
  with:
    command: go
    args: [test, ./...]

- id: build
  type: shell
  with:
    command: go
    args: [build, ./...]
```

It may instead wrap that list in a `steps` field. A `require` entry cannot contain other step
fields. All expanded steps are validated as one workflow, so IDs must remain unique across every
file. Cyclic requirements are rejected. Remote workflow archives can require files bundled in the
archive; direct remote YAML workflows have no companion files to require.

When [direnv](https://direnv.net/) is installed, `wuko run` and `wuko validate` use the environment
it exports for the invocation directory as the host environment. Wuko honors direnv's trust model,
so the applicable `.envrc` must already be approved with `direnv allow`. To load a local `.env`,
put this in that project's `.envrc`:

```sh
dotenv_if_exists
```

If direnv is not installed or no `.envrc` applies, Wuko uses its process environment unchanged.

### Conditional steps

Add `if` to run a step only when an [Expr](https://expr-lang.org/docs/language-definition)
expression evaluates to a boolean `true`:

```yaml
vars:
  deploy: false
steps:
  - id: tests
    type: shell
    with:
      command: go
      args: [test, ./...]

  - id: deploy
    type: shell
    if: 'vars.deploy && steps.tests.exit_code == 0'
    with:
      command: ./deploy
```

Conditions use the same data roots as templates, without a leading dot: `vars`, `env`, `steps`,
`workflow.name`, `workflow.dir`, and `run.dir`. Quote non-trivial expressions so YAML treats them
as a single string. Literal booleans are also accepted: `if: true` always runs and `if: false`
always skips. There is no truthiness; every condition must evaluate to a boolean. Missing fields
and evaluation errors fail the workflow.

A skipped step does not write outputs or variables and is absent from `steps`. Guard a dependent
step with map membership to make skipping cascade safely:

```yaml
vars:
  prepare: false
steps:
  - id: prepare
    type: lua
    if: vars.prepare
    with:
      source: |
        wuko.set_var("artifact_path", "/tmp/artifact.zip")

  - id: upload
    type: shell
    if: '"prepare" in steps'
    with:
      command: upload
      args: ["{{ .vars.artifact_path }}"]
```

The guard is evaluated before `with` is rendered, so `upload` never tries to resolve
`artifact_path` when `prepare` was skipped. Use `"name" in vars` when only variable availability
matters, or `get(vars, "name")` to read an optional value. If a skipped step would have overwritten
an existing variable, the existing value remains unchanged. Dry-run validates and prints guards
but does not evaluate them because preceding step outputs are not available.

### Concurrent steps

Wrap independent steps in `concurrent` to run them with a bounded amount of parallelism:

```yaml
steps:
  - concurrent:
      max_concurrency: 3
      timeout: 10m
      fail_fast: true
      steps:
        - id: lint
          type: shell
          with:
            command: golangci-lint
            args: [run]

        - id: test
          type: shell
          timeout: 3m
          retry:
            max_attempts: 3
          with:
            command: go
            args: [test, ./...]

  - id: package
    type: shell
    with:
      command: ./package
      args: ["{{ .steps.test.stdout }}"]
```

`max_concurrency` defaults to 4 and must be between 1 and 100. `fail_fast` defaults to `true`:
after one child exhausts its retries, running siblings are canceled and queued children are not
started. Set it to `false` to let every child finish and report all failures. The optional group
`timeout` covers the complete group, including time waiting for a concurrency slot, attempts, and
retry delays. The workflow's cancellation and the earliest group or child deadline always win.

Every child evaluates its `if`, templates, and action inputs against the same state snapshot taken
before the group starts. A child cannot consume a sibling's outputs or variables, regardless of
which one finishes first. Child outputs keep their normal workflow-wide IDs and become available
after the complete group succeeds, as shown by `.steps.test` above. Put dependent work after the
group. Step IDs must remain unique across the workflow, including required step files and
concurrent children. Directly nested concurrent groups are not supported.

Each child owns its normal `timeout`, `retry`, backoff, `max_elapsed_time`, and `operation_id`.
Retrying one child never restarts a successful sibling, and retry is not supported on the group
itself. Results are committed atomically after all children succeed and in declaration order. If
two children try to write the same workflow variable, the group fails instead of choosing a
timing-dependent winner. External effects such as commands, files, requests, containers, and
agents cannot be rolled back.

Concurrent children are non-interactive to prevent multiple terminal prompts from competing for
stdin. Input, password, and choice steps can still be used when their variables are supplied in
advance. Docker TTY input is not attached inside a concurrent group. Child stdout and stderr are
safe for concurrent writes, but output from different children can interleave at write boundaries.

### Retries and execution timeouts

Use `timeout` to limit each attempt and `retry` to repeat a failed step with exponential backoff:

```yaml
steps:
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
    with:
      command: ./publish
```

`max_attempts` includes the first attempt. A retry block defaults to 3 attempts, a 1-second initial
delay, a multiplier of 2, a 30-second maximum delay, and 20% jitter. `max_elapsed_time` is optional
and limits the combined time spent executing attempts and waiting between them. `timeout` is useful
without retries too. A timed-out attempt may be retried, while workflow cancellation stops
immediately. Step runners must honor context cancellation for timeout enforcement.

An attempt fails when its runner returns an error. Non-zero shell, agent, and Docker exits are
errors. Lua code must raise a Lua error when application-level results such as an HTTP error status
or `wuko.exec.run` error should trigger a retry.

Wuko commits step outputs and variables only after a successful attempt, but it cannot roll back
external effects from commands, HTTP requests, files, containers, or agents. Retry-enabled steps
therefore have at-least-once execution semantics. `operation_id` is a stable, unique identifier for
one logical operation; it does not make the operation idempotent. When omitted, Wuko generates an
ID that is stable across automatic attempts in the current invocation. Provide an explicit ID when
the same identity must survive a Wuko restart. A command can pass the operation ID to a receiving
service as an idempotency key, for example in an `Idempotency-Key` HTTP header, when that service
supports deduplication. Operation IDs are not credentials and should not contain secrets.

Process-based steps and Lua's `wuko.env` receive `WUKO_STEP_ATTEMPT`,
`WUKO_STEP_MAX_ATTEMPTS`, and `WUKO_STEP_OPERATION_ID`. These names are reserved and override
workflow or step environment values. Retrying a composite `uses` step replays the entire action,
including previously successful inner steps; prefer retry policies on individual inner steps when
the action can define them.

### Execution progress and statistics

`wuko run` writes execution progress to standard error, leaving standard output available for step
output. The display uses durable status lines so command output and interactive prompts do not
corrupt an animated spinner. Color is enabled on a terminal and disabled for redirected output,
`TERM=dumb`, or when `NO_COLOR` is set.

```text
◆ Workflow release · 2 steps
→ [1/2] publish (shell) · up to 3 attempts · timeout 2m0s
  • attempt 1/3 started
  ⏱ attempt 1/3 timed out after 2m0s: deadline exceeded
  ↻ retrying with attempt 2/3 in 500ms
  • attempt 2/3 started
✓ [1/2] publish succeeded after 2m1.2s · 2 attempts
⊘ [2/2] notify skipped
✓ Workflow release succeeded in 2m1.2s · 1 succeeded · 1 skipped · 2 attempts · 1 retry · 1 timeout · 500ms retry wait
```

Every workflow, step, and attempt records its start time, duration, and terminal status. Run
summaries also count successful, failed, skipped, canceled, and unstarted steps; attempts; retries;
timeouts; and time spent waiting to retry. Composite-action progress is indented and gets a nested
summary. Concurrent groups get their own start and finish lines, with child progress indented below
the group. Go callers can read the completed summary from `engine.State.Stats` and subscribe to the
same serialized lifecycle through `engine.Options.Progress` without parsing terminal text.

### Debug tracing

Pass the persistent `--debug` flag to `wuko`, `list`, `run`, `validate`, or `tree` to trace workflow
discovery, loading, required-file expansion, action resolution, validation, and execution to
standard error. Normal progress and workflow output are unchanged when debug tracing is disabled.

```sh
wuko run release --debug
wuko --debug validate release
wuko tree --file ./workflow.yaml --debug
```

Debug lines include elapsed time, the workflow or action source, YAML line and column, step ID and
type, lifecycle phase, duration, and the most specific error. Required fragments retain their own
locations; remote workflows and actions use query-free logical locators instead of temporary
materialization paths. Lua syntax errors are reported during validation before any steps run,
while Lua runtime errors identify the failed attempt and follow the step's retry policy.

Rendered step configuration is emitted as compact JSON. Environment values and fields whose names
look sensitive—such as passwords, secrets, tokens, credentials, API keys, authorization, and
private keys—are replaced with `<redacted>`, and URL query strings are removed. Each configuration
record is limited to 4 KiB. Debug output can still expose sensitive data embedded under an
innocuous field name, including command arguments, scripts, prompts, or action inputs; review it
before sharing logs.

### Remote composite actions

A workflow step can invoke a Wuko-native composite action over HTTPS. The action is downloaded and
validated before any workflow step runs, then its internal steps run sequentially at the `uses`
position. The caller waits for the entire action before continuing:

```yaml
vars:
  action_release: v1
steps:
  - id: prepare
    type: lua
    with:
      source: |
        wuko.output("artifacts", {"app.zip", "checksums.txt"})

  - id: build
    uses: "https://actions.example.com/{{ .vars.action_release }}/build"
    sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    with:
      target: linux
      artifacts:
        expr: steps.prepare.artifacts

  - id: publish
    type: shell
    with:
      command: publish
      args: ["{{ .steps.build.artifact }}"]
```

A scalar `uses` must resolve to an HTTPS URL without embedded credentials. It can use the pre-run
template roots `.vars`, `.env`, `.workflow.name`, `.workflow.dir`, and `.run.dir`; it cannot depend
on `.steps`, because actions are resolved before execution. URL filenames and response content
types are not significant. Query strings are omitted from errors.

An action can instead be fetched by a local command that writes the manifest or archive bytes to
standard output. This is useful for authenticated tools such as `gh`:

```yaml
steps:
  - id: build
    uses:
      command: gh
      args:
        - api
        - --method
        - GET
        - "repos/acme/wuko-actions/contents/build/action.yml?ref=v1.2.3"
        - --header
        - "Accept: application/vnd.github.raw+json"
    sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    with:
      target: linux
```

`command` and every argument support the same pre-run templates as HTTPS references. The command
runs directly, without an implicit shell, in `.run.dir` with the effective workflow environment
and a 30-second timeout. Use `command: sh` with `args: [-c, "..."]` only when shell syntax such as a
pipeline is intentionally required. A non-zero exit, oversized output, or invalid stdout fails
loading; stderr is included in failure diagnostics, while command arguments are omitted. Identical
rendered command sources are executed once per workflow load.

The optional `sha256` is a 64-character hexadecimal digest of the exact downloaded bytes. Put an
action release in its URL, such as a SemVer tag or commit. An immutable URL plus `sha256` is fully
pinned; a floating URL such as `/v1/build` intentionally receives compatible updates. The
manifest's own `version` describes its Wuko schema, not its published release.

A direct URL may return an action manifest:

```yaml
version: 1
name: build
description: Build an application

inputs:
  target:
    type: string
    required: true
  artifacts:
    type: array
    default: []

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
      working_directory: "{{ .workflow.dir }}"
```

Inputs are declared as `string`, `boolean`, `number`, `array`, or `object`. Fixed YAML values and
templated strings can be passed directly. `{expr: "..."}` evaluates an Expr expression without
converting arrays, objects, numbers, or booleans to strings. Use `{literal: value}` when a literal
object would otherwise be mistaken for this wrapper. Missing required inputs, unknown inputs, and
type mismatches are errors.

Internal steps receive `.inputs`/`inputs`, the caller's effective environment, and the caller's run
directory. Their step IDs and variables are isolated from the caller. Only manifest outputs are
exported beneath `.steps.<caller-step-id>`; each output `value` is an Expr expression over the
internal `inputs`, `vars`, `env`, and `steps` state.

The URL may instead return a ZIP or gzip-compressed tar archive with exactly one root
`action.yml` or `action.yaml`. Companion files are extracted to an isolated temporary action
directory, so relative paths in internal steps resolve inside the package. For a direct manifest,
relative paths retain normal workflow behavior and resolve from the caller workflow directory.
Archive extraction rejects traversal paths, links, special files, duplicates, and oversized
packages. Remote actions cannot invoke another remote action in schema version 1.

### Available steps

#### Input

Use `input` when the initial text should remain editable. The optional `value` is rendered when
the step starts, so it can prepopulate text from an earlier step. The required `message` is shown
directly above the field and should tell the user what to enter:

```yaml
- id: release_name
  type: input
  with:
    variable: release_name
    message: Enter the release name
    value: "{{ .steps.suggest_name.value }}"
    required: true
```

#### Password

Use `password` for masked text entry. Its required `message` is displayed above the masked field:

```yaml
- id: credentials
  type: password
  with:
    variable: api_token
    message: Enter the API token
    required: true
```

Input and password steps write their resulting value to `steps.<id>.value` and to the configured
variable. The value is text unless an input modifier converts it. A pre-supplied variable skips
the UI, and non-interactive runs must provide it with `--var`.

Both steps support the same optional `validation` block inside `with`:

```yaml
with:
  validation:
    min_length: 3
    max_length: 40
    pattern: '^[a-z][a-z0-9-]+$'
    message: Use 3–40 lowercase letters, digits, or hyphens
```

Lengths count Unicode characters. `pattern` uses Go regular-expression syntax and is optional;
anchor it with `^` and `$` when the whole value must match. `message` replaces the default error
for any failed rule. Invalid rule configurations fail workflow validation before execution.

Input can convert validated text before storing it. Split on a Go regular-expression pattern:

```yaml
- id: reviewers
  type: input
  with:
    variable: reviewers
    message: Enter comma-separated reviewers
    modifiers:
      trim: true
      split: ','
```

Entering `alice, bob` writes `["alice", "bob"]` to both `.steps.reviewers.value` and
`.vars.reviewers`. `trim` removes leading and trailing Unicode whitespace before validation and
conversion. When combined with `split`, it also trims every resulting item. Empty fields are
preserved.

Alternatively, deserialize one JSON value:

```yaml
- id: metadata
  type: input
  with:
    variable: metadata
    message: Enter metadata as JSON
    modifiers:
      trim: true
      json: true

- id: describe
  type: shell
  with:
    command: printf
    args:
      - 'project=%s first_tag=%s retries=%s\n'
      - "{{ .vars.metadata.project }}"
      - "{{ index .steps.metadata.value.tags 0 }}"
      - "{{ .steps.metadata.value.retries }}"

- id: deploy
  type: shell
  if: vars.metadata.deploy
  with:
    command: ./deploy
    args: ["{{ .vars.metadata.project }}"]
```

For example, entering
`{"project":"wuko","tags":["go","cli"],"retries":3,"deploy":true}` makes the object available
as both `.vars.metadata` and `.steps.metadata.value`. Access object fields with dotted paths, such
as `.vars.metadata.project`; use the Go-template `index` function for array elements, such as
`{{ index .steps.metadata.value.tags 0 }}`. Conditions use the typed value directly, so
`if: vars.metadata.deploy` evaluates the JSON boolean rather than a string.

JSON preserves objects, arrays, strings, booleans, null, and numbers. Invalid JSON remains in the
interactive field with an error. `trim` can be combined with either `split` or `json`; `split` and
`json` are mutually exclusive. With `--var`, pass JSON as a string because modifiers operate on
text, for example
`--var 'metadata="{\"enabled\":true}"'`.

When `required: false`, empty input becomes an empty list for `split` and `null` for `json`.

#### Choice

Static single selection:

```yaml
- id: environment
  type: choice
  with:
    variable: environment
    message: Select environment
    choices:
      - {label: Development, value: dev}
      - {label: Production, value: prod}
```

Typed multi-selection from an earlier output:

```yaml
- id: projects
  type: choice
  with:
    variable: project_ids
    message: Select projects
    multiple: true
    from: steps.fetch.projects
    label_field: name
    value_field: id
```

Dynamic sources must be non-empty lists. Scalar items are both label and value. Object lists use
`label_field` and `value_field`, including dotted paths. Single selection writes a scalar;
multi-selection writes an ordered list.

#### Confirm

Use `confirm` for a boolean decision. `default` controls the initially selected interactive
answer; it does not silently answer a non-interactive prompt:

```yaml
- id: approval
  type: confirm
  with:
    variable: approved
    message: Deploy this release?
    default: false

- id: deploy
  type: shell
  if: vars.approved
  with:
    command: ./deploy
```

The result is written to both `.steps.approval.value` and `.vars.approved`. A pre-supplied value
must be a boolean. Supply it explicitly for non-interactive execution, for example
`wuko run release --var approved=true`. Confirm steps inside a concurrent group likewise require a
pre-supplied value.

#### Set

Use `set` to assign a typed literal or evaluate an Expr expression without dropping into Lua.
Exactly one of `value` or `expr` is required:

```yaml
- id: defaults
  type: set
  with:
    variable: deployment
    value:
      enabled: true
      retries: 3
      regions: [eu-central, us-east]

- id: artifact
  type: set
  with:
    variable: artifact_name
    expr: 'steps.release.value.version + "-" + vars.target + ".tar.gz"'
```

Expressions use the `inputs`, `vars`, `env`, `steps`, `workflow`, and `run` roots used by
conditions. The JSON-compatible result is available through both `.steps.<id>.value` and the
configured variable. Invalid expressions fail validation; missing runtime fields and
non-JSON-compatible results fail the step without committing its variable.

#### Import variables

Use `import_vars` to load JSON or TOML variables during workflow execution. Paths are rendered
when the step starts and resolve from the directory containing the owning workflow or composite
action:

```yaml
- id: configuration
  type: import_vars
  with:
    files:
      - defaults.toml
      - "environments/{{ .vars.environment }}.json"

- id: describe
  type: shell
  with:
    command: printf
    args: ["target=%s\\n", "{{ .vars.target }}"]
```

At least one file is required. Files merge from left to right with top-level replacement, then
the imported values overwrite variables already present in workflow state—including initial
`--var` values. A successful step exposes the merged object as `.steps.configuration.variables`,
its top-level size as `.steps.configuration.count`, and every key beneath `.vars`.

The same strict JSON/TOML and lowercase-key behavior as `--var-file` applies. The import is atomic:
if any file cannot be read or decoded, the step commits no outputs or variables. Retries reread
all files. Concurrent children still share their pre-group snapshot, cannot consume an import
from a sibling, and fail if multiple children write the same variable. Validation and dry-run
check the step configuration without reading its runtime files.

Relative imports work in local workflows and in remote workflow or action archives that bundle
the companion files. A direct remote YAML or action manifest contains no companion files to
import.

See [Variable imports](docs/variable-imports.md) for nested-value access from templates and Lua,
merge examples, concurrency rules, and format-extension guidance.

#### HTTP

Use `http` for structured HTTP API calls. Requests default to `GET`; successful responses default
to any `2xx` status; and response bodies default to text:

```yaml
- id: release
  type: http
  timeout: 30s
  retry:
    max_attempts: 3
  with:
    method: GET
    url: https://api.example.com/releases/latest
    headers:
      Authorization: "Bearer {{ .env.API_TOKEN }}"
    query:
      channel: stable
    response: json
    success_statuses: [200]
```

Supply at most one of `body` or `json`. `json` is encoded as JSON and adds
`Content-Type: application/json` unless the header is already present.

Supported response modes are:

- `text` (the default): exposes the raw response body as a string in `.steps.<id>.value`.
- `json`: requires exactly one JSON value and exposes its typed object, array, string, number,
  boolean, or null value in `.steps.<id>.value`.

Every response also exposes the raw body string as `body`, the integer status code as `status`, and
`headers`, whose values are lists so repeated headers are preserved. There are currently no
dedicated binary, base64, YAML, XML, form-data, file-download, or streaming response modes.

Only HTTP and HTTPS URLs with a host and without embedded user information are accepted. The
response body is limited to 10 MiB. Redirects may upgrade HTTP to HTTPS, but they may not change
host or port or downgrade HTTPS to HTTP, which prevents configured headers from crossing a trust
boundary. Top-level `timeout` and `retry` policies control cancellation and repeated attempts. A
status outside `success_statuses`, or outside `2xx` when the list is omitted, fails the step.

#### File

The `file` step provides strict filesystem operations relative to the run directory. Create a
file atomically and then make it executable:

```yaml
- id: write_script
  type: file
  with:
    operation: write
    path: scripts/release.sh
    content: |
      #!/bin/sh
      exec ./release "{{ .vars.version }}"
    overwrite: true
    mode: "0755"

- id: make_executable
  type: file
  with:
    operation: chmod
    path: scripts/release.sh
    mode: "0755"
```

Supported operations and their extra fields are:

- `read`: outputs text `content` and `size`.
- `write`: requires `content`; accepts `overwrite` and `mode`; outputs `size`, `mode`, and
  `created`.
- `copy` and `move`: require `destination`, accept `overwrite`, and output the resolved source and
  destination, `size`, and `mode`. Copy accepts regular files and preserves their permissions.
  Move also works across filesystems by staging a copy before removing the source.
- `remove`: accepts `recursive`; outputs `removed`. Missing paths are not errors.
- `mkdir`: accepts `recursive` and `mode`; outputs `created` and `mode`.
- `list`: accepts `recursive`; outputs path-sorted `entries` with `name`, relative `path`, `type`,
  `size`, `mode`, and `modified_at`.
- `stat`: outputs `exists` plus the same metadata when the path exists.
- `chmod`: requires `mode` and outputs the normalized mode.

Modes must be quoted four-digit octal strings from `"0000"` through `"0777"`; special permission
bits are not supported. New files default to `"0644"` and new directories to `"0755"`.
Overwriting a file without `mode` preserves its permissions. Chmod rejects symbolic links. Remove
rejects filesystem roots and the run directory, and a non-empty directory requires
`recursive: true`. Absolute paths remain available because Wuko workflows are trusted code, not a
filesystem sandbox.

#### Key-value stores

The `key_value` step persists JSON-compatible values between workflow runs. Every operation names
both a scope and a store. Local stores live in `.wuko/values/` beside the top-level workflow;
global stores live in the platform configuration directory under `wuko/values/`. Workflows using
the same directory, scope, and store name intentionally share values.

Values may be scalars or nested JSON-compatible objects and lists. Set and then read a complex
project configuration:

```yaml
- id: save_project
  type: key_value
  with:
    operation: set
    scope: global
    store: preferences
    key: project
    value:
      name: wuko
      enabled: true
      reviewers:
        - alice
        - bob
      deployment:
        retries: 3
        regions:
          - eu-central
          - us-east
        labels:
          team: platform
          tier: internal

- id: load_project
  type: key_value
  with:
    operation: get
    scope: global
    store: preferences
    key: project

- id: deploy_project
  type: shell
  if: steps.load_project.found && steps.load_project.value.enabled
  with:
    command: ./deploy
    args:
      - "{{ .steps.load_project.value.name }}"
      - "{{ .steps.load_project.value.deployment.retries }}"
      - "{{ index .steps.load_project.value.reviewers 0 }}"
      - "{{ index .steps.load_project.value.deployment.regions 0 }}"
      - "{{ .steps.load_project.value.deployment.labels.team }}"
```

Both `local` and `global` scopes require a safe, single-segment `store` name. Keys are non-empty
flat strings; dots have no special meaning. The four operations are:

- `get`: requires `key`; outputs `value` and `found`.
- `set`: requires `key` and an explicit `value`, including `null`; outputs the stored `value`.
- `delete`: requires `key`; outputs the previous `value` and `deleted`.
- `list`: accepts no `key` or `value`; outputs key-sorted `entries` containing `key` and `value`.

A missing key is not an error: `get` returns `value: null, found: false`, while a stored JSON null
returns `value: null, found: true`. Similarly, deleting a missing key returns `deleted: false`.
Use `.steps.<step-id>` to pass outputs to templates and `steps.<step-id>` in conditions.

Store files are plain, pretty-printed JSON objects. Wuko serializes concurrent access with a lock
and replaces files atomically so simultaneous processes do not lose updates or expose partial
JSON. Values are not encrypted; do not use these stores as a secrets vault. Add
`.wuko/values/` to the applicable `.gitignore` when local values should not be committed.
Remote top-level workflows cannot use local persistence because their files are temporary, but
they can use global stores. Composite actions inherit the caller workflow's local and global
store roots. A successful write is an external effect: it is not rolled back when a later step or
Lua statement fails, and retrying a write applies it again.

#### Lua

Use either `file` or inline `source`:

```yaml
- id: metadata
  type: lua
  with:
    file: ../scripts/metadata.lua
    args:
      task: "{{ .vars.task_name }}"
```

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

The trusted `wuko` Lua host API provides:

- `wuko.args`, `wuko.var(name)`, `wuko.set_var(name, value)`, `wuko.output(name, value)`
- `wuko.env.get(name)`, `wuko.env.all()`
- `wuko.json.encode(value)`, `wuko.json.decode(text)`
- `wuko.kv.get`, `set`, `delete`, and `list`
- `wuko.http.request({method, url, headers, body, timeout})`
- `wuko.fs.read`, `write`, `mkdir_all`, `list`, `stat`, `rename`, and `remove`
- `wuko.exec.run({command, args, env, stdin, working_directory})`

Lua outputs support nil, booleans, strings, numbers, arrays, and string-keyed objects. Cyclic and
mixed-key tables are rejected.

Lua key-value calls use the same store files and return shapes as the YAML step:

```lua
wuko.kv.set({
  scope = "global",
  store = "preferences",
  key = "project",
  value = {
    name = "wuko",
    enabled = true,
    reviewers = {"alice", "bob"},
    deployment = {
      retries = 3,
      regions = {"eu-central", "us-east"},
      labels = {team = "platform", tier = "internal"},
    },
  },
})

local project, found = wuko.kv.get({
  scope = "global",
  store = "preferences",
  key = "project",
})
local removed, deleted = wuko.kv.delete({scope = "global", store = "preferences", key = "old"})
local entries = wuko.kv.list({scope = "global", store = "preferences"})
```

`get` and `delete` each return the value followed by a boolean. `list` returns a key-sorted array
of `{key, value}` tables. Because Lua represents JSON null as `nil`, omitting `value` from
`wuko.kv.set` stores JSON null; a list entry for stored null still contains its `key`.

#### Shell and agent

Shell accepts either argv execution:

```yaml
- id: status
  type: shell
  with:
    command: git
    args: [status, --short]
```

or inline shell. Arguments follow `wuko` and are available as `$1`, `$2`, and so on:

```yaml
- id: branch
  type: shell
  with:
    script: |
      set -eu
      git switch -c "$1"
    args: ["{{ .steps.fetch.task.branch }}"]
```

Agents are regular external processes with their prompt on stdin:

```yaml
- id: codex
  type: agent
  with:
    command: codex
    args: [exec, -]
    prompt: "Work on {{ .steps.fetch.task.id }}"
```

Shell and agent output streams live and is also captured as `stdout`, `stderr`, and `exit_code`.

Shell steps can run as another local account by setting `user` to a username or numeric user ID:

```yaml
- id: identity
  type: shell
  with:
    command: id
    args: [-un]
    user: deploy
```

Changing user uses native Unix process credentials and requires Wuko to have permission to assume
that identity, which normally means running Wuko as root. Wuko does not invoke `sudo` and does not
rewrite environment variables such as `HOME` or `USER`; the selected account must also be able to
access the configured working directory and executable. Omitting `user` inherits Wuko's user.

#### Docker

The `docker` step runs one command in a temporary Docker container. The container and any anonymous
volumes it creates are removed after the step finishes. Docker must be available through the Docker
Engine API (`DOCKER_HOST` is respected by the Docker client).

```yaml
- id: tests
  type: docker
  with:
    image: golang:1.26
    command: go
    args: [test, ./...]
    working_directory: /workspace
    mounts:
      - source: "{{ .run.dir }}"
        target: /workspace
        read_only: true
    network: none
    pull: if-missing
```

`command` and `args` are passed as an argv list. Omitting `command` uses the image's default
command. `working_directory` is a path inside the container. Mount `source` paths are host paths;
relative sources are resolved against the workflow run directory, while mount `target` paths must
be absolute container paths. The step supports `env`, `user`, `platform` (`os/architecture` or
`os/architecture/variant`), `network`, `tty`, and literal `stdin` values. An explicitly configured
`stdin` value, including an empty string, is sent to the container and then closed. When `tty: true`
is used from an interactive terminal, Wuko forwards terminal input until the container exits. Wuko's
effective environment is passed through and step-level `env` overrides it.

The pull policy defaults to `if-missing`; `never`, `missing`, and `always` are also accepted. Pin
production images by digest when reproducibility matters. With `tty: true`, Docker combines the
output streams as a terminal does. Interactive workflows should be run with a terminal attached.

Docker containers created by Wuko receive management, client-host, and owner-process labels. At the
start of a later Docker step, Wuko recovers only labeled containers created from the same client host
whose owner process is no longer alive; containers from other client hosts, legacy containers without
a client-host label, and containers owned by a live Wuko process are left untouched. This covers
process crashes and forced termination on the next Wuko run without interfering with another machine
using the same remote daemon. Cleanup failures are reported and are preserved alongside the original
step error. Images and explicitly configured bind-mounted host directories are retained by design.

## Trust model

Workflows and remote actions are trusted code. Lua can access the network and filesystem and can
start processes; shell and agent steps also execute local programs. Docker steps can access any
explicitly mounted host paths and can use the configured Docker daemon. Do not mount the Docker
socket unless the workflow is trusted, because it grants control over the daemon and can amount to
host-level access. Command-based action sources also execute locally while the workflow is loading.
Review action publishers and pin immutable action releases with SHA-256 before running them. Safe
archive extraction is not an execution sandbox. Wuko does not download whole remote workflows, add
authentication headers to HTTPS action requests, or provide a secrets store.
