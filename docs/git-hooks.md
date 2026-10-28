# Git hooks

Wuko can bind project workflows to client-side Git hooks. The bindings are version-controlled;
the executable dispatchers and their ownership records stay in local Git metadata. Installing a
hook is always explicit because both Git hooks and Wuko workflows run as trusted local code.

## Contents

- [Configure and install](#configure-and-install)
- [Existing hooks and uninstall](#existing-hooks-and-uninstall)
- [Workflow context](#workflow-context)
- [Staged isolation](#staged-isolation)
- [Supported hooks](#supported-hooks)

## Configure and install

Generate a starter configuration and example workflows:

```sh
wuko git hook init
```

This creates `.wuko/git-hooks.yaml`, `.wuko/workflows/git-check.yaml`, and
`.wuko/workflows/git-commit-message.yaml`. The examples use `git_diff_check` to check staged and
outgoing commits for whitespace errors, then compose the `file` and `git_conventional_commit`
steps to validate commit messages. All generated checks are shell-free. `init` refuses to
overwrite any existing scaffold file and does not install hooks. Review and adapt the workflows,
then run `wuko git hook install`. See [Git changes and policy recipes](git-changes.md) for ways to
extend these checks.

The generated manifest uses the same format as a hand-written `.wuko/git-hooks.yaml`:

```yaml
version: 1
hooks:
  pre-commit:
    - workflow: git-check
      target: staged
  commit-msg:
    - workflow: git-commit-message
  pre-push:
    - workflow: git-check
      target: pushed
```

`workflow` uses normal Wuko discovery. `target` is optional. Each hook must contain at least one
binding. Bindings run in declaration order and stop at the first failure. A binding may only name a
locally discovered workflow; remote locators are rejected so that a manifest change alone can never
make a hook fetch and run unreviewed code. A dispatcher whose worktree has no
`.wuko/git-hooks.yaml`, or no binding for its hook, exits successfully without running anything.

Install the dispatchers and inspect their state:

```sh
wuko git hook install
wuko git hook status
```

Wuko asks Git for its worktree, Git directory, common Git directory, and effective hook path. It
does not change `core.hooksPath`. A repository-local custom hooks path works normally; a shared
path outside the repository is refused because installing there could affect unrelated clones.
Linked worktrees share installation metadata through the common Git directory, while a hook run
loads the manifest and workflows from the worktree that triggered it.

The generated dispatcher records the absolute Wuko executable. Run `wuko git hook install` again
after moving the executable. `status` reports a missing or non-executable binary as `broken binary`.

## Existing hooks and uninstall

An existing hook is a conflict by default and is left untouched. To opt into composition:

```sh
wuko git hook install --chain
```

Wuko preserves the existing hook beside the dispatcher. On every invocation it runs the preserved
hook first with Git's original working directory, arguments, environment, and stdin. Wuko workflows
run only if that hook succeeds. Input is captured once and replayed so protocols such as `pre-push`
reach both consumers intact.

Remove selected hooks or every Wuko-managed hook:

```sh
wuko git hook uninstall pre-commit
wuko git hook uninstall
```

Uninstall restores a preserved hook exactly. Wuko refuses to overwrite or remove a dispatcher that
was modified after installation, or to remove a chained dispatcher whose backup is missing.

For manual composition, call the stable runner from an existing hook before anything consumes its
stdin:

```sh
wuko git hook run pre-commit -- "$@"
```

## Workflow context

Git hook runs are non-interactive and ignore workflow schedules. Workflow stdin is empty so an
interactive or process step cannot accidentally consume Git's protocol. The complete invocation is
available in a read-only `git` context:

```yaml
- id: inspect
  type: shell
  with:
    script: echo '{{ .git.hook.name }} in {{ .git.repository.root }}'
```

`git.repository` contains absolute `root`, `git_dir`, and `common_dir` paths. `git.hook` contains:

| Field | Meaning |
| --- | --- |
| `name` | Git hook name |
| `args` | Original ordered argument list |
| `stdin` | Original stdin text |
| `payload` | Parsed fields for the selected hook |

Known payload fields include `message_file`, `source`, and `commit_oid` for commit-message hooks;
`remote_name`, `remote_url`, and `updates` for `pre-push`; old and new object IDs for checkout and
rewrite hooks; merge, checkout, and index-change booleans; rebase arguments; and the send-email
patch path. Paths in parsed fields are absolute. Raw `args` and `stdin` remain available for every
hook.

The same hook cannot recursively invoke itself. Wuko reports recursion instead of silently skipping
checks. Git's native bypass options, including `--no-verify` where supported, retain their normal
behavior.

Hook runs print one result line to stderr as each top-level workflow step finishes, using the
workflow's declared `name` and step ID as the check label. Labels too long for the 72-column line
end in `…`:

```text
repository-checks / whitespace.....................................Passed
repository-checks / tidy..........................................Skipped
repository-checks / vet............................................Failed
```

Conditions that skip a check show `Skipped`; timeouts and cancellation show `Timed out` and
`Canceled`. Checks in concurrent groups print individually as they finish. Nested steps inside
repeated controls or actions contribute to their enclosing check's result instead of printing
separate rows. Retries and polling print only the check's final result.
Output written explicitly by steps is still forwarded. A failure identifies the
hook, configured workflow, failed step, source location, final error, and retry count when relevant.
Intermediate retry and polling progress, spinners, timing summaries, and success banners are
suppressed. Pass the global `--debug` flag when full loading, validation, and execution diagnostics
are needed.

## Staged isolation

`pre-commit` automatically hides unstaged tracked changes before loading `.wuko/git-hooks.yaml`
and its workflows. Checks run in the original working directory against the indexed file versions;
tracked hook configuration also uses its staged version. No additional workflow step or manifest
option is required. Isolation is skipped when the staged (or, if untracked, the worktree)
manifest declares no `pre-commit` workflows. A chained hook runs first, with its original view of the worktree.

Wuko saves a binary-capable recovery patch under the current worktree's Git directory, then
restores only affected paths from the index. It does not create a checkout or copy the whole
repository. Untracked files and submodule working directories remain visible and unchanged.
Edits to assume-unchanged entries, and to skip-worktree entries present in the worktree, are hidden
too, using a private copy of the index; the index flags themselves are never changed.
Paths obstructed by directories or parent symlinks are refused rather than replacing untracked
content.
An unmerged index or intent-to-add entries (`git add -N`) are refused before anything is hidden;
resolve conflicts and stage or remove intent-to-add entries before retrying. Other hooks, including
`pre-push`, retain their normal working-directory behavior.

Restoration runs after every configured workflow and its cleanup have finished, including after
loading errors, failed checks, timeouts, or normal cancellation. Hook edits to tracked files reject
the commit even when their commands exited successfully. Nonconflicting formatter changes remain
available for review and staging. If restoring the saved patch conflicts with hook edits, Wuko
first saves every hook edit to `<git-dir>/wuko/hook-edits.patch`, then restores the original staged
baseline on the saved patch's paths and reapplies the user's changes. The error names that file; it is
relative to the staged versions, so review it or apply it after stashing the unstaged changes. Checks must not stage changes themselves; an index change also
rejects the commit.

Only one Wuko isolation session may run in a worktree at a time; an overlapping pre-commit waits
for the running one to finish. Linked worktrees use separate guards. A forced shutdown (a second
interrupt or an expired grace period) still waits up to 45 seconds for restoration; a third
interrupt exits immediately and leaves the recovery files described below. Recovery files have restrictive permissions and patch contents are not printed in errors.
If restoration fails, or Wuko is forcibly terminated before cleanup, recovery files remain under
`<git-dir>/wuko/staged/`, and subsequent pre-commit runs refuse to overwrite them. The directory
contains `unstaged.patch` and the original indexed tree ID in `tree`. Preserve those files while
recovering the unstaged edits. For a worktree still at the saved baseline, apply the patch from its
repository root:

```sh
git apply --binary --whitespace=nowarn /absolute/path/to/git-dir/wuko/staged/unstaged.patch
```

If that fails, reconcile conflicting edits using the saved tree and patch. Remove the recovery
directory only after confirming the unstaged changes have been recovered.

## Supported hooks

Version 1 supports:

```text
applypatch-msg       pre-applypatch       post-applypatch
pre-commit           pre-merge-commit    prepare-commit-msg
commit-msg           post-commit          pre-rebase
post-checkout        post-merge           pre-push
pre-auto-gc          post-rewrite         sendemail-validate
post-index-change
```

Receive-side hooks for bare repositories, `reference-transaction`, Git-P4 hooks,
`fsmonitor-watchman`, and other server or long-lived protocols are intentionally excluded.

Run `wuko validate` to validate both effective workflows and `.wuko/git-hooks.yaml` when it is
present.
