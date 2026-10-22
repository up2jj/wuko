# Executable plugins

Wuko plugins are persistent executables that exchange newline-delimited JSON with Wuko over stdin and stdout. A plugin may expose namespaced steps, executors, helpers shared by Expr, Go templates, and Lua, and v3 static composite actions. Its stderr is reserved for diagnostics.

Create a project from an embedded language preset:

```sh
wuko marketplace plugin init acme --template go
wuko marketplace plugin init acme ./plugins/acme --template elixir
wuko marketplace plugin init acme --template generic
wuko marketplace plugin init --list-templates
```

`generic` is the default and documents the language-neutral protocol and bundle contract. The Go
template contains a working `acme.uppercase` step, an `acme.local` executor, SDK-backed tests, an
example workflow, and a release helper. The Elixir template contains a Mix escript with an example
step. Every template includes a conventional `wuko.release.yaml`; it owns the native build recipe
and returns the completed bundle directory. No template requires a `justfile`.

New projects always implement Wuko's newest protocol, currently [plugin protocol
v4](plugin-protocol-v4.md). The generated concurrent reader handles cancellation and responses to
plugin-to-host callbacks while operations run. The [protocol v1](plugin-protocol.md) reference
remains available for maintaining existing plugins.

## Contents

- [Declared plugins and local lockfiles](#declared-plugins-and-local-lockfiles)
- [Remote workflows](#remote-workflows)
- [Shared verified cache](#shared-verified-cache)
- [Install and uninstall](#install-and-uninstall)
- [Create and publish a marketplace plugin](#create-and-publish-a-marketplace-plugin)
- [Version resolution and conflicts](#version-resolution-and-conflicts)
- [Helpers](#helpers)
- [Protocol selection](#protocol-selection)
- [Lifecycle](#lifecycle)

## Declared plugins and local lockfiles

Every plugin namespace must be declared by the workflow that uses it. Installed binaries and
`PATH` never implicitly select a plugin. For example:

```yaml
version: 1
name: plugin-example

plugins:
  acme:
    source: github:acme/wuko-plugin-acme@v1.2.0
    with:
      endpoint: production

steps:
  - id: greeting
    type: acme.uppercase
    with:
      value: hello
```

`with` is passed to `plugin.start`. HTTPS and `github:owner/repository@ref[:path]` sources are
supported; GitHub sources require a ref. A source without a manifest filename resolves to
`plugin.json`. Local bundle directories and `plugin.json` paths are also supported, relative to
the declaring workflow file. A local rebuild requires an explicit pin update.

Local workflows share `wuko.lock` with the other workflows in their directory. Wuko does not
search parent directories for a lockfile. Stdin workflows use their base directory. The lock
records the canonical source, namespace, and SHA-256 of the original manifest bytes:

```yaml
version: 1
plugins:
  - namespace: acme
    source: github:acme/wuko-plugin-acme@v1.2.0:plugin.json
    sha256: 8421e8f400000000000000000000000000000000000000000000000000000000
```

The digest above is illustrative. Wuko generates actual pins; the manifest separately pins each
platform archive. Different sources and versions coexist in one lockfile, while identical
namespace/source pairs share a pin. Configuration values are never stored in the lockfile.

```sh
wuko plugin lock build.yaml test.yaml deploy.yaml
wuko run --file build.yaml
wuko run --file build.yaml --frozen-lockfile
wuko plugin lock deploy.yaml --update
wuko plugin lock legacy.yaml --migrate
```

Normal local execution creates missing pins but never refreshes existing ones. `plugin lock`
verifies selected files and their dependency declarations without initializing plugin processes.
`--update` intentionally refreshes the selected sources. `--migrate` moves legacy inline plugin
`sha256` values into the lockfile and removes them from YAML after verification; it cannot be
combined with `--update`. Inline plugin SHA fields are otherwise rejected.

Commit `wuko.lock` with the workflows. First resolution establishes the release identity; later
runs enforce it. `validate`, `tree`, and `run --dry-run` require existing pins. The persistent
`--frozen-lockfile` flag requires existing pins throughout local dependency and child-workflow
execution, without modifying the lock. It still allows verified downloads and cache repair.

Artifacts must remain below the manifest directory and on the original HTTPS origin, or in the
same GitHub repository and ref. An HTTPS query is forwarded to same-origin requests, but omitted
from lockfiles, persisted provenance, diagnostics, and command output. Fragments are rejected.

## Remote workflows

Remote workflow invocations ignore local lockfiles, even malformed ones, and never create or
modify them. They fetch each plugin manifest once per invocation and reuse a cached archive only
if its digest matches that manifest. A changed manifest selects a new cached release; the source
is trusted anew for each invocation. A bundle packaged with a remote workflow must stay inside
that package. Packaged `wuko.lock` files are not imported.

`--frozen-lockfile` is rejected for remote workflows. For locked execution, save the workflow
locally, run `wuko plugin lock FILE`, and then run the local file with the frozen flag. Remote
manifests must remain available even when plugin archives are already cached.

## Shared verified cache

Wuko stores original manifests and platform archives under
`os.UserCacheDir()/wuko/plugins/v1/<manifest-sha256>/<os>-<arch>/`. This is normally
`~/Library/Caches/wuko/plugins` on macOS and `$XDG_CACHE_HOME/wuko/plugins` or
`~/.cache/wuko/plugins` on Linux. `WUKO_PLUGIN_CACHE_DIR` overrides the plugin cache root.

All projects share the cache. Wuko verifies bytes on reuse, extracts into a private temporary
run directory, and cleans that directory up after the command. Cache publication and lockfile
updates are protected against concurrent writers. Damaged entries are repaired from the declared
source; a failed download or checksum mismatch fails resolution without changing existing pins.

After checking the shared cache, Wuko can import matching verified bundle bytes from the nearest
project/ancestor installation, then home and OS config installations. Unrelated or invalid
installations are skipped. Old installations lacking original bundle bytes need a one-time
verified fetch. Bare executables and installation markers alone are never sufficient.

## Install and uninstall

```sh
wuko plugin install https://plugins.example.com/acme
wuko plugin install github:acme/wuko-plugin-acme@v1.2.0
wuko plugin install ./release/plugin.json
wuko plugin install --global --reinstall SOURCE
wuko plugin uninstall acme
wuko plugin uninstall --global --yes acme
```

Installation prefetches only the current-platform archive, verifies both digests, safely extracts regular files, performs the pure `initialize` handshake, and atomically publishes the installation. It does not call `plugin.start`. Original manifest/archive bytes are retained and imported into the shared cache. Workflows still need explicit declarations and local pins. Uninstall removes the selected installation but leaves independently cached releases available. A marketplace selection is prepared in full and committed as one transaction, so a failed plugin does not leave earlier selections installed.

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
handshake checks. New installation markers record their own format version, platform, and the
extracted executable digest. Discovery rejects a copied installation for another platform or an
executable that changed after installation.

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
directories. Create the example Go plugin next to the marketplace and resolve its SDK dependency:

```sh
cd ..
wuko marketplace plugin init hello --template go
cd wuko-plugin-hello
go mod tidy
```

The Go scaffold pins `github.com/up2jj/wuko` but ships no `go.sum`, so the first build needs
`go mod tidy`. It uses `plugin/sdk` for transport and `plugin/sdk/sdktest` for protocol-level tests.

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

Build and publish the release through its conventional workflow:

```sh
cd ../wuko-marketplace
wuko marketplace plugin release \
  --version 0.1.0 \
  --description "A simple uppercase step" \
  ../wuko-plugin-hello
wuko marketplace build --check
```

For a local plugin project, `plugin release` runs its root `wuko.release.yaml` with the project as
the run directory and passes `--version` as `vars.version`. The workflow must return a string output
named `bundle` identifying a directory inside the project. Wuko then validates every declared
archive and digest, verifies the resulting manifest version, adds or updates the imported source,
and rebuilds the marketplace transactionally. A first release requires `--description`; later
releases preserve it unless the flag is supplied. Imported sources and published files have
separate locations:

```text
.wuko/plugin-sources/hello/      # maintainer source, including build-only provenance metadata
plugins/hello/plugin.json        # public release manifest
plugins/hello/dist/*.tar.gz      # public platform archives
```

`marketplace build` also continues to package workflows from `.wuko/workflows/`. It preserves
unchanged files and timestamps and removes a stale generated plugin file only when it still matches
its previously recorded digest. `--check` performs the same validation and comparison without
writing; use it in CI.

Marketplace commands never create or refresh plugin pins. A workflow package that declares
`plugins:` must already have a committed `wuko.lock` (run `wuko plugin lock FILE` in the package);
otherwise the build fails with a missing-pin error. The committed lock is published inside the
package archive, so installers get exactly the pinned plugin releases.

Commit the imported source, generated catalog, and published files:

```sh
git add .
git commit -m "feat: add hello plugin"
git push
```

A public GitHub repository can be installed through its normal repository URL. To build and
publish an update transactionally:

```sh
cd ../wuko-marketplace
wuko marketplace plugin release --version 0.2.0 ../wuko-plugin-hello
wuko marketplace build --check
```

The namespace comes from `plugin.json`, and its version must match `--version`. Releases reject
semantic-version downgrades and changed artifacts under an already-published version; repeating an
identical version is a no-op.
The existing description is preserved unless `--description` is supplied, and failed validation or
marketplace generation leaves the previous source and published catalog intact. The lower-level
`plugin add` and `plugin update` commands remain available when a maintainer intentionally wants to
change imported source without rebuilding immediately.

HTTPS and pinned `github:` sources are already-built bundles and are never executed. A local
already-built bundle can be published explicitly with `--skip-build`; `--version` is optional in
that mode and acts as an assertion when supplied.

Remove a maintained plugin source, then explicitly rebuild to clean its generated files:

```sh
wuko marketplace plugin remove hello --yes
wuko marketplace build
wuko marketplace build --check
```

Marketplace repositories distribute native executable code. SHA-256 verification protects file
integrity, but it does not establish publisher identity, make a plugin safe, or provide a sandbox.
Install plugins only from maintainers you trust.

## Version resolution and conflicts

A marketplace is an installation source, not a runtime version solver. Wuko never automatically
chooses the newest release. Every workflow declares its sources; local workflows pin them in
`wuko.lock`, while remote runs resolve the manifests served during that invocation.

| Situation | Behavior |
| --- | --- |
| Namespace is not declared | Fail, even if installed or on `PATH` |
| Local source has a lock entry | Use that manifest digest; download only missing or damaged content |
| Local source has no lock entry | Normal execution adds it; frozen/read-only commands fail |
| Several workflows use the same namespace/source/digest/`with` | Share one process during the command |
| Workflows in one command conflict by source, digest, or `with` | Fail; configuration values stay redacted |
| Separate commands use different releases | Both releases coexist in the cache |
| A remote workflow is run | Fetch its plugin manifests; leave local lockfiles untouched |

Incompatible parallel APIs within one command must use different namespaces. Project and global
installations can supply matching content, but do not override declarations or lockfile pins.

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

Only plugins explicitly declared in the workflow's `plugins:` map contribute helpers. This lets Wuko
initialize helper declarations before parsing templates. A remote workflow carries these
declarations itself, so its caller does not redeclare them. Composite actions inherit the calling
workflow's helper set, while dependency workflows keep their own sets.

The helper list is fixed by `initialize`; `plugin.start` may prepare configuration or state used by
calls but cannot add or remove helpers. Commands such as `wuko validate` can evaluate environment or
action-reference templates, so they may start a plugin when those values call a helper.

## Protocol selection

Release manifests may declare `wuko.plugin/v1`, `wuko.plugin/v2`, `wuko.plugin/v3`, or
`wuko.plugin/v4`. Wuko records that value in
installation markers and uses it for the handshake. Marker version 2 also records the executable
digest. Legacy markers without `marker_version` remain readable as marker version 1; a missing
`protocol` in a legacy marker continues to mean v1. Legacy markers do not gain executable-integrity
checking until the plugin is reinstalled.

A directly discovered development executable, or an executable found only through `PATH`, may not
have a release manifest or marker. Wuko offers v4, then v3, then v2, then v1; an executable that answers with
one of those versions is accepted on that answer, and one that fails the handshake outright is retried in a fresh
process. This negotiation does not change strict v1 behavior: after v1 is selected, its
declarations, contexts, events, and request direction remain unchanged.

`wuko marketplace plugin init` always targets the newest protocol supported by that Wuko release.
It has a project-template flag, not a protocol-selection flag. The Go template pins the matching
Wuko module and uses `plugin/sdk` for the v4 bidirectional transport, request-scoped cancellation,
serialized writes, declarations, and host callbacks. It retains bounded executor streaming and
process-group cleanup. Other language templates implement the same wire contract. See the
[v2 service and callback examples](plugin-protocol-v2.md#complete-service-sequence).

## Lifecycle

Wuko owns one multiplexed process per namespace for the entire command. Validation launches and initializes a plugin but does not call `plugin.start`. Before the first runtime operation, Wuko calls `plugin.start` once. At teardown it closes executor sessions, runs successful-step cleanup, calls `plugin.stop` once with `completed`, `failed`, or `canceled`, sends mandatory `shutdown`, and escalates the plugin process group from SIGTERM to SIGKILL if necessary. This also removes descendants left behind by a faulty plugin. Plugins are command-scoped and are not placed under the workflow background-service supervisor.

Version 1 excludes TTY interaction, streaming stdin, native task graphs, host-managed services,
nested executor calls from plugin steps, filesystem access from plugin executors, and template or
secret callbacks beyond declared helper calls. Version 2 adds only managed step services and
request-scoped host callbacks; it does not lift the other restrictions. Version 3 adds static
structured actions and otherwise preserves the v2 runtime contract. Version 4 lifts the
execution-environment restrictions for plugins that declare them: an executor filesystem,
streamed stdin, TTY runs, borrowing the user's terminal, and steps that run inside their own
plugin's executors. See [protocol v4](plugin-protocol-v4.md).

A v2 step's `host_callbacks` list is the boundary for what it can reach back into: `secret` is
available only to a step that declares `host.function.call`, and `host.template.render` alone
renders with `secret` denied rather than granting it indirectly. The list is declared by the
plugin itself during `initialize`, so locking the plugin manifest pins what it may ask for. Unless
it declares v4 executor access, a plugin step is rejected inside `executor:` blocks, and one that
declares it runs only inside its own plugin's executors. Unless a plugin executor declares a v4
filesystem, `file` and `edit` are rejected during validation inside its scope.
