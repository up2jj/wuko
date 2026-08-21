---
name: wuko-workflow-debugger
description: Diagnose Wuko workflow validation and runtime failures involving schema, templates, conditions, environment, JSONPath selection, semantic versions, step behavior, concurrency, retries, remote actions, or trust boundaries. Use when a Wuko workflow fails, skips unexpectedly, produces the wrong output, or needs a minimal reproducible diagnosis.
---

# Wuko Workflow Debugger

Find the smallest evidence-backed cause of a Wuko workflow failure before changing behavior.

## Triage workflow

1. Capture the exact workflow selector or file, command, supplied variables, relevant environment names, failing step, and complete non-secret error output.
2. Read the workflow, required files, composite action manifests, nearby examples, and the matching `README.md` section. Check the current Git diff before attributing a regression.
3. Reproduce safely with validation, tree output, or dry-run before running a workflow that can modify files, call services, create containers, or launch an agent.
4. Classify the failure before proposing a fix:
   - Schema or step decoding: version, unknown fields, required values, types, IDs, paths, or registered step types.
   - Template or condition evaluation: malformed or undefined named templates, missing roots,
     skipped-step references, caller/action scope confusion, string versus typed values, or
     non-boolean expressions.
   - Environment and directories: `--env`, `--var`, direnv, workflow directory, run directory, and relative files.
   - Runtime step behavior: shell exit status, Lua errors, HTTP responses, filesystem effects, Docker setup, or agent exit codes.
   - JSONPath selection: query syntax after template rendering, missing `vars` or `steps` source paths, `all` list semantics, and `one` cardinality failures.
   - Semantic versions: strict version syntax, normalized `v` prefixes, precedence versus build
     metadata, prerelease constraint matching, and increment-part behavior.
   - Concurrency and retry: pre-group snapshots, non-interactive children, deadlines, cancellation, duplicate writes, and at-least-once effects.
   - Remote actions and trust: source resolution, archive contents, digest pinning, credentials, and executable permissions.
5. Confirm the diagnosis with the smallest targeted command or test. Separate observed facts from hypotheses and state what evidence would disprove the diagnosis.
6. Implement a fix only when the request includes implementation; otherwise provide the root cause, reproduction, safe workaround, and focused next check.

## Useful checks

- Use `wuko validate NAME` to isolate loading and validation errors.
- For file-backed templates, confirm the declared path is static and relative to the owning
  workflow or packaged action. Direct remote YAML and standalone action manifests cannot carry
  companion template files.
- Distinguish parse-time template errors from runtime data errors. Validation can prove syntax and
  named references; `.steps` values exist only when an earlier sequential producer has succeeded.
- If a template failure is difficult to trace, inline one-use aliases and reduce nested template
  calls before adding helpers. Do not use templating to conceal ordering, condition, or typed-data
  problems; use workflow `if` and action-input `expr` where those semantics belong.
- Use `wuko tree NAME` or `wuko tree --file PATH` to inspect expansion, conditions, retries, concurrency, and composite actions.
- Use `wuko run NAME --dry-run` or `wuko run --file PATH --dry-run` to validate without running step effects.
- Run a focused Go test first, then `go test ./...` when the failure may cross packages.
- Inspect captured `stdout`, `stderr`, and `exit_code` outputs without exposing tokens or passwords.

## Safety rules

- Do not retry or rerun destructive effects merely to gather more output.
- Treat workflows, Lua, shell, Docker, remote actions, and agents as trusted executable code; review them before execution.
- Remember that a successful external effect may remain after a later failure or retry.
- Do not treat a missing key-value entry as an error unless the workflow requires `found` to be true.
- For concurrency failures, check whether a child incorrectly depends on a sibling or requests interactive input.

Finish with a concise diagnosis, evidence, affected scope, and verification status.
