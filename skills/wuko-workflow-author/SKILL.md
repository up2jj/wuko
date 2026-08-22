---
name: wuko-workflow-author
description: Create or update Wuko version-1 YAML workflows, including cron schedules, templates, conditions, early returns, finally cleanup, foreach and matrix controls, required files, composite actions, waits, polling, retries, concurrency, interactive prompts and path selection, typed and imported variables, JSONPath selection, semantic versions, HTTP, files, managed temporary resources, glob discovery, persistent change detectors, content-addressed directory caches, Lua, shell, Docker, and agent steps. Use when designing workflow files, extending existing workflows, or reviewing workflow structure before execution.
---

# Wuko Workflow Author

Create clear, strict, reviewable Wuko workflows and verify them before execution.

## Workflow

1. Inspect `README.md`, nearby workflows, referenced files, and the repository state before editing. Treat the task brief and existing workflow behavior as requirements.
2. Model the workflow with `version: 1`, a stable `name`, a useful `description`, explicit `vars` and `env`, and an ordered `steps` list. Wuko does not infer a dependency graph or reorder steps.
3. Choose the smallest appropriate step type. Declare every producer before its consumers. Use an anonymous `if` plus `steps` wrapper when several sequential children share one condition, `working_directory` plus `steps` when children share an existing run directory, `concurrent` for a fixed set of independent children, `foreach` for a runtime list, and `matrix` for a Cartesian product. Put consumers after the complete group or control.
4. Render dynamic values with the documented template roots. Keep one-off substitutions inline; introduce a named template only for genuine reuse or a substantial multiline artifact. Use `if` only for boolean expressions and guard references to skipped steps with membership checks.
5. Keep local paths relative to the file or workflow context that resolves them. Preserve unique step IDs across required files, concurrent children, main steps, finally cleanup, and composite actions.
6. Treat shell, Lua, Docker, remote actions, and agents as trusted executable code. Keep credentials in environment values, never in workflow text, arguments, URLs, logs, or operation IDs.

## Step selection

- Use `tui_confirm` for boolean approval, `tui_choice` for enumerated values, `tui_input` for
  visible text, `tui_password` for masked text, and `tui_path` for rooted selection of existing
  files or directories.
  Choice descriptions and `description_field` make large static or dynamic lists searchable.
  Mark unavailable static choices with `disabled` plus a non-empty `reason`, or map dynamic object
  metadata with `disabled_field` and `reason_field`; use `default` or `default_field` only to
  initialize interactive selection. Multiple choices preserve selection order and may use
  `min_selected` and `max_selected`; explicit bounds supersede `required`. Optional single choices
  expose `selected` to distinguish no selection from a selected null value.
  Keep path patterns relative to the picker root and supply prompt variables explicitly in
  non-interactive runs.
- Use `set` for JSON-compatible literals and Expr-based values; use Lua only when transformation
  needs imperative logic.
- Use `import_vars` for runtime JSON or TOML variable files. Keep its ordered `files` paths
  relative to the owning workflow or action, and remember imported keys normalize to lowercase.
- Use `jsonpath` for RFC 9535 selection from a typed `vars` or `steps` value. Use `result: all`
  for a nodelist and `result: one` only when exactly one match is required; read normalized match
  locations from `paths`. Use Lua when the selected data also needs transformation or mutation.
- Use `extract` for exactly one typed record embedded in text. Prefer a typed `format` for a
  complete predictable line and named Go regexp captures for substring or multiline matching.
  Read captures directly from the step outputs and map only intentionally shared values through
  `variables`; use `jsonpath` instead when the source is already structured.
- Use `semver` for strict semantic-version parsing, precedence comparison, constraint checks, and
  major/minor/patch increments. Read the operation's primary typed result from `value`; remember
  that build metadata does not affect comparison and ordinary constraints exclude prereleases.
- Use `require_tool` before external commands that need an executable or supported tool version.
  Configure nonstandard version flags with `version_args`, and consume its `path` or checked
  `version` output only after the guard succeeds.
- Use `http` for structured API calls with typed JSON responses, status validation, retries, and
  timeouts. Keep authorization values in environment-backed headers.
- Use `wait` with `duration` for a fixed delay, or embed a `type`/`with` step and an Expr `until`
  predicate for polling. Give every poll a top-level timeout and prefer read-only probes.
- Use `file` for auditable reads, atomic writes and directory swaps, copying, moving, removal,
  directory creation, listing and filtered discovery, metadata and disk-usage inspection, links,
  truncation, bounded tails, permissions, and timestamps. Consult
  `docs/filesystem-operations.md` for the strict fields, outputs, symlink rules, and failure guarantees.
  Quote modes such as `"0755"`, opt into overwrites, and use recursive removal narrowly.
