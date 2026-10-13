# Getting started

[Back to the README](../README.md)

This guide walks from a first workflow to prompts, retries, cleanup, and composition. Every
section links to the detailed reference.

- [1. Your first workflow](#1-your-first-workflow)
- [2. Run and inspect it](#2-run-and-inspect-it)
- [3. Variables and step outputs](#3-variables-and-step-outputs)
- [4. Ask the user](#4-ask-the-user)
- [5. Conditions, retries, and cleanup](#5-conditions-retries-and-cleanup)
- [6. Parallel and repeated work](#6-parallel-and-repeated-work)
- [7. Compose workflows](#7-compose-workflows)
- [Where to go next](#where-to-go-next)

## 1. Your first workflow

Wuko discovers workflows in `.wuko/workflows/` in the current directory, its parents, and your
home directory. Create `.wuko/workflows/check.yaml`:

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

Every workflow has a schema `version`, a `name`, and ordered `steps`. Each step has an `id`, a
`type`, and type-specific settings under `with`. The filename stem (`check`) is how you select it.

## 2. Run and inspect it

```sh
wuko                                    # pick it from the interactive picker
wuko run check                          # run it by name
wuko run check --var package=./cmd/...  # override a variable
wuko tree check                         # see the execution tree
wuko run check --dry-run                # validate and print steps without running
wuko validate check                     # report schema, template, and expression errors
```

Validation runs before anything executes, so typos in step IDs, variables, or step types fail
fast with a source location and a suggestion. See [Validation diagnostics](validation.md) and the
[CLI cheatsheet](cli.md).

## 3. Variables and step outputs

Steps run in declaration order. A successful step publishes outputs under `.steps.<id>`, and
variables live under `.vars`. Wuko uses two small languages over that state:

- **Go templates** inside strings: `"{{ .steps.build.stdout }}"`, `"{{ .vars.target }}"`.
- **Expr expressions** in conditions and expression fields: `steps.tests.exit_code == 0`,
  `vars.target == "linux"`.

State is typed. Lists, objects, numbers, and booleans stay typed between steps, and data steps such
as `decode`, `jsonpath`, `transform`, and `semver` work on them directly:

```yaml
- id: services
  type: decode
  with: {format: json, path: services.json}

- id: healthy
  type: transform
  with:
    from: steps.services.value
    operations:
      - filter: item.healthy
      - map: item.name
      - sort
```

See [Templates](templates.md), [Template and expression functions](template-functions.md),
[Data steps](steps-data.md), and [Variable imports](variable-imports.md).

## 4. Ask the user

Interactive steps prompt in the terminal and store the answer in a variable. Supplying the variable
with `--var` skips the prompt, so the same workflow also runs unattended:

```yaml
- id: environment
  type: tui_choice
  with:
    variable: environment
    message: Select the deployment environment
    choices:
      - {label: Staging, value: staging, default: true}
      - {label: Production, value: production}

- id: approval
  type: tui_confirm
  with:
    variable: approved
    message: Deploy to {{ .vars.environment }}?
    default: false
```

There are also text, password, path, diff-review, table, and dashboard steps, plus optional
[browser forms](forms.md) for `wuko ui`. See [Interactive steps](steps-interactive.md).

## 5. Conditions, retries, and cleanup

This release workflow retries a flaky build, attaches cleanup to it, publishes conditionally, and
always runs a final cleanup:

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

- `if` skips a step unless its expression is true. See [Conditions](execution.md#conditions).
- `attempt` bounds, retries, or polls a body of steps. See [Attempts](execution.md#attempts).
- A failed step stops ordinary execution. `try`/`catch` recovers explicitly. See
  [Workflow controls](workflow-control.md#try-and-catch).
- Cleanup attached with `defer` is registered after its owner succeeds and runs in reverse owner
  order before `finally`. See [Finally cleanup](finally.md) and
  [Graceful shutdown](graceful-shutdown.md).
- `cron` runs the workflow on a schedule; `--once` ignores it. See
  [Scheduled runs](execution.md#scheduled-runs).
- `return` finishes early and publishes typed outputs. See [Early successful return](return.md).

## 6. Parallel and repeated work

```yaml
- concurrent:
    max_concurrency: 4
    steps:
      - {id: deps, type: shell, with: {command: go, args: [mod, download]}}
      - {id: lint, needs: [deps], type: shell, with: {command: golangci-lint, args: [run]}}
      - {id: test, needs: [deps], type: shell, with: {command: go, args: [test, ./...]}}

- id: builds
  matrix:
    axes:
      os: [linux, darwin]
      arch: [amd64, arm64]
    steps:
      - id: build
        type: shell
        with: {script: "GOOS={{ .matrix.os }} GOARCH={{ .matrix.arch }} go build ./..."}
```

`concurrent` runs a fixed dependency graph; `foreach`, `batch`, and `matrix` repeat a block over
runtime data; `loop` repeats until a condition holds. See [Concurrency](execution.md#concurrency)
and [Workflow controls](workflow-control.md).

## 7. Compose workflows

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
stop dependent workflows. Wuko also supports `run_workflow` steps, named `targets`, `require` to
split one workflow across files, and reusable composite actions with `uses`, including actions
hosted in GitHub repositories:

```yaml
- id: build
  uses: acme/private-wuko-actions@main:actions/build
  with: {target: linux}
```

See [Choosing a composition mechanism](execution.md#choosing-a-composition-mechanism),
[Workflow prerequisites](execution.md#workflow-prerequisites), and
[GitHub-hosted Wuko actions](execution.md#github-hosted-wuko-actions).

Execution providers can add read-only roots across templates, expressions, Lua, actions, and
dependencies. Under GitHub Actions, Wuko automatically exposes `.github`; see
[Execution provider contexts](templates.md#execution-provider-contexts).

## Where to go next

- Browse the [demo gallery](gallery.md) for recorded workflows you can copy.
- Look up any step or control in the [reference](reference.md).
- Run workflows in CI with the [GitHub Action](github-actions.md), or locally as
  [Git hooks](git-hooks.md).
- Read the [security model](security.md) before running workflows from others.
