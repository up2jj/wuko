---
name: wuko-workflow-author
description: Create or update Wuko version-1 YAML workflows, including templates, conditions, required files, composite actions, waits, polling, retries, concurrency, interactive prompts, typed and imported variables, JSONPath selection, HTTP, files, Lua, shell, Docker, and agent steps. Use when designing workflow files, extending existing workflows, or reviewing workflow structure before execution.
---

# Wuko Workflow Author

Create clear, strict, reviewable Wuko workflows and verify them before execution.

## Workflow

1. Inspect `README.md`, nearby workflows, referenced files, and the repository state before editing. Treat the task brief and existing workflow behavior as requirements.
2. Model the workflow with `version: 1`, a stable `name`, a useful `description`, explicit `vars` and `env`, and an ordered `steps` list. Wuko does not infer a dependency graph or reorder steps.
3. Choose the smallest appropriate step type. Declare every producer before its consumers. Use `concurrent` only for independent children and put consumers after the complete group.
4. Render dynamic values with the documented template roots. Keep one-off substitutions inline; introduce a named template only for genuine reuse or a substantial multiline artifact. Use `if` only for boolean expressions and guard references to skipped steps with membership checks.
5. Keep local paths relative to the file or workflow context that resolves them. Preserve unique step IDs across required files, concurrent children, and composite actions.
6. Treat shell, Lua, Docker, remote actions, and agents as trusted executable code. Keep credentials in environment values, never in workflow text, arguments, URLs, logs, or operation IDs.

## Step selection

- Use `confirm` for boolean approval, `choice` for enumerated values, `input` for visible text, and
  `password` for masked text. Supply their variables explicitly in non-interactive runs.
- Use `set` for JSON-compatible literals and Expr-based values; use Lua only when transformation
  needs imperative logic.
- Use `import_vars` for runtime JSON or TOML variable files. Keep its ordered `files` paths
  relative to the owning workflow or action, and remember imported keys normalize to lowercase.
- Use `jsonpath` for RFC 9535 selection from a typed `vars` or `steps` value. Use `result: all`
  for a nodelist and `result: one` only when exactly one match is required; read normalized match
  locations from `paths`. Use Lua when the selected data also needs transformation or mutation.
- Use `http` for structured API calls with typed JSON responses, status validation, retries, and
  timeouts. Keep authorization values in environment-backed headers.
- Use `wait` with `duration` for a fixed delay, or embed a `type`/`with` step and an Expr `until`
  predicate for polling. Give every poll a top-level timeout and prefer read-only probes.
- Use `file` for auditable read, write, copy, move, remove, mkdir, list, stat, and chmod operations.
  Quote modes such as `"0755"`, opt into overwrites, and use recursive removal narrowly.
- Use shell for external programs, Lua for multi-operation scripting, Docker for isolated
  containers, and agent for coding-agent execution.

## Important behavior

- Step outputs and variables are committed only after success and are available only to later sequential steps. A forward `.steps` reference fails at runtime; a skipped producer is absent from `steps`.
- Treat templates as string presentation, not workflow logic. Keep step ordering, `if` conditions,
  retries, types, and typed data explicit in YAML. Do not split a readable one-line substitution
  into a named template or build chains of templates that merely rename values.
- Use inline named templates for text reused in multiple places. Use file-backed templates for
  substantial generated files or scripts that are easier to review separately. Keep template
  file paths static and relative; bundle them with remote workflow or action archives.
- Templates always return strings. Preserve boolean, number, array, and object action inputs with
  `expr`; keep reusable typed configuration in `vars`. Quote or encode rendered values for their
  destination format, because templates do not provide shell, JSON, YAML, or HTML escaping.
- Prefer direct `.vars`, `.env`, `.steps`, and `.inputs` access over clever `printf`, deeply nested
  `range`/`with` blocks, or duplicated control flow. Move imperative transformation to Lua and
  keep executable shell behavior visible in the owning step.
- Use `require` for local step files and keep required paths relative to the containing workflow file.
- Supply invocation-time JSON or TOML variables with repeatable `--var-file`; later files replace
  earlier top-level values and explicit `--var` entries take final initial-state precedence.
- Use `timeout` and `retry` deliberately. Retries have at-least-once effects; do not assume commands, requests, writes, containers, or agents can be rolled back.
- Distinguish polling from retries: polling repeats successful observations until `until` matches,
  commits only the final result, and can still repeat external effects with at-least-once semantics.
- Give repeated external operations an explicit stable `operation_id` when the receiving service can use it for idempotency.
- Remember that concurrent children share a pre-group state snapshot, cannot consume sibling outputs, and cannot safely compete for interactive input.
- Pin remote composite actions with an immutable release and `sha256` when reproducibility or supply-chain trust matters.
- Keep provider-specific CLI flags in the `agent` step configuration. Keep the prompt itself portable across Claude and Codex.

## Verification

Run the narrowest checks that prove the change, then broaden them when the workflow or code path warrants it:

- Validate a discovered workflow with `wuko validate NAME`.
- Inspect structure with `wuko tree NAME` or `wuko tree --file PATH`.
- Render and validate a file without executing effects with `wuko run --file PATH --dry-run`.
- Supply required `--var-file`, `--var`, and `--env` values explicitly for validation or dry runs;
  never print secret values.
- Run `go test ./...` when workflow changes accompany Go implementation changes.

Report the files changed, the workflow behavior, and the exact verification commands and results.
