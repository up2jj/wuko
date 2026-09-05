# Git hooks

Wuko can bind project workflows to client-side Git hooks. The bindings are version-controlled;
the executable dispatchers and their ownership records stay in local Git metadata. Installing a
hook is always explicit because both Git hooks and Wuko workflows run as trusted local code.

## Configure and install

Generate a starter configuration and example workflows:

```sh
wuko git hook init
```

This creates `.wuko/git-hooks.yaml`, `.wuko/workflows/git-check.yaml`, and
`.wuko/workflows/git-commit-message.yaml`. The examples check staged and outgoing commits for
whitespace errors and compose the `file` and `git_conventional_commit` steps to validate commit
messages without a shell script. `init` refuses to overwrite any existing scaffold file and does
not install hooks. Review and adapt the workflows, then run `wuko git hook install`.

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

Hook runs use a compact reporter suited to Git's command-line flow. Successful workflows add no
status output; output written explicitly by steps is still forwarded. A failure identifies the
hook, configured workflow, failed step, source location, final error, and retry count when relevant.
Intermediate retry and polling progress, spinners, timing summaries, and success banners are
suppressed. Pass the global `--debug` flag when full loading, validation, and execution diagnostics
are needed.

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
