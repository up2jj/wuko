# Variable imports

Wuko can load typed workflow variables from JSON and TOML files. The same Viper-backed loader is
used by command-line variable files and the `import_vars` workflow step, so both entry points use
the same decoding, key normalization, and merge rules.

## File shape and supported values

A JSON file must contain a top-level object, and a TOML file uses its root table. The document is
the variable object itself; it must not be wrapped in a `vars` key.

```json
{
  "target": "linux",
  "database": {
    "host": "localhost",
    "ports": [5432, 5433],
    "credentials": {
      "username": "admin"
    }
  }
}
```

```toml
target = "linux"

[database]
host = "localhost"
ports = [5432, 5433]

[database.credentials]
username = "admin"
```

Strings, booleans, numbers, arrays, and nested objects remain typed. Viper treats keys as
case-insensitive and exposes them in lowercase, including nested keys. Dotted keys use Viper's
nested-key semantics.

Each top-level key becomes one workflow variable. In the examples above, `target` and `database`
are variables; `host` remains a field of `database`.

## Loading variables before a run

Use repeatable `--var-file` flags with `run`, `validate`, or `tree`:

```sh
wuko run release \
  --var-file defaults.toml \
  --var-file environments/production.json \
  --var version=v1.2.3
```

Relative paths resolve from the directory where Wuko was invoked. Values are prepared before the
workflow or command is evaluated, using this precedence from lowest to highest:

1. The workflow's `vars`.
2. Variable files in command-line order.
3. Explicit `--var` values.

Files merge from left to right by replacing complete top-level variables. Nested objects are not
deep-merged.

For example, these values:

```toml
# defaults.toml
[database]
host = "localhost"
port = 5432
```

```json
{
  "database": {
    "host": "production.example.com"
  }
}
```

produce a `database` variable containing only `host`. The later file replaces the entire
`database` object, so `port` is no longer present.

## Loading variables during a workflow

Use `import_vars` when the files must be selected or read during execution:

```yaml
- id: configuration
  type: import_vars
  with:
    files:
      - defaults.toml
      - "environments/{{ .vars.environment }}.json"

- id: describe
  type: shell
  with:
    command: printf
    args: ["target=%s\\n", "{{ .vars.target }}"]
```

At least one file is required. Paths are rendered when the step starts and are relative to the
directory containing the owning workflow or composite action. Files merge from left to right
with the same top-level replacement rule as `--var-file`.

Because this is a runtime step, imported values overwrite variables already in workflow state,
including values originally supplied with `--var`. The variables are committed only after every
file has been read and decoded successfully. If any file fails, the step commits neither partial
variables nor outputs. A retry rereads every file.

After a successful step, its results are available as:

- `.vars.<name>` for every imported top-level variable.
- `.steps.<step-id>.variables` for the complete merged object.
- `.steps.<step-id>.count` for its number of top-level variables.

Validation and dry-run validate the step configuration without reading runtime files.

## Accessing nested values

Go templates access nested object fields normally. Arrays use zero-based indexing through the
`index` function:

```yaml
host: "{{ .vars.database.host }}"
first_port: "{{ index .vars.database.ports 0 }}"
imported_host: "{{ .steps.configuration.variables.database.host }}"
```

Lua receives nested objects and arrays as tables. `wuko.var` looks up a top-level variable, so
read `database` first and then traverse it. Lua arrays are one-based:

```lua
local database = wuko.var("database")

if database and database.credentials then
  print(database.host)
  print(database.ports[1])
  print(database.credentials.username)
end
```

Variables supplied through `--var-file` are available to Lua from the beginning of execution.
Variables produced by `import_vars` are available to later sequential steps. A Lua step can
publish another typed value with `wuko.set_var(name, value)`; cyclic or mixed-key Lua tables
cannot be converted into workflow variables.

## Sequential and concurrent execution

Sequential steps see variables committed by earlier steps. Concurrent children receive the same
snapshot taken before their group starts, so a child cannot consume variables imported by a
sibling. Concurrent children also fail if more than one child writes the same top-level variable.

Place `import_vars` before a concurrent group when every child needs the imported configuration:

```yaml
- id: configuration
  type: import_vars
  with:
    files: [configuration.toml]

- concurrent:
    steps:
      - id: build
        type: lua
        with:
          source: |
            local build = wuko.var("build")
      - id: deploy
        type: shell
        with:
          command: deploy
          args: ["{{ .vars.deployment.target }}"]
```

## Remote workflows and actions

Runtime imports work in local workflows and in remote workflow or composite-action archives that
include the referenced variable files. A workflow loaded directly from a remote YAML URL, or an
action loaded from a standalone remote manifest, has no companion files to import.

## Adding another format

The shared loader intentionally keeps format support in one extension-to-Viper-format mapping.
Supporting another Viper configuration format is localized: register its extension and format in
`variables/files.go`, then add decoding, command, workflow-step, and documentation coverage. Both
`--var-file` and `import_vars` will use the new format automatically.

JSON and TOML are currently the only accepted formats. Keeping an explicit allowlist prevents a
file from being interpreted differently merely because Viper supports additional formats.
