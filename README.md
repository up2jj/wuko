# Wuko

Wuko is a trusted, local workflow runner for development tasks. Workflows are strict, versioned
YAML files made from independently registered Go step packages. They can prompt for input, work
with typed data and files, call APIs, run scripts or containers, and start coding agents.

## Features

- **Local-first workflows** — keep readable automation beside the code it changes.
- **Interactive runs** — collect text, passwords, choices, filesystem paths, and confirmations, or
  script PTY prompts before handing a live terminal to the user.
- **Local browser forms** — collect typed variables, load dynamic choices, follow live progress,
  and render explicit workflow results without changing standard runs.
- **Typed workflow state** — use variables, step outputs, JSON/TOML imports, JSONPath, semantic
  versions, templates, and expressions without converting everything to strings.
- **Useful execution controls** — conditions, early successful returns, bounded and repeating attempts, polling,
  concurrency, batch, foreach and matrix expansion, scoped working-directory blocks, scheduled runs, dry
  runs, execution trees, and guaranteed cleanup.
- **Portable operations** — use built-in HTTP, filesystem, glob, native watches, cache, change detection,
  key-value, temporary resource, and Docker steps instead of platform-specific shell commands.
- **Extensible automation** — run Lua, direct commands, inline shell, or an external agent such as
  Codex.
- **Reusable definitions** — split steps across files, compose local actions, consume public remote
  workflows, and load complete Wuko action packages from public or private GitHub repositories.
