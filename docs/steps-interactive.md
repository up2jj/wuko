# Interactive steps

[Back to the available steps](../README.md#available-steps)

Interactive steps write their result to `.steps.<id>` and to the variable named by
`with.variable`. Single-value steps expose `value`; multiple choice and path selections expose
`values`. A value supplied with `--var` skips the prompt. Non-interactive runs, and interactive
steps inside concurrent groups, require a supplied value unless an optional `tui_choice` resolves
to no selection.

## `tui_input`

Collect editable text. Use `required`, validation rules, or modifiers when the workflow needs a
specific shape.

Prompt for a release name:

```yaml
- id: release_name
  type: tui_input
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
  type: tui_input
  with:
    variable: reviewers
    message: Enter comma-separated reviewers
    modifiers: {trim: true, split: ','}
```

Collect one typed JSON value:

```yaml
- id: deployment
  type: tui_input
  with:
    variable: deployment
    message: Enter deployment settings as JSON
    modifiers: {trim: true, json: true}
```

`trim` happens before validation. `split` uses a Go regular expression and preserves empty fields;
when combined with `trim`, each item is trimmed. `json` preserves JSON objects, lists, strings,
numbers, booleans, and null. `split` and `json` are mutually exclusive.

## `tui_password`

Collect masked text. Passwords support the same `required` and `validation` fields as `tui_input`,
but not its conversion modifiers.

Prompt for a token:

```yaml
- id: credentials
  type: tui_password
  with:
    variable: api_token
    message: Enter the API token
    required: true
```

Require a minimum-length passphrase:

```yaml
- id: signing
  type: tui_password
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

## `tui_choice`

Choose one value or an ordered list of values.

Use static choices:

```yaml
- id: environment
  type: tui_choice
  with:
    variable: environment
    message: Select an environment
    choices:
      - {label: Development, description: Local and test infrastructure, value: dev, default: true}
      - {label: Staging, description: Shared release verification, value: staging}
      - label: Production
        description: Customer-facing infrastructure
        value: prod
        disabled: true
        reason: Production access is not configured
```

Select multiple objects from an earlier step:

```yaml
- id: projects
  type: tui_choice
  with:
    variable: project_ids
    message: Select projects
    multiple: true
    min_selected: 2
    max_selected: 4
    from: steps.fetch.value.projects
    label_field: name
    value_field: id
    description_field: summary
    disabled_field: selection.disabled
    reason_field: selection.reason
    default_field: selection.default
```

Use scalar dynamic values without field mappings:

```yaml
- id: region
  type: tui_choice
  with:
    variable: region
    message: Select a region
    from: vars.available_regions
```

Static choices require `label` and scalar `value`; `description`, `disabled`, `reason`, and
`default` are optional. Every disabled choice requires a non-empty reason and cannot be a default.
In single mode, at most one choice may be the default; the picker initially focuses it. In multiple
mode, all defaults start selected in source order.

A scalar dynamic item is both its label and value and does not carry choice metadata. Object lists
use `label_field` and `value_field`, which may be dotted paths. `description_field` displays
additional searchable text. `disabled_field` and `default_field` must resolve to booleans. When
`disabled_field` is true, `reason_field` must resolve to a non-empty string. Disabled choices remain
visible and focusable, show their reason inline, and cannot be selected. Labels, descriptions, and
disabled reasons are all searchable.

The picker supports arrow keys or `j`/`k`, Home, End, Page Up, and Page Down. `/` filters labels
and metadata. In multiple mode, Space toggles values and Enter confirms them in selection order.
Outside filter editing, Ctrl+A selects enabled visible matches up to the remaining maximum and
Ctrl+X clears visible matches. Selections hidden by the filter are preserved. The header shows the
live selected count and configured minimum or maximum. Shortcut help wraps on narrow terminals.

`multiple` defaults to `false` and `required` defaults to `true`. Single selection exposes
`.steps.<id>.value`, `.steps.<id>.label`, and `.steps.<id>.selected`. With `required: false`, the
explicit `(none)` entry produces `selected: false`, `value: null`, and an empty label. A real
`null` choice remains distinguishable because it produces `selected: true`. Multiple selection
exposes `.steps.<id>.values`, `.steps.<id>.labels`, and `.steps.<id>.count`.

`min_selected` and `max_selected` are non-negative integers available only in multiple mode. When
either is present, these explicit bounds supersede `required`: an omitted minimum is 0 and an
omitted maximum is unlimited. The minimum cannot exceed the maximum or the number of enabled
choices. Defaults may start below the minimum so the user can add choices, but cannot exceed the
maximum. `max_selected: 0` is valid when the effective minimum is zero.

Pre-supplied values skip the picker but still reject disabled choices and enforce the bounds after
duplicate values are removed. Defaults only initialize an interactive picker; they never supply a
missing value in a non-interactive run. Without explicit bounds, optional empty choice sets and
empty selections succeed with empty lists, and an optional missing non-interactive value resolves
directly to no selection.

## `tui_path`

Select existing files or directories with a rooted filesystem browser. The selected variable
contains slash-normalized paths relative to `root`, making the default configuration portable
between machines.

Select several Go source files:

```yaml
- id: sources
  type: tui_path
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
  type: tui_path
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

## `tui_review`

Review scrollable plain text or a colored unified diff and collect a boolean approval decision.

```yaml
- id: review
  type: tui_review
  with:
    variable: approved
    message: Review the deployment plan
    content: "{{ .steps.plan.stdout }}"
    format: diff
    default: false
```

`format` accepts `plain` or `diff` and defaults to `plain`. Plain text wraps to the terminal width;
diff lines retain their alignment, color additions, removals, headers, and hunks, and support
horizontal panning. Use arrows or `j`/`k` to scroll, Page Up and Page Down to move by a page,
left/right or `h`/`l` to focus Reject or Approve, and Enter to submit. Shift+Left and Shift+Right
pan a diff horizontally. Shortcut help wraps on narrow terminals.

Reject is selected by default and returns `false` without failing the workflow. Set `default: true`
to focus Approve initially. A supplied variable must be a boolean and skips the review; a missing
variable fails a non-interactive run.

## `tui_confirm`

Collect a boolean decision. `default` selects the initial interactive answer; it does not answer a
non-interactive prompt automatically.

Guard deployment:

```yaml
- id: approval
  type: tui_confirm
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
  type: tui_confirm
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
