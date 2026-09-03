---
name: wuko-workflow-author
description: Create or update Wuko version-1 YAML workflows, including cron schedules, explicit time capture and transformation, Conventional Commit messages, templates and scaffold trees, conditions, early returns, finally cleanup, cancel-on monitors, foreach and matrix controls, required files, composite actions, waits, polling, retries, concurrency, interactive prompts and path selection, typed and imported variables, structured decoding, JSONPath selection, semantic versions, HTTP, files, managed temporary resources, glob discovery, native filesystem watches, persistent change detectors, persistent key-value stores, content-addressed directory caches, Lua, shell, Docker, and agent steps. Use when designing workflow files, extending existing workflows, or reviewing workflow structure before execution.
---

# Wuko Workflow Author

Create clear, strict, reviewable Wuko workflows and verify them before execution.

## Workflow

1. Inspect `README.md`, nearby workflows, referenced files, and the repository state before editing. Treat the task brief and existing workflow behavior as requirements.
2. Model the workflow with `version: 1`, a stable `name`, a useful `description`, explicit `vars` and `env`, and an ordered `steps` list. Declare every referenced `vars.<name>` key that no step produces, using a type-appropriate placeholder such as `""`, `false`, `[]`, `{}`, or `null`. A variable assigned by an earlier `set`, `tui_*` prompt, `extract`, `jsonpath`, `semver`, or `key_value` step counts as declared from that step onward, and invocation variables supplied with `--var` or `--var-file` count for that invocation. Sequential steps keep declaration order; Wuko never infers dependencies, and only explicit sibling `needs` inside one `concurrent` group can alter admission order. Use `depends_on` for prerequisite workflows with declared outputs, and set `invokable: false` when a prerequisite must not be selected through bare `wuko`, `wuko run`, or `wuko ui`. Use `require` only to split the current workflow without a state boundary, and `uses` for reusable behavior behind declared action inputs and outputs.
3. Choose the smallest appropriate step type. Declare every producer before its consumers. Use an anonymous `if` plus `steps` wrapper when several sequential children share one condition, `env` plus `steps` when descendants share an environment overlay, `working_directory` plus `steps` when children share an existing run directory, `concurrent` for a fixed bounded DAG, `foreach` for a runtime list, and `matrix` for a Cartesian product. Put consumers after the complete group or control unless a concurrent child explicitly names sibling producers with `needs`.
4. Render dynamic values with the documented template roots. Static root keys and visible step IDs are checked before execution, including constant `index` and `get` access; genuinely dynamic keys remain allowed, and a `hasKey` presence test checks only the container it inspects. Environment names are not checked, because the effective environment inherits the host process environment. Keep one-off substitutions inline; introduce a named template only for genuine reuse or a substantial multiline artifact. Use `if` only for boolean expressions and guard references to skipped steps with membership checks.
5. Keep local paths relative to the file or workflow context that resolves them. Preserve unique step IDs across required files, concurrent children, main steps, finally cleanup, and composite actions.
6. Treat shell, Lua, Docker, composite actions, and agents as trusted executable code. Keep credentials in environment values, never in workflow text, arguments, URLs, logs, or operation IDs.

## Step selection

- Use `tui_confirm` for boolean approval, `tui_choice` for enumerated values, `tui_input` for
  visible text, `tui_password` for masked text, and `tui_path` for rooted selection of existing
  files or directories.
  Choice descriptions and `description_field` make large static or dynamic lists searchable.
  Mark unavailable static choices with `disabled` plus a non-empty `reason`, or map dynamic object
  metadata with `disabled_field` and `reason_field`; use `default` or `default_field` only to
  initialize interactive selection. For computed dynamic mappings, use `label_expr`, `value_expr`,
  `description_expr`, `disabled_expr`, `reason_expr`, or `default_expr` instead of the matching
  field. These expressions receive `item` and normal workflow roots, run in property order, and
  may reference earlier resolved properties such as `disabled` from `reason_expr`. Use
  `select_all: true` in multiple mode to initialize every
  enabled choice as selected; it is incompatible with a `max_selected` smaller than the enabled
  choice count. Use `auto_select_single: true` in single-select mode when an interactive dynamic
  selector should bypass the picker if exactly one enabled choice remains; non-interactive runs do
  not infer a value and retain their normal required or optional behavior. Multiple choices preserve
  selection order and may use `min_selected` and
  `max_selected`; explicit bounds supersede `required`. Optional single choices
  expose `selected` to distinguish no selection from a selected null value. Dynamic object choices
  retain the selected source object in `item`, or ordered source objects in `items` for multiple
  selection, while the configured variable continues to contain only mapped scalar values.
  Keep path patterns relative to the picker root and supply prompt variables explicitly in
  non-interactive runs.
