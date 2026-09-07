# Persistent Git worktrees

Wuko can express the core non-LLM Worktrunk lifecycle with small, composable workflow steps. The
[runnable lifecycle example](../examples/worktree-lifecycle.yaml) provides `list`, `start`, `shell`,
`remove`, and `merge` targets:

```sh
wuko run --file ./examples/worktree-lifecycle.yaml list --var base=main
wuko run --file ./examples/worktree-lifecycle.yaml start --var branch=feature/payments --var base=main
wuko run --file ./examples/worktree-lifecycle.yaml shell --var branch=feature/payments
wuko run --file ./examples/worktree-lifecycle.yaml remove --var branch=feature/payments --var base=main
wuko run --file ./examples/worktree-lifecycle.yaml merge --var branch=feature/payments --var base=main --var squash_message='feat: add payments'
```

These steps are host-local. They are rejected inside Docker or plugin executor scopes because a
worktree path must remain valid for later `working_directory` blocks on the Wuko host. They do not
fetch: remote-branch discovery uses only local remote-tracking refs. Like every Wuko workflow,
this is trusted local code; the Git steps do not introduce a separate approval layer.

## `git_worktree`

`git_worktree` has three operations.

### List

```yaml
- id: worktrees
  type: git_worktree
  with:
    operation: list
    comparison: main
```

`comparison` defaults to the detected default branch. Detection checks `origin/HEAD`, a unique
symbolic HEAD on another remote, local `main`, local `master`, and finally the primary worktree
branch. Outputs include
`repository_root`, `default_branch`, `comparison`, and `items`. Each item contains:

- `branch`, `path`, `head`, and `short_head`
- `current`, `primary`, `detached`, `bare`, `locked`, `lock_reason`, and `prunable`
- `status_available`, `clean`, and `changes` counts for staged, modified, untracked, conflicted,
  renamed, deleted, and total entries
- `upstream`, `upstream_ahead`, and `upstream_behind` for the branch's configured upstream
- `ahead` and `behind` relative to the comparison revision

Feed `steps.worktrees.items` directly to `tui_table`, `tui_choice`, `foreach`, or Lua.

### Ensure

```yaml
- id: worktree
  type: git_worktree
  with:
    operation: ensure
    branch: "{{ .vars.branch }}"
    base: main
    create: true

- working_directory: "{{ .steps.worktree.worktree.path }}"
  steps:
    - id: check
      type: shell
      with: {command: go, args: [test, ./...]}
```

`ensure` is idempotent. It returns an existing checkout, checks out an existing local branch, tracks
an already-fetched remote branch whose name after the remote matches exactly, or creates a branch
from `base` when `create: true`. If `path` is omitted, Wuko uses a sibling named
`<repository>.<sanitized-branch>`. A relative explicit path is resolved from the primary repository
root, and any occupied path is rejected.

Outputs include `created`, `branch_created`, `base`, `repository_root`, and a `worktree` object with
`path`, `branch`, `head`, and `short_head`.

### Remove

```yaml
- id: remove
  type: git_worktree
  with:
    operation: remove
    target: feature/payments
    delete_branch: safe
    integrated_into: main
```

`target` accepts a worktree branch or path. Dirty worktrees require `force: true`; the primary
worktree is never removable. `delete_branch` is `safe` by default, `never`, or `force`. Safe mode
deletes only when Git proves the tip is integrated by commit ancestry, equivalent trees, an empty
three-dot diff, a merge that adds nothing, or patch equivalence. An unintegrated branch is retained
without failing the removal. Branch deletion uses the removed worktree's expected object ID, so a
concurrent branch move fails instead of deleting the new value. Outputs report `removed`, `path`,
`branch`, `branch_deleted`, and `branch_delete_reason`.

## `git_squash`

```yaml
- id: squash
  type: git_squash
  with:
    target: main
    message: "feat: add payments"
    stage: all
    verify: true
```

`message` is always required; Wuko does not synthesize it. `target` defaults to the detected
default branch. `stage` is `all` (the default), `tracked`, or `none`. Commit `body`, `trailers`,
`author`, `committer`, `signoff`, and `verify` follow `git_commit`. Before rewriting, the step saves
the original tip under `refs/wuko/backups/...`. It refuses, leaving the branch untouched, when the
squash would carry no net change against the merge base. Outputs include the branch, target, merge
base, old and new object IDs, and recovery ref.

## `git_rebase`

```yaml
- id: rebase
  type: git_rebase
  with: {target: main}
```

The target defaults to the detected default branch. If it is already an ancestor, the step is a
no-op. Otherwise it runs Git rebase and publishes `target`, `before`, `after`, and `rebased`. On a
conflict, the step returns an error and intentionally leaves Git's rebase state open for recovery.

## `git_merge`

```yaml
- id: merge
  type: git_merge
  with:
    target: main
    source: feature/payments
    mode: ff_only
```

`target` defaults to the detected default branch and `source` to the current branch or HEAD.
`mode` is `ff_only` by default. `no_ff` requires an explicit `message`. The target checkout must be
clean. If it is not checked out, fast-forward mode performs a compare-and-swap ref update; no-FF
mode uses a temporary attached worktree and removes it afterward. A conflict in a temporary worktree
is discarded with it; a conflict in a checkout you own is reported and left open for recovery.
Outputs include `target`, `source`, `mode`, `before`, `after`, `changed`, and `merge_commit`.

## Mapping Worktrunk concepts

| Worktrunk-style capability | Wuko building blocks |
| --- | --- |
| Switch/create a worktree | `git_worktree ensure` then `working_directory` |
| Enter a worktree | `shell` with `tty: true` inside `working_directory` |
| List/status picker | `git_worktree list` with `tui_table` or `tui_choice` |
| Merge a worktree | `git_squash`, `git_rebase`, project checks, `git_merge`, then `git_worktree remove` |
| Lifecycle hooks | `uses`, `require`, `concurrent`, `defer`, and `finally` |
| Per-project configuration | workflow `vars`, scoped `env`, and `key_value` |

The existing `worktree:` execution block remains the right tool for temporary detached isolation
that Wuko automatically cleans up. `git_worktree` is for persistent, branch-attached worktrees.
Agent/LLM orchestration, PR/CI integration, shell aliases, copying ignored files, promotion,
relocation, tethering, and automatic pruning are intentionally outside this mapping.
