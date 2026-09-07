# Executable plugins

Wuko plugins are persistent executables that exchange newline-delimited JSON with Wuko over stdin and stdout. A plugin may expose namespaced steps, executors, and helpers shared by Expr, Go templates, and Lua. Its stderr is reserved for diagnostics.

Create a standard-library-only Go plugin project:

```sh
wuko plugin init acme
wuko plugin init acme ./plugins/acme
```

The generated project contains a working `acme.uppercase` step, an `acme.local` executor, tests, a `justfile`, an example workflow, and a release-manifest template.

## Local discovery

A reference such as `type: acme.uppercase` searches for `wuko-plugin-acme` in this order:

1. The workflow's authoritative `plugins.acme` declaration.
2. The nearest ancestor `.wuko/plugins/acme/` directory.
3. `~/.wuko/plugins/acme/`.
4. `$XDG_CONFIG_HOME/wuko/plugins/acme/`.
5. `PATH`.

Installed and `PATH` plugins need no workflow declaration. An invalid higher-precedence installation is reported instead of being skipped. Referencing a namespaced type never initiates a network request unless the workflow explicitly declares that namespace.

## Pinned workflow plugins

```yaml
version: 1
name: plugin-example

plugins:
  acme:
    source: github:acme/wuko-plugin-acme@0123456789abcdef
    sha256: 8421e8f400000000000000000000000000000000000000000000000000000000
    with:
      endpoint: production

steps:
  - id: greeting
    type: acme.uppercase
    with:
      value: hello
```

`sha256` pins `plugin.json`; the manifest separately pins every platform archive. HTTPS and `github:owner/repository@ref[:path]` sources are supported. GitHub sources require a ref. A URL without an explicit manifest filename resolves to `plugin.json`. Artifacts must remain below the manifest directory and on the original HTTPS origin, or in the same GitHub repository and ref.

`with` is passed to `plugin.start`. Dependencies declaring the same namespace must use identical source, manifest digest, and start parameters.

## Install and uninstall

```sh
wuko plugin install https://plugins.example.com/acme
wuko plugin install github:acme/wuko-plugin-acme@v1.2.0
wuko plugin install ./release/plugin.json
wuko plugin install --global --reinstall SOURCE
wuko plugin uninstall acme
wuko plugin uninstall --global --yes acme
```

Installation downloads only the current-platform archive, verifies both digests, safely extracts regular files, performs the pure `initialize` handshake, and atomically publishes the installation. It does not call `plugin.start`.

## Helpers

A declared plugin can return immutable helper metadata from `initialize`:

```json
{
  "protocol": "wuko.plugin/v1",
  "namespace": "acme",
  "helpers": [{"name": "slug"}],
  "steps": [],
  "executors": []
}
```

Helper names use lowercase snake case. Wuko replaces hyphens in the plugin namespace with
underscores and exposes `<namespace>_<name>`. The example above is called as
`acme_slug(value)` in Expr, `{{ acme_slug value }}` in templates, and
`wuko.helpers.acme_slug(value)` in Lua. Generated names must not collide with built-in helpers or
another helper visible in the same workflow.

On invocation Wuko first runs `plugin.start` if necessary, then sends:

```json
{"id":"3","method":"helper.call","params":{"name":"slug","args":["Hello World"]}}
```

The plugin returns a wrapped JSON-compatible value so that `null` remains distinguishable from a
missing response:

```json
{"id":"3","result":{"value":"hello-world"}}
```

Helpers accept any number of JSON-compatible arguments. The plugin owns argument validation; Wuko
does not send workflow roots or other ambient context. Go-template pipelines append their piped
value to the argument list according to normal Go-template rules.

Only plugins explicitly pinned in the workflow's `plugins:` map contribute helpers. This lets Wuko
initialize helper declarations before parsing templates. A remote workflow carries these
declarations itself, so its caller does not redeclare them. Composite actions inherit the calling
workflow's helper set, while dependency workflows keep their own sets.

The helper list is fixed by `initialize`; `plugin.start` may prepare configuration or state used by
calls but cannot add or remove helpers. Commands such as `wuko validate` can evaluate environment or
action-reference templates, so they may start a plugin when those values call a helper.

## Lifecycle

Wuko owns one multiplexed process per namespace for the entire command. Validation launches and initializes a plugin but does not call `plugin.start`. Before the first runtime operation, Wuko calls `plugin.start` once. At teardown it closes executor sessions, runs successful-step cleanup, calls `plugin.stop` once with `completed`, `failed`, or `canceled`, sends mandatory `shutdown`, and escalates from SIGTERM to SIGKILL if necessary. Plugins are command-scoped and are not placed under the workflow background-service supervisor.

Version 1 excludes TTY interaction, streaming stdin, native task graphs, host-managed services, nested executor calls from plugin steps, filesystem access from plugin executors, and template or secret callbacks beyond declared helper calls. Because a plugin step cannot run through the host executor, plugin steps are rejected inside `executor:` blocks; because a plugin executor exposes no filesystem, `file` and `edit` are rejected during validation inside a plugin executor's scope.