- Use `set` for JSON-compatible literals and Expr-based values; use Lua only when transformation
  needs imperative logic.
- Use `import_vars` for runtime JSON or TOML variable files. Keep its ordered `files` paths
  relative to the owning workflow or action, and remember imported keys normalize to lowercase.
- Use `decode` to turn bounded JSON, YAML, TOML, or line-oriented string/file content into a typed
  `.steps.<id>.value`. Use exactly one dotted `from` reference or `path`, set `max_bytes` above its
  `1MiB` default only when the input is intentionally larger, and use `trim` or `omit_empty` only
  with `format: lines`.
- Use `jsonpath` for RFC 9535 selection from a typed `vars` or `steps` value. Use `result: all`
  for a nodelist and `result: one` only when exactly one match is required; read normalized match
  locations from `paths`. Use `edit` when the selected data also needs transformation.
- Use `extract` for exactly one typed record embedded in text. Prefer a typed `format` for a
  complete predictable line and named Go regexp captures for substring or multiline matching.
  Read captures directly from the step outputs and map only intentionally shared values through
  `variables`; use `jsonpath` instead when the source is already structured.
- Use `edit` to set, create, delete, append/insert, deep-merge, or rename JSON/YAML/TOML nodes
  without re-encoding the whole file, or to derive an edited clone from a variable/expression.
  Choose exactly one `from.file`, `from.var`, or `from.expr`; use `expr` with `current` for
  calculated values. File edits are in place, while variable/expression sources are output-only.
  Missing paths fail unless `missing: ignore`; `missing: create` is only for `set` with a singular
  key path and cannot synthesize array entries.
- Use `semver` for strict semantic-version parsing, precedence comparison, constraint checks, and
  major/minor/patch increments. Read the operation's primary typed result from `value`; remember
  that build metadata does not affect comparison and ordinary constraints exclude prereleases.
- Use `git_conventional_commit` to create a reusable Conventional Commit message or assert that an
  existing message matches configured types, scopes, strictness, and an optional task-suffix rule.
  A create-time `task` works without `task_regex`; the regex is only an optional constraint there.
  Use `buildConventionalCommit` or `isConventionalCommit` in templates and Expr, or their snake_case
  Lua equivalents, when the value belongs inside a larger transformation rather than a named step.
- Use `git_commit` to commit the current index or stage explicit `paths` first. It commits the whole
  resulting index and skips an unchanged index by default. Prefer structured `author` and optional
  `committer` identities in fresh CI environments, ordered trailers for duplicate tokens, `signoff`
  for DCO workflows, and leave `verify` enabled unless bypassing hooks is deliberate.
- Use `time` as the only current-time boundary. Its output and default same-named variable are
  recordable and may be supplied with `--var` for reproducible runs. Use `parseTime`, `addTime`, and
  `formatTime` in templates, Expr, or Lua only to transform explicit strings; they never read the
  clock. Top-level `timezone` supplies the workflow default even without `cron`.
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
- Use `scaffold` to render every UTF-8 file and relative path component in a packaged template tree.
  Keep `from` relative to the owning workflow or action package, choose `on_conflict: fail` for a
  new artifact, `skip` for additive generation, or `overwrite` for deliberate regeneration, and
  consume its sorted `files` output when later steps need the generated paths. Scaffold preserves
  filename suffixes and file modes, rejects binary files and symlinks, and is unavailable in
  executors. Packaging carries files alone, so keep a placeholder file in any template directory
  that must survive an archive.
