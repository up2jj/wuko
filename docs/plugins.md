# Executable plugins

Wuko plugins are persistent executables that exchange newline-delimited JSON with Wuko over stdin and stdout. A plugin may expose namespaced steps, executors, and helpers shared by Expr, Go templates, and Lua. Its stderr is reserved for diagnostics.

Create a standard-library-only Go plugin project:

```sh
wuko marketplace plugin init acme
wuko marketplace plugin init acme ./plugins/acme
```

The generated project contains a working `acme.uppercase` step, an `acme.local` executor, tests, a `justfile`, an example workflow, and a release-manifest template.

To implement a plugin in any language, use the technology-neutral
[Plugin protocol v1](plugin-protocol.md) reference. It starts with a minimal message loop and then
documents every request, response, event, lifecycle call, and cancellation rule.

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

Marketplace repositories are also accepted. An interactive terminal opens a searchable picker;
scripts select namespaces with repeatable `--package` flags:

```sh
wuko plugin install --global https://github.com/acme/wuko-marketplace
wuko plugin install --global --package acme https://github.com/acme/wuko-marketplace
wuko plugin install --global --reinstall --package acme https://github.com/acme/wuko-marketplace
```

The picker includes only plugins supporting the current OS and architecture. Explicitly selecting
an incompatible plugin returns a platform-specific error. A non-interactive marketplace install
without `--package` fails instead of implicitly installing everything. Wuko verifies the
marketplace's pinned `plugin.json` digest before applying the normal manifest, artifact, and
handshake checks.

## Create and publish a marketplace plugin

The following walkthrough starts with neither a plugin nor a marketplace. First initialize a
marketplace repository:

```sh
mkdir wuko-marketplace
cd wuko-marketplace
git init
wuko marketplace init
```

This creates a `manifest.json` plus `.wuko/workflows/` and `.wuko/plugin-sources/` source
directories. Create the example Go plugin next to the marketplace and build its complete release:

```sh
cd ..
wuko marketplace plugin init hello
cd wuko-plugin-hello
go test ./...
just release 0.1.0
```

The generated release helper uses only the Go toolchain and standard library. It cross-compiles
with CGO disabled for Darwin and Linux on amd64 and arm64, creates deterministic `tar.gz` archives,
and replaces the placeholder `plugin.json` with the selected version and archive digests:

```text
wuko-plugin-hello/
├── plugin.json
└── dist/
    ├── wuko-plugin-hello_Darwin_amd64.tar.gz
    ├── wuko-plugin-hello_Darwin_arm64.tar.gz
    ├── wuko-plugin-hello_Linux_amd64.tar.gz
    └── wuko-plugin-hello_Linux_arm64.tar.gz
```

Import and publish the release:

```sh
cd ../wuko-marketplace
wuko marketplace plugin add \
  --description "A simple uppercase step" \
  ../wuko-plugin-hello
wuko marketplace build
wuko marketplace build --check
```

`plugin add` accepts the same local, HTTPS, and pinned `github:` sources as direct installation. It
downloads every declared platform archive, validates its digest and safe executable structure,
and commits the import atomically without executing any binary. Imported sources and published
files have separate locations:

```text
.wuko/plugin-sources/hello/      # maintainer source, including build-only provenance metadata
plugins/hello/plugin.json        # public release manifest
plugins/hello/dist/*.tar.gz      # public platform archives
```

`marketplace build` also continues to package workflows from `.wuko/workflows/`. It preserves
unchanged files and timestamps and removes a stale generated plugin file only when it still matches
its previously recorded digest. `--check` performs the same validation and comparison without
writing; use it in CI.

Commit the imported source, generated catalog, and published files:

```sh
git add .
git commit -m "feat: add hello plugin"
git push
```

A public GitHub repository can be installed through its normal repository URL. To publish an
update, build the new release and replace the import transactionally:

```sh
cd ../wuko-plugin-hello
just release 0.2.0

cd ../wuko-marketplace
wuko marketplace plugin update hello ../wuko-plugin-hello
wuko marketplace build
wuko marketplace build --check
```

The fetched namespace must match `hello`. The existing description is preserved unless
`--description` is supplied; explicitly passing an empty description clears it. Failed validation
leaves the previous import intact.

Marketplace repositories distribute native executable code. SHA-256 verification protects file
integrity, but it does not establish publisher identity, make a plugin safe, or provide a sandbox.
Install plugins only from maintainers you trust.

## Version resolution and conflicts

A marketplace is an installation source, not a runtime version solver. Wuko runs at most one
plugin process for a namespace during a command and never automatically chooses the newest version.

| Workflow situation | Selected plugin | Conflict behavior |
| --- | --- | --- |
| `plugins.acme` declares a source and digest | That exact remote release | Installed project/global versions are ignored |
| No `plugins.acme` declaration | First valid project, home, config, or `PATH` candidate | An invalid higher-precedence candidate blocks fallback |
| Multiple workflows declare the same source, digest, and `with` | One shared process | No conflict |
| Declarations differ by source or digest | None | Command fails with both source identities and abbreviated digests |
| Declarations use the same release but different `with` | None | Command fails without printing configuration values |
| Project and global installations both exist | Project installation | Shadowing is intentional; versions are not compared |

An explicit declaration is authoritative and reproducible:

```yaml
plugins:
  acme:
    source: github:acme/wuko-plugin-acme@v1.2.0
    sha256: 8421e8f400000000000000000000000000000000000000000000000000000000
    with:
      endpoint: production
```

Without that declaration, a namespaced step or executor uses ambient local discovery. A
marketplace-installed plugin participates exactly like any other local installation. Installed
manifest version labels are informational during resolution; source and digest establish the
identity of pinned releases. Incompatible parallel APIs must use different namespaces.

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
