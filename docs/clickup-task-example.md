# ClickUp task agent example

[`examples/clickup-task.yaml`](../examples/clickup-task.yaml) is a complete task-start workflow. It
asks for a ClickUp task ID, downloads the Markdown description, creates a task branch, and launches
Claude Code or Codex with a prepopulated implementation prompt.

## Requirements

- Run the workflow from the repository root.
- Install and authenticate the agent CLI you intend to select.
- Set a ClickUp personal API token in `CLICKUP_TOKEN`.
- For a custom task ID, also set `CLICKUP_TEAM_ID` to the numeric Workspace ID.

```sh
export CLICKUP_TOKEN=pk_...

# Required only for custom task IDs.
export CLICKUP_TEAM_ID=123456

wuko run --file ./examples/clickup-task.yaml
```

Native ClickUp task IDs work without a Workspace ID. For custom IDs, the workflow sends ClickUp's
`custom_task_ids` and `team_id` query parameters.

## What it creates

The task brief is written to `.wuko/context/<task-id>.md`, which this repository ignores. The
branch is named `<task-id>_<lowercase-task-name-slug>`.

Before creating the branch, the workflow rejects:

- a dirty working tree;
- an invalid generated branch name;
- an existing local branch; or
- an existing remote branch.

Wuko performs the HTTP request before starting the agent, so it cannot reuse ClickUp MCP
authentication. The API token is used only in the request's `Authorization` header.