- Use `temp` for an empty file, directory, or POSIX FIFO that should live through the complete root
  run and be removed automatically after explicit `finally`. Consume its absolute `path` output in
  later steps; use `file` afterward when file content or custom permissions are required. Prefer an
  ordinary shell pipeline when both programs can start together. When a filesystem FIFO path is
  required, run its producer and consumer concurrently and bound them with a timeout because opening
  either endpoint can block. FIFOs connect same-host, same-user processes and are not automatically
  visible inside executor containers.
- Use `glob` to discover regular files with portable `*`, `?`, character-class, and recursive `**`
  patterns. Keep patterns relative to its `root` and consume the sorted metadata from `files`.
- Use `watch` to block until the first selected create, modify, rename, or remove notification below
  an existing local root. Prefer it over polling a shell probe, keep recursive roots narrow, give
  bounded waits a top-level timeout, and consume the relative `path` plus `operations` list.
- Use the named `observe:` control for a supervised background loop. Select a `filesystem`,
  `http`, or `shell` source under `source.type`; the `shell` source polls a command every `every`
  and triggers on a changed stdout or exit code, exposing `.observe.shell.value` and `exit_code`.
  Its body runs once and then on debounced source triggers while
  later foreground steps continue. Choose `restart`, `queue`, or `skip` for triggers received while
  active, and `on_error: continue` when a transient source failure should not end the run. Body runs read `.observe` and start from the declaration-time state snapshot. The workflow
  joins observers before `finally` on Ctrl-C or `return`.
- Use `log_wait` to follow an existing or newly created regular log file until a regex matches.
  Scan existing content first, set a top-level timeout and an appropriate `max_bytes`, and consume
  its `match` plus named `captures` outputs.
- Use `key_value` to persist JSON-compatible values between runs with `get`, `set`, `update`,
  `delete`, `list`, and `clear` against a named `local` or `global` store. Prefer `expr` over
  `value` for anything computed, because a template renders to text and stores `"3"` rather than
  `3`. Use `update` whenever the new value derives from the stored one: its `expr` sees `current`
  and `found` under one lock across the read and the write, which a `get` then `set` pair cannot
  hold. Bind a result with `variable`, give `get` a `default` instead of branching on `found`, and
  narrow `list` with `prefix`. Fetched code -- a remote workflow, a URL action, or a GitHub-hosted
  Wuko action -- has only `global` scope, and the store names `changed`, `once`, and `picker` are
  reserved for Wuko. Load a complete public or private GitHub repository directory with
  `uses: {github: owner/repo[@ref]:path, token: optional-template}`, or scalar
  `uses: owner/repo[@ref]:path` when no token is needed. The directory must contain one
  root `action.yml` or `action.yaml`; use `.workflow.dir` for its scripts, templates, and binary
  sidecars. This is a Wuko action package, not a GitHub Actions action. Prefer a commit ref for
  immutability; `sha256` is unavailable for GitHub directory sources.
- Use a named `once` block for bootstrap, fixture, or migration work that is complete after one
  successful persisted key. Set an explicit rendered `key`, choose `scope: local|global`, and keep
  the default `on_busy: error` unless concurrent invocations should wait and replay with
  `on_busy: wait`. Children are private; consume their recorded results through
  `steps.<once_id>.steps.<child_id>.outputs`, while recorded variable writes are restored normally.
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
  containers, and agent for coding-agent execution. For large machine-readable shell output, set
  `stdout: capture` and consume `steps.<id>.stdout` instead of redirecting through a temporary file;
  leave stderr omitted or set it to `inherit` so failures remain visible. Apply `capture_limit` when
  the producer may return unbounded data. For a command probe whose non-zero status is data rather
  than failure, set a complete `allowed_exit_codes` list and branch on `steps.<id>.exit_code`; keep
  Lua for probes that require multiple operations or structured interpretation. The output policy
  and capture-limit options are also available to `wuko.exec.run`.
