# Reference

[Back to the README](../README.md)

Every built-in step, control, and composition pattern, with links to detailed guides. Each linked
guide contains multiple examples. For a guided introduction, start with
[Getting started](getting-started.md).

- [Steps](#steps)
- [Workflow controls](#workflow-controls)
- [Workflow composition](#workflow-composition)

## Steps

### Interactive

Prompt for input or show rich terminal views. See also [browser forms](forms.md).

| Step | Use it to | Examples |
| --- | --- | --- |
| `tui_form` | Collect multiple typed fields as named step outputs | [Interactive steps](steps-interactive.md#tui_form) |
| `tui_input` | Collect editable text or typed JSON/lists | [Interactive steps](steps-interactive.md#tui_input) |
| `tui_password` | Collect masked text | [Interactive steps](steps-interactive.md#tui_password) |
| `tui_choice` | Select bounded static/dynamic values with defaults or select-all | [Interactive steps](steps-interactive.md#tui_choice) |
| `tui_date` | Choose a bounded date with a calendar or relative day shortcut | [Interactive steps](steps-interactive.md#tui_date) |
| `tui_path` | Select one or many files/directories | [Interactive steps](steps-interactive.md#tui_path) |
| `tui_review` | Review plain text or a colored unified diff | [Interactive steps](steps-interactive.md#tui_review) |
| `tui_table` | Browse object-backed data in a paginated table | [Interactive steps](steps-interactive.md#tui_table) |
| `dashboard` | Monitor grouped template widgets and run contextual actions | [Interactive steps](steps-interactive.md#dashboard) |
| `tui_confirm` | Collect a boolean decision | [Interactive steps](steps-interactive.md#tui_confirm) |

### Data and state

Work with typed values, structured files, and persisted state.

| Step | Use it to | Examples |
| --- | --- | --- |
| `set` | Assign a literal or expression result | [Data steps](steps-data.md#set) |
| `template` | Render inline or packaged templates to memory or a file | [Data steps](steps-data.md#template) |
| `transform` | Run deterministic list, object, aggregation, and tuple pipelines | [Data steps](steps-data.md#transform) |
| `assert` | Stop unless an expression is true | [Data steps](steps-data.md#assert) |
| `import_vars` | Load JSON or TOML into workflow state | [Data steps](steps-data.md#import_vars) |
| `decode` | Convert JSON, YAML, TOML, or lines into a typed value | [Data steps](steps-data.md#decode) |
| `jsonpath` | Select values with RFC 9535 JSONPath | [Data steps](steps-data.md#jsonpath) |
| `edit` | Update structured files or values with RFC 9535 JSONPath | [Data steps](steps-data.md#edit) |
| `markdown_edit` | Update Markdown by semantic AST nodes while preserving unrelated source | [Data steps](steps-data.md#markdown_edit) |
| `extract` | Extract typed records and marker fields from text | [Text extraction](extract.md) |
| `semver` | Parse, compare, constrain, or increment versions | [Data steps](steps-data.md#semver) |
| `time` | Capture, parse, adjust, and format an explicit time value | [Data steps](steps-data.md#time) |
| `key_value` | Persist JSON-compatible values between runs | [Data steps](steps-data.md#key_value) |
| `changed` | Detect changed files or structured inputs | [Data steps](steps-data.md#changed) |

### Running workflows

Call other workflows or guard work so it runs once.

| Step | Use it to | Examples |
| --- | --- | --- |
| `once` | Run a named block once per successful persisted key | [Execution and composition](execution.md#idempotency-across-runs) |
| `run_workflow` | Run a discovered workflow and consume its declared outputs | [Execution and composition](execution.md#running-child-workflows) |

### Git

Inspect history and changes, enforce policy, and manage branches and worktrees. See also [Git changes and policy recipes](git-changes.md).

| Step | Use it to | Examples |
| --- | --- | --- |
| `git_revision` | Read the current or a selected Git commit as structured data | [System steps](steps-system.md#git_revision) |
| `git_merge_base` | Find the unambiguous common ancestor of two Git revisions | [System steps](steps-system.md#git_merge_base) |
| `git_log` | Read bounded, structured Git history for automation | [System steps](steps-system.md#git_log) |
| `git_diff` | Inspect committed, staged, or unstaged changes as structured data | [System steps](steps-system.md#git_diff) |
| `git_diff_check` | Reject Git whitespace errors and conflict markers, including pushed commits | [System steps](steps-system.md#git_diff_check) |
| `git_worktree` | List, ensure, and safely remove persistent branch worktrees | [Persistent Git worktrees](worktrees.md#git_worktree) |
| `git_squash` | Squash branch work into one explicitly messaged commit | [Persistent Git worktrees](worktrees.md#git_squash) |
| `git_rebase` | Rebase the current worktree branch onto a target | [Persistent Git worktrees](worktrees.md#git_rebase) |
| `git_merge` | Fast-forward or explicitly merge a branch into a clean target | [Persistent Git worktrees](worktrees.md#git_merge) |
| `git_clean` | Assert that a Git working tree is clean | [System steps](steps-system.md#git_clean) |
| `git_branch` | Assert local branch existence with an extensible operation schema | [System steps](steps-system.md#git_branch) |
| `git_remote_branch` | Assert local remote-tracking branch existence | [System steps](steps-system.md#git_remote_branch) |
| `git_branch_name` | Assert that a string is a valid Git branch name | [System steps](steps-system.md#git_branch_name) |
| `git_on_branch` | Assert the repository's current branch | [System steps](steps-system.md#git_on_branch) |
| `git_conventional_commit` | Create or validate a Conventional Commit message | [System steps](steps-system.md#git_conventional_commit) |
| `git_commit` | Stage selected paths and create a Git commit | [System steps](steps-system.md#git_commit) |

### GitHub

Read pull requests, releases, and Actions runs through `gh`.

| Step | Use it to | Examples |
| --- | --- | --- |
| `github_pr` | Find an open GitHub pull request from CI metadata or a Git branch | [System steps](steps-system.md#github_pr) |
| `github_release` | Check GitHub repository drift since the latest stable release | [System steps](steps-system.md#github_release) |
| `github_actions` | Watch one GitHub Actions run to completion through `gh` | [System steps](steps-system.md#github_actions) |

### Filesystem

Portable file operations, discovery, watches, and caches.

| Step | Use it to | Examples |
| --- | --- | --- |
| `file` | Perform shell-independent filesystem operations | [System steps](steps-system.md#file) |
| `scaffold` | Render a packaged template directory tree | [System steps](steps-system.md#scaffold) |
| `glob` | Discover regular files with portable patterns | [System steps](steps-system.md#glob) |
| `watch` | Wait for native filesystem events | [System steps](steps-system.md#watch) |
| `log_wait` | Follow a growing log until a regex matches | [System steps](steps-system.md#log_wait) |
| `temp` | Create automatically cleaned files, directories, or FIFOs | [System steps](steps-system.md#temp) |
| `cache` | Restore and save directory caches | [System steps](steps-system.md#cache) |

### System, network, and containers

Probe the environment, call APIs, and run containers.

| Step | Use it to | Examples |
| --- | --- | --- |
| `require_tool` | Require an executable and optionally validate its version | [System steps](steps-system.md#require_tool) |
| `tcp_probe` | Inspect TCP listeners without platform-specific commands | [System steps](steps-system.md#tcp_probe) |
| `multiplexer` | Label and annotate the current tmux, cmux, or Herdr context | [System steps](steps-system.md#multiplexer) |
| `http` | Make structured HTTP API calls | [System steps](steps-system.md#http) |
| `docker` | Run containers and manage Docker files and resources | [System steps](steps-system.md#docker) |

### Processes and automation

Run commands, services, scripts, and coding agents.

| Step | Use it to | Examples |
| --- | --- | --- |
| `shell` | Run argv commands, inline shell, or scripted PTY interactions | [Automation steps](steps-automation.md#shell) |
| `process` | Run a readiness-gated, lifecycle-managed service | [Automation steps](steps-automation.md#process) |
| `process_call` | Send a correlated JSONL request to one process or a process pool | [Automation steps](steps-automation.md#calling-a-process) |
| `agent` | Start an external coding agent with a prompt | [Automation steps](steps-automation.md#agent) |
| `lua` | Run typed in-process automation | [Automation steps](steps-automation.md#lua) |

## Workflow controls

Use controls to run independent work or repeat a block over runtime data.

| Control | Use it to | Examples |
| --- | --- | --- |
| `if` | Run a step or sequential block only when an expression is true | [Execution and composition](execution.md#conditions) |
| `env` | Set or remove scoped variables with a map or ordered `set`/`unset` operations | [Execution and composition](execution.md#scoped-environments) |
| `working_directory` | Run a block from an existing directory | [Execution and composition](execution.md#scoped-working-directories) |
| `concurrent` | Run a bounded fixed DAG of parallel steps | [Execution and composition](execution.md#concurrency) |
| `batch` | Process a runtime list in fixed-size chunks | [Workflow controls](workflow-control.md#batch) |
| `foreach` | Run a block once per item in a runtime list | [Workflow controls](workflow-control.md#foreach) |
| `matrix` | Run every combination of named dimensions | [Workflow controls](workflow-control.md#matrix) |
| `loop` | Repeat a sequential block until an expression is true | [Workflow controls](workflow-control.md#loop) |
| `try` / `catch` | Recover from a failed or timed-out sequential block | [Workflow controls](workflow-control.md#try-and-catch) |
| `cancel_on` | Race a sequential body against one or more named monitors and record the winner | [Workflow controls](workflow-control.md#cancel-on) |
| `observe` | Run a background loop driven by filesystem, HTTP, or shell events | [Workflow controls](workflow-control.md#observe) |
| `executor` | Run a block's steps through Docker, devenv, or SSH | [Executor scopes](executors.md) |
| `attempt` | Bound, repeat, or poll a body of steps | [Execution and composition](execution.md#attempts) |
| `return` | Finish successfully early and publish explicit outputs | [Early successful return](return.md) |
| `defer` | Attach cleanup to a successful step | [Finally cleanup](finally.md) |
| `finally` | Run workflow-level cleanup after the main phase | [Finally cleanup](finally.md) |
| `install` | Run steps before an installed workflow is committed | [Workflow installation](execution.md#workflow-installation) |
| `uninstall` | Run steps before an installed workflow is removed | [Workflow installation](execution.md#workflow-installation) |
| `plugin install/uninstall/lock` | Prefetch releases and generate or update workflow plugin pins | [Executable plugins](plugins.md) |
| `marketplace init/build` | Create, rebuild, or check a workflow and plugin marketplace | [Workflow installation](execution.md#workflow-marketplaces) |
| `marketplace workflow add/remove` | Add or remove maintained workflow package sources | [Workflow installation](execution.md#workflow-marketplaces) |
| `marketplace plugin init/add/update/release/remove` | Scaffold language presets and manage marketplace plugin releases | [Executable plugins](plugins.md#create-and-publish-a-marketplace-plugin) |
| `cron` | Run a workflow on a schedule | [Execution and composition](execution.md#scheduled-runs) |

## Workflow composition

| Pattern | Use it to | Examples |
| --- | --- | --- |
| `depends_on` | Run another discovered workflow first and consume its declared outputs | [Workflow prerequisites](execution.md#workflow-prerequisites) |
| `run_workflow` | Invoke a discovered workflow at a specific step, including inside bounded parallel controls | [Running child workflows](execution.md#running-child-workflows) |
| `targets` | Divide one workflow into named executable variants selected as `wuko run NAME TARGET` | [Workflow targets](execution.md#workflow-targets) |
| `invokable: false` | Keep a workflow available as a prerequisite without allowing direct invocation | [Workflow prerequisites](execution.md#workflow-prerequisites) |
| `require` | Split one workflow across files while keeping the same state and step sequence | [Splitting a workflow across files](execution.md#splitting-a-workflow-across-files) |
| `uses` | Reuse a composite action with explicit inputs, isolated internals, and declared outputs | [Composite actions](execution.md#composite-actions) |

See [Choosing a composition mechanism](execution.md#choosing-a-composition-mechanism) for a
side-by-side comparison and examples.
