# System steps

[Back to the available steps](../README.md#available-steps)

System steps provide portable filesystem, network, cache, temporary-resource, and container
operations. Relative runtime paths normally resolve from the directory where Wuko was invoked.

## `file`

Perform strict, shell-independent filesystem operations.

Write an executable atomically:

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
```

Read a file or copy a directory:

```yaml
- id: read_manifest
  type: file
  with: {operation: read, path: dist/manifest.json}

- id: stage_assets
  type: file
  with:
    operation: copy
    path: assets
    destination: dist/assets
    recursive: true
```

Other operations are `move`, `remove`, `mkdir`, `list`, `stat`, `chmod`, `find`, `link`,
`truncate`, `tail`, `disk_usage`, `atomic_swap`, `permissions`, and `touch`. See
[Filesystem operations](filesystem-operations.md) for every field, output, safety rule, and example.

## `glob`

Discover regular files with portable patterns. Patterns use forward slashes on every platform and
support `*`, `?`, character classes, and recursive `**`.

Find source and script files:

```yaml
- id: sources
  type: glob
  with:
    root: .
    patterns: ["**/*.go", "scripts/[a-z]*.sh"]
```

Match normally hidden workflow files explicitly:

```yaml
- id: workflows
  type: glob
  with:
    root: .
    patterns: [".github/**/*.yaml", ".wuko/workflows/*.yml"]
```

Patterns form a union and results are deduplicated and path-sorted. Wildcards skip hidden paths
unless the leading dot is explicit. Outputs include absolute `root`, `count`, and `files` metadata.

## `temp`

Create a managed empty file or directory. Wuko removes it after root workflow cleanup, including
when the resource was created in an action, retry, polling loop, or concurrent branch.

Create a build directory:

```yaml
- id: workspace
  type: temp
  with: {kind: directory, pattern: wuko-build-*}

- id: build
  type: shell
  with:
    command: ./build
    args: [--output, "{{ .steps.workspace.path }}"]
```

Create a response file:

```yaml
- id: response
  type: temp
  with: {kind: file, pattern: api-*.json}

- id: write_response
  type: file
  with:
    operation: write
    path: "{{ .steps.response.path }}"
    content: "{{ .steps.request.body }}"
    overwrite: true
```

`kind` is `file` or `directory`. The optional `pattern` defaults to `wuko-*`. Outputs are `path`
and `kind`; validation and dry runs create nothing.

## `cache`

Restore and save a group of directories using a deterministic key derived from key-file contents
and target paths. Target directories must exist before restore.

```yaml
- id: prepare_vendor
  type: file
  with: {operation: mkdir, path: vendor, recursive: true}

- id: restore_dependencies
  type: cache
  with:
    operation: restore
    cache_dir: .wuko/cache
    key_files: [go.mod, go.sum]
    paths: [vendor]

- id: vendor
  type: shell
  if: "!steps.restore_dependencies.hit"
  with: {command: go, args: [mod, vendor]}
```

Save after a miss:

```yaml
- id: save_dependencies
  type: cache
  if: "!steps.restore_dependencies.hit"
  with:
    operation: save
    cache_dir: .wuko/cache
    key_files: [go.mod, go.sum]
    paths: [vendor]
```

A restore miss succeeds with `hit: false`; a hit includes `key`. Save returns `key`, `stored`, and
compressed `size`. Entries are immutable and stored as `<cache_dir>/<key>.tar.gz`.

## `http`

Make structured HTTP requests. The defaults are `GET`, any `2xx` status, and a text response.

Fetch JSON with retries and bearer authentication:

```yaml
- id: release
  type: http
  timeout: 30s
  retry: {max_attempts: 3}
  with:
    url: https://api.example.com/releases/latest
    query: {channel: stable}
    auth: {bearer_token: "{{ .env.API_TOKEN }}"}
    response: json
    success_statuses: [200]
```

Post JSON with Basic authentication and persistent cookies:

```yaml
- id: create_release
  type: http
  with:
    method: POST
    url: https://api.example.com/releases
    auth:
      basic:
        username: "{{ .env.API_USER }}"
        password: "{{ .env.API_PASSWORD }}"
    cookies: {jar: .wuko/api.cookies}
    json:
      version: "{{ .vars.version }}"
      channel: stable
    response: json
    success_statuses: [201]
```

Supply at most one of `body` or `json`. Every response exposes `status`, `headers`, and raw `body`;
`value` is text or decoded JSON. Bodies are limited to 10 MiB. Dedicated bearer/Basic auth is
mutually exclusive with a raw `Authorization` header. The step also supports explicit proxies,
cookie values/jars, and mutual-TLS certificate files.

## `docker`

Run one command in a temporary Docker container. The container and anonymous volumes are removed
after the step.

Run tests with a read-only project mount:

```yaml
- id: tests
  type: docker
  with:
    image: golang:1.26
    command: go
    args: [test, ./...]
    working_directory: /workspace
    mounts:
      - {source: "{{ .run.dir }}", target: /workspace, read_only: true}
    network: none
    pull: if-missing
```

Run an image's default command with explicit input and environment:

```yaml
- id: formatter
  type: docker
  with:
    image: ghcr.io/acme/formatter:1.2.3
    env: {FORMAT: json}
    stdin: "{{ .steps.request.body }}"
    pull: never
```

The step supports `env`, `user`, `platform`, `network`, `tty`, `stdin`, mounts, and pull policies.
Relative mount sources resolve from the run directory; container targets must be absolute. Pin
production images by digest when reproducibility matters.
