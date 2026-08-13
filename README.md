# Wuko

Wuko is a trusted workflow runner for everyday development tasks. Local workflows are YAML files
composed from independently registered Go step packages. The built-in steps collect text and
password input, select one or more choices, run Lua automation, execute commands or inline shell,
and launch an external agent such as Codex.

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
wuko run start-task --var 'reviewers=["alice","bob"]' --env CLICKUP_TOKEN=secret
wuko run start-task --dry-run
```

`--var` attempts JSON decoding and otherwise stores a string. `--env` always stores a string.
Both flags are also accepted by `validate`, including when they are needed to resolve a remote
action reference.

`wuko list` shows the effective workflows and labels each one as `local` or `global`. Bare `wuko`
also includes shadowed definitions from other scopes.

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

### Input

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

### Password

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

### Docker

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
