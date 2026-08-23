---
name: wuko-workflow-runner
description: Safely select, preview, and execute an existing Wuko workflow from a discovered name, local file, HTTPS URL, or GitHub locator. Use when the user wants to run a workflow and review its inputs and effects before execution; use the author or debugger skill instead for workflow edits or detailed failure diagnosis.
---

# Wuko Workflow Runner

Run a trusted existing workflow only after its source, inputs, and expected effects are clear.

## Prepare the run

1. Record the working directory and exact selector. Use `wuko list` when discovery is needed. If a
   name is shadowed or the user identified a particular file, preserve that choice with `--file`
   rather than substituting another definition.
2. For a local workflow, inspect its YAML, recursively required files, referenced templates, and
   visible action sources before invoking Wuko. Identify shell, Lua, Docker, HTTP, file, agent,
   persistent key-value, retry, polling, and concurrent effects. Do not edit the workflow while
   preparing to run it. If it declares `invokable: false`, do not try another selector: it may run
   only as another workflow's `depends_on` prerequisite.
3. Treat a remote workflow or action as trusted executable code. Before the first command that
   loads one, show its locator, whether it is pinned, and the fact that loading may download
   content. Require explicit trust confirmation. A command-based `uses` source requires the same
   separate confirmation because `validate`, `tree`, and dry-run execute its resolver command
   while loading the workflow.
4. Gather required non-secret `--var` and `--var-file` inputs and list environment names without
   exposing their values. Preserve workflow prompts when an interactive terminal is available;
   otherwise arrange their values explicitly before previewing.

Never ask the user to paste a secret into chat or put a secret in `--env`, `--var`, command
arguments, URLs, prompts, or logs. Ask the user to export a missing secret in their own environment,
then check only whether its name is present. Do not display secret-bearing variable files.

## Preview and execute

1. After any source-trust confirmation, use the narrowest commands that establish the plan:
   `wuko validate NAME` for a discovered local workflow, `wuko tree NAME` or
   `wuko tree --file PATH` for structure, and `wuko run ... --dry-run` for final rendered
   validation. Use the same non-secret variable overrides and variable files, and the same
   inherited environment, intended for the real run. Validation and tree inspection accept
   dependency-only workflows, but dry-run is a direct invocation and does not bypass
   `invokable: false`.
2. Avoid redundant loads when loading itself has effects. In particular, choose one sufficient
   preview command for a workflow with a command-based action source instead of executing that
   resolver through validate, tree, and dry-run separately.
3. Summarize the exact command, working directory, selector, non-secret inputs by name, interactive
   prompts, retries, and expected external effects. Require explicit confirmation immediately
   before the real `wuko run`; an earlier request to run the workflow is not this confirmation.
4. If the command, working directory, source, or inputs change after preview, preview the changed
   run and confirm it again. Otherwise execute the confirmed command once and stream its normal
   progress without enabling debug output unnecessarily.

Do not automatically rerun a failed, canceled, or interrupted workflow. Wuko retries have
at-least-once effects, and completed external effects are not rolled back when a later step fails.

## Report the result

On success, report the selector, command, and completion status. On failure, preserve the exact
command, failed step when identifiable, exit status, and redacted relevant output. State that
partial effects may remain, but do not edit the workflow, retry it, or expand into detailed
diagnosis unless the user requests that work. Use `$wuko-workflow-debugger` for a subsequent
diagnosis and `$wuko-workflow-author` for requested workflow changes.
