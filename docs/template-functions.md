# Template, Expr, and Lua functions

The `secret(reference)` helper is available to Go templates and Expr expressions. It resolves
1Password (`op://...`) and Bitwarden (`bw://selector/item`) references lazily; see
[Secrets](secrets.md) for provider and authentication configuration.

Wuko exposes the same named helpers to Go templates, Expr, and Lua. Most are deterministic and
side-effect-free; the explicitly named generation and current-time helpers use secure randomness
or the host clock. Go templates always render strings, while Expr and Lua preserve typed results
such as booleans, numbers, lists, and objects.

Go template pipelines pass the value as the final function argument:

```gotemplate
{{ .vars.name | trim | lower | default "unknown" }}
```

Expr uses ordinary value-first calls:

```expr
default(lower(trim(vars.name)), "unknown")
```

Lua provides the helpers under `wuko.helpers` and uses snake_case names:

```lua
local name = wuko.helpers.default(wuko.helpers.lower(wuko.helpers.trim(wuko.args.name)), "unknown")
```

The tables below abbreviate `wuko.helpers` as `h`.

Invalid argument types, invalid encodings, and explicit validation failures stop template
rendering, Expr evaluation, or Lua execution.

## String functions

All string functions require string arguments. `split` returns a string list; `join` accepts a
list or array containing only strings.

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `lower` | `{{ value \| lower }}` | `lower(value)` | `h.lower(value)` | Unicode-aware lowercase string |
| `upper` | `{{ value \| upper }}` | `upper(value)` | `h.upper(value)` | Unicode-aware uppercase string |
| `trim` | `{{ value \| trim }}` | `trim(value)` | `h.trim(value)` | String with surrounding whitespace removed |
| `trimPrefix` | `{{ value \| trimPrefix prefix }}` | `trimPrefix(value, prefix)` | `h.trim_prefix(value, prefix)` | String with one matching prefix removed |
| `trimSuffix` | `{{ value \| trimSuffix suffix }}` | `trimSuffix(value, suffix)` | `h.trim_suffix(value, suffix)` | String with one matching suffix removed |
| `contains` | `{{ value \| contains substring }}` | `value contains substring` | `h.contains(value, substring)` | Boolean substring test |
| `hasPrefix` | `{{ value \| hasPrefix prefix }}` | `hasPrefix(value, prefix)` | `h.has_prefix(value, prefix)` | Boolean prefix test |
| `hasSuffix` | `{{ value \| hasSuffix suffix }}` | `hasSuffix(value, suffix)` | `h.has_suffix(value, suffix)` | Boolean suffix test |
| `replace` | `{{ value \| replace old replacement }}` | `replace(value, old, replacement)` | `h.replace(value, old, replacement)` | String with every match replaced |
| `split` | `{{ value \| split separator }}` | `split(value, separator)` | `h.split(value, separator)` | List of strings |
| `join` | `{{ values \| join separator }}` | `join(values, separator)` | `h.join(values, separator)` | Joined string |

For example:

```yaml
with:
  args:
    - '{{ .vars.application | trim | lower | replace "_" "-" }}'
```

The equivalent Expr is `replace(lower(trim(vars.application)), "_", "-")`; Lua uses
`h.replace(h.lower(h.trim(wuko.args.application)), "_", "-")`.

### Slugification

`slugify` converts text into a deterministic lowercase ASCII slug. It folds accented Latin
characters, drops remaining non-ASCII characters, replaces runs of punctuation or whitespace,
and trims separators. The options object is optional:

| Option | Values | Default |
| --- | --- | --- |
| `mode` | `"slug"` or `"git"` | `"slug"` |
| `separator` | `"-"`, `"_"`, or `"."` | `"-"` |
| `preserve_slash` | Boolean | `false`, or `true` in Git mode |

Go templates pass the value through the pipeline:

```gotemplate
{{ .vars.name | slugify }}
{{ .vars.name | slugify (dict "mode" "git") }}
```

Expr and Lua pass the value first:

```expr
slugify(vars.name, {"mode": "git"})
```

```lua
wuko.helpers.slugify(wuko.args.name, {mode = "git"})
```

Git mode preserves slash-separated hierarchy by default, producing values such as
`feature/payment-api`. It only permits `-` and `_` separators in that mode; `.` is rejected to
avoid Git-invalid trailing-dot and `.lock` components. Empty results and invalid option names or
types stop evaluation.