- For a PTY command that needs scripted input, use `shell.with.interactions`: omit `expect` for
  immediate ordered writes and set it to a Go regex for prompt-driven sends. Use normal Wuko
  templates in `send`, set `newline: true` to press Enter, mark credential sends `sensitive: true`,
  and set `interact: true` only when the user should take control after the final send. Keep prompt
  regexes bounded and account for raw ANSI and carriage-return bytes when the target styles output.

## Important behavior

- `invokable` defaults to `true`. A workflow with `invokable: false` remains discoverable and may
  execute through `depends_on`, but direct name, file, HTTPS, and GitHub run/UI selectors reject it.
  Validation and tree inspection remain available; this marker is not a security boundary.
- `wuko validate`, `wuko run`, and dry runs perform static data-reference validation before any
  step is constructed or executed. Direct variable references must name a workflow declaration, an
  invocation variable, or a variable an earlier step declares it assigns.
  Forward or out-of-scope step references fail validation. Output fields below a visible step stay
  open because their runtime shape is not inferred.
- Step outputs and variables are committed only after success and are available only to later
  sequential steps. A statically valid reference to an earlier conditional step can still fail at
  render or expression-evaluation time when that producer was skipped and is absent from `steps`;
  guard optional producers with membership checks.
- A triggered `return` preserves prior commits, marks later declared work skipped, publishes its
  typed expressions through workflow outputs or the invoking action step, and still runs `finally`
  with successful main status. Use `outputs: {}` for a successful no-op result.
- Treat templates as string presentation, not workflow logic. Keep step ordering, `if` conditions,
  retries, types, and typed data explicit in YAML. Do not split a readable one-line substitution
  into a named template or build chains of templates that merely rename values.
- Use inline named templates for text reused in multiple places. Use file-backed templates for
  substantial generated files or scripts that are easier to review separately. Keep template
  file paths static and relative; bundle them with remote workflow or action archives.
- Use `scaffold` instead of repeated `file` writes when a reusable artifact is naturally a directory
  tree. Its files and path components share the owning workflow or action's strict template roots,
  functions, and named-template namespace.
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
- Use a named `try`/`catch` control for recovery. Both sibling blocks contain `steps`, and the
  parent ID is required. Failed or timed-out try children expose structured `.error` metadata to
  catch; parent cancellation bypasses catch. Catch entries are best effort, successful recovery
  continues ordinary execution, a catch whose entries are all skipped recovers nothing, and child
  records are namespaced under `.steps.<id>.try` and `.steps.<id>.catch`. Registered child defers run after catch and before the atomic parent commit.
  Do not nest try/catch, put `return` inside it, or place it inside `cancel_on`.
- Managed `temp` resources outlive nested workflows and actions and remain available to explicit
  `finally`; the engine removes them afterward. Cleanup failures fail the run after every managed
  removal has been attempted.
- Supply invocation-time JSON or TOML variables with repeatable `--var-file`; later files replace
  earlier top-level values and explicit `--var` entries take final initial-state precedence.
- Use `timeout` and `retry` deliberately. Retries have at-least-once effects; do not assume commands, requests, writes, containers, or agents can be rolled back.
- Use top-level `cron` only when `wuko run` should remain alive and execute repeatedly. Write five
  conventional cron fields or six fields with seconds first. Set an IANA `timezone` whenever
  schedule or calendar operations must not use the machine-local default. Scheduled attempts are serial, skip missed occurrences,
  reload the workflow at each occurrence, and continue after failures until canceled.
- Distinguish polling from retries: polling repeats successful observations until `until` matches,
  commits only the final result, and can still repeat external effects with at-least-once semantics.
