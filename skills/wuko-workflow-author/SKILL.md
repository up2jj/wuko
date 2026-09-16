---
name: wuko-workflow-author
description: Create, update, or review Wuko version-1 YAML workflows. Use for workflow structure, step selection, templates, controls, reusable actions, execution safety, and pre-run verification; use the runner or debugger skill for execution or failure diagnosis.
---

# Wuko Workflow Author

Create clear, strict, reviewable Wuko workflows and verify them before execution.

## Workflow

1. Inspect `README.md`, nearby workflows, referenced files, and the repository state before editing. Treat the task brief and existing workflow behavior as requirements.
2. Model the workflow with `version: 1`, a stable `name`, a useful `description`, explicit `vars` and `env`, and an ordered `steps` list. Declare every referenced `vars.<name>` key that no step produces, using a type-appropriate placeholder such as `""`, `false`, `[]`, `{}`, or `null`. A variable assigned by an earlier `set`, `tui_*` prompt, `extract`, `jsonpath`, `semver`, or `key_value` step counts as declared from that step onward, and invocation variables supplied with `--var` or `--var-file` count for that invocation. Sequential steps keep declaration order; Wuko never infers dependencies, and only explicit sibling `needs` inside one `concurrent` group can alter admission order. Use `depends_on` for prerequisite workflows with declared outputs, and set `invokable: false` when a prerequisite must not be selected through bare `wuko`, `wuko run`, or `wuko ui`. Use `require` only to split the current workflow without a state boundary, and `uses` for reusable behavior behind declared action inputs and outputs.
3. Choose the smallest appropriate step type. Declare every producer before its consumers. Use an anonymous `if` plus `steps` wrapper when several sequential children share one condition, `env` plus `steps` when descendants share an environment overlay, `working_directory` plus `steps` when children share an existing run directory, `concurrent` for a fixed bounded DAG, `foreach` for a runtime list, and `matrix` for a Cartesian product. Put consumers after the complete group or control unless a concurrent child explicitly names sibling producers with `needs`.
4. Render dynamic values with the documented template roots. Static root keys and visible step IDs are checked before execution, including constant `index` and `get` access; genuinely dynamic keys remain allowed, and a `hasKey` presence test checks only the container it inspects. Environment names are not checked, because the effective environment inherits the host process environment. Execution-provider roots are read-only and exist only when their provider is active; use `.github`/`github`/`wuko.github` under GitHub Actions, guard the optional `pull_request` key, keep merge/event `github.sha` distinct from `github.pull_request.head.sha`, and treat every `github.payload` value as untrusted input. Keep one-off substitutions inline; introduce a named template only for genuine reuse or a substantial multiline artifact. Use `if` only for boolean expressions and guard references to skipped steps with membership checks.
5. Keep local paths relative to the file or workflow context that resolves them. Preserve unique step IDs across required files, concurrent children, main steps, finally cleanup, and composite actions.
6. Treat shell, Lua, Docker, plugins, composite actions, and agents as trusted executable code. Keep credentials in environment values, never in workflow text, arguments, URLs, logs, operation IDs, or plugin configuration that may be reported.

## Detailed guidance

Read only the references relevant to the requested workflow:

- For prompts, variables, decoding, JSONPath, extraction, structured edits, Git, time, and expression helpers, read [data and transforms](references/data-and-transforms.md).
- For HTTP and plugin steps, retries, files, managed resources, persistence, caches, shell, Lua, Docker, and agents, read [resources and execution](references/resources-and-execution.md).
- For invocation, templates, dependencies, returns, schedules, scopes, concurrency, fan-out, cleanup, and composite actions, read [control flow and lifecycle](references/control-flow-and-lifecycle.md).

## Verification

Run the narrowest checks that prove the change, then broaden them when the workflow or code path warrants it:

- Validate a discovered workflow with `wuko validate NAME`.
- Inspect structure with `wuko tree NAME` or `wuko tree --file PATH`.
- Render and validate a file without executing effects with `wuko run --file PATH --dry-run`.
- Supply required `--var-file`, `--var`, and `--env` values explicitly for validation or dry runs; never print secret values.
- Run `go test ./...` when workflow changes accompany Go implementation changes.

Report the files changed, the workflow behavior, and the exact verification commands and results.
