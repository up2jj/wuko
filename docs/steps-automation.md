# Automation steps

[Back to the available steps](../README.md#available-steps)

Automation steps run local programs, coding agents, in-process Lua, or cancellation-aware waits.
Use `timeout` and `retry` on ordinary steps when an operation can hang or fail transiently.

## `shell`

Run an argv command directly or execute inline shell source.

Direct execution avoids implicit shell parsing:

```yaml
- id: status
  type: shell
  with:
    command: git
    args: [status, --short]
```

Use `script` when shell syntax is intentional. Values in `args` become `$1`, `$2`, and so on:

```yaml
- id: branch
  type: shell
  with:
    script: |
      set -eu
      git switch -c "$1"
    args: ["{{ .vars.branch }}"]
```

Run as another Unix account when Wuko has permission:

```yaml
- id: identity
  type: shell
  with: {command: id, args: [-un], user: deploy}
```

Live output is forwarded and also captured as `stdout`, `stderr`, and `exit_code`. `user` uses
native process credentials; Wuko does not invoke `sudo` or rewrite `HOME` and `USER`. The boolean
outputs `stdout_truncated` and `stderr_truncated` report whether capture reached its configured
bound.

Set `tty: true` for a local command that needs an interactive terminal, such as a shell, SSH
session, REPL, or terminal UI:

```yaml
- id: console
  type: shell
  with:
    command: /bin/sh
    args: [-i]
    tty: true
```

TTY mode connects the command to the workflow terminal, switches the terminal to raw mode for the
command's lifetime, and follows terminal resizes. The combined terminal stream is forwarded live
and the first 1 MiB is captured as `stdout`; `stderr` is empty and `stdout_truncated` is true when
more output was streamed. This keeps memory bounded for long-running interactive commands.

TTY mode requires an interactive, file-backed terminal and is unavailable in browser runs,
concurrent execution, and executor blocks. It cannot be combined with non-empty `stdin`. Terminal
state is restored when the command succeeds, fails, times out, or is canceled.

## `agent`

Start an external agent process and send its prompt on standard input.

Launch Codex:

```yaml
- id: codex
  type: agent
  with:
    command: codex
    args: [exec, -]
    prompt: "Implement task {{ .vars.task_id }} using {{ .vars.brief }}"
```

Choose between installed agents with conditions:

```yaml
- id: claude
  type: agent
  if: vars.agent == "claude"
  with:
    command: claude
    args: [-p]
    prompt: "Review {{ .steps.brief.value }}"

- id: codex
  type: agent
  if: vars.agent == "codex"
  with:
    command: codex
    args: [exec, -]
    prompt: "Review {{ .steps.brief.value }}"
```

The selected CLI must already be installed and authenticated. Like `shell`, the step streams and
captures `stdout`, `stderr`, and `exit_code`. See
[`examples/clickup-task.yaml`](../examples/clickup-task.yaml) for a complete task-to-agent flow.

## `lua`

Run a Lua file or inline source with typed arguments. Lua source is not templated; pass dynamic
values through `args`.

Run a file:

```yaml
- id: metadata
  type: lua
  with:
    file: ../scripts/metadata.lua
    args:
      task: "{{ .vars.task_name }}"
```

Produce typed outputs and variables inline:

```yaml
- id: metadata
  type: lua
  with:
    source: |
      local token = wuko.env.get("API_TOKEN")
      wuko.output("task", {id = "TASK-1", title = wuko.args.title})
      wuko.set_var("task_id", "TASK-1")
    args:
      title: "{{ .vars.task_name }}"
```

Use the host API for richer automation:

```lua
local response = wuko.http.request({method = "GET", url = wuko.args.url, timeout = 10})
wuko.fs.write("response.json", response.body)
local result = wuko.exec.run({command = "git", args = {"status", "--short"}})
wuko.output("clean", result.stdout == "")
```

The trusted `wuko` API exposes `args`, variables, outputs, environment, JSON, shared helpers,
key-value stores, HTTP, filesystem operations, and direct command execution. Outputs may be nil,
booleans, strings, numbers, arrays, or string-keyed objects; cyclic and mixed-key tables are
rejected.

## `wait`

Pause for a duration or poll one embedded step until an Expr condition becomes true.

Use a fixed cancellation-aware delay:

```yaml
- id: settle
  type: wait
  with: {duration: 30s}
```

Poll an API immediately and then every five seconds:

```yaml
- id: await_release
  type: wait
  timeout: 5m
  with:
    interval: 5s
    step:
      type: http
      with:
        url: https://api.example.com/releases/42
        response: json
    until: 'error == nil && result.value.status == "ready"'
```

Poll a local command with a different interval:

```yaml
- id: await_socket
  type: wait
  timeout: 1m
  with:
    interval: 1s
    step:
      type: shell
      with: {command: test, args: [-S, /tmp/app.sock]}
    until: error == nil
```

A polling wait requires a top-level timeout. Its expression can use the normal workflow roots plus
`result`, nullable `error`, and the one-based `poll` number. The embedded step accepts only `type`
and `with`; its final successful outputs are published under the wait step's ID.
