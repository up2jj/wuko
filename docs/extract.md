# Text extraction

[Back to the available steps](../README.md#available-steps)

The `extract` step turns one string into named, typed step outputs. Use a friendly `format` for a
predictable line of text, or a Go regular expression with named captures for substring and
multiline matching. Extraction requires exactly one match by default; `match: all` collects an
ordered list. A `fields` map can derive several outputs from marker-delimited or regular-expression
content in the same input.

## Typed line formats

A format describes one complete input line. Literal spaces and tabs in the format match one or
more spaces or tabs in the input. Placeholders use `{name}` or `{name:type}`; an omitted type is
`string`.

```yaml
- id: release
  type: extract
  with:
    from: steps.build.stdout
    format: 'Release {version:string} build {build:integer}'
```

Given `Release 1.4.2 build 27`, this publishes the string `.steps.release.version` and the integer
`.steps.release.build`. The format may match one line within multiline input, but it must match
exactly one complete line unless `match: all` is selected.

Use `text` when the source is already convenient to render as a string:

```yaml
- id: coordinates
  type: extract
  with:
    text: '{{ .vars.location }}'
    format: 'latitude={latitude:number}, longitude={longitude:number}'
```

Use `from` for an existing workflow value. It reads the value without routing it through a
template and requires that value to be a string:

```yaml
- id: artifact
  type: extract
  with:
    from: vars.artifact_line
    format: 'artifact={name} size={size:integer}'
```

Exactly one of `text` and `from` is required. A `from` path must be dotted and rooted at `vars` or
`steps`.

## Capture types

The supported types are `string`, `integer`, `number`, `boolean`, and `json`. Conversion is strict
and does not trim captured text.

```yaml
- id: metadata
  type: extract
  with:
    text: 'name=wuko attempts=3 ratio=0.75 enabled=true labels={"tier":"stable","ports":[80,443]}'
    format: 'name={name:string} attempts={attempts:integer} ratio={ratio:number} enabled={enabled:boolean} labels={labels:json}'
```

This produces:

```yaml
name: wuko
attempts: 3
ratio: 0.75
enabled: true
labels:
  tier: stable
  ports: [80, 443]
```

Integers are signed base-10 64-bit values. Numbers are finite 64-bit floating-point values.
Booleans are exactly `true` or `false`. JSON must contain one complete JSON value and may produce
null, a scalar, an array, or an object.

## Raw regular expressions

Set `pattern` instead of `format` for Go's RE2-compatible regular expressions. The pattern searches
the complete input text with normal Go regexp semantics. Add anchors when the complete input or a
line must match.

```yaml
- id: release
  type: extract
  with:
    from: steps.build.stdout
    pattern: 'version=(?P<version>\S+)\s+build=(?P<build>[0-9]+)'
    types:
      build: integer
```

Every pattern must contain at least one uniquely named capture. `(?P<name>...)` and
`(?<name>...)` are accepted by Go. Unnamed groups may organize the expression but are ignored.
Named captures default to `string`; `types` overrides conversion by capture name.

Anchor the complete input:

```yaml
- id: checksum
  type: extract
  with:
    from: vars.checksum_line
    pattern: '^(?P<algorithm>sha256):(?P<digest>[0-9a-f]{64})$'
```

Use `(?m)` for line anchors or `(?s)` when `.` should include newlines:

```yaml
- id: section
  type: extract
  with:
    from: steps.report.stdout
    pattern: '(?s)BEGIN METADATA\s+(?P<body>\{.*?\})\s+END METADATA'
    types: {body: json}
```

Alternation can accept several labels while preserving one common named capture:

```yaml
- id: revision
  type: extract
  with:
    from: steps.describe.stdout
    pattern: '(?:revision|commit)=(?P<revision>[0-9a-f]{7,40})'
```

An optional named group must still participate in the selected match. For example,
`name(?:=(?P<value>.+))?` fails on `name`; it does not publish a null or omit `value`.

## Multiple matches

Set `match: all` with either `format` or `pattern` to collect every match in source order. No
matches succeed with an empty list:

```yaml
- id: queries
  type: extract
  with:
    from: steps.generate.stdout
    pattern: '(?m)^SQL: (?P<query>.+)$'
    match: all
    variables: {query: sql_queries}
```

Given two `SQL:` lines, the step publishes:

```yaml
matches:
  - query: SELECT 1;
  - query: SELECT 2;
count: 2
```

`match: all` replaces the per-capture outputs with `matches` and `count`, so a capture may not be
named `matches` or `count` in that mode.

`.vars.sql_queries` is the ordered list `["SELECT 1;", "SELECT 2;"]`. With several named
captures, each `matches` item is one complete typed record and each mapped variable receives the
ordered list for its capture. A non-participating capture or failed conversion in any record fails
the whole extraction. Omitting `match`, or setting it to `one`, preserves the direct capture
outputs and requires exactly one match.

## Marker fields

Use `fields` when one string contains several independently derived values. It is mutually
exclusive with top-level `format`, `pattern`, `types`, and `match`:

```yaml
- id: build_outputs
  type: extract
  with:
    from: steps.build.stdout
    fields:
      build_output:
        marker: action_test_binary
      binary_path:
        marker: action_test_binary
        regex: 'binary=(?P<value>[^\r\n]+)'
      binary_size:
        marker: action_test_binary
        regex: 'size=(?P<value>[0-9]+)'
        type: integer
      warnings:
        regex: 'warning: (?P<value>[^\r\n]+)'
        match: all
```

Each field requires `marker`, `regex`, or both. A regex is a Go RE2 expression with exactly one
named `value` capture. It defaults to `string`; `type` accepts the same conversions as raw pattern
captures. Marker-only fields return their complete payload as a string and cannot set `type`.
`match` defaults independently to `one` for each field; `all` returns an ordered list and permits
no matches. When both selectors are present, the regex searches only the selected marker payloads
and flattens its matches in source order.

Marker boundaries are complete LF- or CRLF-delimited lines:

```text
WUKO_OUTPUT_V1 {"event":"begin","key":"action_test_binary"}
binary=dist/action.test
size=128
WUKO_OUTPUT_V1 {"event":"end","key":"action_test_binary"}
```

Boundary lines are excluded and payload bytes, including line endings, are preserved. Keys are
case-sensitive and repeated blocks are allowed. A prefixed line is a boundary only when the rest of
the line is a JSON object, so program output that merely mentions `WUKO_OUTPUT_V1` stays payload;
once a line claims to be a boundary, parsing rejects malformed JSON, unknown fields or events,
nesting, unmatched keys, and unclosed blocks. All fields are atomic: one invalid
field prevents every output and variable from being published.

## Publishing workflow variables

Every named capture in one-match format/pattern mode is published directly under `.steps.<id>`.
Variables are opt-in and may be renamed explicitly:

```yaml
- id: release
  type: extract
  with:
    text: 'version=1.4.2 build=27'
    pattern: 'version=(?P<version>\S+) build=(?P<build>[0-9]+)'
    types: {build: integer}
    variables:
      version: release_version
      build: release_build
```

The step publishes `.steps.release.version`, `.steps.release.build`, `.vars.release_version`, and
`.vars.release_build`. Unmapped captures remain step outputs only. Two captures cannot target the
same variable. In `match: all` mode, mapped captures become lists. In `fields` mode, map field names
instead; scalar and list shapes are preserved.

## Shell and HTTP output

Extract a version from captured shell output:

```yaml
- id: inspect
  type: shell
  with: {command: my-tool, args: [--version]}

- id: version
  type: extract
  with:
    from: steps.inspect.stdout
    format: 'my-tool {version}'
```

Extract fields from a text HTTP response:

```yaml
- id: request
  type: http
  with:
    url: https://example.test/status
    response: text

- id: status
  type: extract
  with:
    from: steps.request.body
    pattern: 'state=(?P<state>[a-z]+); retry=(?P<retry>[0-9]+)'
    types: {retry: integer}
```

When an HTTP response is JSON, prefer `response: json` and `jsonpath`; `extract` accepts only
string input.

## Literal braces and backslashes

Within a friendly format, backslash escapes `{`, `}`, and backslash itself. Single-quoted YAML is
usually easiest because it preserves those backslashes:

```yaml
- id: windows_path
  type: extract
  with:
    text: 'path={root}\wuko'
    format: 'path=\{root\}\\{name}'
```

This extracts `name: wuko`. Any other backslash escape in `format` is rejected. Backslashes inside
`pattern` retain their normal Go regexp meaning.

## Failures and limitations

One-match extraction fails without publishing outputs or variables when no match is found, when
more than one match is found, when a named group does not participate, or when conversion fails.
For example, all of these fail:

```yaml
# No matching line.
- {id: missing, type: extract, with: {text: other, format: 'value={value}'}}

# Two matching lines.
- id: ambiguous
  type: extract
  with:
    text: |
      value=one
      value=two
    format: 'value={value}'

# Invalid integer conversion in raw-regex mode.
- id: invalid_integer
  type: extract
  with:
    text: 'count=many'
    pattern: 'count=(?P<count>.+)'
    types: {count: integer}
```

Additional limitations:

- There is no first-match mode. Use `one` for exact cardinality or `all` for every match.
- Friendly formats match complete individual lines and cannot span lines.
- Flexible format whitespace covers spaces and tabs, not line breaks.
- Raw patterns use Go RE2 and therefore do not support lookaround or backreferences.
- Regex captures are strings before conversion. Conversion is strict and locale-independent.
- Field regexes expose one named `value`; use top-level `pattern` for multi-capture records.
- The step does not trim, default, omit, recursively extract, infer schemas, or automatically
  publish workflow variables.
- Input must already be a string. Use `jsonpath` for typed arrays and objects, and Lua for
  transformations that need branching or mutation.
- The complete input remains in memory during matching. RE2 matching is linear-time, but very large
  text still consumes memory proportional to its size.
