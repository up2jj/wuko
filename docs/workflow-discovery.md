# Workflow discovery

Wuko workflow discovery turns the current working directory, home directory, and platform
configuration directory into a list of selectable workflow sources. The same discovery rules are
used by `wuko run NAME`, `wuko list`, `wuko validate`, shell completion, and the bare `wuko`
workflow picker.

## Search locations

Discovery checks locations in this order:

1. `.wuko/workflows/` in the current working directory.
2. `.wuko/workflows/` in each parent directory, moving toward the filesystem root.
3. `~/.wuko/workflows/`.
4. The platform user config directory under `wuko/workflows/`.

The current directory and all of its parents are `local` locations. The home and platform config
locations are `global` locations. Missing directories are skipped, and duplicate paths created by
the directory inputs are visited only once.

## Files and names

Each ordinary directory contributes files whose extension is `.yaml` or `.yml` (case-insensitive),
including nested directories. An installed package directory containing `.wuko-package.json` is
different: its root `wuko.yaml` is discovered as one workflow, and the YAML `name` is the selector.
Package sidecars and nested implementation files are not discovered separately. A standalone
`deploy.yaml` is run with `wuko run deploy`; an installed package named `release` is run with
`wuko run release`.

The YAML `name` field is still required and validated, but it is metadata for the loaded workflow;
lookup by `NAME` uses the filename stem. The description shown by `wuko list` and the picker comes
from the YAML `description` field. Workflows with `depends_on` also show a sorted `depends on ...`
summary; aliases that differ from their workflow name use `alias=workflow`. The optional
`invokable` boolean defaults to `true`. Set it to `false` for a workflow that may run only as a
`depends_on` prerequisite. A workflow may optionally declare named `targets`; each target is
listed as a separate selectable source while retaining the same filename-stem workflow name.

Within each directory, workflow names are sorted before loading. Declaring both `name.yaml` and
`name.yml` in the same directory is an error. Selectors reject absolute paths and traversal
components.

## What discovery loads

Discovery reads every candidate workflow to obtain its metadata. Each file must be a valid,
single-document Wuko workflow: known YAML fields, version, required steps, step IDs, and workflow
schema are checked. `require` step fragments are expanded relative to the file that references
them, including nested fragments.

Discovery does not resolve composite actions referenced by `uses`, whether their source is a local
path, HTTPS URL, or command. Action resolution happens later when a workflow is fully prepared for
execution, validation, or tree rendering.

An error in any discovered candidate aborts the discovery operation, including an error in a
workflow that would otherwise be shadowed by a closer definition. Missing directories are the
only discovery-time filesystem errors that are ignored.

## Precedence and shadowing

The first definition encountered for a filename stem is effective. Therefore, a closer local
workflow wins over the same name in a parent directory, and any local workflow wins over global
workflows. Between global locations, the home workflow wins over the platform-config workflow.

`Discover` returns only effective sources. `DiscoverAll` returns effective and shadowed sources and
marks each source with `Effective: true` or `false`.

Invocation metadata does not affect precedence. An effective workflow with `invokable: false`
still shadows same-named workflows in lower-precedence locations and remains resolvable as a
dependency.

For example, if all of these exist, the project copy is selected:

```text
project/.wuko/workflows/release.yaml   local, effective
home/.wuko/workflows/release.yaml      global, shadowed
config/wuko/workflows/release.yaml     global, shadowed
```

Results are sorted by workflow name and then target after precedence has been applied. Sorting
affects display order, not which definition wins.

## Command behavior

- `wuko run NAME [TARGET]` calls effective discovery and runs the selected source only when it is
  directly invokable. Target workflows require `TARGET`; legacy workflows retain `wuko run NAME`
  and reject an extra target. The same check and target forwarding apply to `wuko run --file`,
  HTTPS, and `github:` selectors and to all equivalent `wuko ui` selectors.
- `wuko list` displays effective sources as tab-separated name, scope, description, and path. A
  workflow with prerequisites has an additional trailing `depends on ...` field; a workflow with
  `invokable: false` has a trailing `not directly invokable` field.
- `wuko validate NAME` and `wuko tree NAME` use effective discovery; without a name, `validate`
  validates every effective workflow. Both inspection commands accept dependency-only workflows.
- Bare `wuko` calls `DiscoverAll`. In an interactive terminal it shows effective and shadowed
  directly invokable sources in the picker, including one row per target and each workflow's direct
  prerequisites. Enter runs the exact selected source, `u` opens its selected target's form, `e` opens its file in `$VISUAL` or `$EDITOR`,
  `p` toggles a plain-text `[pinned]` marker, and `s` switches between name and recently-used
  sorting. Press `m` on a marketplace-installed workflow to open its marketplace URL, or `r` to reinstall it. Shift+Enter prints `wuko run NAME [TARGET]` for an effective source or `wuko run --file
  PATH [TARGET]` for a shadowed source so the printed command remains unambiguous. Picker state
  and the selected sort preference are global; successful runs are remembered, and entries for
  workflows no longer discovered as directly invokable are pruned.
- Bare `wuko` in a non-interactive context prints directly invokable discovered sources as
  tab-separated rows, appending the target name and dependency summary when present. Target rows
  are emitted separately.
- Shell completion returns effective workflow names and, after a targeted workflow name, its target
  names. Run and UI completion omit dependency-only workflows; validation and tree completion
  retain them.
- `wuko install SOURCE` saves a standalone local, HTTPS, or `github:` workflow under the current
  directory’s `.wuko/workflows/`; `--global` selects `~/.wuko/workflows/`. The YAML `name` is used
  as the installed filename. An HTTPS repository URL with a root `manifest.json` is treated as a
  version-1 package marketplace: the searchable picker supports space to toggle, `ctrl+a`/`ctrl+x`
  for visible bulk selection, and Enter to install. Repeatable `--package NAME` flags bypass the
  picker and install in manifest order; names are validated before any package is written.
  Selected marketplace packages are stored below a repository-named subdirectory, with a marker
  preventing different repositories from sharing it. `wuko marketplace init` creates a version-1
  manifest, while `wuko marketplace build` discovers package directories and incrementally rebuilds
  deterministic archives. `wuko uninstall NAME` removes a complete installed package directory,
  runs its uninstall hook first, and accepts `--yes` for non-interactive use.
- `wuko run --file PATH [TARGET]` bypasses discovery. HTTP and `github:` locators also bypass local
  discovery and use the remote workflow loader, but none bypass `invokable: false`.

## Implementation

The core rules are implemented in [`workflow/discovery.go`](../workflow/discovery.go). The bare
command's picker and non-interactive output are in [`cmd/root.go`](../cmd/root.go), while named
execution is wired through [`cmd/run.go`](../cmd/run.go).
