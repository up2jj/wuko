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

## Availability and safety

Helpers are available in named and inline Go templates, in every Wuko Expr surface, and in Lua as
`wuko.helpers`. Expr surfaces include step conditions, polling `until` expressions, foreach and
matrix expressions, composite-action inputs and outputs, and the `set` and `assert` steps.

Wuko intentionally keeps these helpers side-effect-free. They cannot read the process environment
or filesystem, execute commands, access the network, obtain the current time, generate random
values, perform cryptography, or quote shell commands. The helpers operate only on their
arguments. JSON and YAML serialization does not make a value safe to interpolate into executable
shell source. Lua's existing `wuko.json.encode` remains the compact JSON encoder;
`wuko.helpers.to_json` adds the shared indented form.
