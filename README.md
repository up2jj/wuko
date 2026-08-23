# Wuko

Wuko is a trusted, local workflow runner for development tasks. Workflows are strict, versioned
YAML files made from independently registered Go step packages. They can prompt for input, work
with typed data and files, call APIs, run scripts or containers, and start coding agents.

## Features

- **Local-first workflows** — keep readable automation beside the code it changes.
- **Interactive runs** — collect text, passwords, choices, filesystem paths, and confirmations in
  the terminal.
- **Local browser forms** — collect typed variables, load dynamic choices, follow live progress,
  and render explicit workflow results without changing standard runs.
- **Typed workflow state** — use variables, step outputs, JSON/TOML imports, JSONPath, semantic
  versions, templates, and expressions without converting everything to strings.
- **Useful execution controls** — conditions, early successful returns, retries, timeouts, polling,
  concurrency, batch, foreach and matrix expansion, scoped working-directory blocks, scheduled runs, dry
  runs, execution trees, and guaranteed cleanup.
- **Portable operations** — use built-in HTTP, filesystem, glob, native watches, cache, change detection,
  key-value, temporary resource, and Docker steps instead of platform-specific shell commands.
- **Extensible automation** — run Lua, direct commands, inline shell, or an external agent such as
  Codex.
- **Reusable definitions** — split steps across files, compose local actions, and consume public
  remote workflows or pinned remote actions.
- **Visible execution** — get live progress, retry and polling details, run statistics, and
  redacted debug tracing.

Wuko is not a GitHub Actions runtime. It is designed for local and interactive development
automation; GitHub Actions is designed for repository-hosted CI/CD. A CI job may invoke Wuko, but
the two workflow formats are separate.

## Install

On macOS:

```sh
brew install --cask up2jj/tap/wuko
```

With Go 1.26 or newer:

```sh
go install github.com/up2jj/wuko@latest
```

For repository development:

```sh
just build
just test
```

Run `just hooks` once to install the prek hooks. Use `prek run --all-files` to run every hook and
`just snapshot` to build local release archives.

## Quick start

Create `.wuko/workflows/check.yaml`:

```yaml
version: 1
name: check
description: Run the project checks

vars:
  package: ./...

steps:
  - id: tests
    type: shell
    with:
      command: go
      args: [test, "{{ .vars.package }}"]

  - id: summary
    type: shell
    with:
      command: printf
      args: ["tests exited with %s\n", "{{ .steps.tests.exit_code }}"]
```

Then discover, inspect, validate, or run it:

```sh
wuko
wuko list
wuko tree check
wuko validate check
wuko run check
wuko ui check
wuko run check --var package=./cmd/...
wuko run check --dry-run
```

Bare `wuko` opens a searchable picker in a terminal and shows each workflow's direct prerequisites.
Press Enter to run the selected workflow, `u` to open a declared browser form, or Shift+Enter to
print its reproducible `wuko run` command. `wuko run NAME` searches the nearest
`.wuko/workflows/` directory first, then the user workflow directories. Use `--file` for an explicit
path:

```sh
wuko run --file ./workflows/release.yaml
```

See [Workflow discovery](docs/workflow-discovery.md) for search order, shadowing, and
non-interactive behavior.

## GitHub Actions

Wuko can report a run through GitHub Actions without changing the workflow format. Reporters are
explicit and repeatable: `plain` keeps the normal progress log, while `github` adds error
annotations, a job summary, and step outputs. GitHub supplies the output and summary files to every
run step.

```yaml
- name: Run Wuko checks
  id: wuko
  run: wuko run check --once --reporter plain --reporter github
  env:
    API_TOKEN: ${{ secrets.API_TOKEN }}

- uses: actions/upload-artifact@v4
  with:
    path: ${{ steps.wuko.outputs.artifact }}
```

`--once` runs a workflow immediately even when it declares `cron`, leaving GitHub responsible for
the schedule. Values produced by a successful workflow `return` are exported by name. Wuko also
exports the complete typed output map as JSON under `wuko_outputs`. The GitHub reporter is never
enabled automatically; omit all `--reporter` flags to use only the default plain reporter.

## Workflow building blocks

Every workflow has a schema version, name, and ordered steps. It may also define a description,
variables, environment values, templates, a cron schedule, and cleanup steps:

