# Executor scopes

[Back to execution and composition](execution.md)

Wuko runs shell steps locally by default. An `executor` block temporarily selects another command
environment for its child shell steps. When the block finishes, later shell steps run locally
again.

## Block syntax

An executor block is an anonymous sequential scope:

```yaml
- executor:
    type: docker
    with:
      image: alpine:3.22
  steps:
    - id: in_container
      type: shell
      with: {command: uname, args: [-a]}
  finally:
    - id: container_cleanup
      type: shell
      with: {command: ./cleanup-inside-container}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `executor.type` | Yes | Registered executor provider. Version 1 provides `docker` and `devenv`. |
| `executor.with` | Provider-specific | Configuration used to open the session. Unknown fields are rejected. |
| `steps` | Yes | Non-empty list executed through the opened session. |
| `finally` | No | Cleanup list executed through the same session before it closes. |

The block has no `id` and publishes no block result. Its child IDs and outputs remain directly in
the surrounding `.steps` namespace. An executor block cannot also declare `type`, `uses`,
`working_directory`, `if`, `attempt`, `batch`, `foreach`, `matrix`, `concurrent`, or `with` at the
block level. Put those controls inside the block where supported.

`executor.type` is static. String values beneath `executor.with` may use templates and are rendered
when execution enters the block, so they can consume state committed by earlier sequential steps.
Validation rejects an unknown provider or invalid provider configuration before running the
workflow.

`run.os` and `run.arch` always describe the host running Wuko, not the executor target. They stay
stable inside the block. When a workflow needs the container or remote target's actual platform,
query that environment through an executor-aware step.

```yaml
version: 1
name: mixed-build

steps:
  - id: prepare
    type: shell
    with: {command: ./scripts/prepare.sh}

  - executor:
      type: docker
      with:
        image: golang:1.26
    steps:
      - id: generate
        type: shell
        with: {command: go, args: [generate, ./...]}
      - id: build
        type: shell
        with: {command: go, args: [build, -o, dist/app, ./cmd/app]}
    finally:
      - id: clean_container_cache
        type: shell
        with: {command: go, args: [clean, -cache]}

  - id: package
    type: shell
    with:
      command: ./scripts/package.sh
      args: [dist/app, "{{ .steps.build.stdout }}"]
```

Each block opens one executor session. Multiple shell steps in the same Docker block share that
container. A later executor block opens an independent session, even when it uses the same image.
Child step IDs, outputs, variables, conditions, retries, and statistics remain in the surrounding
workflow namespace.

Execution returns to the enclosing executor when the block ends. Because executor blocks cannot be
nested in version 1, that currently means returning to the local executor. A root workflow
`finally` list is therefore local; use the block's own `finally` when cleanup must run inside its
container.

Version 1 executor scopes support shell and lifecycle-managed
[`process`](steps-automation.md#process) steps, the file steps listed under
[Reading and writing files](#reading-and-writing-files), working-directory and conditional blocks, early
return, and sequential batch, foreach, or matrix controls. Other leaf steps, actions, waits, concurrent
groups, parallel fan-out, and nested executor blocks are rejected instead of running unexpectedly
on the host.

For `batch`, `foreach`, or `matrix` inside an executor block, explicitly set `max_concurrency: 1`. Every
iteration then uses the one persistent session sequentially. Transparent conditional and
working-directory blocks may wrap an executor block, and may also appear inside it. Executor blocks
cannot be placed inside a batch, foreach, or matrix body, concurrent group, or composite action.

Managed processes are stopped and joined before the executor session closes. Replicas in a
sequential fan-out start one at a time but remain alive together. Unix host signals do not address
a container exec and the Docker API can only detach from one, so a Docker-hosted service is
launched through the session's `init.command` shell: it records the service PID inside the
container and `exec`s the service, and stopping runs a second exec that signals that PID and
escalates to `SIGKILL` after `shutdown.timeout`. A session whose `init.command` is not a shell
cannot do this, and there a `process` step configuring `restart` is rejected unless it also sets
`shutdown.command`, because each restart would otherwise leave the previous instance alive.

## devenv executor

The devenv executor runs process-backed steps in a reproducible devenv environment. It supports
`shell`, `require_tool`, `devenv_task`, and the file steps below; HTTP, persistence, TUI, and Lua
steps remain host-side. A devenv shell runs on the host, so `file` and `edit` inside the block act on
the host filesystem. Devenv must be version 2.2 or newer.

```yaml
- executor:
    type: devenv
    with:
      directory: .
      profiles: [backend, testing]
      secrets:
        mode: runtime
        profile: development
        provider: keyring
  steps:
    - id: tools
      type: require_tool
      with: {tool: go}
    - id: build
      type: devenv_task
      with:
        names: [app:prepare, app:build]
        mode: before
        inputs: {target: production}
        show_output: false
        capture_limit: 1MiB
    - id: tests
      type: shell
      with: {command: go, args: [test, ./...]}