- Use `temp` for an empty file or directory that should live through the complete root run and be
  removed automatically after explicit `finally`. Consume its absolute `path` output in later
  steps; use `file` afterward when content or custom permissions are required.
- Use `glob` to discover regular files with portable `*`, `?`, character-class, and recursive `**`
  patterns. Keep patterns relative to its `root` and consume the sorted metadata from `files`.
- Use `changed` before guarded work that should run only when selected file contents or named
  values differ from the detector's previous local snapshot. Branch on its `changed` output, and
  give repeated foreach, matrix, or action detectors a templated key containing their binding.
- Use `cache` with an early `restore` and a later `save` for dependency or build directories.
  Derive the key from stable lockfiles or manifests, keep restore and save declarations identical,
  and branch on the restore step's `hit` output when work can be skipped.
- Use anonymous `return` with Expr-valued `outputs` to finish a workflow or composite action
  successfully after a cache hit or other terminal condition. Keep it in the main sequential flow
  or a conditional/working-directory block; do not place it inside concurrent, foreach, matrix, or
  finally. Composite-action return keys must exactly match the declared action outputs.
- Use shell for external programs, Lua for multi-operation scripting, Docker for isolated
  containers, and agent for coding-agent execution.

## Important behavior

- Step outputs and variables are committed only after success and are available only to later sequential steps. A forward `.steps` reference fails at runtime; a skipped producer is absent from `steps`.
- A triggered `return` preserves prior commits, marks later declared work skipped, publishes its
  typed expressions through workflow outputs or the invoking action step, and still runs `finally`
  with successful main status. Use `outputs: {}` for a successful no-op result.
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
- Use one workflow- or action-level `finally` list for multi-step cleanup. Give cleanup operations
  their own timeouts, make them idempotent, and select failures with stable `finally.status` and
  `finally.errors` metadata rather than error-message text. Cleanup cannot recover from an error;
  consult `docs/finally.md` when it is available for lifecycle and limitations.
- Managed `temp` resources outlive nested workflows and actions and remain available to explicit
  `finally`; the engine removes them afterward. Cleanup failures fail the run after every managed
  removal has been attempted.
- Supply invocation-time JSON or TOML variables with repeatable `--var-file`; later files replace
  earlier top-level values and explicit `--var` entries take final initial-state precedence.
- Use `timeout` and `retry` deliberately. Retries have at-least-once effects; do not assume commands, requests, writes, containers, or agents can be rolled back.
- Use top-level `cron` only when `wuko run` should remain alive and execute repeatedly. Write five
  conventional cron fields or six fields with seconds first; add an IANA `timezone` only when the
  machine-local default is inappropriate. Scheduled attempts are serial, skip missed occurrences,
  reload the workflow at each occurrence, and continue after failures until canceled.
- Distinguish polling from retries: polling repeats successful observations until `until` matches,
  commits only the final result, and can still repeat external effects with at-least-once semantics.
- Give repeated external operations an explicit stable `operation_id` when the receiving service can use it for idempotency.
- Remember that concurrent children share a pre-group state snapshot, cannot consume sibling outputs, and cannot safely compete for interactive input.
- A `working_directory` block transparently scopes `.run.dir` and every child step request without
  changing the process-wide directory. Its target must already exist. Relative and nested paths
  resolve from the enclosing `.run.dir`; the prior scope is restored when the block ends. Compose
  conditions by nesting an anonymous `if` block, and treat a directory block directly inside
  `concurrent` as one atomic sequential branch occupying one concurrency slot.
- A multi-step conditional uses `- if: EXPR` with a sibling `steps` list. Its condition is evaluated once, its children retain their surrounding IDs and sequential state flow, and the wrapper has no ID, outputs, timeout, or retry policy. It may contain `concurrent`, `foreach`, or `matrix` subject to their normal nesting rules, but cannot be directly nested or placed directly inside `concurrent`.
- A successful `changed` detector advances its local snapshot immediately, even if later guarded
  work fails. It is unavailable to direct remote workflows and does not react to file timestamps
  or permissions.
- Foreach and matrix iterations also start from one pre-control snapshot, but steps within an
  iteration remain sequential. Read bindings from `.foreach` or `.matrix`; consume ordered results
  beneath the parent step ID. Iteration variables do not escape, fan-out controls cannot nest, and
  parallel controls are non-interactive. Consult `docs/workflow-control.md` when it is available in
  the project for the complete schema and limitations.
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