```yaml
version: 1
name: release
description: Build and publish a release
cron: "0 9 * * 1-5"
timezone: Europe/Warsaw

vars:
  target: linux

env:
  API_TOKEN: "{{ .env.API_TOKEN }}"

steps:
  - id: build
    type: shell
    timeout: 5m
    retry:
      max_attempts: 3
    with:
      command: ./build
      args: ["{{ .vars.target }}"]
    defer:
      - id: remove_build
        type: shell
        with: {command: ./remove-build}

  - id: publish
    type: shell
    if: steps.build.exit_code == 0
    with:
      command: ./publish
      args: ["{{ .steps.build.stdout }}"]

finally:
  - id: cleanup
    type: shell
    with:
      command: ./cleanup
```

Steps run in declaration order. A successful step publishes outputs under `.steps.<id>` for Go
templates and `steps.<id>` for expressions. Variables live under `.vars`/`vars`. A failed step
stops ordinary execution. Cleanup attached with `defer` is registered after its owner succeeds,
then runs in reverse owner order before `finally`.

A workflow can require other discovered workflows and consume their declared typed outputs:

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

Prerequisites form sequential chains, shared prerequisites run once per invocation, and failures
stop dependent workflows. See [Workflow prerequisites](docs/execution.md#workflow-prerequisites)
for output contracts, validation rules, scheduling behavior, and runnable examples.

For workflow syntax and execution behavior, see:

- [Execution and composition](docs/execution.md) — prerequisites, conditions, concurrency,
  scheduling, waits, retries, required files, remote reuse, progress, and debugging.
- [Executor scopes](docs/executors.md) — mix local shell steps with persistent Docker sessions.
- [Workflow controls](docs/workflow-control.md) — batch, foreach, and matrix expansion.
- [Early successful return](docs/return.md) — finish workflows and actions with explicit outputs.
- [Finally cleanup](docs/finally.md) and [graceful shutdown](docs/graceful-shutdown.md).
- [Templates](docs/templates.md) and [template, Expr, and Lua functions](docs/template-functions.md).
- [Variable imports](docs/variable-imports.md).
- [Browser forms](docs/forms.md) — typed fields, dynamic pre-run data, live progress, and results.

## Available steps

Each linked guide contains multiple examples for every step.

| Step | Use it to | Examples |
| --- | --- | --- |
| `tui_input` | Collect editable text or typed JSON/lists | [Interactive steps](docs/steps-interactive.md#tui_input) |
| `tui_password` | Collect masked text | [Interactive steps](docs/steps-interactive.md#tui_password) |
| `tui_choice` | Select bounded static/dynamic values with defaults | [Interactive steps](docs/steps-interactive.md#tui_choice) |
| `tui_path` | Select one or many files/directories | [Interactive steps](docs/steps-interactive.md#tui_path) |
| `tui_review` | Review plain text or a colored unified diff | [Interactive steps](docs/steps-interactive.md#tui_review) |
| `tui_confirm` | Collect a boolean decision | [Interactive steps](docs/steps-interactive.md#tui_confirm) |
| `set` | Assign a literal or expression result | [Data steps](docs/steps-data.md#set) |
| `assert` | Stop unless an expression is true | [Data steps](docs/steps-data.md#assert) |
| `import_vars` | Load JSON or TOML into workflow state | [Data steps](docs/steps-data.md#import_vars) |
| `jsonpath` | Select values with RFC 9535 JSONPath | [Data steps](docs/steps-data.md#jsonpath) |
| `extract` | Extract typed fields from text | [Text extraction](docs/extract.md) |
| `semver` | Parse, compare, constrain, or increment versions | [Data steps](docs/steps-data.md#semver) |
| `key_value` | Persist JSON-compatible values between runs | [Data steps](docs/steps-data.md#key_value) |
| `changed` | Detect changed files or structured inputs | [Data steps](docs/steps-data.md#changed) |
| `require_tool` | Require an executable and optionally validate its version | [System steps](docs/steps-system.md#require_tool) |
| `file` | Perform shell-independent filesystem operations | [System steps](docs/steps-system.md#file) |
| `glob` | Discover regular files with portable patterns | [System steps](docs/steps-system.md#glob) |
| `watch` | Wait for native filesystem events | [System steps](docs/steps-system.md#watch) |
| `log_wait` | Follow a growing log until a regex matches | [System steps](docs/steps-system.md#log_wait) |
| `temp` | Create automatically cleaned files, directories, or FIFOs | [System steps](docs/steps-system.md#temp) |
| `cache` | Restore and save directory caches | [System steps](docs/steps-system.md#cache) |
| `http` | Make structured HTTP API calls | [System steps](docs/steps-system.md#http) |
| `docker` | Run containers and manage Docker files and resources | [System steps](docs/steps-system.md#docker) |
| `shell` | Run argv commands or inline shell | [Automation steps](docs/steps-automation.md#shell) |
| `agent` | Start an external coding agent with a prompt | [Automation steps](docs/steps-automation.md#agent) |
| `lua` | Run typed in-process automation | [Automation steps](docs/steps-automation.md#lua) |
| `wait` | Delay or poll another step | [Automation steps](docs/steps-automation.md#wait) |

## Workflow controls

Use controls to run independent work or repeat a block over runtime data.

| Control | Use it to | Examples |
| --- | --- | --- |
| `concurrent` | Run a fixed set of independent steps in parallel | [Execution and composition](docs/execution.md#concurrency) |
| `batch` | Process a runtime list in fixed-size chunks | [Workflow controls](docs/workflow-control.md#batch) |
| `foreach` | Run a block once per item in a runtime list | [Workflow controls](docs/workflow-control.md#foreach) |
| `matrix` | Run every combination of named dimensions | [Workflow controls](docs/workflow-control.md#matrix) |

## Common workflow patterns

Choose the composition mechanism by the boundary you need:

| Pattern | Use it to | Examples |
| --- | --- | --- |
| `depends_on` | Run another discovered workflow first and consume its declared outputs | [Workflow prerequisites](docs/execution.md#workflow-prerequisites) |
| `invokable: false` | Keep a workflow available as a prerequisite without allowing direct invocation | [Workflow prerequisites](docs/execution.md#workflow-prerequisites) |
| `require` | Split one workflow across files while keeping the same state and step sequence | [Splitting a workflow across files](docs/execution.md#splitting-a-workflow-across-files) |
| `uses` | Reuse a composite action with explicit inputs, isolated internals, and declared outputs | [Composite actions](docs/execution.md#composite-actions) |

See [Choosing a composition mechanism](docs/execution.md#choosing-a-composition-mechanism) for a
side-by-side comparison and examples.

For example, a dependency-only producer remains available to `wuko list`, `wuko validate`, and
`wuko tree`, but cannot be selected by bare `wuko`, `wuko run`, or `wuko ui`:

```yaml
version: 1
name: build-artifacts
invokable: false
outputs:
  artifact: {type: string, value: steps.build.path}
steps:
  - id: build
    type: shell
    with: {command: ./build.sh}
```

Split a long workflow at the point where the steps should be inserted:

```yaml
steps:
  - require: steps/checks.yaml
  - require: steps/release.yaml
```

Run independent work concurrently:

```yaml
steps:
  - concurrent:
      max_concurrency: 2
      steps:
        - id: lint
          type: shell
          with: {command: golangci-lint, args: [run]}
        - id: test
          type: shell
          with: {command: go, args: [test, ./...]}
```

Load initial variables and override them from the command line:

```sh
wuko run release --var-file defaults.toml --var-file local.json
wuko run release --var target=darwin --env API_TOKEN=secret
```

Run a public remote workflow:

```sh
wuko run https://example.com/workflows/release.yaml
wuko run github:acme/wuko-workflows@v1.2.3:release.yaml
```

See the [ClickUp task agent example](docs/clickup-task-example.md) for a complete workflow that
fetches a task, creates a branch, and launches Claude Code or Codex.

## Agent skills

Wuko can install its bundled skills for supported coding-agent CLIs found on `PATH`:

```sh
wuko agent list
wuko agent install claude
wuko agent install
```

Claude skills are installed under `~/.claude/skills/`; Codex skills are installed under
`~/.agents/skills/`. Reinstalling replaces the bundled skill files.

## Documentation

- [Execution and composition](docs/execution.md)
- [ClickUp task agent example](docs/clickup-task-example.md)
- [Interactive steps](docs/steps-interactive.md)
- [Data steps](docs/steps-data.md)
- [System steps](docs/steps-system.md)
- [Automation steps](docs/steps-automation.md)
- [Filesystem operation reference](docs/filesystem-operations.md)
- [Docker operation reference](docs/docker-operations.md)
- [Workflow controls](docs/workflow-control.md)
- [Early successful return](docs/return.md)
- [Workflow discovery](docs/workflow-discovery.md)
- [Templates](docs/templates.md)
- [Template, Expr, and Lua functions](docs/template-functions.md)
- [Variable imports](docs/variable-imports.md)
- [Finally cleanup](docs/finally.md)
- [Graceful shutdown](docs/graceful-shutdown.md)

## Trust model

Workflows and composite actions are trusted code. Lua, shell, agent, Docker, and command-based action
sources can access local resources with the permissions granted to Wuko. Review publishers, pin
immutable remote action releases with SHA-256, and do not mount the Docker socket for untrusted
workflows. Safe archive extraction is not an execution sandbox, and Wuko does not provide a
secrets store.
