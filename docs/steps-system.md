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
  retry:
    max_attempts: 3
    statuses: [408, 429, "500-599"]
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

Submit a URL-encoded form:

```yaml
- id: create_session
  type: http
  with:
    method: POST
    url: https://api.example.com/session
    form:
      username: "{{ .vars.username }}"
      remember: "true"
```

Upload files with multipart form data:

```yaml
- id: publish_artifact
  type: http
  timeout: 10m
  with:
    method: POST
    url: https://api.example.com/releases
    form:
      channel: stable
    files:
      - {field: artifact, path: dist/release.tar.gz}
      - {field: attachment, path: docs/release-notes.txt}
```

Stream a large response directly to a file:

```yaml
- id: download_artifact
  type: http
  timeout: 30m
  with:
    url: https://api.example.com/releases/latest/artifact
    download:
      path: downloads/release.tar.gz
      overwrite: true
```

Supply one request body mode: `body`, `json`, or `form`/`files`. A `form` without files uses
`application/x-www-form-urlencoded`; adding `files` switches the request to `multipart/form-data`
and includes the form fields. Each file requires a multipart `field` and a `path`; multiple entries
may use the same field. Relative paths resolve from the workflow or action that owns the step, and
absolute paths are accepted.

Uploads must be regular files. The filename comes from the path's base name, and the media type is
inferred from its extension with `application/octet-stream` as the fallback. Wuko streams files
from disk with a computed `Content-Length`, so uploads are not buffered in memory and have no
Wuko-specific size limit. Do not set `Content-Type` for multipart requests because Wuko supplies
the required boundary. Files are reopened for retries and body-preserving redirects; do not modify
them while the request is running.

Use `download` instead of `response` to stream a successful response to disk. Relative download
paths resolve from the run directory; absolute paths are also accepted. Downloads have no
Wuko-specific size limit and expose `status`, `headers`, resolved `path`, and `size` without
buffering `body` or `value`. The destination's parent directory must already exist. Existing
destinations are rejected unless `overwrite: true` is set, and an overwritten regular file keeps
its permissions.

Downloads are written to a temporary file beside the destination and installed atomically only
after the entire successful response has been read and synced. Cancellation, connection/read
errors, and unsuccessful HTTP statuses remove the temporary file without changing the destination.
Unsuccessful responses retain the normal bounded `body` and `value` outputs so retry and error
handling continue to work normally. A status listed in `success_statuses` is considered successful
and is therefore downloaded even when it is outside `2xx`.

Buffered responses expose `status`, `headers`, and raw `body`; `value` is text or decoded JSON.
Buffered response bodies are limited to 10 MiB. Dedicated bearer/Basic auth is mutually exclusive
with a raw `Authorization` header. The step also supports explicit proxies, cookie values/jars,
and mutual-TLS certificate files.

HTTP retries use the ordinary step-level `retry` timing and attempt limits. Unless overridden with
`retry.methods`, only idempotent methods (`GET`, `HEAD`, `OPTIONS`, `PUT`, `DELETE`, and `TRACE`)
are retried. The default retryable statuses are `408`, `425`, `429`, and `500-599`; individual
codes and inclusive quoted ranges can be supplied with `retry.statuses`. Include `POST` or `PATCH`
explicitly when the endpoint makes those requests safe to repeat.

A retryable response's `Retry-After` delta-seconds or HTTP date can extend the next backoff, up to
the policy's `max_delay` and `max_elapsed_time`. For buffered responses containing `ETag` or
`Last-Modified`, the next attempt sends `If-None-Match` or `If-Modified-Since` unless that header
was configured explicitly. A `304 Not Modified` then succeeds with the previous response's body
and value while exposing the `304` status and refreshed headers. Validators are retained only
between attempts of that one step execution and are not applied to downloads.

## `docker`

Run containers and perform Docker image, registry, build, network, or volume operations. The
`operation` field defaults to `run`, so existing Docker steps remain compatible.

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

The other operations are `pull`, `push`, `tag`, `inspect`, `login`, `verify_digest`,
`network_create`, `volume_create`, and `build`. See
[Docker operations](docker-operations.md) for every field, output, lifecycle rule, and example.
