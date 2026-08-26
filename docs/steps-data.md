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

## `extract`

Extract named, typed fields from exactly one line of text with a friendly format:

```yaml
- id: release
  type: extract
  with:
    from: steps.build.stdout
    format: 'Release {version:string} build {build:integer}'
    variables: {version: release_version}
```

Raw named Go regular-expression captures are available for substring and multiline matching. See
[Text extraction](extract.md) for the complete syntax, capture types, examples, failure behavior,
and RE2 limitations.

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

## `key_value`

Persist JSON-compatible values between runs. `local` stores live beside the workflow under
`.wuko/values/`; `global` stores live in Wuko's user configuration directory.

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

List or delete entries:

```yaml
- id: list_preferences
  type: key_value
  with: {operation: list, scope: global, store: preferences}

- id: delete_legacy
  type: key_value
  with:
    operation: delete
    scope: local
    store: build
    key: legacy-artifact
```

`get` returns `value` and `found`; `set` returns `value`; `delete` returns the previous `value` and
`deleted`; `list` returns key-sorted `entries`. Stores are atomic plain JSON, not encrypted secret
vaults.

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
`.wuko/values/changed.json`; file timestamps and permissions do not affect the fingerprint.
