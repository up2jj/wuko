---
name: wuko-workflow-author
description: Create or update Wuko version-1 YAML workflows, including templates, conditions, required files, composite actions, retries, concurrency, Lua, shell, Docker, and agent steps. Use when designing workflow files, extending existing workflows, or reviewing workflow structure before execution.
---

# Wuko Workflow Author

Create clear, strict, reviewable Wuko workflows and verify them before execution.

## Workflow

1. Inspect `README.md`, nearby workflows, referenced files, and the repository state before editing. Treat the task brief and existing workflow behavior as requirements.
2. Model the workflow with `version: 1`, a stable `name`, a useful `description`, explicit `vars` and `env`, and an ordered `steps` list.
3. Choose the smallest appropriate step type. Keep dependent work sequential; use `concurrent` only for independent children and put consumers after the group.
4. Render dynamic values with the documented template roots. Use `if` only for boolean expressions and guard references to skipped steps with membership checks.
5. Keep local paths relative to the file or workflow context that resolves them. Preserve unique step IDs across required files, concurrent children, and composite actions.
6. Treat shell, Lua, Docker, remote actions, and agents as trusted executable code. Keep credentials in environment values, never in workflow text, arguments, URLs, logs, or operation IDs.

## Important behavior

- Use `require` for local step files and keep required paths relative to the containing workflow file.
- Use `timeout` and `retry` deliberately. Retries have at-least-once effects; do not assume commands, requests, writes, containers, or agents can be rolled back.
- Give repeated external operations an explicit stable `operation_id` when the receiving service can use it for idempotency.
- Remember that concurrent children share a pre-group state snapshot, cannot consume sibling outputs, and cannot safely compete for interactive input.
- Pin remote composite actions with an immutable release and `sha256` when reproducibility or supply-chain trust matters.
- Keep provider-specific CLI flags in the `agent` step configuration. Keep the prompt itself portable across Claude and Codex.

## Verification

Run the narrowest checks that prove the change, then broaden them when the workflow or code path warrants it:

- Validate a discovered workflow with `wuko validate NAME`.
- Inspect structure with `wuko tree NAME` or `wuko tree --file PATH`.
- Render and validate a file without executing effects with `wuko run --file PATH --dry-run`.
- Supply required `--var` and `--env` values explicitly for validation or dry runs; never print secret values.
- Run `go test ./...` when workflow changes accompany Go implementation changes.

Report the files changed, the workflow behavior, and the exact verification commands and results.
