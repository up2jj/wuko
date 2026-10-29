# Filesystem operations

The `file` step provides strict, shell-independent filesystem operations. Every operation requires
`operation` and `path` (the [editing operations](#edit-text-files) accept `paths` instead); unknown fields and fields belonging to another operation are errors. Relative
paths are resolved from the workflow run directory, while absolute paths are used as written. Wuko
workflows are trusted code, so paths are not confined to the run directory.

## Contents

- [Common rules](#common-rules)
- [Quick reference](#quick-reference)
- [Read](#read)
- [Write](#write)
- [Copy and move](#copy-and-move)
- [Remove](#remove)
- [Make a directory](#make-a-directory)
- [List](#list)
- [Stat](#stat)
- [Chmod](#chmod)
- [Find](#find)
- [Link](#link)
- [Truncate](#truncate)
- [Tail](#tail)
- [Disk usage](#disk-usage)
- [Atomic swap](#atomic-swap)
- [Permissions](#permissions)
- [Touch](#touch)
- [Edit text files](#edit-text-files)
- [Consuming outputs](#consuming-outputs)

## Common rules

- Modes are quoted four-digit octal strings from `"0000"` through `"0777"`. Special permission
  bits are not supported.
- Sizes are non-negative integers followed by `B`, `KiB`, `MiB`, `GiB`, or `TiB`, such as `64KiB`.
- Ages use Go duration syntax, such as `30m` or `24h`. Timestamps use RFC3339 or the literal `now`.
- Traversals do not follow symbolic links. Discovery operations may return links as entries;
  mutating operations reject or explicitly skip them as documented below.
- Long-running reads and traversals check the step context and return on cancellation. Recursive
  mutations may already have changed earlier entries when cancellation or another error occurs.
- `replace` is explicit: `never` rejects any existing destination, `file` permits replacement of a
  non-directory entry, and `any` also permits removal of a directory tree.
- Inside an [executor scope](executors.md#reading-and-writing-files), only `read`, `write`, `mkdir`,
  and `stat` are available, and they act on the execution target rather than the host. The other
  operations rely on host semantics the session filesystem does not carry and are rejected during
  validation.

## Quick reference

| Operation | Additional fields | Principal outputs |
| --- | --- | --- |
| `read` | none | `content`, `size` |
| `write` | `content`, `overwrite`, `mode` | `size`, `mode`, `created` |
| `copy` | `destination`, `overwrite` | `destination`, `size`, `mode` |
| `move` | `destination`, `overwrite` | `destination`, `size`, `mode` |
| `remove` | `recursive` | `removed` |
| `mkdir` | `recursive`, `mode` | `created`, `mode` |
| `list` | `recursive` | `entries` |
| `stat` | none | `exists`, entry metadata |
| `chmod` | `mode` | `mode` |
| `find` | `patterns`, `types`, size/age ranges, `mode` | `root`, `count`, `entries` |
| `link` | `destination`, `link_type`, `replace` | `destination`, `link_type`, `replaced` |
| `truncate` | `size` | `previous_size`, `size` |
| `tail` | `lines` or `bytes`, `max_bytes` | `content`, `size`, `truncated` |
| `disk_usage` | `largest` | `size`, counts, `largest_entries` |
| `atomic_swap` | `destination`, `replace` | `destination`, `replaced` |
| `permissions` | `mode`, `recursive` | `changed`, `skipped_links` |
| `touch` | `create`, `mode`, timestamps | `created`, `mode`, timestamps |
| `append` | `content`, `create`, `mode`, `ensure_newline`, `unless_contains`, `unless_matches`, `paths`, `check` | `changed`, `would_change`, `matched_path`, `bytes` |
| `ensure_line` | `line`, `match`, `present_if`, `create`, `mode`, `paths`, `check` | `changed`, `would_change`, `matched_path`, `action` |
| `ensure_block` | `content`, `marker`, `present_if`, `create`, `mode`, `paths`, `check` | `changed`, `would_change`, `matched_path`, `action` |

Every successful operation also outputs its resolved `path`. Operations with a destination output
that resolved path as `destination`. Entry metadata contains `name`, a forward-slash `path`, `type`,
`size`, `mode`, and UTC `modified_at`.

## Read

`read` reads a file as text and outputs `content` and its byte `size`. It checks cancellation while
reading but does not impose a size limit.

```yaml
- id: manifest
  type: file
  with:
    operation: read
    path: dist/manifest.json
```

## Write

`write` requires `content` and creates a regular file atomically. The parent directory must already
exist. A destination is rejected unless `overwrite: true` is set. New files default to mode `"0644"`;
overwriting without `mode` preserves an existing regular file's permissions.

```yaml
- id: write_script
  type: file
  with:
    operation: write
    path: scripts/release.sh
    content: |
      #!/bin/sh
      exec ./release
    overwrite: true
    mode: "0755"
```

Outputs are `created`, `size`, and normalized `mode`.

## Copy and move

Both operations require `destination` and reject an existing destination unless `overwrite: true`
is set. `copy` accepts regular files and preserves their permissions. `move` accepts regular files,
directories, and symbolic links. A cross-filesystem move stages a copy before removing the source.

```yaml
- id: copy_binary
  type: file
  with:
    operation: copy
    path: build/wuko
    destination: dist/wuko
    overwrite: true

- id: publish_tree
  type: file
  with:
    operation: move
    path: build/site
    destination: public
```

Both output the resolved source and destination plus `size` and `mode`.

## Remove

`remove` deletes a file, link, or empty directory. Set `recursive: true` for a non-empty directory.
Missing paths succeed with `removed: false`. Wuko refuses to remove a filesystem root, the run
directory, or a directory that contains it, however the path is spelled.

```yaml
- id: clean
  type: file
  with:
    operation: remove
    path: build/tmp
    recursive: true
```

## Make a directory

`mkdir` creates a directory with mode `"0755"` by default. Set `recursive: true` to create missing
parents. An existing directory succeeds with `created: false`; another entry type is an error.

```yaml
- id: output_directory
  type: file
  with:
    operation: mkdir
    path: build/output
    recursive: true
    mode: "0750"
```

## List

`list` returns path-sorted `entries` immediately below a directory. With `recursive: true`, it walks
all descendants without following directory symlinks.

```yaml
- id: contents
  type: file
  with:
    operation: list
    path: build
    recursive: true
```

## Stat

`stat` inspects a path without following a symbolic link. Missing paths succeed with `exists: false`.
Existing paths output `exists: true` and entry metadata.

```yaml
- id: artifact
  type: file
  with:
    operation: stat
    path: dist/wuko
```

## Chmod

`chmod` requires `mode`, applies it to one file or directory, and rejects symbolic links. Use
`permissions` for a recursive tree.

```yaml
- id: executable
  type: file
  with:
    operation: chmod
    path: dist/wuko
    mode: "0755"
```

## Find

`find` requires one or more portable doublestar `patterns` relative to the directory in `path`.
Patterns support `*`, `?`, character classes, and recursive `**`; absolute patterns and `..` path
components are rejected. Multiple patterns form a union, while all filters combine with AND.

- `types` accepts `file`, `directory`, and `symlink`; all three are included by default.
- `min_size` and `max_size` compare the entry's metadata size, inclusively.
- `min_age` and `max_age` compare age since modification, inclusively.
- `mode` is a required-bits mask: `"0040"` matches entries containing group-read permission even if
  other permission bits are also set.

```yaml
- id: old_logs
  type: file
  with:
    operation: find
    path: logs
    patterns: ["**/*.log", "**/*.log.gz"]
    types: [file]
    min_size: 1MiB
    min_age: 24h
    mode: "0400"
```

The operation outputs resolved `root`, `count`, and path-sorted `entries`. It does not follow
symbolic links during traversal.

## Link

`link` treats `path` as the target and requires the new link's `destination`. `link_type` must be
`symbolic` or `hard`. Symbolic links store the resolved absolute target and may be dangling. Hard
links require an existing regular-file target. `replace` defaults to `never`.

```yaml
- id: current_release
  type: file
  with:
    operation: link
    path: releases/v2.4.0
    destination: current
    link_type: symbolic
    replace: file
```

Replacement removes the old destination before creating the link, so a subsequent creation failure
does not restore the displaced entry. Outputs include `link_type` and `replaced`.

## Truncate

`truncate` resizes an existing regular file and rejects directories and symbolic links. `size`
defaults to `0B`; enlarging a file fills the new region according to operating-system semantics.

```yaml
- id: empty_log
  type: file
  with:
    operation: truncate
    path: logs/application.log

- id: resize_image
  type: file
  with:
    operation: truncate
    path: disk.img
    size: 512MiB
```

Outputs are `previous_size` and the new `size`.

## Tail

`tail` reads a regular file from the end without loading the entire file. Set either `lines` or
`bytes`, not both. It defaults to 10 lines. Byte mode takes a size string. Line mode reads at most
`max_bytes`, which defaults to `1MiB`; when that cap splits a long line, the returned content begins
with the available suffix of that line.

```yaml
- id: recent_errors
  type: file
  with:
    operation: tail
    path: logs/application.log
    lines: 50
    max_bytes: 2MiB

- id: raw_suffix
  type: file
  with:
    operation: tail
    path: logs/application.log
    bytes: 64KiB
```

`size` is the returned byte count. `truncated` is true when bytes before `content` were omitted.
Content is exposed as a Go string; byte mode therefore preserves bytes but does not promise valid
UTF-8.

## Disk usage

`disk_usage` recursively sums regular-file sizes without following links. It outputs `size`,
`file_count`, `directory_count` (including the root directory), and `symlink_count`. `largest_entries`
contains the largest files and non-root directories by recursive size, ordered by descending size
and then path. `largest` defaults to 10; set it to zero to omit the list.

```yaml
- id: build_usage
  type: file
  with:
    operation: disk_usage
    path: build
    largest: 20
```

Only the requested number of largest candidates is retained while walking, so memory for that list
is bounded by `largest`.

## Atomic swap

`atomic_swap` treats `path` as a completed staging directory and requires `destination`. The paths
must be real, non-overlapping directories on the same filesystem. If the destination is missing,
the staging directory is atomically renamed into place. If it exists, `replace: any` is required;
Linux `RENAME_EXCHANGE` or macOS `RENAME_SWAP` atomically exchanges the trees.

```yaml
- id: publish
  type: file
  with:
    operation: atomic_swap
    path: .staging/site
    destination: public
    replace: any
```

After an exchange, Wuko removes the displaced tree now located at the staging path. Success consumes
the staging path. If cancellation or cleanup failure occurs after the atomic exchange, the new
destination remains installed and any portion of the old tree that has not yet been removed remains
at `path`; cleanup is not transactional. The step returns an error explaining that state. The
operation does not provide a non-atomic cross-filesystem fallback.

## Permissions

`permissions` requires `mode`. Without `recursive`, it applies the mode to one non-symlink entry.
With `recursive: true`, it applies the same mode to the root and all regular-file and directory
descendants. Symbolic-link descendants are counted in `skipped_links` and never followed; a symlink
root is rejected.

```yaml
- id: lock_down
  type: file
  with:
    operation: permissions
    path: secrets
    mode: "0600"
    recursive: true
```

`changed` counts entries whose mode actually changed. The operation is not transactional: an error
or cancellation reports how many earlier changes completed but does not roll them back.

## Touch

`touch` creates a missing regular file by default or updates timestamps on an existing non-symlink
file or directory. Set `create: false` to require an existing path. New files default to mode
`"0644"`; `mode` only controls creation.

With neither timestamp supplied, both are set to the current time. `accessed_at` and `modified_at`
accept `now` or RFC3339. When only one is supplied, the other is preserved.

```yaml
- id: refresh_stamp
  type: file
  with:
    operation: touch
    path: state/last-success

- id: normalize_timestamp
  type: file
  with:
    operation: touch
    path: artifact.tar.gz
    create: false
    accessed_at: now
    modified_at: "2026-08-21T12:00:00Z"
```

Outputs include `created`, `mode`, `accessed_at`, and `modified_at`.

## Edit text files

`append`, `ensure_line`, and `ensure_block` change a text file in place without rewriting what they
do not own, which suits configuration such as shell startup files. All three decide first and write
second, and that decision can be made on its own with `check: true`.

### Append

`append` adds `content` to the end of the file. `ensure_newline: true` first writes a newline when the
existing file does not end with one. The append is skipped, making the step idempotent, when the file
contains the `unless_contains` string or has a line matching the `unless_matches` regular expression.

```yaml
- id: add_hook
  type: file
  with:
    operation: append
    path: "{{ .env.HOME }}/.zshrc"
    content: |
      # added by the bootstrap workflow
      eval "$(direnv hook zsh)"
    create: true
    ensure_newline: true
    unless_contains: direnv hook
```

The text is written with one append (`O_APPEND`), not atomically. It goes through a symbolic link to
the file behind it, and the file keeps its inode, mode, and owner.

### Ensure a line

`ensure_line` makes sure `line` (a single line, without a line break) is in the file:

1. If a line equals `line`, or matches the optional `present_if` regular expression, nothing changes.
2. Otherwise, when `match` is set, the last line matching it is replaced by `line`, keeping that
   line's `\r\n` ending if it had one.
3. Otherwise `line` is appended, after a newline when the file does not end with one.

```yaml
- id: editor
  type: file
  with:
    operation: ensure_line
    path: "{{ .env.HOME }}/.config/tool/config"
    line: editor = nvim
    match: '^editor\s*='
```

### Ensure a block

`ensure_block` keeps `content` between two marker lines. `marker` defaults to
`# {mark} WUKO MANAGED BLOCK`, and `{mark}` becomes `BEGIN` and `END`. A block whose body differs is
replaced; a block that is already current is left alone; a missing block is appended, unless a line
matches `present_if`. An existing managed block is always kept current, even when `present_if` would
match its old text. A begin marker without its end marker, or the reverse, is an error. Use a
distinct marker for each block a workflow manages in the same file.

```yaml
- id: path_block
  type: file
  with:
    operation: ensure_block
    path: "{{ .env.HOME }}/.zshrc"
    marker: "# {mark} tool paths"
    content: |
      export PATH="$HOME/.tool/bin:$PATH"
```

### Rules shared by the editing operations

- `paths` (a list) may replace `path`. Every listed file is searched for the condition that makes
  the step a no-op (`unless_*`, an equal line, `present_if`, or a managed block), and an in-place
  replacement edits the file where its match was found. New text goes to the first path. Missing
  files and missing directories count as "no match", not as errors.
- A missing file to write is an error unless `create: true`; `mode` (default `"0644"`) applies only
  to a file the step creates. The parent directory must already exist.
- Regular expressions use Go syntax and are matched line by line, so `^` and `$` anchor at line
  boundaries. `unless_contains` is a plain substring of the whole file.
- A replacement (`ensure_line` with `match`, or a changed managed block) rewrites the file atomically
  and keeps its mode. It writes the file behind a symbolic link, so the link survives; the rewritten
  file is owned by the user running the workflow.
- These operations act on the host and are not available inside executor scopes.

Outputs:

| Output | Meaning |
| --- | --- |
| `path` | The file written, or that would be written. With no change, the file where the condition matched. |
| `matched_path` | The file where the condition, line, or block was found; `""` when nothing matched. |
| `exists` | Whether `path` existed beforehand. |
| `action` | `none`, `appended`, `inserted`, `replaced`, or `created`. |
| `would_change` | Whether a write is needed. |
| `changed` | Whether a write happened; always `false` with `check: true`. |
| `created` | Whether the step created `path`. |
| `bytes` | Bytes appended or written; `0` without a write. |

### Check, then write

`--dry-run` shows the workflow's structure without running steps, so it cannot preview an edit.
`check: true` does: the step evaluates everything, writes nothing, and reports `would_change` and the
`path` it would write. A later step with the same configuration and no `check` performs the write,
which lets a prompt sit between the two:

```yaml
- id: hook
  type: file
  with:
    operation: ensure_line
    paths:
      - "{{ .env.HOME }}/.zshrc"
      - "{{ .env.HOME }}/.zprofile"
      - "{{ .env.HOME }}/.zshenv"
    line: 'eval "$(direnv hook zsh)"'
    present_if: 'direnv hook|^[^#]*plugins=\([^)]*\bdirenv\b'   # also accept the oh-my-zsh plugin
    create: true
    check: true

- id: confirm_add_hook
  if: steps.hook.would_change
  type: tui_confirm
  with:
    variable: add_hook
    message: "Add the direnv hook to {{ .steps.hook.path }}?"
    default: false

- id: add_hook
  if: '"confirm_add_hook" in steps && steps.confirm_add_hook.value'
  type: file
  with:
    operation: ensure_line
    paths:
      - "{{ .env.HOME }}/.zshrc"
      - "{{ .env.HOME }}/.zprofile"
      - "{{ .env.HOME }}/.zshenv"
    line: 'eval "$(direnv hook zsh)"'
    present_if: 'direnv hook|^[^#]*plugins=\([^)]*\bdirenv\b'
    create: true
```

A missing file without `create: true` fails the check as it would fail the write, so the two agree.

## Consuming outputs

File-step outputs are available to later sequential steps beneath `.steps`:

```yaml
- id: usage
  type: file
  with:
    operation: disk_usage
    path: build

- id: report
  type: shell
  with:
    command: printf
    args:
      - "Build contains {{ .steps.usage.file_count }} files using {{ .steps.usage.size }} bytes\n"
```

Array entries can be inspected in expressions, foreach inputs, or Lua steps. As with every Wuko
step, outputs are committed only after successful completion.
