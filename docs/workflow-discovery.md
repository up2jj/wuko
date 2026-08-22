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
from the YAML `description` field.

Within a directory, workflow names are sorted before loading. Declaring both `name.yaml` and
`name.yml` in the same directory is an error.

## What discovery loads

Discovery reads every candidate workflow to obtain its metadata. Each file must be a valid,
single-document Wuko workflow: known YAML fields, version, required steps, step IDs, and workflow
schema are checked. `require` step fragments are expanded relative to the file that references
them, including nested fragments.

Discovery does not fetch or resolve remote composite actions referenced by `uses`. Remote action
resolution happens later when a workflow is fully loaded for execution, validation, or tree
rendering.

An error in any discovered candidate aborts the discovery operation, including an error in a
workflow that would otherwise be shadowed by a closer definition. Missing directories are the
only discovery-time filesystem errors that are ignored.

## Precedence and shadowing

The first definition encountered for a filename stem is effective. Therefore, a closer local
workflow wins over the same name in a parent directory, and any local workflow wins over global
workflows. Between global locations, the home workflow wins over the platform-config workflow.

`Discover` returns only effective sources. `DiscoverAll` returns effective and shadowed sources and
marks each source with `Effective: true` or `false`.

For example, if all of these exist, the project copy is selected:

```text
project/.wuko/workflows/release.yaml   local, effective
home/.wuko/workflows/release.yaml      global, shadowed
config/wuko/workflows/release.yaml     global, shadowed
```

Results are sorted by workflow name after precedence has been applied. Sorting affects display
order, not which definition wins.

## Command behavior

- `wuko run NAME` calls effective discovery and runs the selected source.
- `wuko list` displays effective sources as tab-separated name, scope, description, and path.
- `wuko validate NAME` and `wuko tree NAME` use effective discovery; without a name, `validate`
  validates every effective workflow.
- Bare `wuko` calls `DiscoverAll`. In an interactive terminal it shows effective and shadowed
  sources in the picker. Enter runs the exact selected source. Shift+Enter prints `wuko run NAME`
  for an effective source or `wuko run --file PATH` for a shadowed source so the printed command
  remains unambiguous.
- Bare `wuko` in a non-interactive context prints all discovered sources as tab-separated rows.
- Shell completion returns effective workflow names only.
- `wuko run --file PATH` bypasses discovery. HTTP and `github:` locators also bypass local
  discovery and use the remote workflow loader.

## Implementation

The core rules are implemented in [`workflow/discovery.go`](../workflow/discovery.go). The bare
command's picker and non-interactive output are in [`cmd/root.go`](../cmd/root.go), while named
execution is wired through [`cmd/run.go`](../cmd/run.go).
