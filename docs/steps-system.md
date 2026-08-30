# System steps

[Back to the available steps](../README.md#available-steps)

System steps provide portable filesystem, network, cache, temporary-resource, and container
operations. Relative runtime paths normally resolve from the directory where Wuko was invoked.

## Git assertions

Git assertions run the installed `git` executable in the workflow run directory. They are
read-only, work inside executor blocks, and return no outputs on success. Remote-branch checks
inspect local remote-tracking references; they do not fetch from a remote.

## `git_clean`

Assert that the working tree has no staged, modified, deleted, or normal untracked files. Ignored
files are excluded.

```yaml
- id: clean
  type: git_clean
```

## `git_branch`

Assert whether a local branch exists. `operation` is currently required to be `assert`; it leaves
room for future branch operations such as creation or switching. `branch` and `exists` are required.

```yaml
- id: no_release_branch
  type: git_branch
  with:
    operation: assert
    branch: release/v1
    exists: false
```

## `git_remote_branch`

Assert whether a remote-tracking branch exists locally. Omit `remote` to match any remote; provide
it to restrict the check to one remote.

```yaml
- id: upstream_release
  type: git_remote_branch
  with:
    branch: release/v1
    remote: origin
    exists: true
```

## `git_branch_name`

Assert that a string is accepted by Git as a branch name.

```yaml
- id: valid_branch
  type: git_branch_name
  with:
    name: "{{ .steps.fetch.branch }}"
```

## `git_on_branch`

Assert that the repository is currently checked out on the requested branch. Detached HEAD fails.

```yaml
- id: on_main
  type: git_on_branch
  with:
    branch: main
```

## `github_pr`

Find an open GitHub pull request through the installed `gh` CLI. This is currently a read-only
operation; it does not open, close, edit, merge, or otherwise mutate pull requests. `operation:
find` is required so future GitHub operations cannot be confused with lookup behavior. With no
repository or branch configured, the step uses a pull-request ref or head branch from GitHub Actions
when available, then falls back to the current Git branch. Set `repository` and `branch` to override
that inference.

```yaml
- id: pull_request
  type: github_pr
  with:
    operation: find
    repository: acme/project
    branch: feature/release
```

In GitHub Actions, the following resolves the PR associated with the event or checkout:

```yaml
- id: pull_request
  type: github_pr
  with:
    operation: find
```

The result includes `found`, `number`, `url`, `title`, `state`, `is_draft`, `head_branch`, `head_sha`,
`base_branch`, and `repository`. A branch with no open pull request succeeds with `found: false`
and empty metadata. If multiple open pull requests match a branch, the step fails as ambiguous.
Authentication, repository, and other `gh` failures also fail the step. Use `require_tool` when a
workflow should report a missing `gh` executable before attempting the lookup:

```yaml
- id: require_gh
  type: require_tool
  with:
    tool: gh

- id: pull_request
  type: github_pr
  with:
    operation: find
```

## `github_release`

Check whether a GitHub repository's default branch has commits after its latest stable release.
The step uses the installed `gh` CLI and performs only read-only lookups. `operation:
check_drift` is required; the repository must use the `owner/repository` form.

```yaml
- id: release_status
  type: github_release
  with:
    operation: check_drift
    repository: acme/project
```

The step resolves the repository's default branch, reads GitHub's latest stable release, and
compares `release_tag...default_branch`. A repository without a stable release succeeds with
`status: no_release`; authentication, repository, `gh`, and API failures fail the step. Use
`require_tool` before the step when a workflow wants to report a missing `gh` executable early.

Outputs include `repository`, `found`, `status`, `has_changes`, `release_tag`, `release_url`,
`published_at`, `branch`, `ahead_by`, `behind_by`, `total_commits`, and `compare_url`. `status` is
`changed` when `ahead_by` is greater than zero, otherwise it is `current` when a release exists.

## `github_actions`

Observe one GitHub Actions workflow run through the installed `gh` CLI. The step performs one
non-blocking lookup; use the `loop` control when the run may still be queued or in progress.

```yaml
- id: pull_request
  type: github_pr
  with:
    operation: find

- id: ci
  loop:
    until: steps.poll.terminal
    delay: 10s
    timeout: 30m
    steps:
      - id: poll
        type: github_actions
        with:
          workflow: ci.yml
          pull_request: "{{ .steps.pull_request.number }}"

- id: verify_ci
  type: assert
  with:
    expr: steps.poll.success
    message: GitHub Actions CI did not succeed
```

`repository` is optional. Wuko passes it to `gh` when configured, otherwise `gh` uses the current
repository context. Set `run_id` to observe a known run directly. Otherwise configure `workflow`
and either `pull_request` or `head_sha`; those selectors are mutually exclusive.

The result includes `found`, `run_id`, `run_number`, `workflow`, `workflow_id`, `status`,
`conclusion`, `terminal`, `success`, `event`, `head_sha`, `head_branch`, `url`, `attempt`,
`created_at`, `started_at`, and `updated_at`. A run that has not been created yet returns
`found: false` and `status: not_found`; completed failures are returned as observations with
`success: false` so a following assertion can decide whether the Wuko workflow should fail.

## `require_tool`

Require an executable before dependent work begins. The step runs the configured version command
in the current execution environment, including inside an executor block.

```yaml
- id: require_go
  type: require_tool
  with:
    tool: go
    version_args: [version]
    constraint: ">= 1.25.0"
```

`tool` is required. `version_args` defaults to `[--version]`; set it to `[]` for tools whose
availability probe takes no arguments. Without `constraint`, a successful invocation is enough.
With a constraint, Wuko extracts the first complete semantic version from stdout, then stderr,
and validates it with the same constraint syntax as the `semver` step. A lowercase `v` prefix is
accepted. Outputs include `path`, plus normalized `version` when a constraint was checked. On the
local host, `path` is resolved through `PATH`; inside an executor it is the configured tool name or
path.

## `multiplexer`

Control the terminal context hosting Wuko. The step detects tmux, Herdr, then cmux in that order;
set `provider` to `tmux`, `herdr`, or `cmux` only when a nested environment should target a
specific outer integration. When the selected integration is absent the step succeeds with
`active: false`, `changed: false`, and empty `provider` and `target` outputs.

Set a persistent pane or tab label:

```yaml
- id: label
  type: multiplexer
  with:
    operation: title
    title: "Deploy {{ .vars.environment }}"
```

Use `operation: clear_title` to clear the explicit label. Changes are not restored when the
workflow finishes; sequential changes are last-write-wins. To require an active multiplexer,
assert the step output:

```yaml
- id: require_multiplexer
  type: assert
  with:
    expr: steps.label.active
    message: This workflow must run inside tmux, cmux, or Herdr
```

### Tab scope

`title` and `clear_title` accept `scope`, which is `pane` by default. Set `scope: tab` to label
the tab or window containing the current pane instead:

```yaml
- id: tab
  type: multiplexer
  with:
    operation: title
    scope: tab
    title: "Deploy {{ .vars.environment }}"
```

Tab scope resolves the tab from the detected pane. herdr reads it from `HERDR_TAB_ID` when that
is set and otherwise asks `herdr pane get`; tmux uses the window owning `TMUX_PANE`; cmux uses
`rename-tab` and reports an unsupported operation when the installed release does not advertise
it. Only `title` and `clear_title` accept tab scope - `zoom`, `notify`, and the provider-specific
operations remain pane or surface operations.

Every title operation outputs `previous_title`, the label the target carried beforehand. herdr
has no tab equivalent of `pane rename --clear`, so restoring a tab means setting the previous
value back rather than clearing it:

```yaml
- id: tab
  type: multiplexer
  with: {operation: title, scope: tab, title: "Deploy production"}

finally:
  - id: restore_tab
    type: multiplexer
    if: steps.tab.active
    with:
      operation: title
      scope: tab
      title: "{{ .steps.tab.previous_title }}"
```

`previous_title` is empty when the target had no label or the provider cannot report one, and
`scope` is echoed back on the step output. For tmux, `clear_title` with `scope: tab` restores
tmux's own `automatic-rename` rather than blanking the window name.

Portable operations are:

| Operation | Fields | Behavior |
| --- | --- | --- |
| `title` | `title` | Set the current context label |
| `clear_title` | none | Clear the current context label |
| `zoom` | `mode: on`, `off`, or `toggle` | Set current-pane zoom where supported |
| `notify` | `title`, optional `body` | Show a native notification or tmux message |

Provider-specific operations are rejected when the detected provider or installed CLI does not
advertise them:

| Provider | Operations and fields |
| --- | --- |
| cmux | `status` (`key`, `value`, optional `icon`, `color`, `priority`), `clear_status` (`key`), `progress` (`progress` from 0 to 1, optional `label`), `clear_progress`, `log` (`message`, optional `level` and `source`), `clear_log` |
| Herdr | `metadata` with optional `source`, `title`, `display_agent`, `state_labels`, `tokens`, `clear_title`, `clear_display_agent`, `clear_state_labels`, `clear_tokens`, and `ttl_ms` |

Herdr metadata sources default to `wuko.<workflow>.<step>`. State-label keys are `idle`, `working`,
`blocked`, `done`, or `unknown`; `ttl_ms` may be at most 86400000. cmux compatibility depends on
the commands advertised by the installed release: older releases label the current tab, while the
noun-first CLI labels the current pane. Older cmux releases without pane zoom or sidebar commands
return an explicit unsupported-operation error.

All successful active operations output `active`, `provider`, `operation`, `scope`, `target`,
`changed`, and `previous_title`.
The step controls the local host terminal and is not available inside executor blocks. Display
text rejects terminal control characters. Raw provider commands remain available through `shell`.

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

## `scaffold`

Render a packaged directory tree into the active run directory. `from` is a required relative path
within the owning workflow or composite-action package; `into` is required and resolves from
`run.dir` unless it is absolute. Both configuration fields, every relative source path component,
and every regular file body use the owning workflow or action's strict Go-template environment.
Filename suffixes are preserved.

```yaml
- id: new_service
  type: scaffold
  with:
    from: templates/service
    into: "services/{{ .vars.name }}"
    on_conflict: fail
```

For example, `templates/service/cmd/{{ .vars.name }}/main.go` becomes
`services/<name>/cmd/<name>/main.go`, with its contents rendered from the same `.vars`, `.inputs`,
`.env`, `.steps`, control bindings, functions, and named templates available to ordinary step
configuration. The source directory itself is not added below `into`. Hidden files are included,
and new files preserve source permission bits. Empty directories and directory permission bits
survive only while the owning workflow or action runs from a directory: packaging carries files
alone, so a scaffold tree shipped inside a workflow package or action archive loses its empty
directories and the directories the step creates fall back to `0755`. Keep a placeholder file in
any directory that must survive packaging.

`on_conflict` defaults to `fail`:

- `fail` checks the complete rendered plan before writing and fails when any destination file
  exists.
- `skip` leaves existing files or leaf symbolic links unchanged and creates the remaining files.
- `overwrite` atomically replaces existing files or leaf symbolic links.

Existing directories are merged without changing their modes. File/directory type mismatches,
symbolic-link destination directories, source symbolic links, special files, invalid UTF-8, unsafe
rendered path components, and duplicate rendered paths fail. Two entries whose rendered paths
differ only in case are rejected as duplicates on every platform, because such a tree cannot be
written on a case-insensitive filesystem. Every source regular file is treated as text; binary
assets are not supported. `from` requires a directory the owning package carries, so a remote
action loaded as a plain YAML manifest is refused rather than read from the caller's package. The
step is local-filesystem-only and cannot run inside an executor block.

Outputs are absolute `from` and `into` paths, integer `created`, `skipped`, and `overwritten`
counts, and `files`, a sorted list of absolute destination paths represented by the scaffold.

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

## `watch`

Wait for the first matching native filesystem notification without polling or invoking a shell.
Patterns use the same relative, forward-slash syntax and hidden-path behavior as `glob`.

This is the one-shot step form. For a background development loop that reruns a block and keeps the
foreground workflow moving, use the [`observe` workflow control](workflow-control.md#observe) with
the `filesystem` source.

Wait up to five minutes for Go source activity:

```yaml
- id: source_changed
  type: watch
  timeout: 5m
  with:
    root: .
    patterns: ["src/**/*.go"]
    events: [create, modify, rename, remove]
```

`root` defaults to `.` and must already be a directory. `patterns` is required and forms a union.
`events` defaults to all four supported operations; `modify` means file content was written and
does not include permission-only changes. The step returns absolute `root`, slash-normalized
relative `path`, and an `operations` list. Native notifications may combine operations, so consume
the list rather than assuming exactly one value.

Wuko watches every existing directory below `root`, adds newly created directory trees, and does
not follow directory symlinks. Recursive trees consume one or more operating-system watch
resources per directory, so keep the root narrow. A directory moved into the tree is watched from
that point onward, but native APIs may not report activity that happened within it before Wuko
registered the new directories.

A rename reports the old path. When both locations are watched, the destination normally arrives
as a separate create notification; because version 1 returns the first match, that later event is
not included. Native notifications require filesystem support and are unavailable or unreliable on
NFS, SMB, FUSE, `/proc`, and `/sys`; there is no polling fallback. The step may wait indefinitely,
so use a top-level `timeout` unless an unbounded workflow is intentional. `watch` observes the local
host filesystem and is rejected inside executor blocks.

## `log_wait`

Follow a growing regular file until a Go regular expression matches. Existing content is scanned
first, so a readiness message written before the step starts is still observed.

Wait for a service to report readiness:

```yaml
- id: await_service
  type: log_wait
  timeout: 2m
  with:
    path: logs/service.log
    pattern: 'ready on port (?P<port>[0-9]+)'
    max_bytes: 2MiB
```

`path` is resolved from the workflow run directory. The containing directory must already exist,
but the file may be created after the step starts. The follower handles truncation and atomic file
replacement by reopening and scanning the replacement from the beginning; a present non-regular
file is an error. `max_bytes` defaults to `1MiB` and bounds unmatched content retained in memory.

Successful output contains the resolved `path`, the full matched `match`, and a `captures` object
containing named regular-expression captures. Unnamed expressions are supported and produce an
empty captures object. Use a top-level `timeout` because the step waits indefinitely until a match,
timeout, or cancellation. `log_wait` observes the host filesystem and is rejected inside executor
blocks.

## `temp`

Create a managed empty file, directory, or POSIX FIFO. Wuko removes it after root workflow cleanup,
including when the resource was created in an action, retry, polling loop, or concurrent branch.

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

Create a FIFO before starting cooperating processes, then use its committed path from both
concurrent branches:

```yaml
- id: channel
  type: temp
  with: {kind: fifo, pattern: events-*}

- concurrent:
    max_concurrency: 2
    timeout: 30s
    steps:
      - id: consumer
        type: shell
        with:
          script: cat "$1" > received.txt
          args: ["{{ .steps.channel.path }}"]
      - id: producer
        type: shell
        with:
          script: printf '%s\n' "$2" > "$1"
          args: ["{{ .steps.channel.path }}", "ready"]
```

The FIFO can also connect a process that Wuko did not launch. Publish its generated path through a
known, unused rendezvous file and keep the workflow active while the external process connects:

```yaml
steps:
  - id: channel
    type: temp
    with: {kind: fifo}
  - id: publish_channel
    type: file
    with:
      operation: write
      path: "{{ .vars.rendezvous }}"
      content: "{{ .steps.channel.path }}"
  - id: receive
    type: shell
    timeout: 10m
    with:
      script: cat "$1"
      args: ["{{ .steps.channel.path }}"]

finally:
  - id: remove_rendezvous
    type: file
    with: {operation: remove, path: "{{ .vars.rendezvous }}"}
```

Run that workflow with an unused path in one terminal:

```sh
wuko run external-fifo --var rendezvous="$PWD/.channel-path"
```

Then write from another terminal as the same operating-system user:

```sh
fifo_path=$(cat .channel-path)
printf '%s\n' "external message" > "$fifo_path"
```

Prefer an ordinary shell pipeline such as `producer | consumer` when both programs can be launched
together and do not require a filesystem path. Opening a FIFO may block until its opposite endpoint
connects, so run its producer and consumer concurrently and bound the operation with a timeout. A
FIFO is a byte stream and provides no message boundaries.

`kind` is `file`, `directory`, or `fifo`. The optional `pattern` defaults to `wuko-*`. Outputs are
`path` and `kind`; validation and dry runs create nothing. FIFOs use mode `0600` inside a private
`0700` temporary directory. They are available only to same-host, same-user processes while the
workflow is running, and they are not automatically visible inside executor containers. Version 1
does not support arbitrary or persistent FIFO paths, configurable FIFO permissions, or FIFO
read/write operations in the `temp` step. The FIFO kind is supported on Wuko's Linux and macOS
release targets.

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

`retry.when` replaces method and status eligibility when present, so it can accept a normally
terminal response or reject a normally transient one. Inspect the response code as
`error.outputs.status`; connection, DNS, TLS, and timeout failures never reach a response, so they
report status `0` and carry no other outputs. Because the expression replaces the defaults
entirely, retry those transport failures explicitly:

```yaml
- id: publish
  type: http
  retry:
    max_attempts: 4
    when: 'error.outputs.status == 0 || error.outputs.status >= 500'
  with: {method: POST, url: "{{ .vars.endpoint }}"}
```

`when` cannot be combined with explicit `retry.methods` or `retry.statuses`; encode the complete
eligibility decision in the expression instead. HTTP `Retry-After` handling still applies after the
expression permits a retry.

A retryable response's `Retry-After` delta-seconds or HTTP date can extend the next backoff, up to
the policy's `max_delay` and `max_elapsed_time`. For buffered responses containing `ETag` or
`Last-Modified`, the next attempt sends `If-None-Match` or `If-Modified-Since` unless that header
was configured explicitly. A `304 Not Modified` then succeeds with the previous response's body
and value while exposing the `304` status and refreshed headers. Validators are retained only
between attempts of that one step execution and are not applied to downloads.

## `docker`

Run containers, wait for container health, transfer container files, and perform Docker image,
registry, build, network, or volume operations. The `operation` field defaults to `run`, so
existing Docker steps remain compatible.

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

The other operations are `pull`, `push`, `tag`, `inspect`, `health_wait`, `copy_to`, `copy_from`,
`login`, `verify_digest`, `network_create`, `volume_create`, and `build`. See
[Docker operations](docker-operations.md) for every field, output, lifecycle rule, and example.
