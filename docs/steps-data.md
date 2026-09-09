# Data steps

[Back to the available steps](../README.md#available-steps)

Data steps create, validate, select, import, or persist typed workflow values. Templates reference
them through `.vars` and `.steps`; expressions use `vars` and `steps` without the leading dot.

## `set`

Assign a typed literal or evaluate an Expr expression. Exactly one of `value` or `expr` is
required.

```yaml
- id: defaults
  type: set
  with:
    variable: deployment
    value:
      enabled: true
      retries: 3
      regions: [eu-central, us-east]
```

```yaml
- id: artifact
  type: set
  with:
    variable: artifact_name
    expr: 'steps.release.value.version + "-" + vars.target + ".tar.gz"'
```

The result is available as `.steps.<id>.value` and as the configured variable.

## `assert`

Stop the workflow with a clear message unless an Expr expression returns boolean `true`.

```yaml
- id: verify_build
  type: assert
  with:
    expr: steps.build.exit_code == 0
    message: Build must succeed before release
```

```yaml
- id: verify_target
  type: assert
  with:
    expr: vars.target in ["linux", "darwin"]
    message: "Unsupported target: {{ .vars.target }}"
```

Both fields are required. A false result fails with `assertion failed: <message>`; a true result
succeeds with an empty output object.

## `import_vars`

Load JSON or TOML while the workflow is running. Files merge from left to right with top-level
replacement; imported values then overwrite variables already in workflow state.

```yaml
- id: configuration
  type: import_vars
  with:
    files: [defaults.toml, local.json]
```

```yaml
- id: environment
  type: import_vars
  with:
    files:
      - defaults.toml
      - "environments/{{ .vars.environment }}.json"
```

Relative paths resolve from the owning workflow or action. A successful import exposes
`.steps.<id>.variables`, `.steps.<id>.count`, and every imported key beneath `.vars`. The import is
atomic. See [Variable imports](variable-imports.md) for file shape, precedence, nesting, and remote
archive behavior.

## `decode`

Decode JSON, YAML, TOML, or text lines into a typed value. Exactly one of `from` or `path` is
required. `from` is a dotted `vars` or `steps` path that must resolve to a string:

```yaml
- id: deployment_data
  type: decode
  with:
    format: json
    from: steps.deployments.stdout

- id: deployment_names
  type: jsonpath
  with:
    from: steps.deployment_data.value
    query: $.items[*].metadata.name
    result: all
```

Use `path` for file content. Relative paths resolve from the active run directory and therefore
follow `working_directory` scopes:

```yaml
- id: configuration
  type: decode
  with:
    format: yaml
    path: generated/config.yaml
    max_bytes: 2MiB
```

The default `max_bytes` is `1MiB`. Inputs larger than the limit fail before parsing. JSON and YAML
may contain any single structured value; multiple JSON values or YAML documents are rejected. TOML
returns its root table. All structured results are normalized to JSON-compatible Wuko values.

For command output with one item per line, use `lines`. CRLF and LF endings are normalized, a final
line terminator does not add an empty item, and the optional filters apply in order:

```yaml
- id: contexts
  type: decode
  with:
    format: lines
    from: steps.kubectl_contexts.stdout
    trim: true
    omit_empty: true
```

The decoded result is available as `.steps.<id>.value`. `trim` and `omit_empty` are valid only for
`lines`; `decode` does not infer the format from a filename or create workflow variables.

## `jsonpath`

Select data from a typed value with an [RFC 9535](https://www.rfc-editor.org/rfc/rfc9535.html)
query.

Return every active project ID:

```yaml
- id: active_projects
  type: jsonpath
  with:
    from: steps.fetch.value
    query: "$.projects[?@.active == true].id"
    result: all
    variable: active_project_ids
```

Require exactly one version:

```yaml
- id: stable_version
  type: jsonpath
  with:
    from: vars.catalog
    query: "$.releases[?@.channel == 'stable'].version"
    result: one
```

`from` is a dotted path rooted at `vars` or `steps`. `all` returns an ordered list and permits no
matches; `one` requires exactly one match. Results also include `count` and normalized `paths`.

## `edit`

Mutate structured values selected with [RFC 9535 JSONPath](https://www.rfc-editor.org/rfc/rfc9535.html).
File edits preserve unrelated comments and formatting and are installed atomically. The format is
inferred from `.json`, `.yaml`, `.yml`, or `.toml`; use `format` when a file has another extension.
Inside an [executor scope](executors.md#reading-and-writing-files), the document is read from and
written back to the execution target rather than the host.

Bump a version in place:

```yaml
- id: bump
  type: edit
  with:
    operation: set
    from:
      file: package.json
    path: $.version
    value: "{{ .steps.next.version }}"
```

Find matching nodes and calculate each replacement from its previous value:

```yaml
- id: expand_node_services
  type: edit
  with:
    operation: set
    from:
      file: services.yaml
    path: "$.services[?@.runtime == 'node'].replicas"
    expr: current + 1
    result: all
```

`expr` is evaluated once per selected node against the original document. It can use the normal
expression roots plus `current`, the normalized JSONPath `path`, and the zero-based match `index`.
Use exactly one of `value` or `expr`.

Create a key and any missing map parents with a singular path:

```yaml
- id: add_dependency
  type: edit
  with:
    operation: set
    from:
      file: package.json
    path: $.devDependencies.wuko
    value: ^1.0.0
    missing: create
```

Array and object mutations use the same source and selection model:

```yaml
- id: append_service
  type: edit
  with:
    operation: append
    from:
      file: services.yaml
    path: $.services
    value: {name: worker, replicas: 2}

- id: insert_before_production
  type: edit
  with:
    operation: insert
    from:
      var: environments
    path: "$[?@.name == 'production']"
    position: before
    value: {name: staging}

- id: merge_defaults
  type: edit
  with:
    operation: merge
    from:
      file: compose.yaml
    path: $.services.api
    value:
      environment:
        LOG_LEVEL: info

- id: rename_key
  type: edit
  with:
    operation: rename
    from:
      file: config.toml
    path: $.server.bind
    name: address
```

The operation determines its additional fields and target type:

| Operation | Fields | Behavior |
| --- | --- | --- |
| `set` | exactly one of `value`, `expr` | Replace a selected value. |
| `delete` | none | Remove selected object members or array elements; `$` is rejected. |
| `append` | exactly one of `value`, `expr` | Add the supplied value as one element to each selected array. |
| `insert` | exactly one of `value`, `expr`; `position: before \| after` | Insert one element relative to each selected array element. |
| `merge` | exactly one of `value`, `expr` | Deep-merge maps; nested maps merge while arrays and other leaves replace. |
| `rename` | `name` | Rename selected object members; an existing destination key is an error. |

All matches and expressions use the original document snapshot. Array insertions and deletions are
then applied in location-safe order, so one edit cannot shift a later selected target. Duplicate
locations collapse and overlapping locations are rejected. Keys added by `merge` are written in
sorted order, so the same merge produces the same file on every run.

Deleting the last entry of an object or array rewrites it as an empty collection rather than
leaving the key that introduces it without a value.

Members of a TOML inline table (`server = {host = "h"}`) cannot be edited individually; the step
reports this rather than writing a dotted key that TOML would reject. Replace the whole value with
`set`, or promote the inline table to a `[server]` table header.

Edit a workflow variable without mutating it:

```yaml
- id: adjusted
  type: edit
  with:
    operation: set
    from:
      var: deployment
    path: $.spec.replicas
    expr: current + 1

- id: save_adjusted
  type: set
  with:
    variable: deployment
    expr: steps.adjusted.value
```

An expression can also provide the source document:

```yaml
- id: production
  type: edit
  with:
    operation: set
    from:
      expr: steps.configuration.value
    path: $.environment
    value: production
```

`from` must contain exactly one of `file`, `var`, or `expr`. A file source must be a regular file
— a symlink is rejected rather than replaced — and is changed in place; variable and expression
sources are cloned, never mutated, and the transformed document is returned as `steps.<id>.value`.

`result` defaults to `one`, which requires exactly one match. Set `result: all` to mutate every
match. A missing path fails by default. Set `missing: ignore` for a successful no-op; in that case
an expression is not evaluated and a file is not rewritten. `missing: create` is available only
for `set`: the path must be singular, its final selector must be a key, and any array indexes must
already exist. Missing map parents are created. Missing source files or variables always fail.

Values keep their type across a rewrite. A TOML float is written with a decimal point, so a whole
number is not read back as an integer, and TOML strings use TOML's own escapes, so a control
character does not produce a file its parser rejects. An integer too large for a 64-bit type passes
through unchanged rather than being rounded through a float, which also means an `expr` of `current`
rewrites nothing. Arithmetic on such a number fails instead of silently losing its low digits.

Outputs include the complete transformed `value`, original normalized `paths`, operation-produced
`replacements`, `count`, `changed`, and `changed_count`. `delete` returns an empty `replacements`
list. File edits also return the resolved `file` and `format`. Files are limited to `1MiB` by
default; increase `max_bytes` with a byte size such as `4MiB` when the larger input is intentional.

## `markdown_edit`

Edit Markdown by native syntax nodes while preserving unrelated bytes, comments, whitespace, and
line endings. Parsing uses Goldmark v2 with GFM and heading attributes enabled. A heading is a
heading node only: deleting it does not implicitly delete the content below it.

Prepare a new release heading while renaming the previous one:

````yaml
- id: prepare_release
  type: markdown_edit
  with:
    from:
      file: operations.md
    edits:
      - operation: set
        select:
          kind: heading
          level: 3
          text: "Next release (2026-09-09)"
        field: text
        value: "2026-09-09"

      - operation: insert
        select:
          kind: heading
          level: 3
          text: "Next release (2026-09-09)"
        position: before
        value: |+
          ### Next release (2026-09-27)

          - Add release notes here.

````

Every selector is resolved against the original document. The two edits above can therefore use
the same original heading even though the first edit changes its text. Overlapping replacements
are rejected before a file is written; insertions exactly at a replacement boundary are allowed.

Duplicate matches fail by default and include their line and column. Select a one-based occurrence
or explicitly request every match:

```yaml
- type: markdown_edit
  with:
    from: {file: CHANGELOG.md}
    edits:
      - operation: set
        select: {kind: heading, level: 2, text: Fixed, occurrence: 2}
        field: text
        value: Resolved

      - operation: set
        select: {kind: list_item, task: true, text: Publish release}
        result: all
        field: checked
        value: true
```

Structural selectors disambiguate repeated content without relying on a global occurrence number.
`parent` matches the immediate native parent, `ancestor` matches any parent, and `contains` matches
a descendant. `before` and `after` are unique positional anchors:

```yaml
- type: markdown_edit
  with:
    from: {file: CHANGELOG.md}
    edits:
      - operation: set
        select:
          kind: heading
          text: Fixed
          after: {kind: heading, text: "Version 2.0"}
          before: {kind: heading, text: "Version 1.9"}
        field: text
        value: Bug fixes
```

Explicit heading IDs and attributes can be selected and edited. Automatic IDs are not generated:

```yaml
- type: markdown_edit
  with:
    from: {file: guide.md}
    edits:
      - operation: set
        select:
          kind: heading
          id: install
          attributes: {owner: docs}
        field: attribute.reviewed
        value: "true"
```

Set `attribute.<name>` to `null` to remove it. Heading `text` and `level` are also editable. ATX
markers and Setext underlines are retained when possible; changing a Setext heading to level 3–6
converts only that heading to ATX form.

Fenced code blocks, inline links and images, and existing GFM task items expose typed fields:

```yaml
- type: markdown_edit
  with:
    from: {file: README.md}
    edits:
      - operation: set
        select: {kind: code_block, language: ruby}
        field: content
        value: |
          Feature.enable!(:new_checkout)
          Checkout::Backfill.enqueue()

      - operation: set
        select: {kind: link, destination: /old/setup}
        result: all
        field: destination
        value: /getting-started
```

Code fields are `content`, `info`, and `language`; a replacement that contains a closing fence
automatically lengthens the original fence. Link fields are `text`, `destination`, and `title`;
image fields are `alt`, `source`, and `title`. Typed link fields require inline syntax. Reference
links and autolinks can still be replaced or deleted as complete nodes.

GFM tables support semantic cell and body-row editing. Select columns by one-based number or by a
unique header name:

```yaml
- type: markdown_edit
  with:
    from: {file: services.md}
    edits:
      - operation: set
        select:
          kind: table_cell
          column: Status
          header: false
          ancestor:
            kind: table
            headers: [Service, Status]
          parent:
            kind: table_row
            contains: {kind: table_cell, column: Service, text: API}
        field: content
        value: stable

      - operation: append
        select: {kind: table, headers: [Service, Status]}
        value: [Scheduler, experimental]

      - operation: delete
        select:
          kind: table_row
          ancestor: {kind: table, headers: [Service, Status]}
          contains: {kind: table_cell, column: Service, text: Legacy API}
```

Rows are lists of strings and must match the table width. `insert` adds a row before or after a
selected body row; `prepend` and `append` add body rows to a selected table. Column and alignment
mutations are not supported.

The complete operation set is:

| Operation | Fields | Behavior |
| --- | --- | --- |
| `set` | `field`; exactly one of `value`, `expr` | Update a typed field on a heading, fenced code block, inline link/image, task item, or table cell. |
| `replace` | exactly one of `value`, `expr` | Replace the selected native node. |
| `insert` | exactly one of `value`, `expr`; `position: before \| after` | Insert relative to the selected node. |
| `delete` | none | Delete only the selected native node. |
| `prepend` | exactly one of `value`, `expr` | Insert at the start of a document/node, or add the first table body row. |
| `append` | exactly one of `value`, `expr` | Insert at the end of a document/node, or add the last table body row. |

Selectors support `kind`, exact `text`, Go regular-expression `text_regex`, one-based
`occurrence`, and type-specific fields. Supported kinds are `document`, `heading`, `paragraph`,
`blockquote`, `code_block`, `link`, `image`, `list`, `list_item`, `table`, `table_row`, and
`table_cell`. `result` defaults to `one`; use `all` intentionally for repeated matches. Missing
nodes fail by default, while `missing: ignore` produces a successful no-op.

Use an expression for a computed replacement. It receives a JSON-compatible `current` descriptor,
the AST child-index `path`, and the zero-based match `index`:

```yaml
- id: number_chapters
  type: markdown_edit
  with:
    from:
      expr: vars.generated_markdown
    edits:
      - operation: set
        select: {kind: heading, level: 2}
        result: all
        field: text
        expr: '"Chapter " + string(index + 1) + ": " + current.text'
```

`from` requires exactly one of `file`, `var`, or `expr`. Variables and expressions must produce a
string and remain immutable; save the returned `steps.<id>.value` with `set` when desired. File
paths resolve from the active run directory, follow executor filesystems, reject symlinks and other
non-regular files, preserve permissions, and are installed atomically. Outputs are `value`,
`changed`, `count`, `changed_count`, and `matches`; file sources also return the resolved `file`.
The default `max_bytes` is `1MiB`.

## `extract`

Extract named, typed fields from one or every matching line of text with a friendly format:

```yaml
- id: release
  type: extract
  with:
    from: steps.build.stdout
    format: 'Release {version:string} build {build:integer}'
    variables: {version: release_version}
```

Raw named Go regular-expression captures are available for substring and multiline matching. See
[Text extraction](extract.md) for `match: all`, marker-delimited field extraction, the complete
capture syntax, failure behavior, and RE2 limitations.

## `semver`

Parse, compare, constrain, or increment semantic versions. A lowercase `v` prefix is accepted and
removed from normalized output.

Parse or compare:

```yaml
- id: release
  type: semver
  with:
    operation: parse
    version: v1.4.2-rc.1+build.7
    variable: release_version

- id: ordering
  type: semver
  with:
    operation: compare
    version: "{{ .vars.current_version }}"
    other: "{{ .vars.candidate_version }}"
```

Check a constraint or increment a part:

```yaml
- id: supported
  type: semver
  with:
    operation: constrain
    version: "{{ .vars.version }}"
    constraint: ">= 1.4.0, < 2.0.0"

- id: next_release
  type: semver
  with:
    operation: increment
    version: "{{ .vars.version }}"
    part: minor
    variable: next_version
```

Parse exposes normalized components. Compare exposes `comparison`, `less`, `equal`, and `greater`.
Constrain exposes `matched`; increment exposes `previous` and the new `version`.

## `time`

Capture the current time through an explicit workflow step, or parse an existing timestamp. The
final string is published as `.steps.<id>.value` and as a workflow variable. `variable` defaults
to the step ID, so this date stamp is available as both `.steps.stamp.value` and `.vars.stamp`:

```yaml
timezone: Europe/Warsaw
steps:
  - id: stamp
    type: time
    with:
      format: "2006-01-02"
```

Only the `time` step can read the clock. Supply the variable before the run -- with `--var` or in
the workflow's top-level `vars:` -- to make a run reproducible; the supplied string is published
unchanged and the clock is not consulted:

```console
wuko run release --var stamp=2026-08-29
```

A quoted string is published verbatim. An unquoted YAML date -- `stamp: 2026-08-29` -- decodes to
a time value rather than text, so it is rendered with the step's `format` in the zone it carries;
quote it to publish it exactly as written.

Only that pre-run set pins the step. A variable a step writes during the run never does, so a
`time` step inside a `loop` captures the clock on every iteration, and an earlier step that happens
to write a same-named variable does not silently replace the capture.

Parse, convert, adjust, and format in one step:

```yaml
- id: release_date
  type: time
  with:
    value: "2026-08-29T10:00:00Z"
    parse_format: "2006-01-02T15:04:05Z07:00"
    timezone: America/New_York
    add: {years: 1, months: -2, days: 3, duration: 90m}
    format: "2006-01-02"
    variable: published_date
```

Without `value`, the step captures the clock once per attempt. `parse_format` defaults to
RFC3339Nano and may only be used with `value`; `format` also defaults to RFC3339Nano. Timezone
precedence is the step's `timezone`, the workflow's top-level `timezone`, then the machine's local
timezone. Calendar years, months, and days are applied in that location before the exact Go
duration, so calendar days follow daylight-saving transitions. Negative adjustments subtract.

Variable names, overrides, formats, timezones, timestamps, durations, and adjustment fields are
validated strictly. A failed step publishes neither its output nor its variable. Use the shared
[`parseTime`, `addTime`, and `formatTime` helpers](template-functions.md#time-functions) to transform
recorded values later without reading the clock.

## `key_value`

Persist JSON-compatible values between runs. `local` stores live beside the workflow under
`.wuko/values/`; `global` stores live in Wuko's user configuration directory. Code Wuko fetched --
a remote workflow, or an action pulled from a URL -- has no local store and may use only `global`.

Set and get a preference:

```yaml
- id: save_theme
  type: key_value
  with:
    operation: set
    scope: global
    store: preferences
    key: theme
    value: dark

- id: load_theme
  type: key_value
  with:
    operation: get
    scope: global
    store: preferences
    key: theme
```

`set` takes exactly one of `value` or `expr`. Templates render to text, so
`value: "{{ .vars.count }}"` stores the string `"3"`; `expr` evaluates an Expr expression and stores
its result with the JSON type intact, which is what survives to the next run:

```yaml
- id: record_size
  type: key_value
  with:
    operation: set
    scope: local
    store: build
    key: artifact_bytes
    expr: "steps.package.value.size"
```

`update` reads and writes under one lock, so concurrent steps and separate `wuko` runs compose
instead of overwriting one another. Its `expr` sees the stored value as `current` and whether the
key existed as `found`. A `get` followed by a `set` releases the lock in between and cannot make
that guarantee:

```yaml
- id: count_run
  type: key_value
  with:
    operation: update
    scope: local
    store: counter
    key: runs
    expr: "found ? current + 1 : 1"
```

`variable` assigns the result to a workflow variable too, so a stored value is read once and used as
`vars.<name>` afterwards. `get` takes a `default` for a key that is absent, `list` takes a key
`prefix`, and `clear` empties a store in one operation:

```yaml
- id: load_retries
  type: key_value
  with:
    operation: get
    scope: global
    store: preferences
    key: retries
    default: 3
    variable: retries

- id: list_artifacts
  type: key_value
  with: {operation: list, scope: local, store: build, prefix: "artifact-"}

- id: drop_legacy
  type: key_value
  with: {operation: delete, scope: local, store: build, key: legacy-artifact}

- id: reset_build_state
  type: key_value
  with: {operation: clear, scope: local, store: build}
```

`get` returns `value` and `found`; `set` returns `value`; `update` returns the new `value`, the
`found` state it replaced, and whether it `changed`; `delete` returns the previous `value` and
`deleted`; `list` returns key-sorted `entries`; `clear` returns the number of keys it `cleared`. A
configured `variable` receives that same value, or the entries a `list` returned.

Each operation accepts only the fields it uses, so a `key` on a `list` or a `prefix` on a `get`
fails when the workflow loads. `get` and `list` create nothing: reading a store that was never
written returns an empty result and leaves the values directory absent. The store names `changed`,
`once`, and `picker` are reserved, because Wuko keeps change snapshots, idempotency outcomes, and
workflow picker history there; neither a workflow nor Lua's `wuko.kv` API may open them. Stores are
atomic plain JSON, not encrypted secret vaults.

## `changed`

Compare files and structured values with the detector's previous successful execution.

Detect source changes:

```yaml
- id: source_changed
  type: changed
  with:
    key: build-inputs
    root: .
    files: [go.mod, go.sum, "src/**/*.go", assets]

- id: build
  type: shell
  if: steps.source_changed.changed
  with: {command: ./build}
```

Detect configuration changes in a matrix:

```yaml
- id: target_changed
  type: changed
  with:
    key: "build-{{ .matrix.os }}-{{ .matrix.go_version }}"
    values:
      os: "{{ .matrix.os }}"
      go_version: "{{ .matrix.go_version }}"
```

At least one non-empty `files` or `values` input is required. The first execution returns
`changed: true`. Snapshots are stored atomically in the workflow-local
`.wuko/values/changed.json`; file timestamps and permissions do not affect the fingerprint. Files in
the values root are Wuko's own state, so a pattern that reaches it never makes a detector trigger on
a snapshot or on a store a `key_value` step wrote.