```

`profiles` is an ordered list and is emitted as repeated `--profile` flags. Automatic hostname and
user profiles remain active. If Wuko is already running inside devenv, it reuses the active root and
profile identity; a configured directory or profile set that does not match is rejected.

Invocation environment loaders and the devenv executor are separate layers. mise, asdf, or
direnv may make the `devenv` executable available and may supply an already active `DEVENV_*`
environment. The executor consumes that prepared environment, reuses a matching active devenv,
or enters `devenv shell` when inactive. Inside the block, devenv owns the child command
environment. Devenv is therefore not listed in `.run.environment_loaders`, and selecting
`--env-loader none` does not disable an explicitly declared devenv executor.

SecretSpec modes are `auto`, `runtime`, `inherit`, and `disabled`. Runtime mode invokes
`secretspec run --` for each child command and keeps secret values out of workflow state and
diagnostics. `auto` enables runtime mode when `secretspec.toml` or active SecretSpec configuration
is present.

`devenv_task` requires exactly one of `name` or `names`. The latter supplies multiple roots to one
task-graph invocation in list order. Task names must be non-blank and must not start with `-`, so a
rendered value can never reach devenv as a flag. It supports `single`, `before`, `after`, and `all` task modes;
the `inputs` map is passed as JSON to every matching root. Set `show_output: true` to forward
devenv's `--show-output` flag.

On success, `steps.<id>.value` is the typed JSON object returned by `devenv tasks run`, keyed by
the tasks that produced outputs, and `steps.<id>.value_decoded` reports whether it is published.
Decoding requires devenv's stdout to hold nothing but that one JSON document, so `value` is absent
whenever `show_output: true` forwards a task's own output, a wrapping `devenv shell` or
`secretspec run` writes to stdout, or `capture_limit` truncates the document. A task graph that
matched no task publishes an empty `value`. None of these fail the step: a zero exit means the graph
already committed its side effects, so gate on `value_decoded` rather than on `retry` or `catch`.
The raw `stdout`, `stderr`, and `exit_code` remain available, along with `stdout_truncated` and
`stderr_truncated`. `capture_limit` accepts the same binary sizes as a shell step and bounds each
captured stream independently; it defaults to `1MiB`, since the step holds both the raw stdout and
the materialized value for the rest of the run.

Task exports remain data under each task output's `devenv.env`; Wuko does not install them into the
environment of later Wuko steps. Devenv still applies exports between tasks inside the same graph.
On a failed invocation, Wuko preserves the raw process outputs for retry and catch handling but
publishes neither `value` nor `value_decoded`. Task dependencies, status checks, task caching, and transient process
cleanup remain owned by devenv.

The optional `processes` setting ensures persistent devenv processes are ready. Wuko reuses already
running processes, records only processes it starts, and stops only those owned processes when the
executor closes. If process state or ownership cannot be established, Wuko fails safely without
starting or stopping anything; it never calls broad `devenv down`.

An opt-in real-environment smoke test is available with:

```text
WUKO_SMOKE_REQUIRED=1 go test -tags devenv_smoke ./smoke
```

It requires devenv 2.2+ and the SecretSpec CLI, creates an isolated fixture, tests multiple profiles,
active-environment reuse, task inputs, runtime SecretSpec, typed task values under both disabled and
runtime secret modes, and process cleanup, and skips with a clear message when the required tools are
unavailable. The `devenv` workflow runs it nightly, which is what keeps the decoding rules above
honest: whether devenv's stdout survives the `devenv shell` and `secretspec run` wrappers is asserted
there rather than assumed. The executor's own end-to-end behaviour — argv construction, stdout
capture, and decoding — is covered on every change by `go test ./steps/devenv`, which drives a fake
devenv CLI and needs no nix.

## Docker executor

The Docker executor requires `image` and accepts `pull`, `platform`, `network`, `user`,
`resources`, `workspace`, `mounts`, and `init`:

```yaml
- executor:
    type: docker
    with:
      image: node:24
      pull: if-missing
      network: none
      user: "1000:1000"
      resources:
        cpus: 1.5
        memory: 512MiB
        pids: 128
      workspace:
        target: /workspace
        read_only: false
      mounts:
        - {type: volume, source: npm-cache, target: /npm-cache}
      init:
        command: /bin/sh
        args: [-c, "trap 'exit 0' TERM INT; while :; do sleep 86400; done"]
  steps:
    - id: test
      type: shell
      with: {command: npm, args: [test]}
