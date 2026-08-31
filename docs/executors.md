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
`working_directory`, `if`, `timeout`, `retry`, `batch`, `foreach`, `matrix`, `concurrent`, or `with` at the
block level. Put those controls inside the block where supported.

`executor.type` is static. String values beneath `executor.with` may use templates and are rendered
when execution enters the block, so they can consume state committed by earlier sequential steps.
Validation rejects an unknown provider or invalid provider configuration before running the
workflow.

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
[`process`](steps-automation.md#process) steps, working-directory and conditional blocks, early
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
`shell`, `require_tool`, and `devenv_task`; HTTP, filesystem, persistence, TUI, and Lua steps remain
host-side. Devenv must be version 2.2 or newer.

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
        name: app:build
        mode: before
        inputs: {target: production}
    - id: tests
      type: shell
      with: {command: go, args: [test, ./...]}
```

`profiles` is an ordered list and is emitted as repeated `--profile` flags. Automatic hostname and
user profiles remain active. If Wuko is already running inside devenv, it reuses the active root and
profile identity; a configured directory or profile set that does not match is rejected.

SecretSpec modes are `auto`, `runtime`, `inherit`, and `disabled`. Runtime mode invokes
`secretspec run --` for each child command and keeps secret values out of workflow state and
diagnostics. `auto` enables runtime mode when `secretspec.toml` or active SecretSpec configuration
is present.

`devenv_task` supports `single`, `before`, `after`, and `all` task modes. Its `inputs` map is passed
as JSON to devenv. Task dependencies, status checks, task caching, and transient process cleanup
remain owned by devenv.

The optional `processes` setting ensures persistent devenv processes are ready. Wuko reuses already
running processes, records only processes it starts, and stops only those owned processes when the
executor closes. If process state or ownership cannot be established, Wuko fails safely without
starting or stopping anything; it never calls broad `devenv down`.

An opt-in real-environment smoke test is available with:

```text
WUKO_SMOKE_REQUIRED=1 go test -tags devenv_smoke ./smoke
```

It requires devenv 2.2+, creates an isolated fixture, tests multiple profiles, active-environment
reuse, task inputs, runtime SecretSpec, and process cleanup, and skips with a clear message when the
required tools are unavailable.

## Docker executor

The Docker executor requires `image` and accepts `pull`, `platform`, `network`, `user`,
`workspace`, `mounts`, and `init`:

```yaml
- executor:
    type: docker
    with:
      image: node:24
      pull: if-missing
      network: none
      user: "1000:1000"
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