## Text transformations

These deterministic helpers cover common text cleanup and reshaping operations. Functions marked
as line-oriented transform each line independently and preserve the original `\n` or `\r\n`
separators, including a terminal newline. `reverseText`, `truncate`, and `rotate` count Unicode
grapheme clusters, so combining marks and joined emoji stay together.

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `reverseText` | `{{ value \| reverseText }}` | `reverseText(value)` | `h.reverse_text(value)` | Reverse each line by grapheme cluster |
| `reverseWords` | `{{ value \| reverseWords }}` | `reverseWords(value)` | `h.reverse_words(value)` | Reverse the words on each line and normalize spacing |
| `repeat` | `{{ value \| repeat count separator }}` | `repeat(value, count, separator)` | `h.repeat_text(value, count, separator)` | Repeat the complete value with an optional separator |
| `truncate` | `{{ value \| truncate length suffix }}` | `truncate(value, length, suffix)` | `h.truncate(value, length, suffix)` | Limit each line to a grapheme length, including the suffix |
| `squeeze` | `{{ value \| squeeze }}` | `squeeze(value)` | `h.squeeze(value)` | Collapse whitespace runs on each line to one space |
| `removeWhitespace` | `{{ value \| removeWhitespace }}` | `removeWhitespace(value)` | `h.remove_whitespace(value)` | Remove every Unicode whitespace character |
| `removePunctuation` | `{{ value \| removePunctuation }}` | `removePunctuation(value)` | `h.remove_punctuation(value)` | Keep only Unicode letters, numbers, and whitespace |
| `removeAccents` | `{{ value \| removeAccents }}` | `removeAccents(value)` | `h.remove_accents(value)` | Remove Unicode combining marks after decomposition |
| `removeNonASCII` | `{{ value \| removeNonASCII }}` | `removeNonASCII(value)` | `h.remove_non_ascii(value)` | Remove every non-ASCII character |
| `stripHTML` | `{{ value \| stripHTML }}` | `stripHTML(value)` | `h.strip_html(value)` | Remove angle-bracketed tags and decode HTML entities |
| `tabsToSpaces` | `{{ value \| tabsToSpaces width }}` | `tabsToSpaces(value, width)` | `h.tabs_to_spaces(value, width)` | Replace each tab with `width` spaces |
| `spacesToTabs` | `{{ value \| spacesToTabs width }}` | `spacesToTabs(value, width)` | `h.spaces_to_tabs(value, width)` | Replace each run of `width` spaces with a tab |
| `newlinesToSpaces` | `{{ value \| newlinesToSpaces }}` | `newlinesToSpaces(value)` | `h.newlines_to_spaces(value)` | Join lines with one space |
| `spacesToNewlines` | `{{ value \| spacesToNewlines }}` | `spacesToNewlines(value)` | `h.spaces_to_newlines(value)` | Put each whitespace-separated word on its own line |
| `rotate` | `{{ value \| rotate count }}` | `rotate(value, count)` | `h.rotate(value, count)` | Rotate each line left; a negative count rotates right |
| `quote` | `{{ value \| quote delimiter }}` | `quote(value, delimiter)` | `h.quote(value, delimiter)` | Wrap each line with a delimiter |
| `escapeRegex` | `{{ value \| escapeRegex }}` | `escapeRegex(value)` | `h.escape_regex(value)` | Escape text for literal use in a Go RE2 pattern |
| `normalizeUnicode` | `{{ value \| normalizeUnicode form }}` | `normalizeUnicode(value, form)` | `h.normalize_unicode(value, form)` | Apply NFC, NFD, NFKC, or NFKD normalization |

The optional arguments use these defaults: `repeat` uses count `2` and an empty separator;
`truncate` uses length `80` and an empty suffix; tab conversion uses width `4`; `rotate` uses count
`1`; `quote` uses `"`; and `normalizeUnicode` uses NFC. Lua calls the repeat helper
`repeat_text` because `repeat` is a Lua keyword. Arguments may be omitted from the right:

```gotemplate
{{ .vars.message | truncate 72 "..." }}
{{ .vars.pattern_literal | escapeRegex }}
{{ .vars.label | normalizeUnicode "nfkc" }}
```

