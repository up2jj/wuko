# Interactive steps

[Back to the available steps](../README.md#available-steps)

Interactive steps write their result to `.steps.<id>.value` and to the variable named by
`with.variable`. A value supplied with `--var` skips the prompt. Non-interactive runs, and
interactive steps inside concurrent groups, require a supplied value.

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
