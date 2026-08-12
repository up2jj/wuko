# Wuko

Wuko is a trusted local workflow runner for everyday development tasks. Workflows are YAML files
composed from independently registered Go step packages. The built-in steps prompt for text,
select one or more choices, run Lua automation, execute commands or inline shell, and launch an
external agent such as Codex.

## Install

Wuko requires Go 1.26 or newer.

```sh
go install github.com/up2jj/wuko@latest
```

During development:

```sh
go build -o wuko .
```

## Workflow discovery

`wuko run NAME` looks for `NAME.yaml` and `NAME.yml` in this order:

1. `.wuko/workflows/` in the current directory and each parent, nearest first.
2. `~/.wuko/workflows/`.
3. The platform user config directory under `wuko/workflows/`.

The first definition wins. Declaring both extensions for the same name in one directory is an
error. Referenced Lua files are relative to the workflow file; commands and Lua host operations
default to the directory from which Wuko was invoked.

```sh
wuko list
wuko validate start-task
wuko validate
wuko run start-task
wuko run start-task --var 'reviewers=["alice","bob"]' --env CLICKUP_TOKEN=secret
wuko run start-task --dry-run
```

`--var` attempts JSON decoding and otherwise stores a string. `--env` always stores a string.

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

### Prompt

```yaml
- id: task_name
  type: prompt
  with:
    variable: task_name
    message: Task name
    default: Optional default
    required: true
```

Prompts use Bubble Tea v2. A pre-supplied variable skips the UI. Non-interactive runs must supply
the variable with `--var`.

### Choice

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

### Lua

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
- `wuko.http.request({method, url, headers, body, timeout})`
- `wuko.fs.read`, `write`, `mkdir_all`, `list`, `stat`, `rename`, and `remove`
- `wuko.exec.run({command, args, env, stdin, working_directory})`

Lua outputs support nil, booleans, strings, numbers, arrays, and string-keyed objects. Cyclic and
mixed-key tables are rejected.

### Shell and agent

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

## Trust model

Workflows are trusted local code. Lua can access the network and filesystem and can start
processes; shell and agent steps also execute local programs. Review workflows before running
them. Wuko does not download remote workflows or provide a secrets store.