```expr
repeat(reverseWords(vars.words), 3, " | ")
rotate(vars.token, -1)
```

```lua
local h = wuko.helpers
local summary = h.truncate(h.squeeze(wuko.args.message), 72, "...")
wuko.output("summary", summary)
wuko.output("literal_pattern", h.escape_regex(wuko.args.pattern_literal))
```

Expr already provides `reverse(list)` for collections. Wuko leaves that builtin unchanged and
uses `reverseText(string)` for text, so both operations remain type-specific and unambiguous.
Counts and lengths must be non-negative, tab widths must be positive, a truncation suffix must fit
inside the requested length, and quote delimiters must not be empty. Invalid normalization forms
stop evaluation, as do expansions whose result would exceed the 64 MiB memory budget: `repeat` caps
both its count and its total output size, and `tabsToSpaces` caps its total output size. Numeric
arguments accept any integer type, so counts and lengths read from `parseJSON` output work the same
as literals.

`stripHTML` removes only real tags: a `<` is markup only when a name, `/`, `!`, or `?` follows it,
so `5 > 3 and 2 < 4` survives unchanged, and `>` inside a quoted attribute value does not end the
tag early. Because it decodes entities after removing tags, `&lt;b&gt;` becomes literal `<b>` — the
result is plain text for humans and logs, not sanitized HTML, so never inject it back into a page.

## Defaults and validation

`default`, `coalesce`, and `required` use Go template truth rules. These values are empty:

- `nil`
- `false`
- Numeric zero
- An empty string
- An empty list, array, or map
- A nil pointer, channel, or function

Struct values and non-nil pointers are not empty.

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `default` | `{{ value \| default fallback }}` | `default(value, fallback)` | `h.default(value, fallback)` | `fallback` when `value` is empty; otherwise `value` |
| `coalesce` | `{{ coalesce first second ... }}` | `coalesce(first, second, ...)` | `h.coalesce(first, second, ...)` | First non-empty value, or `nil` |
| `required` | `{{ value \| required message }}` | `required(value, message)` | `h.required(value, message)` | Value, or an evaluation error containing `message` |

Templates remain strict. A missing field fails before `default` receives it:

```gotemplate
{{ .vars.missing | default "fallback" }}
```

Use the explicit optional lookup helper when absence is expected:

```gotemplate
{{ get "region" .vars | default "eu-west-1" }}
```

```expr
default(get(vars, "region"), "eu-west-1")
```

`required` is useful for a present but empty value:

```gotemplate
{{ .vars.application | required "application is required" }}
```

## Collections

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `list` | `{{ list first second ... }}` | `list(first, second, ...)` | `h.list(first, second, ...)` | List containing the arguments |
| `dict` | `{{ dict "key" value ... }}` | `dict("key", value, ...)` | `h.dict("key", value, ...)` | String-keyed object |
| `get` | `{{ get key object }}` | `get(object, key)` | `h.get(object, key)` | Entry value, or `nil` when absent |
| `hasKey` | `{{ hasKey key object }}` | `hasKey(object, key)` | `h.has_key(object, key)` | Boolean key-presence test |
| `keys` | `{{ object \| keys }}` | `keys(object)` | `h.keys(object)` | Alphabetically sorted string keys |
| `sortAlpha` | `{{ values \| sortAlpha }}` | `sortAlpha(values)` | `h.sort_alpha(values)` | Alphabetically sorted copy of a string list |

`dict` requires alternating string keys and values. An odd argument count or non-string key is an
error. Repeated keys are allowed and the final value wins. `get`, `hasKey`, and `keys` accept maps
with string keys. `sortAlpha` never mutates the source list.

Expr also supports native list and object literals, which are usually clearer than constructors:

```expr
["linux", "darwin"]
{"enabled": true, "replicas": 3}
```

Use `"key" in object` as an idiomatic alternative to `hasKey(object, "key")`.

## Expr-only typed collection operations

Expr provides higher-order operations for transforming typed lists. These functions are available
wherever Wuko accepts Expr, including `set.expr`, `assert.expr`, conditions, batch items/sizes,
foreach items, and matrix axes. They are not Go-template or Lua helpers.

