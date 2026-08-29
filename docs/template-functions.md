# Template, Expr, and Lua functions

Wuko exposes the same deterministic, side-effect-free helpers to Go templates, Expr, and Lua.
Go templates always render strings, while Expr and Lua preserve typed results such as booleans,
numbers, lists, and objects.

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

## Indentation and serialization

| Function | Go template | Expr | Lua | Result |
| --- | --- | --- | --- | --- |
| `indent` | `{{ value \| indent spaces }}` | `indent(value, spaces)` | `h.indent(value, spaces)` | Every line prefixed with `spaces` spaces |
| `nindent` | `{{ value \| nindent spaces }}` | `nindent(value, spaces)` | `h.nindent(value, spaces)` | A newline followed by the indented value |
| `toJSON` | `{{ value \| toJSON }}` | `toJSON(value)` | `h.to_json(value)` | JSON indented with two spaces |
| `toJSONCompact` | `{{ value \| toJSONCompact }}` | `toJSONCompact(value)` | `h.to_json_compact(value)` | Compact JSON |
| `toYAML` | `{{ value \| toYAML }}` | `toYAML(value)` | `h.to_yaml(value)` | YAML with a terminal newline |

Indent widths must be non-negative. A terminal newline is preserved without adding trailing
spaces after it. JSON object keys and string-keyed YAML map keys are serialized deterministically.
Encoding an unsupported value stops evaluation.

`nindent` is useful when embedding generated configuration:

```gotemplate
spec:
  labels:{{ .vars.labels | toYAML | nindent 4 }}
```

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

## Availability and safety

Helpers are available in named and inline Go templates, in every Wuko Expr surface, and in Lua as
`wuko.helpers`. Expr surfaces include step conditions, polling `until` expressions, batch, foreach,
and matrix expressions, composite-action inputs and outputs, and the `set` and `assert` steps.

Wuko intentionally keeps these helpers side-effect-free. They cannot read the process environment
or filesystem, execute commands, access the network, obtain the current time, generate random
values, perform cryptography, or quote shell commands. Time helpers parse and transform only their
explicit arguments; current time enters state only through the `time` step. Expr's own `now()`
builtin is disabled for the same reason, so an expression that calls it fails to compile with
`unknown name now`: capture the instant in a [`time` step](steps-data.md#time) and read
`.vars.<id>`, which keeps the run recordable and overridable. JSON and YAML serialization does not
make a value safe to interpolate into executable shell source. Lua's existing `wuko.json.encode`
remains the compact JSON encoder; `wuko.helpers.to_json` adds the shared indented form.
