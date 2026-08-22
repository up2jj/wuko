# Interactive steps

[Back to the available steps](../README.md#available-steps)

Interactive steps write their result to `.steps.<id>` and to the variable named by
`with.variable`. Single-value steps expose `value`; multiple choice and path selections expose
`values`. A value supplied with `--var` skips the prompt. Non-interactive runs, and interactive
steps inside concurrent groups, require a supplied value.

## `input`

Collect editable text. Use `required`, validation rules, or modifiers when the workflow needs a
specific shape.

Prompt for a release name:

```yaml
- id: release_name
  type: input
  with:
    variable: release_name
    message: Enter the release name
    value: "{{ .steps.suggestion.value }}"
    required: true
    validation:
      min_length: 3
      max_length: 40
      pattern: '^[a-z][a-z0-9-]+$'
```

Collect a trimmed list:

```yaml
- id: reviewers
  type: input
  with:
    variable: reviewers
    message: Enter comma-separated reviewers
    modifiers: {trim: true, split: ','}
```

Collect one typed JSON value:

```yaml
- id: deployment
  type: input
  with:
    variable: deployment
    message: Enter deployment settings as JSON
    modifiers: {trim: true, json: true}
```

`trim` happens before validation. `split` uses a Go regular expression and preserves empty fields;
when combined with `trim`, each item is trimmed. `json` preserves JSON objects, lists, strings,
numbers, booleans, and null. `split` and `json` are mutually exclusive.

## `password`

Collect masked text. Passwords support the same `required` and `validation` fields as `input`, but
not its conversion modifiers.

Prompt for a token:

```yaml
- id: credentials
  type: password
  with:
    variable: api_token
    message: Enter the API token
    required: true
```

Require a minimum-length passphrase:

```yaml
- id: signing
  type: password
  with:
    variable: signing_passphrase
    message: Enter the signing passphrase
    validation:
      min_length: 12
      message: Use at least 12 characters
```

For unattended use, supply the variable explicitly, for example
`wuko run release --var api_token=secret`. Prefer environment-backed secrets where practical so
they do not appear in shell history.

## `choice`

Choose one value or an ordered list of values.

Use static choices:

```yaml
- id: environment
  type: choice
  with:
    variable: environment
    message: Select an environment
    choices:
      - {label: Development, value: dev}
      - {label: Production, value: prod}
```

Select multiple objects from an earlier step:

```yaml
- id: projects
  type: choice
  with:
    variable: project_ids
    message: Select projects
    multiple: true
    from: steps.fetch.value.projects
    label_field: name
    value_field: id
```

Use scalar dynamic values without field mappings:

```yaml
- id: region
  type: choice
  with:
    variable: region
    message: Select a region
    from: vars.available_regions
```

Dynamic sources must be non-empty lists. A scalar item is both its label and value. Object lists
use `label_field` and `value_field`, which may be dotted paths. Single selection stores a scalar;
multiple selection stores an ordered list.

## `path`

Select existing files or directories with a rooted filesystem browser. The selected variable
contains slash-normalized paths relative to `root`, making the default configuration portable
between machines.

Select several Go source files:

```yaml
- id: sources
  type: path
  with:
    variable: source_paths
    message: Select source files
    root: .
    kind: file
    multiple: true
    required: true
    patterns: ['**/*.go']
    show_hidden: false
```

Select one directory, including the configured root itself as `.`:

```yaml
- id: project
  type: path
  with:
    variable: project_dir
    message: Select a project directory
    root: projects
    kind: directory
```

`root` defaults to the active `.run.dir`. A relative root resolves from that directory, including
inside a `working_directory` block; an absolute root is also allowed. The resolved root must be an
existing directory and the browser cannot navigate above it. Symlinks are usable only when their
resolved targets remain inside the root.

`kind` accepts `file`, `directory`, or `either` and defaults to `file`. `multiple` defaults to
`false`, `required` defaults to `true`, and `show_hidden` defaults to `false`. `patterns` uses the
same relative doublestar syntax as the `glob` step and restricts selectable files; directories
remain visible for navigation. Patterns cannot be combined with `kind: directory`.

The picker always shows its active shortcuts. Use arrow keys or `j`/`k` to move, right or `l` to
open a directory, and left, `h`, or backspace to return. Enter selects in single mode; Space
toggles paths and Enter confirms in multiple mode. `/` filters the current listing, `ctrl+h`
toggles hidden entries, and Esc cancels. While filtering, Enter applies the filter and Esc clears
it. Shortcut help wraps on narrow terminals instead of disappearing.

Single selection exposes `.steps.<id>.value` and `.steps.<id>.root`. Multiple selection exposes
`.steps.<id>.values`, `.steps.<id>.count`, and `.steps.<id>.root`; list order follows selection
order. The `root` output is the canonical absolute root. Combine it with a selected relative path
when a later command needs an absolute path.

With `required: false`, selecting no path stores an empty string in single mode or an empty list in
multiple mode. Pre-supplied values skip the browser but must still be relative, exist, match the
declared kind and patterns, and remain inside the resolved root.

## `confirm`

Collect a boolean decision. `default` selects the initial interactive answer; it does not answer a
non-interactive prompt automatically.

Guard deployment:

```yaml
- id: approval
  type: confirm
  with:
    variable: approved
    message: Deploy this release?
    default: false

- id: deploy
  type: shell
  if: vars.approved
  with: {command: ./deploy}
```

Guard a destructive file operation:

```yaml
- id: replace
  type: confirm
  with:
    variable: replace_existing
    message: Replace the existing artifact?
    default: true

- id: remove_old
  type: file
  if: vars.replace_existing
  with: {operation: remove, path: dist/app.tar.gz}
```

A supplied value must be a boolean, for example `wuko run release --var approved=true`.