| Function | Example | Result |
| --- | --- | --- |
| `groupBy` | `groupBy(items, .tier)` | Object whose keys contain ordered lists of matching items |
| `indexBy` | `indexBy(items, "id")` | Object mapping each string field value to its original item |
| `sort` | `sort(items)` or `sort(items, "desc")` | Sorted copy of a scalar list |
| `sortBy` | `sortBy(items, .priority)` | Copy sorted by a computed value; accepts optional `"desc"` |
| `uniq` | `uniq(items)` | Copy containing the first occurrence of each value |
| `flatten` | `flatten(items)` | Recursively flattened, one-dimensional list |
| `chunk` | `chunk(items, 3)` | Ordered lists of at most three items |

Predicates use Expr's current item shorthand. For example, this groups services without changing
their order inside each group:

```expr
groupBy(vars.services, .tier)
```

`indexBy` accepts an exact top-level field name. Every item must be a string-keyed object, the
field must exist and contain a string, and keys must be unique. Invalid items and duplicate keys
stop evaluation rather than silently discarding or replacing data:

```expr
indexBy(vars.services, "id")
```

`sort`, `sortBy`, `uniq`, `flatten`, and `chunk` return new lists and do not modify their inputs.
`flatten` recursively removes all nested list levels. `chunk` requires a positive size and retains
a shorter final chunk. Collection operations compose naturally in pipelines:

```expr
vars.priorities | uniq() | sort("desc") | chunk(2)
```

Use `set` to retain a transformed value for later steps:

```yaml
- id: service_index
  type: set
  with:
    variable: services_by_id
    expr: 'indexBy(vars.services, "id")'
```

