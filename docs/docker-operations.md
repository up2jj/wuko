# Docker operations

[Back to system steps](steps-system.md#docker)

The `docker` step runs temporary containers, waits for container health, transfers container
files, and performs image, registry, build, network, and volume operations. Set `operation` to
select behavior. Omitting it is equivalent to `operation: run`, preserving workflows written
before the other operations were added.

To run several shell steps in one persistent container and then return to local execution, use a
[Docker executor scope](executors.md#docker-executor).

| Operation | Purpose |
| --- | --- |
| `run` | Run a command in a temporary container |
| `pull` | Pull an image into the local Docker daemon |
| `push` | Push a local image to a registry |
| `tag` | Add a tag to a local image |
| `inspect` | Return stable metadata for a local image |
| `health_wait` | Wait for an existing container to become healthy |
| `copy_to` | Copy a host file or directory into a container |
| `copy_from` | Copy a container file or directory onto the host |
| `login` | Validate registry credentials without persisting them |
| `verify_digest` | Verify a local image's OCI digest |
| `network_create` | Create a workflow-scoped Docker network |
| `volume_create` | Create a workflow-scoped Docker volume |
| `build` | Build through Docker Buildx |

## Run containers

Run tests with a read-only bind mount:

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

The same operation may be written explicitly:

```yaml
- id: format
  type: docker
  with:
    operation: run
    image: ghcr.io/acme/formatter:1.2.3
    env: {FORMAT: json}
    stdin: "{{ .steps.request.body }}"
    pull: never
```

`run` requires `image` and supports `command`, `args`, `working_directory`, `mounts`,
`env`, `network`, `user`, `platform`, `tty`, and `stdin`. Its `pull` policy is
`never`, `if-missing` (the default), or `always`. Relative bind sources resolve from the run
directory, while container targets must be absolute.

The outputs are `stdout`, `stderr`, and `exit_code`. Wuko streams output while retaining these
values, then removes the temporary container and its anonymous volumes.

## Wait for container health

Wait for an existing container, addressed by name or ID, to pass its Docker healthcheck:

```yaml
- id: api_ready
  type: docker
  timeout: 2m
  with:
    operation: health_wait
    container: api
```

`health_wait` inspects immediately and then once per second while Docker reports `starting`.
It succeeds only when the container is running with health status `healthy`. It fails immediately
when Docker reports `unhealthy`, the container is not running, or the container has no configured
healthcheck. A missing container or inspection failure also fails the step.

Use the standard top-level `timeout` to bound the wait. Without one, Wuko waits until Docker
reports a terminal state or the workflow context is canceled. Normal step-level `retry` can start
a new wait after an unhealthy result.

Successful results contain the same structured snapshot attached to health-related failure
diagnostics:

| Output | Meaning |
| --- | --- |
| `container` | Requested container name or ID |
| `id` | Docker's resolved container ID |
| `container_status` | Docker container state such as `running` or `exited` |
| `health_status` | Docker health status: `starting`, `healthy`, `unhealthy`, or `none` |
| `failing_streak` | Consecutive failed healthchecks reported by Docker |
| `health_checks` | Ordered Docker healthcheck history with `started_at`, `finished_at`, `exit_code`, and `output` |

When diagnostics are enabled, failures expose these fields as structured attributes, including a
compact representation of `health_checks`. `health_wait` does not collect the container's general
stdout or stderr logs.

## Copy files to and from containers

Copy a host build artifact into a container, then copy a generated report back into the workflow
run directory:

```yaml
- id: upload_artifact
  type: docker
  with:
    operation: copy_to
    container: integration-api
    source: dist/app
    target: /opt/app

- id: download_report
  type: docker
  with:
    operation: copy_from
    container: integration-api
    source: /var/reports/result.json
    target: artifacts/result.json
```

Both operations require `container`, `source`, and `target`. `container` accepts any name or ID
known to the configured Docker daemon; the container may be running or stopped and does not need
to have been created by Wuko. For `copy_to`, `source` is the host path and `target` is the
container path. For `copy_from`, their roles are reversed. Relative host paths resolve from the
workflow run directory, while container paths use Docker's root-relative path rules.

Copy behavior matches `docker cp`: files may be renamed by a non-directory target, directories
merge into existing directories, source symlinks are copied without following them, and a source
ending in `/.` copies the directory's contents. Existing files may be replaced, but a directory
cannot replace a non-directory or vice versa. The destination's parent directory must already
exist. Ownership uses Docker's defaults rather than preserving the source UID and GID.

Both operations return `container`, `source`, and `target` exactly as configured. They do not
stream tar archives through workflow stdin or stdout and do not copy directly between containers.

## Pull and inspect images

Pull a public image for a specific platform:

```yaml
- id: pull
  type: docker
  with:
    operation: pull
    image: alpine:3.22
    platform: linux/amd64
```

Pull a private image with credentials scoped to this operation:

```yaml
- id: pull_private
  type: docker
  with:
    operation: pull
    image: registry.example.com/acme/app:1.4.0
    auth:
      username: "{{ .env.REGISTRY_USER }}"
      password: "{{ .env.REGISTRY_PASSWORD }}"
```

Inspect a local image and consume its metadata:

```yaml
- id: inspect
  type: docker
  with:
    operation: inspect
    image: registry.example.com/acme/app:1.4.0

- id: print_image_id
  type: shell
  with:
    command: printf
    args: ["%s\n", "{{ .steps.inspect.id }}"]
```

`pull` and `inspect` return:

| Output | Meaning |
| --- | --- |
| `image` | Requested image reference |
| `id` | Local content-addressable image ID |
| `repo_tags` | Local repository tags referencing the image |
| `repo_digests` | Locally available repository digests |
| `created` | Image creation timestamp |
| `size` | Total image size in bytes |
| `platform` | Combined `os/architecture[/variant]` value |
| `os`, `architecture`, `variant` | Individual platform fields |
| `labels` | Image configuration labels |

Pull progress is streamed to the step's standard output.

## Verify an image digest

`verify_digest` checks an image already present in the local daemon. It compares
`expected_digest` with the image descriptor and the digest portion of every repository digest.

```yaml
- id: verify
  type: docker
  with:
    operation: verify_digest
    image: registry.example.com/acme/app:1.4.0
    expected_digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    platform: linux/amd64
```

Success returns `image`, `expected_digest`, `actual_digest`, and `verified: true`. A malformed
digest, missing local image, or mismatch fails the step.

## Tag and push images

Add a tag, then push it:

```yaml
- id: tag
  type: docker
  with:
    operation: tag
    source: acme/app:1.4.0
    target: registry.example.com/acme/app:stable

- id: push
  type: docker
  with:
    operation: push
    image: "{{ .steps.tag.target }}"
    auth:
      identity_token: "{{ .env.REGISTRY_IDENTITY_TOKEN }}"
```

`tag` returns `source` and `target`. `push` accepts an optional `platform`, streams registry
progress, and returns `image`. It also returns `digest` when the daemon reports or exposes the
pushed manifest digest.

## Registry authentication

Inline `auth` accepts exactly one credential mode:

- `username` together with `password`;
- `identity_token`; or
- `registry_token`.

`pull` and `push` accept optional inline authentication. `login` requires authentication and a
`server_address`:

```yaml
- id: registry_access
  type: docker
  with:
    operation: login
    auth:
      server_address: registry.example.com
      username: "{{ .env.REGISTRY_USER }}"
      password: "{{ .env.REGISTRY_PASSWORD }}"
```

`login` returns `server` and the registry's non-secret `status`. It validates credentials only:
it does not write Docker configuration, persist tokens, or establish authentication for later
steps. Supply authentication again to a later Engine operation when required.

## Managed networks and volumes

Create a private network and named volume, then use both from a container:

```yaml
- id: network
  type: docker
  with:
    operation: network_create
    name: wuko-integration
    driver: bridge
    internal: true
    options:
      com.docker.network.driver.mtu: "1450"
    labels:
      purpose: integration-tests

- id: data
  type: docker
  with:
    operation: volume_create
    name: wuko-integration-data
    cleanup: true
    driver: local
    driver_options:
      type: tmpfs
      device: tmpfs
      o: size=256m

- id: integration
  type: docker
  with:
    image: acme/integration-tests:latest
    network: "{{ .steps.network.name }}"
    mounts:
      - type: volume
        source: "{{ .steps.data.name }}"
        target: /data
```

`network_create` requires `name` and optionally accepts `driver`, `internal`, `attachable`,
`options`, `labels`, and `cleanup`. It returns `resource_type`, `id`, `name`, and `warnings`.

`volume_create` requires `name` and optionally accepts `driver`, `driver_options`, `labels`, and
`cleanup`. It returns `resource_type`, `name`, `driver`, `mountpoint`, `scope`, and the internal
`ownership_id` used to make cleanup safe against name reuse.

Names must not already exist. Wuko never adopts an existing resource. Created resources receive
reserved ownership labels. By default, they are removed in reverse completion order after the root
workflow and its `finally` steps finish. Set `cleanup: false` to leave a network or volume in place.
Missing resources during cleanup are treated as already cleaned.

A run mount's `type` is `bind` by default. Set it to `volume` to keep `source` as a Docker
volume name instead of resolving it as a host path. `read_only` applies to either mount type.

## Build with Buildx

Build one platform and load the result into the local daemon:

```yaml
- id: build
  type: docker
  with:
    operation: build
    context: .
    dockerfile: Dockerfile
    tags: [acme/app:dev]
    platforms: [linux/amd64]
    output: load
    build_args:
      VERSION: "{{ .vars.version }}"
    target: production
    pull: true
```

Build multiple platforms, publish them, and use a registry cache:

```yaml
- id: publish
  type: docker
  with:
    operation: build
    context: .
    tags:
      - registry.example.com/acme/app:{{ .vars.version }}
      - registry.example.com/acme/app:latest
    platforms: [linux/amd64, linux/arm64]
    output: push
    cache_from:
      - type=registry,ref=registry.example.com/acme/app:buildcache
    cache_to:
      - type=registry,ref=registry.example.com/acme/app:buildcache,mode=max
```

`build` requires at least one `tags` entry and `output: load|push`. It supports:

- local directory `context`, defaulting to the run directory;
- `dockerfile`, defaulting to `Dockerfile` within the context;
- `platforms`, with at most one platform for `output: load`;
- `target`, `build_args`, `pull`, and `no_cache`;
- repeated `cache_from` and `cache_to` Buildx descriptors.

Wuko invokes `docker buildx build` directly without shell parsing. Outputs are `tags`,
`platforms`, and `output`, plus `digest` when Buildx reports one in its metadata. Buildx uses
the caller's existing Docker credential configuration; the operation fails clearly when Docker or
the Buildx plugin is unavailable.

Remote Git, URL, and standard-input build contexts are not supported.