- **Workflow marketplaces** — publish self-contained workflow packages with sidecars and let users
  browse and install one or more packages interactively. See
  [Workflow marketplaces](docs/execution.md#workflow-marketplaces).
- **Visible execution** — get live progress, retry and polling details, run statistics, and
  redacted debug tracing.

## See Wuko in action

The workflow picker and an end-to-end interactive run are recorded from deterministic, offline
fixtures:

![Wuko workflow picker](docs/assets/workflow-picker.gif)

![Wuko interactive workflow](docs/assets/interactive-workflow.gif)

See [Screenshot and demo maintenance](docs/screenshots.md) for the VHS tapes, fixtures, and local
regeneration commands.

Wuko is not a GitHub Actions runtime. It is designed for local and interactive development
automation; GitHub Actions is designed for repository-hosted CI/CD. A CI job may invoke Wuko, but
the two workflow formats are separate.

A GitHub-hosted Wuko action is likewise a Wuko composite action, not a GitHub Actions action. Point
`uses.github` at a repository directory containing `action.yml` or `action.yaml`; Wuko downloads
the complete directory, including scripts, templates, and binary files:

```yaml
- id: build
  uses:
    github: acme/private-wuko-actions@main:actions/build
  with: {target: linux}
```

A scalar `uses` carries the same locator when no token is needed:

```yaml
- id: build
  uses: acme/private-wuko-actions@main:actions/build
  with: {target: linux}
```

Authentication may come from an explicit `uses.token`, `GH_TOKEN`, `GITHUB_TOKEN`, or an existing
`gh auth login` session. See [GitHub-hosted Wuko actions](docs/execution.md#github-hosted-wuko-actions).

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
wuko install https://example.com/workflows/release.yaml
wuko install https://example.com/workflows
wuko install --global https://example.com/workflows
wuko install --global github:acme/workflows@main:release.yaml
wuko uninstall release
wuko uninstall --global --yes release
```

Bare `wuko` opens a searchable picker in a terminal and shows each workflow's direct prerequisites.
Press Enter to run the selected workflow, `u` to open a declared browser form, `m` to open the
marketplace it came from, `r` to reinstall it from that marketplace, `e` to open it in the
configured editor, `p` to toggle its plain-text `[pinned]` marker, or `s` to switch between name
and recently-used sorting. Shift+Enter prints its reproducible `wuko run` command. Picker
pins, successful-run history, and the sort preference are stored globally, and unavailable
workflows are pruned.
`wuko run NAME` searches the nearest
`.wuko/workflows/` directory first, then the user workflow directories. Use `--file` for an explicit
path:

```sh
wuko run --file ./workflows/release.yaml
```

Use `-` to read a generated workflow from standard input:

```sh
./generate-workflow | wuko run --file -
```

Stdin workflows resolve `require` entries, template files, and local actions from the current
working directory. They are always non-interactive, and a scheduled stdin workflow reuses the
YAML snapshot read at startup. Use `--file ./-` to select a literal file named `-`.

See [Workflow discovery](docs/workflow-discovery.md) for search order, shadowing, and
non-interactive behavior.

`wuko install SOURCE` saves a standalone workflow under the current project’s `.wuko/workflows/`
directory. Use `--global` to save it under `~/.wuko/workflows/` instead. `SOURCE` may be a local
YAML file, an HTTPS URL, or a `github:` locator. An HTTPS repository URL, including a normal
GitHub repository URL, is also checked for a version-1 package marketplace manifest. When found,
Wuko opens a picker unless packages are selected explicitly with repeatable `--package` flags:

```sh
wuko install https://github.com/acme/wuko-marketplace
wuko install --global --package release --package lint https://github.com/acme/wuko-marketplace
```

Marketplace packages are stored under a repository-named subdirectory and preserve their complete
package tree, including JSON sidecars and local scripts. Use `wuko marketplace init` to create an
empty manifest and `wuko marketplace build` to build only new or changed package archives from
`.wuko/workflows/<package>/`. Set `package_version` in the package root `wuko.yaml` to publish a
package release version; the picker displays it separately from the workflow schema `version`.
Install and uninstall also accept the `--var`, `--var-file`, and `--env` flags for lifecycle hooks.

For example, install a marketplace directly from a GitHub repository page:

```sh
wuko install --global https://github.com/up2jj/wuko-marketplace
```

Workflows may declare optional installation lifecycle steps:

```yaml
install:
  - id: setup
    type: shell
    with: {script: echo setup}

uninstall:
  - id: cleanup
    type: shell
    with: {script: echo cleanup}
```

Install hooks run from the staged package root before the package is committed. Uninstall hooks run
after confirmation and before the complete package directory is removed. `wuko uninstall` asks for
confirmation; use `--yes` for non-interactive use. Without `--global`, uninstall targets the
current project’s workflow copy.

## GitHub Actions

Use the repository's composite action to install a pinned Wuko release and run a workflow with
GitHub error annotations, a job summary, and typed outputs:

```yaml
permissions:
  contents: read
  pull-requests: read
  actions: read

steps:
  - uses: actions/checkout@v6

  - name: Run Wuko checks
    uses: up2jj/wuko@v0.13.0
    with:
      workflow: check
      vars: |
        package=./cmd/...
        race=true
    env:
      API_TOKEN: ${{ secrets.API_TOKEN }}
```

The action automatically exports the job's `github.token` as `GH_TOKEN` while Wuko runs, so
GitHub-backed steps and direct `gh` commands work without repeating token wiring. The token keeps
exactly the access granted to the job through `permissions`; the action does not broaden it. For
example, a workflow can discover its pull request without declaring authentication:

```yaml
version: 1
name: check
steps:
  - id: pull_request
    type: github_pr
    with:
      operation: find
```

Override the job token when an integration needs different credentials, such as access to another
repository:

```yaml
- name: Run cross-repository release workflow
  uses: up2jj/wuko@v0.13.0
  with:
    workflow: release
    token: ${{ secrets.WUKO_GITHUB_TOKEN }}
```

The `outputs` output carries the workflow's complete return map as JSON, so a later step can read a
single value. Wuko writes it only for a successful run whose workflow ends in a `return`; any other
run leaves the output empty, and `fromJSON('')` fails the job. Guard the consuming step when the
return is conditional:

```yaml
- name: Build the release archive
  id: wuko
  uses: up2jj/wuko@v0.13.0
  with:
    workflow: build

- uses: actions/upload-artifact@v4
  if: steps.wuko.outputs.outputs != ''
  with:
    path: ${{ fromJSON(steps.wuko.outputs.outputs).artifact }}
```

Execution metadata is separate from workflow return values. The action writes `status`,
`execution-id`, `duration-ms`, `failed-step`, and a versioned JSON `report` before a failed Wuko
invocation exits. `execution-id` identifies the complete invocation, including failures during
discovery, loading, or validation. `status` is `succeeded`, `failed`, `timed_out`, or `canceled`.
`failed-step` is empty unless an unsuccessful top-level step with an ID was recorded.

The metadata describes the Wuko invocation, not the action step. It is empty when the action fails
before Wuko starts - a failed install, both or neither of `workflow` and `definition`, or a missing
`working-directory` - and `status` can still read `succeeded` when the invocation itself succeeded
but the run reporters failed to publish their results. Treat a non-empty `status` other than
`succeeded` as a Wuko failure, and `steps.<id>.outcome` as the authoritative step result.

Use `continue-on-error` when later steps should inspect a failed run without failing the job at
that point:

```yaml
- name: Run Wuko checks
  id: wuko
  continue-on-error: true
  uses: up2jj/wuko@v0.13.0
  with:
    workflow: check

- name: Inspect the failure
  if: steps.wuko.outcome == 'failure' && steps.wuko.outputs.status != ''
  env:
    WUKO_REPORT: ${{ steps.wuko.outputs.report }}
    WUKO_FAILED_STEP: ${{ steps.wuko.outputs.failed-step }}
  run: |
    echo "Failed step: $WUKO_FAILED_STEP"
    jq . <<<"$WUKO_REPORT"
```

Guarding on `outcome` rather than `status == 'failed'` also covers a timed-out or canceled
invocation, and the `status != ''` test skips the step when the action failed before Wuko produced
any metadata.

Without `continue-on-error`, use `if: failure()` on a following diagnostic step. Workflow outputs
remain empty on failure even though execution metadata is available.

`workflow` accepts a discovered name, a local file, an HTTPS URL, or a `github:` locator. Use
`target` to select a workflow target and `working-directory` to change the run directory. The
optional `vars` input accepts one `key=value` assignment per line. Wuko preserves JSON values
such as booleans, numbers, arrays, and objects; unquoted values are strings:

```yaml
vars: |
  package=./cmd/...
  race=true
  retries=3
  platforms=["linux","darwin"]
```

For generated or deeply nested values, pass one JSON object instead. A value whose first
non-whitespace character is `{` is loaded as a typed variable file:

```yaml
vars: |
  {
    "revision": ${{ toJSON(github.sha) }},
    "matrix": {"os":["linux","darwin"]}
  }
```

Pass secrets through the action's `env` block rather than interpolating them into variables or the
workflow definition.

For a small workflow constructed by the GitHub workflow itself, provide an inline definition:

```yaml
- name: Run inline Wuko workflow
  id: wuko
  uses: up2jj/wuko@v0.13.0
  with:
    definition: |
      version: 1
      name: checks
      steps:
        - id: tests
          type: shell
          with:
            command: go
            args: [test, ./...]
        - return:
            outputs:
              revision: 'vars.revision'
    vars: |
      revision=${{ toJSON(github.sha) }}
```

Exactly one of `workflow` and `definition` is required. Inline definitions use the working
directory as the base for relative `require` entries, templates, and local actions. The action runs
once even when the definition declares `cron`. Pin the action to a release tag or full commit SHA;
`version` can override the Wuko binary version recorded by that action revision.

When Wuko is already installed, the equivalent lower-level invocation remains available:

```yaml
- name: Run Wuko checks
  id: wuko
  run: wuko run check --once --reporter plain --reporter github
```

Write the same canonical report to a local file with `--report-json`. Wuko atomically replaces the
file when the invocation finishes, including failure paths; the parent directory must already
exist:

```sh
wuko run check --once --report-json ./wuko-report.json
```

The `github` reporter is never enabled automatically outside the composite action. With no
`--reporter` flags, `wuko run` uses only the default plain reporter.

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
    attempt:
      timeout: 5m
      max_attempts: 3
      when: 'error.exit_code == 75 || error.stderr contains "rate limit"'
      steps:
        - id: build_command
          type: shell
          with:
            command: ./build
            args: ["{{ .vars.target }}"]
    defer:
      - id: remove_build
        type: shell
        with: {command: ./remove-build}

  - id: publish
    type: shell
    if: steps.build.steps.build_command.exit_code == 0
    with:
      command: ./publish
      args: ["{{ .steps.build.steps.build_command.stdout }}"]

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

See the [Documentation](#documentation) section for detailed workflow syntax and execution
guides.

## Available steps

Each linked guide contains multiple examples for every step.

| Step | Use it to | Examples |
| --- | --- | --- |
| `tui_input` | Collect editable text or typed JSON/lists | [Interactive steps](docs/steps-interactive.md#tui_input) |
| `tui_password` | Collect masked text | [Interactive steps](docs/steps-interactive.md#tui_password) |
| `tui_choice` | Select bounded static/dynamic values with defaults or select-all | [Interactive steps](docs/steps-interactive.md#tui_choice) |
| `tui_path` | Select one or many files/directories | [Interactive steps](docs/steps-interactive.md#tui_path) |
| `tui_review` | Review plain text or a colored unified diff | [Interactive steps](docs/steps-interactive.md#tui_review) |
| `tui_table` | Browse object-backed data in a paginated table | [Interactive steps](docs/steps-interactive.md#tui_table) |
| `tui_confirm` | Collect a boolean decision | [Interactive steps](docs/steps-interactive.md#tui_confirm) |
| `set` | Assign a literal or expression result | [Data steps](docs/steps-data.md#set) |
| `assert` | Stop unless an expression is true | [Data steps](docs/steps-data.md#assert) |
| `import_vars` | Load JSON or TOML into workflow state | [Data steps](docs/steps-data.md#import_vars) |
| `decode` | Convert JSON, YAML, TOML, or lines into a typed value | [Data steps](docs/steps-data.md#decode) |
| `jsonpath` | Select values with RFC 9535 JSONPath | [Data steps](docs/steps-data.md#jsonpath) |
| `edit` | Update structured files or values with RFC 9535 JSONPath | [Data steps](docs/steps-data.md#edit) |
| `extract` | Extract typed fields from text | [Text extraction](docs/extract.md) |
| `semver` | Parse, compare, constrain, or increment versions | [Data steps](docs/steps-data.md#semver) |
| `time` | Capture, parse, adjust, and format an explicit time value | [Data steps](docs/steps-data.md#time) |
| `key_value` | Persist JSON-compatible values between runs | [Data steps](docs/steps-data.md#key_value) |
| `changed` | Detect changed files or structured inputs | [Data steps](docs/steps-data.md#changed) |
| `once` | Run a named block once per successful persisted key | [Execution and composition](docs/execution.md#idempotency-across-runs) |
| `require_tool` | Require an executable and optionally validate its version | [System steps](docs/steps-system.md#require_tool) |
| `multiplexer` | Label and annotate the current tmux, cmux, or Herdr context | [System steps](docs/steps-system.md#multiplexer) |
| `git_revision` | Read the current or a selected Git commit as structured data | [System steps](docs/steps-system.md#git_revision) |
| `git_log` | Read bounded, structured Git history for automation | [System steps](docs/steps-system.md#git_log) |
| `git_clean` | Assert that a Git working tree is clean | [System steps](docs/steps-system.md#git_clean) |
| `git_branch` | Assert local branch existence with an extensible operation schema | [System steps](docs/steps-system.md#git_branch) |
| `git_remote_branch` | Assert local remote-tracking branch existence | [System steps](docs/steps-system.md#git_remote_branch) |
| `git_branch_name` | Assert that a string is a valid Git branch name | [System steps](docs/steps-system.md#git_branch_name) |
| `git_on_branch` | Assert the repository's current branch | [System steps](docs/steps-system.md#git_on_branch) |
| `git_conventional_commit` | Create or validate a Conventional Commit message | [System steps](docs/steps-system.md#git_conventional_commit) |
| `git_commit` | Stage selected paths and create a Git commit | [System steps](docs/steps-system.md#git_commit) |
| `github_pr` | Find an open GitHub pull request from CI metadata or a Git branch | [System steps](docs/steps-system.md#github_pr) |
| `github_release` | Check GitHub repository drift since the latest stable release | [System steps](docs/steps-system.md#github_release) |
| `github_actions` | Observe one GitHub Actions run through `gh` | [System steps](docs/steps-system.md#github_actions) |
| `file` | Perform shell-independent filesystem operations | [System steps](docs/steps-system.md#file) |
| `scaffold` | Render a packaged template directory tree | [System steps](docs/steps-system.md#scaffold) |
| `glob` | Discover regular files with portable patterns | [System steps](docs/steps-system.md#glob) |
| `watch` | Wait for native filesystem events | [System steps](docs/steps-system.md#watch) |
| `log_wait` | Follow a growing log until a regex matches | [System steps](docs/steps-system.md#log_wait) |
| `temp` | Create automatically cleaned files, directories, or FIFOs | [System steps](docs/steps-system.md#temp) |
| `cache` | Restore and save directory caches | [System steps](docs/steps-system.md#cache) |
| `http` | Make structured HTTP API calls | [System steps](docs/steps-system.md#http) |
| `docker` | Run containers and manage Docker files and resources | [System steps](docs/steps-system.md#docker) |
| `shell` | Run argv commands, inline shell, or scripted PTY interactions | [Automation steps](docs/steps-automation.md#shell) |
| `process` | Run a readiness-gated, lifecycle-managed service | [Automation steps](docs/steps-automation.md#process) |
| `process_call` | Send a correlated JSONL request to one process or a process pool | [Automation steps](docs/steps-automation.md#calling-a-process) |
| `agent` | Start an external coding agent with a prompt | [Automation steps](docs/steps-automation.md#agent) |
| `lua` | Run typed in-process automation | [Automation steps](docs/steps-automation.md#lua) |

## Workflow controls

Use controls to run independent work or repeat a block over runtime data.

| Control | Use it to | Examples |
| --- | --- | --- |
| `if` | Run a step or sequential block only when an expression is true | [Execution and composition](docs/execution.md#conditions) |
| `env` | Apply an environment overlay to a block | [Execution and composition](docs/execution.md#scoped-environments) |
| `working_directory` | Run a block from an existing directory | [Execution and composition](docs/execution.md#scoped-working-directories) |
| `concurrent` | Run a bounded fixed DAG of parallel steps | [Execution and composition](docs/execution.md#concurrency) |
| `batch` | Process a runtime list in fixed-size chunks | [Workflow controls](docs/workflow-control.md#batch) |
| `foreach` | Run a block once per item in a runtime list | [Workflow controls](docs/workflow-control.md#foreach) |
| `matrix` | Run every combination of named dimensions | [Workflow controls](docs/workflow-control.md#matrix) |
| `loop` | Repeat a sequential block until an expression is true | [Workflow controls](docs/workflow-control.md#loop) |
| `try` / `catch` | Recover from a failed or timed-out sequential block | [Workflow controls](docs/workflow-control.md#try-and-catch) |
| `cancel_on` | Race a sequential body against one or more named monitors and record the winner | [Workflow controls](docs/workflow-control.md#cancel-on) |
| `attempt` | Bound, repeat, or poll a body of steps | [Execution and composition](docs/execution.md#attempts) |
| `return` | Finish successfully early and publish explicit outputs | [Early successful return](docs/return.md) |
| `defer` | Attach cleanup to a successful step | [Finally cleanup](docs/finally.md) |
| `finally` | Run workflow-level cleanup after the main phase | [Finally cleanup](docs/finally.md) |
| `install` | Run steps before an installed workflow is committed | [Workflow installation](docs/execution.md#workflow-installation) |
| `uninstall` | Run steps before an installed workflow is removed | [Workflow installation](docs/execution.md#workflow-installation) |
| `marketplace init/build` | Create or rebuild a versioned workflow marketplace manifest | [Workflow installation](docs/execution.md#workflow-marketplaces) |
| `cron` | Run a workflow on a schedule | [Execution and composition](docs/execution.md#scheduled-runs) |

## Workflow composition

| Pattern | Use it to | Examples |
| --- | --- | --- |
| `depends_on` | Run another discovered workflow first and consume its declared outputs | [Workflow prerequisites](docs/execution.md#workflow-prerequisites) |
| `targets` | Divide one workflow into named executable variants selected as `wuko run NAME TARGET` | [Workflow targets](docs/execution.md#workflow-targets) |
| `invokable: false` | Keep a workflow available as a prerequisite without allowing direct invocation | [Workflow prerequisites](docs/execution.md#workflow-prerequisites) |
| `require` | Split one workflow across files while keeping the same state and step sequence | [Splitting a workflow across files](docs/execution.md#splitting-a-workflow-across-files) |
| `uses` | Reuse a composite action with explicit inputs, isolated internals, and declared outputs | [Composite actions](docs/execution.md#composite-actions) |

See [Choosing a composition mechanism](docs/execution.md#choosing-a-composition-mechanism) for a
side-by-side comparison and examples.

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
- [Secrets](docs/secrets.md)
- [Variable imports](docs/variable-imports.md)
- [Finally cleanup](docs/finally.md)
- [Graceful shutdown](docs/graceful-shutdown.md)
- [Screenshot and demo maintenance](docs/screenshots.md)

## Trust model

Workflows and composite actions are trusted code. Lua, shell, agent, Docker, and command-based action
sources can access local resources with the permissions granted to Wuko. Review publishers, pin
immutable remote action releases with SHA-256, and do not mount the Docker socket for untrusted
workflows. Safe archive extraction is not an execution sandbox, and Wuko does not provide a
secrets store.