A collection expression can also feed foreach directly. This remains useful when further Expr
composition determines the chunks. For ordinary fixed-size execution, prefer the first-class
[`batch` control](workflow-control.md#batch), which provides `.batch.index`, `.batch.items`, fan-out
limits, and clearer tree/progress output.

The equivalent manual foreach form passes each list through `.foreach.item`:

```yaml
- id: deploy_batches
  foreach:
    items: chunk(vars.targets, 10)
    steps:
      - id: deploy
        type: shell
        with:
          command: ./deploy-batch
          args: ['{{ .foreach.item | toJSONCompact }}']
```

## Parsing and serialization

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `indent` | `{{ value \| indent spaces }}` | `indent(value, spaces)` | `h.indent(value, spaces)` | Every line prefixed with `spaces` spaces |
| `nindent` | `{{ value \| nindent spaces }}` | `nindent(value, spaces)` | `h.nindent(value, spaces)` | A newline followed by the indented value |
| `toJSON` | `{{ value \| toJSON }}` | `toJSON(value)` | `h.to_json(value)` | JSON indented with two spaces |
| `toJSONCompact` | `{{ value \| toJSONCompact }}` | `toJSONCompact(value)` | `h.to_json_compact(value)` | Compact JSON |
| `toYAML` | `{{ value \| toYAML }}` | `toYAML(value)` | `h.to_yaml(value)` | YAML with a terminal newline |
| `parseJSON` | `{{ value \| parseJSON }}` | `parseJSON(value)` | — | Typed value decoded from one JSON value |
| `parseYAML` | `{{ value \| parseYAML }}` | `parseYAML(value)` | — | Typed value decoded from one YAML document |

Indent widths must be non-negative. A terminal newline is preserved without adding trailing
spaces after it. JSON object keys and string-keyed YAML map keys are serialized deterministically.
Encoding an unsupported value stops evaluation.

`parseJSON` and `parseYAML` return Wuko's runtime value types: null, booleans, strings, signed or
unsigned integers, floating-point numbers, lists, and string-keyed objects. JSON integral values
remain integers when they fit in 64 bits; fractional and exponent values become floating-point
numbers. YAML mappings must have string keys, and YAML-native scalar types outside Wuko's runtime
model, such as timestamps, are rejected.

Go templates can parse an object and continue through the normal collection helpers:

```gotemplate
{{ .vars.payload | parseJSON | get "name" }}
```

Expr preserves the parsed structure, so member access, indexing, and collection operations work
without an intermediate decode step:

```expr
parseJSON(vars.payload).services[0].name
```

```expr
parseYAML(vars.config).targets | sortAlpha()
```

Parsed scalar types are preserved as well:

```expr
parseJSON('{"enabled":true,"retries":3}').enabled == true
```

Parsing and serialization can be composed to normalize input into another format:

```yaml
- id: normalize
  type: set
  with:
    variable: normalized
    expr: 'toJSONCompact(parseYAML(inputs.configuration))'
```

Each parser accepts exactly one value or document. Whitespace after a JSON value is allowed, but
a second value is rejected:

```text
{"first": true} {"second": true}
```

YAML multi-document streams are also rejected, including streams whose additional document is
empty:

```yaml
name: first
---
name: second
```

Lua does not expose `h.parse_json` or `h.parse_yaml`. Its existing `wuko.json.decode(value)` API
remains available for JSON input.

`nindent` is useful when embedding generated configuration:

```gotemplate
spec:
  labels:{{ .vars.labels | toYAML | nindent 4 }}
```

## URI functions

`parseURI` and `buildURI` parse and construct absolute or relative URI references without making
network requests. Hierarchical components and query parameters are decoded while represented as
typed values; `buildURI` applies the required percent-encoding when it creates the final string.
The `opaque` field follows Go's `net/url` representation and contains the encoded opaque payload.

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `parseURI` | `{{ value \| parseURI }}` | `parseURI(value)` | `h.parse_uri(value)` | Object containing decoded URI components |
| `buildURI` | `{{ parts \| buildURI }}` | `buildURI(parts)` | `h.build_uri(parts)` | Canonically encoded URI string |

Build an HTTP endpoint in a template without manually escaping query values. A query value may be
a string or a list of strings; lists create repeated parameters:

```yaml
- id: fetch
  type: http
  with:
    url: >-
      {{ dict
          "scheme" "https"
          "host" "api.example.com"
          "path" "/releases/latest"
          "query" (dict
            "channel" "stable"
            "tag" (list "v1" "v2"))
        | buildURI }}
```

The resulting URL is:

```text
https://api.example.com/releases/latest?channel=stable&tag=v1&tag=v2
```

Parse a URI and select its host:

```gotemplate
{{ "https://api.example.com:8443/releases?id=42#notes" | parseURI | get "host" }}
```

The result is `api.example.com:8443`. Serialize the complete parsed object when it needs to remain
text inside a template:

```gotemplate
{{ "https://example.com/search?q=hello+world&q=wuko" | parseURI | toJSON }}
```

That produces:

```json
{
  "fragment": "",
  "host": "example.com",
  "opaque": "",
  "path": "/search",
  "query": {
    "q": [
      "hello world",
      "wuko"
    ]
  },
  "scheme": "https"
}
```

Expr preserves the typed result. Construct an endpoint inside a `set` step:

```yaml
- id: endpoint
  type: set
  with:
    variable: endpoint
    expr: |
      buildURI({
        "scheme": "https",
        "host": "api.example.com",
        "path": "/search",
        "query": {
          "q": vars.search_term,
          "include": ["releases", "prereleases"]
        }
      })
```

Parsed components and individual query values can be used directly:

```expr
parseURI(vars.endpoint).host
parseURI(vars.endpoint).query.include[0]
```

The second expression returns the first `include` value, `"releases"`. A scheme and host are not
required, so Expr can also create a relative reference:

```expr
buildURI({
  "path": "/search",
  "query": {"q": "hello world"}
})
```

The result is `/search?q=hello+world`.

Lua exposes the same operations with snake_case names:

```lua
local h = wuko.helpers

local endpoint = h.build_uri({
  scheme = "https",
  host = "api.example.com",
  path = "/releases",
  query = {
    channel = "stable",
    tag = {"v1", "v2"}
  }
})

local parts = h.parse_uri(endpoint)

wuko.output("uri", endpoint)
wuko.output("host", parts.host)
wuko.output("tags", parts.query.tag)
```

The outputs are the URI
`https://api.example.com/releases?channel=stable&tag=v1&tag=v2`, host `api.example.com`, and tag
list `["v1", "v2"]`.

Opaque URIs such as `mailto:` use `opaque` instead of `host` and `path`:

```lua
local mailto = wuko.helpers.build_uri({
  scheme = "mailto",
  opaque = "ops@example.com",
  query = {subject = "Deployment ready"}
})
```

The result is `mailto:ops@example.com?subject=Deployment+ready`.

Parsed objects always contain `scheme`, `opaque`, `host`, `path`, `query`, and `fragment`.
`username` appears only when userinfo is present, and `password` appears only when the URI contains
a password delimiter; an explicitly empty password is therefore distinct from no password. Query
keys map to ordered string lists so duplicate values remain ordered. Building sorts query keys for
deterministic output, preserves the order of values for each key, and uses standard form encoding
(`+` for spaces in query values). This is semantic canonicalization rather than a byte-for-byte
round trip.

Blank or malformed URIs, invalid percent escapes, unknown component names, non-string components,
invalid query value types, a password without `username`, and combinations of `opaque` with
userinfo, `host`, or `path` stop evaluation with an error.

## Conventional Commit functions

`buildConventionalCommit` creates a validated message without running Git. Its object accepts
`type`, optional `scope`, `subject`, optional `breaking` and `body`, the `types`, `scopes`, and
`force_scope` validation options, and an optional `task` suffix. `task` works by itself;
`task_regex` optionally validates it.

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `buildConventionalCommit` | `{{ config \| buildConventionalCommit }}` | `buildConventionalCommit(config)` | `h.build_conventional_commit(config)` | Commit message string |
| `isConventionalCommit` | `{{ message \| isConventionalCommit options }}` | `isConventionalCommit(message, options)` | `h.is_conventional_commit(message, options)` | Boolean validity |

Build a task-bearing message in a Go template:

```gotemplate
{{ dict
    "type" "fix"
    "scope" "auth"
    "subject" "prevent expired session reuse"
    "task" "WUKO-142"
  | buildConventionalCommit }}
```

Validate a rendered value with options. Invalid messages return `false`; malformed options and
regexes stop template evaluation:

```gotemplate
{{ .vars.commit_message
   | isConventionalCommit
       (dict
         "strict" true
         "types" (list "feat" "fix")
         "task_regex" "WUKO-[0-9]+") }}
```

Expr uses value-first calls and preserves the boolean result:

```expr
buildConventionalCommit({
  "type": "fix",
  "scope": "auth",
  "subject": "prevent expired session reuse",
  "task": "WUKO-142"
})
```

```expr
isConventionalCommit(vars.commit_message, {
  "strict": true,
  "types": ["feat", "fix"],
  "task_regex": "WUKO-[0-9]+"
})
```

Lua exposes the same operations with snake_case names:

```lua
local h = wuko.helpers

local message = h.build_conventional_commit({
  type = "fix",
  scope = "auth",
  subject = "prevent expired session reuse",
  task = "WUKO-142",
})

if not h.is_conventional_commit(message, {
  strict = true,
  task_regex = "WUKO-[0-9]+",
}) then
  error("generated commit message is invalid")
end
```

The validation options are `types`, `scopes`, `force_scope`, `strict`, and `task_regex`. A configured
task pattern uses Go RE2, is automatically anchored to the end of the first-line header, must start
at the header's beginning or follow whitespace, and makes the suffix mandatory. Bodies may follow the
task-bearing header, but a `body` whose lines start with `#` is rejected because Git strips them.

## Time functions

Time helpers transform explicit string values. They never read the clock; use the [`time`
step](steps-data.md#time) to capture a recordable, overridable current time. Go layouts use the
reference instant syntax, such as `2006-01-02` for a calendar date.

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `parseTime` | `{{ value \| parseTime layout timezone }}` | `parseTime(value, layout, timezone)` | `h.parse_time(value, layout, timezone)` | Canonical RFC3339Nano string |
| `addTime` | `{{ value \| addTime adjustments }}` | `addTime(value, adjustments)` | `h.add_time(value, adjustments)` | Adjusted RFC3339Nano string |
| `formatTime` | `{{ value \| formatTime layout timezone }}` | `formatTime(value, layout, timezone)` | `h.format_time(value, layout, timezone)` | Formatted string |

The timezone argument to `parseTime` and `formatTime` is optional. Zone-less parsing defaults to
UTC; an explicit IANA timezone supplies calendar and daylight-saving semantics. `addTime` accepts
an object containing `years`, `months`, `days`, `duration`, and optional `timezone`. A blank
`timezone` keeps the instant's own zone, so `.workflow.timezone` can be passed straight through in
a workflow that declares none. It requires at least one of `years`, `months`, `days`, or
`duration` to be present -- a computed adjustment may be zero, which returns the same instant --
applies calendar fields before the exact Go duration, and rejects unknown options:

```gotemplate
{{ .vars.stamp | addTime (dict "days" 1 "timezone" .workflow.timezone) | formatTime "January 2, 2006" .workflow.timezone }}
```

```expr
formatTime(
  addTime(vars.stamp, {"days": 1, "timezone": workflow.timezone}),
  "January 2, 2006",
  workflow.timezone,
)
```

```lua
local h = wuko.helpers
local parsed = h.parse_time(wuko.args.date, "2006-01-02", "UTC")
local next_week = h.add_time(parsed, {days = 7, timezone = "UTC"})
wuko.output("next_week", h.format_time(next_week, "2006-01-02", "UTC"))
```

`parseTime` accepts a custom source layout and normalizes it for composition. `addTime` and
`formatTime` consume RFC3339 or RFC3339Nano strings, so custom source text must be parsed first.
Offset-bearing inputs retain their instant before an explicit timezone conversion.

## Encoding and decoding

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `base64Encode` | `{{ value \| base64Encode options }}` | `base64Encode(value, options)` | `h.base64_encode(value, options)` | Base64 text |
| `base64Decode` | `{{ value \| base64Decode options }}` | `base64Decode(value, options)` | `h.base64_decode(value, options)` | Decoded UTF-8 text |
| `hexEncode` | `{{ value \| hexEncode uppercase }}` | `hexEncode(value, uppercase)` | `h.hex_encode(value, uppercase)` | Hexadecimal text |
| `hexDecode` | `{{ value \| hexDecode }}` | `hexDecode(value)` | `h.hex_decode(value)` | Decoded UTF-8 text |
| `urlEncode` | `{{ value \| urlEncode }}` | `urlEncode(value)` | `h.url_encode(value)` | RFC 3986 component encoding |
| `urlDecode` | `{{ value \| urlDecode }}` | `urlDecode(value)` | `h.url_decode(value)` | Decoded URI component |
| `htmlEncode` | `{{ value \| htmlEncode }}` | `htmlEncode(value)` | `h.html_encode(value)` | Escaped HTML text |
| `htmlDecode` | `{{ value \| htmlDecode }}` | `htmlDecode(value)` | `h.html_decode(value)` | Text with entities resolved |

The options argument is optional. Base64 options are `alphabet` (`standard` or `url`) and
`padding` (default `true`). Decoding uses the selected alphabet and padding mode strictly and
rejects binary results that are not valid UTF-8. URL encoding is for one URI component: it keeps
only RFC 3986 unreserved bytes, represents spaces as `%20`, and preserves a literal `+` during
decoding. Use `buildURI` when constructing a complete URI.

## Hashing and authentication codes

| Function | Go template | Expr | Lua |
| --- | --- | --- | --- |
| `md5` | `{{ value \| md5 options }}` | `md5(value, options)` | `h.md5(value, options)` |
| `sha1` | `{{ value \| sha1 options }}` | `sha1(value, options)` | `h.sha1(value, options)` |
| `sha256` | `{{ value \| sha256 options }}` | `sha256(value, options)` | `h.sha256(value, options)` |
| `sha512` | `{{ value \| sha512 options }}` | `sha512(value, options)` | `h.sha512(value, options)` |
| `hmacSHA256` | `{{ value \| hmacSHA256 key options }}` | `hmacSHA256(value, key, options)` | `h.hmac_sha256(value, key, options)` |
| `hmacSHA512` | `{{ value \| hmacSHA512 key options }}` | `hmacSHA512(value, key, options)` | `h.hmac_sha512(value, key, options)` |

Options are optional: `encoding` is `hex` (the default) or `base64`, and `uppercase` applies only
to hex output; combining it with `base64` is rejected rather than ignored. MD5 and SHA-1 are compatibility checksums and must not be used for security. Use
SHA-256 or SHA-512 for digests and HMAC for keyed authentication. Resolve HMAC keys with `secret`
or pass them through protected Lua arguments; never place them directly in workflow source.

## Numbers and inspection

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `baseConvert` | `{{ value \| baseConvert from to uppercase }}` | `baseConvert(value, from, to, uppercase)` | `h.base_convert(value, from, to, uppercase)` | Signed integer in another base |
| `romanEncode` | `{{ value \| romanEncode }}` | `romanEncode(value)` | `h.roman_encode(value)` | Canonical Roman numeral |
| `romanDecode` | `{{ value \| romanDecode }}` | `romanDecode(value)` | `h.roman_decode(value)` | Integer |
| `ordinal` | `{{ value \| ordinal }}` | `ordinal(value)` | `h.ordinal(value)` | English ordinal such as `22nd` |
| `countBytes` | `{{ value \| countBytes }}` | `countBytes(value)` | `h.count_bytes(value)` | UTF-8 byte count |
| `countRunes` | `{{ value \| countRunes }}` | `countRunes(value)` | `h.count_runes(value)` | Unicode code-point count |
| `countGraphemes` | `{{ value \| countGraphemes }}` | `countGraphemes(value)` | `h.count_graphemes(value)` | User-perceived character count |
| `countWords` | `{{ value \| countWords }}` | `countWords(value)` | `h.count_words(value)` | Whitespace-separated word count |
| `countLines` | `{{ value \| countLines }}` | `countLines(value)` | `h.count_lines(value)` | Logical line count |

`baseConvert` supports bases 2 through 36 and arbitrary-size integers; `uppercase` is optional and
defaults to `false`. Roman numerals cover 1 through 3999 and decoding rejects non-canonical forms.
Empty text has zero lines, and a terminal newline does not add another empty line.

## Secure generators and current time

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `uuid` | `{{ uuid options }}` | `uuid(options)` | `h.uuid(options)` | UUID v4 or v7 string |
| `randomString` | `{{ randomString length charset }}` | `randomString(length, charset)` | `h.random_string(length, charset)` | Secure random characters |
| `randomInt` | `{{ randomInt min max }}` | `randomInt(min, max)` | `h.random_int(min, max)` | Inclusive secure random integer |
| `randomToken` | `{{ randomToken bytes encoding }}` | `randomToken(bytes, encoding)` | `h.random_token(bytes, encoding)` | Secure random token |
| `password` | `{{ password length options }}` | `password(length, options)` | `h.password(length, options)` | Secure random password |
| `currentTime` | `{{ currentTime }}` | `currentTime()` | `h.current_time()` | Current UTC RFC3339Nano time |
| `unixTimestamp` | `{{ unixTimestamp unit }}` | `unixTimestamp(unit)` | `h.unix_timestamp(unit)` | Current Unix timestamp |

Every argument shown after the function name is optional except both `randomInt` bounds.
Defaults are UUID v4, a 16-character alphanumeric string, a 32-byte hexadecimal token, a
20-character password, and Unix seconds. UUID options are `version` (`4` or `7`), `uppercase`, and
`compact`. Token encodings are `hex`, padded `base64`, and unpadded `base64url`.

Password groups `lower`, `upper`, `digits`, and `symbols` default to enabled. Set a group to
`false` to omit it or set `exclude_ambiguous` to `true`; at least one group must remain, the length
must accommodate every enabled group, and every enabled group is guaranteed to occur.

Each generator call and each clock call is evaluated independently. Capture a value once when it
must be reused:

```yaml
- id: identity
  type: set
  with:
    variable: run_id
    expr: 'uuid({"version": 7})'
```

For reproducible or `--var`-overridable time, continue to use the `time` step. Expr's implicit
`now()` builtin remains disabled; `currentTime()` and `unixTimestamp()` are the explicit,
documented nondeterministic boundaries.

## Availability and safety

Helpers are available in named and inline Go templates, in every Wuko Expr surface, and in Lua as
`wuko.helpers`. Expr surfaces include step conditions, polling `until` expressions, batch, foreach,
and matrix expressions, composite-action inputs and outputs, and the `set` and `assert` steps.

Helpers cannot read the process environment or filesystem, execute commands, access the network,
or quote shell commands. Hashes are deterministic; generator and current-time helpers are the only
nondeterministic functions. They use the operating system's cryptographic random source or clock
and may change whenever an expression or template is evaluated again. JSON and YAML serialization
does not make a value safe to interpolate into executable shell source. Lua's existing
`wuko.json.encode` remains the compact JSON encoder; `wuko.helpers.to_json` adds the shared indented
form.
