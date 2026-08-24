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

Each existing directory contributes files whose extension is `.yaml` or `.yml` (case-insensitive).
Directories are skipped. The workflow selector is the filename stem, so `deploy.yaml` is run with
`wuko run deploy`.

The YAML `name` field is still required and validated, but it is metadata for the loaded workflow;
lookup by `NAME` uses the filename stem. The description shown by `wuko list` and the picker comes
from the YAML `description` field. Workflows with `depends_on` also show a sorted `depends on ...`
summary; aliases that differ from their workflow name use `alias=workflow`. The optional
`invokable` boolean defaults to `true`. Set it to `false` for a workflow that may run only as a
`depends_on` prerequisite.

Within a directory, workflow names are sorted before loading. Declaring both `name.yaml` and
`name.yml` in the same directory is an error.

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

Results are sorted by workflow name after precedence has been applied. Sorting affects display
order, not which definition wins.

## Command behavior

- `wuko run NAME` calls effective discovery and runs the selected source only when it is directly
  invokable. The same check applies to `wuko run --file`, HTTPS, and `github:` selectors and to all
  equivalent `wuko ui` selectors.
- `wuko list` displays effective sources as tab-separated name, scope, description, and path. A
  workflow with prerequisites has an additional trailing `depends on ...` field; a workflow with
  `invokable: false` has a trailing `not directly invokable` field.
- `wuko validate NAME` and `wuko tree NAME` use effective discovery; without a name, `validate`
  validates every effective workflow. Both inspection commands accept dependency-only workflows.
- Bare `wuko` calls `DiscoverAll`. In an interactive terminal it shows effective and shadowed
  directly invokable sources in the picker, including each workflow's direct prerequisites. Enter
  runs the exact selected source, `u` opens its form, `e` opens its file in `$VISUAL` or `$EDITOR`,
  `p` toggles a plain-text `[pinned]` marker, and `s` switches between name and recently-used
  sorting. Shift+Enter prints `wuko run NAME` for an effective source or `wuko run --file PATH`
  for a shadowed source so the printed command remains unambiguous. Picker state and the selected
  sort preference are global; successful runs are remembered, and entries for workflows no longer
  discovered as directly invokable are pruned.
- Bare `wuko` in a non-interactive context prints directly invokable discovered sources as
  tab-separated rows, appending the dependency summary when present.
- Shell completion returns effective workflow names. Run and UI completion omit dependency-only
  workflows; validation and tree completion retain them.
- `wuko run --file PATH` bypasses discovery. HTTP and `github:` locators also bypass local
  discovery and use the remote workflow loader, but none bypass `invokable: false`.

## Implementation

The core rules are implemented in [`workflow/discovery.go`](../workflow/discovery.go). The bare
command's picker and non-interactive output are in [`cmd/root.go`](../cmd/root.go), while named
execution is wired through [`cmd/run.go`](../cmd/run.go).