```

| Setting | Default | Meaning |
| --- | --- | --- |
| `image` | — | Required image reference. |
| `pull` | `if-missing` | `never`, `if-missing` (or `missing`), or `always`. |
| `platform` | Docker default | OCI platform such as `linux/amd64`. |
| `network` | Docker default | Docker network mode or network name. |
| `user` | Image default | Default user for the container and its shell commands. A shell step's `with.user` overrides it. |
| `resources.cpus` | Unlimited | Positive decimal CPU limit with up to nine fractional digits, such as `1.5`. |
| `resources.memory` | Unlimited | Memory limit of at least `6MiB`, using `B`, `KiB`, `MiB`, `GiB`, or `TiB`. |
| `resources.pids` | Unlimited | Positive integer limit for processes and threads. |
| `workspace.enabled` | `true` | Whether to bind the active host run directory. |
| `workspace.target` | `/workspace` | Container path for the automatic workspace bind. |
| `workspace.read_only` | `false` | Whether the automatic workspace bind is read-only. |
| `mounts` | `[]` | Additional bind mounts or external named volumes. |
| `init.command` | `/bin/sh` | Persistent container process. |
| `init.args` | Keepalive script | Arguments for the persistent process. |

The active host run directory is mounted read-write at `/workspace` by default. Set
`workspace.enabled: false` to disable it, or change `target` and `read_only`. Relative bind-mount
sources resolve from the host run directory; volume sources remain Docker volume names. Container
targets must be absolute and unique.

Resource limits apply to the executor container as a whole. Its init process, shell and file
operations, and all managed `process` workers share one CPU, memory, and PID budget; a
`process_call` consumes the budget of its existing worker rather than receiving a separate limit.
PID accounting includes threads as reported by the host kernel. Setting `resources.memory` leaves
Docker's swap setting unspecified, so the daemon keeps its normal swap behavior. Resource values
may use templates like other strings beneath `executor.with`; rendered values are validated before
the container is created.

The default container process requires `/bin/sh`. Use `init` when an image needs a different
keepalive. Relative shell working directories are translated through the most specific bind mount;
explicit absolute container directories are passed through unchanged. A host-derived directory
that is not covered by a bind mount is rejected. This prevents an absolute host path from silently
being interpreted as an unrelated container path.

Each child is invoked with Docker exec; the image entrypoint is not rerun for every step. The
command or shell named by the shell step must exist in the image. Workflow and step environment
values, retry metadata, stdin, stdout, stderr, exit codes, timeouts, and shell output capture retain
their normal shell-step behavior. Interactive `shell.with.tty` is local-only and is rejected inside
Docker executor blocks.

## Reading and writing files

A command is not the only way to touch a file, so an executor session can also expose its own
filesystem. Where it does, [`edit`](steps-data.md#edit) and the `read`, `write`, `mkdir`, and `stat`
operations of [`file`](filesystem-operations.md) run inside the block and act on the target rather
than on the host:

```yaml
- executor:
    type: docker
    with: {image: golang:1.26}
  steps:
    - id: pin_version
      type: edit
      with:
        operation: set
        from: {file: config.json}
        path: $.version
        value: "{{ .vars.release }}"

    - id: record
      type: file
      with: {operation: write, path: build/stamp.txt, content: "{{ .vars.release }}", mode: "0640"}
