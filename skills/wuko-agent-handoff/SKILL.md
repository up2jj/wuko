---
name: wuko-agent-handoff
description: Prepare provider-neutral Wuko agent steps and task prompts for Claude or Codex. Use when handing an implementation task to an external coding agent, designing a task-start workflow, or reviewing whether an agent handoff contains requirements, branch context, verification, and a clear completion report.
---

# Wuko Agent Handoff

Create handoffs that give Claude or Codex enough context to implement and verify work without coupling the task brief to one provider.

## Build the handoff

1. Identify the source of truth: task brief path, issue or task ID, repository, current branch, working directory, constraints, acceptance criteria, and required tests.
2. Keep the shared prompt explicit and provider-neutral. Require the agent to read the complete brief, inspect the repository, preserve the current branch policy, implement the requested change, run relevant verification, and report the result.
3. State what the agent must not infer: branch switching, commits, pushes, destructive cleanup, unavailable credentials, or unrelated refactors. Make each behavior an explicit workflow or prompt decision.
4. Pass the prompt through the Wuko `agent` step. Keep the executable command and provider-specific flags in `with.command` and `with.args`; do not embed CLI syntax or permission assumptions in the shared prompt.
5. Add a conditional step when selecting between Claude and Codex. Give each branch the same task content and completion contract, changing only the command configuration required by the selected CLI.

## Prompt contract

Include these elements in order:

- Task identity and concise objective.
- Path to the full requirements brief and instruction to treat it as authoritative.
- Current repository and branch context, including whether branch changes are forbidden.
- Implementation expectations and explicit out-of-scope behavior.
- Verification commands and acceptance criteria.
- Required final report: summary of changes, tests or checks run, results, and any remaining risks or follow-up.

Use a file path for long task context instead of duplicating or truncating the brief inside YAML.
Keep one-off prompt substitutions inline. Use a named template only for a stable prompt fragment
reused across providers, and use a file-backed template only for a reviewable reusable prompt
skeleton—not as a second copy of the task brief. Avoid nested template chains that hide the final
prompt. Templates return strings, so keep typed agent configuration explicit. Quote rendered
values safely, and avoid putting secrets in prompts, arguments, logs, templates, or task files.

## Verification

- Use `wuko tree NAME` or `wuko tree --file PATH` to confirm the agent step and its condition.
- Use `wuko run NAME --dry-run` or `wuko run --file PATH --dry-run` to validate rendered prompt inputs without launching the agent.
- Run the handoff only in a trusted repository with the intended agent CLI installed and authenticated.
- Keep agent steps out of concurrent groups when they need interactive behavior or shared mutable state.
- Review the agent's final report against the brief and independently inspect the diff and verification output.

Prefer a short, complete handoff over a long generic instruction block. The workflow owns execution policy; the prompt owns task intent and completion criteria.
