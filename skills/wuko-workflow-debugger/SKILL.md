---
name: wuko-workflow-debugger
description: Diagnose Wuko workflow loading, validation, and runtime failures involving schema, templates, plugins, controls, step behavior, cleanup, or trust boundaries. Use when a workflow fails, skips unexpectedly, produces the wrong output, or needs a minimal reproducible diagnosis.
---

# Wuko Workflow Debugger

Find the smallest evidence-backed cause of a Wuko workflow failure before changing behavior.

## Triage workflow

1. Capture the exact workflow selector or file, command, supplied variables, relevant environment names, failing step, and complete non-secret error output.
2. Read the workflow, required files, composite action and plugin manifests, installation markers,
   nearby examples, and the matching `README.md` section. Check the current Git diff before
   attributing a regression.
3. Reproduce safely with validation, tree output, or dry-run before running a workflow that can
   modify files, call services, create containers, start plugins, or launch an agent. Inspect
   manifests and existing plugin stderr first when even loading would execute untrusted code.
4. Classify the failure before proposing a fix:
   - Schema or step decoding: version, unknown fields, required values, types, IDs, paths, or registered step types.
   - Template or condition evaluation: malformed or undefined named templates, missing roots,
     skipped-step references, caller/action scope confusion, string versus typed values, or
     non-boolean expressions.
   - Environment and directories: `--env`, `--var`, `--env-loader`, `WUKO_ENV_LOADERS`, mise,
     asdf, direnv, `.run.environment_loaders`, workflow directory, run directory, and relative files.
   - Runtime step behavior: shell exit status, Lua errors, HTTP responses, filesystem effects, Docker setup, or agent exit codes.
   - Plugin loading and execution: namespace discovery precedence, authoritative declarations,
     source or manifest-digest conflicts, differing `with` configuration, installation markers,
     artifact verification, v1/v2 negotiation, initialization, start, step execution, and cleanup.
   - Plugin v2 services and callbacks: failures before the `ready` event, background failures after
     readiness, service cancellation and shutdown verification, undeclared callback capabilities,
     invalid or completed parent requests, callback limits, and protocol reader or write ordering.
   - JSONPath selection: query syntax after template rendering, missing `vars` or `steps` source paths, `all` list semantics, and `one` cardinality failures.
   - Semantic versions: strict version syntax, normalized `v` prefixes, precedence versus build
     metadata, prerelease constraint matching, and increment-part behavior.
   - Concurrency and retry: pre-group snapshots, non-interactive children, deadlines, cancellation, duplicate writes, and at-least-once effects.
   - Background observe controls: synchronous source readiness, declaration-time state snapshots,
     debounce and `on_change` policy, body failures that intentionally keep observing, source
     errors that are fatal under `on_error: fail` and tolerated with backoff under `continue`,
     implicit final joining, and cancellation before detached cleanup.
   - Finally cleanup: main status, committed-state visibility, structured errors, continued cleanup
     failures, detached cancellation, action attempts, and forced-shutdown limits.
   - Composite actions and trust: declaring-file-relative local paths, action-root companion files,
     remote archive contents, digest pinning, credentials, and executable permissions.
5. Confirm the diagnosis with the smallest targeted command or test. Separate observed facts from hypotheses and state what evidence would disprove the diagnosis.
6. Implement a fix only when the request includes implementation; otherwise provide the root cause, reproduction, safe workaround, and focused next check.

## Useful checks

- Use `wuko validate NAME` to isolate loading and validation errors.
- For file-backed templates, confirm the declared path is static and relative to the owning
  workflow or action. Local actions read companions from their manifest directory. Direct remote
  YAML and standalone fetched action manifests cannot carry companion template files.
- For local `uses`, resolve the rendered relative path from the workflow or required fragment that
  declares it, not from the run directory or `working_directory`. Directories must contain exactly
  one root action manifest, local archives are unsupported, and local references reject `sha256`.
- Distinguish parse-time template errors from runtime data errors. Validation can prove syntax and
  named references; `.steps` values exist only when an earlier sequential producer has succeeded.
- Resolve a plugin namespace in order: the workflow's authoritative declaration, the nearest
  project installation, home installation, config installation, then `PATH`. An invalid
  higher-precedence candidate blocks fallback. Compare declaration conflicts by source, manifest
  digest, and `with` identity without printing configuration values.
- Check the release manifest and installation marker before blaming negotiation. A pinned protocol
  must match the plugin response; a marker without `protocol` means v1, while a bare development or
  `PATH` executable may negotiate from v2 to v1 in a fresh process.
- Separate plugin lifecycle phases. Initialization advertises capabilities; start applies plugin
  configuration; a v2 service failure before `ready` fails its owning step, while a failure after
  `ready` is a background-service outcome governed by its service policy. Cancellation is followed
  by bounded draining and then cleanup, so a shutdown verification error may be the primary cause.
- For host-callback failures, confirm that the v2 step declared the callback, the `parent_id` still
  names an active request, and the plugin continues reading and serializing protocol traffic while
  calls are outstanding. A callback-limit refusal is retryable only after an earlier call finishes.
- For cleanup failures, inspect `finally.status` separately from the progressively accumulated
  `finally.errors`. Failed main and cleanup steps do not commit outputs; match stable status and
  step metadata rather than error-message text. Consult `docs/finally.md` when available.
- If a template failure is difficult to trace, inline one-use aliases and reduce nested template
  calls before adding helpers. Do not use templating to conceal ordering, condition, or typed-data
  problems; use workflow `if` and action-input `expr` where those semantics belong.
- Use `wuko tree NAME` or `wuko tree --file PATH` to inspect expansion, conditions, retries, concurrency, and composite actions.
- Use `wuko run NAME --dry-run` or `wuko run --file PATH --dry-run` to validate without running step effects.
- Run a focused Go test first, then `go test ./...` when the failure may cross packages.
- Inspect captured `stdout`, `stderr`, and `exit_code` outputs without exposing tokens or passwords.

## Safety rules

- Do not retry or rerun destructive effects merely to gather more output.
- Treat workflows, Lua, shell, Docker, plugins, composite actions, and agents as trusted executable
  code; review them before execution. Prefer manifest inspection and captured plugin stderr over
  repeatedly starting an unknown or side-effectful plugin.
- Remember that a successful external effect may remain after a later failure or retry.
- Do not treat a missing key-value entry as an error unless the workflow requires `found` to be true.
- For key-value failures, check scope and store name first: fetched code has no `local` store, and `changed`, `once`, and `picker` are reserved for Wuko.
- For concurrency failures, check whether a child incorrectly depends on a sibling or requests interactive input.

Finish with a concise diagnosis, evidence, affected scope, and verification status.