```

Paths resolve against the run directory and are then translated through the same bind mounts as a
command's working directory, so a path with no counterpart inside the container is rejected rather
than redirected to the host. Docker file writes stage a same-directory temporary file as the
executor's configured or image user and install it atomically, including a race-safe refusal when
`overwrite` is false. Directory creation also preserves that executor user. These operations need
the executor's init command to be a recognized shell; a Docker executor configured with a non-shell
init rejects filesystem steps during validation.

The remaining `file` operations -- `copy`, `move`, `remove`, `list`, `chmod`, `find`, `link`,
`truncate`, `tail`, `disk_usage`, `atomic_swap`, `permissions`, and `touch` -- depend on host
semantics such as links, renames, and directory walks that the session filesystem does not carry,
and are rejected during validation. An executor that runs commands but exposes no filesystem at all,
as [plugin executors](plugins.md) do, rejects every file step in its scope.

## Sharing files with local steps

The default workspace bind makes file handoff direct. Files written under `/workspace` in Docker
are immediately visible beneath the host run directory, and local files are visible to later
container commands:

```yaml
steps:
  - id: prepare_locally
    type: shell
    with: {command: ./prepare-source}

  - executor:
      type: docker
      with: {image: golang:1.26}
    steps:
      - id: build_in_docker
        type: shell
        with: {command: go, args: [build, -o, dist/app, ./cmd/app]}

  - id: inspect_locally
    type: shell
    with: {command: file, args: [dist/app]}
```

Only mounted storage crosses the boundary. Files elsewhere in the container filesystem disappear
when the session is removed. Step outputs such as `.steps.build_in_docker.stdout` cross the
boundary through ordinary workflow state rather than through the filesystem.

## Cleanup and persistence

An executor block's `finally` list runs in that executor after success, failure, timeout,
cancellation, or early return. Cleanup steps can inspect the normal `finally.status` and
`finally.errors` bindings. Wuko then removes the Docker container and its anonymous volumes before
restoring local execution.

Bind-mounted files remain on the host. External named volumes are not removed because the executor
does not own them. Volumes and networks created with Wuko's Docker `volume_create` or
`network_create` operations retain their normal workflow-level cleanup behavior.

On command timeout or cancellation, Wuko removes the container immediately. A retry or scoped
cleanup recreates it with the same configuration: mounted files remain, but container-local files,
installed packages, environment mutations, and background processes do not.

If opening the executor session itself fails, its `finally` list cannot run because no execution
environment exists. Root workflow cleanup still follows the normal workflow lifecycle. Wuko also
removes stale managed containers from dead local Wuko processes when opening a later Docker
session on the same host.

See [Finally cleanup](finally.md) for the `finally` bindings and error model, and
[Graceful shutdown](graceful-shutdown.md) for signal handling and shutdown budgets.