- Give repeated external operations an explicit stable `operation_id` when the receiving service can use it for idempotency.
- Remember that concurrent roots share a pre-group state snapshot and cannot safely compete for interactive input. A direct child may use `needs` to consume successful sibling ancestors in the same group; keep the graph acyclic and use no `needs` outside that direct child list. Failed prerequisites skip descendants, while the group still publishes nothing unless every runnable child succeeds and the final write-conflict check passes.
- Use a named `cancel_on` control when a sequential body should race one or more named monitors.
  Every participant starts from the pre-control state, monitor IDs are required, and the first
  terminal participant wins; a monitor whose steps all skipped never triggers, while a skipped body
  still ends the race. Inspect the captured result through
  `.steps.<id>.status`, `.steps.<id>.winner`, `.steps.<id>.steps`, `.steps.<id>.vars`, and
  `.steps.<id>.monitors`; body variables never escape to the outer `.vars`. Canceled or unstarted
  declarations have null outputs. Use the optional `collect` Expr only for a smaller typed summary;
  it receives `steps`, `vars`, `monitors`, and `cancel_on`. A participant failure or timeout is data
  on the successful logical parent, while parent cancellation and collection failure still fail.
  Do not nest `cancel_on`, `return`, `require`, or declared `defer` inside it, do not place it
  inside an executor block, and do not rely on interactive stdin in monitor branches. Its
  participants keep the restrictions of the scope the control sits in. Consult `docs/workflow-control.md` for the full output
  contract and examples.
- A `working_directory` block transparently scopes `.run.dir` and every child step request without
  changing the process-wide directory. Its target must already exist. Relative and nested paths
  resolve from the enclosing `.run.dir`; the prior scope is restored when the block ends. Compose
  conditions by nesting an anonymous `if` block, and treat a directory block directly inside
  `concurrent` as one atomic sequential branch occupying one concurrency slot.
- Read `.run.environment_loaders` when behavior must depend on invocation environment activation;
  it is the ordered list of `mise`, `asdf`, and `direnv` loaders that actually changed the
  environment, and remains unchanged through working-directory, worktree, action, control,
  lifecycle, and executor scopes. A loader that ran without activating anything is omitted.
- An `env` block transparently overlays string-valued environment entries for all descendants.
  Values render together on entry from the enclosing runtime state, so they may use earlier step
  outputs but not sibling entries in the same map. Nested blocks override outer keys and restore
  the prior environment on exit. A child's `defer` retains its scoped environment, while workflow
  `finally` does not. `uses` sources resolve before runtime scopes, but the resolved action's
  execution receives the overlay. Tree and dry-run output show names only, never values.
- A multi-step conditional uses `- if: EXPR` with a sibling `steps` list. Its condition is evaluated once, its children retain their surrounding IDs and sequential state flow, and the wrapper has no ID, outputs, timeout, or retry policy. It may contain `concurrent`, `foreach`, or `matrix` subject to their normal nesting rules, but cannot be directly nested or placed directly inside `concurrent`.
- A successful `changed` detector advances its local snapshot immediately, even if later guarded
  work fails. It is unavailable to direct remote workflows and does not react to file timestamps
  or permissions.
- A `once` block records only after its complete body succeeds. Later runs report the block skipped
  while republishing its `steps` and `vars` outcome. Failure, cancellation, or a crash before the
  record is written causes a later retry; changing the key intentionally starts a new completion.
  Do not put `defer` or `return` inside `once` (its body runs on a private state clone, so a
  `return` cannot end the surrounding workflow), and remember that URL-fetched code has only global
  persistence.
- Foreach and matrix iterations also start from one pre-control snapshot, but steps within an
  iteration remain sequential. Read bindings from `.foreach` or `.matrix`. Add a typed `collect`
  Expr to expose one ordered value per iteration; it can read the final iteration's steps, local
  variables, runtime roots, and active binding. Without `collect`, the parent exposes only `count`.
  Collect only the fields later steps need. Iteration variables do not otherwise escape, fan-out
  controls cannot nest, and parallel controls are non-interactive. Consult
  `docs/workflow-control.md` when it is available in the project for the complete schema and
  limitations.
- Use a relative `uses` path for a local composite action. It may name a manifest or a directory
  containing exactly one `action.yml` or `action.yaml`, and resolves from the workflow or required
  fragment containing the declaration. Local actions use companion files from their action root
  and reject `sha256`; pin remote composite actions with an immutable release and `sha256` when
  reproducibility or supply-chain trust matters.
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
