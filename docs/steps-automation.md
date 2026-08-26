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

When a previous step returns a complete argument vector, evaluate it without converting the list
to template text:

```yaml
- id: console
  type: shell
  with:
    argv:
      expr: steps.resolve.argv
    tty: true
```

`argv` contains exactly one `expr` field and cannot be combined with `command`, `script`, `shell`,
or `args`. The expression is compiled during validation and evaluated immediately before the
process starts. It must return a non-empty array or slice whose first item is a non-empty
executable. Strings remain unchanged; booleans and finite numbers are converted to their standard
base-10 text. Nulls, objects, nested lists, and other values are rejected.

The evaluated vector is passed directly to the process API. Spaces, quotes, glob characters, empty
arguments, and shell metacharacters therefore remain ordinary argument data. This behavior is the
same locally and for shell steps inside Docker or devenv executor scopes. It does not change the
separate `docker` step's run-operation schema.

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

By default, only exit code 0 succeeds. Set `allowed_exit_codes` to a non-empty list of codes from
0 through 255 when a command uses non-zero statuses as useful observations. The configured list
replaces the default, so include every accepted code explicitly:

```yaml
- id: authorization
  type: shell
  with:
    command: kubectl
    args: [auth, can-i, get, deployments]
    allowed_exit_codes: [0, 1]
    stdout: capture
    stderr: capture
```

An allowed exit commits the usual `exit_code`, `stdout`, `stderr`, `stdout_truncated`, and
`stderr_truncated` outputs for later conditions. Command startup, executor, stream, timeout, and
cancellation errors still fail the step. A disallowed exit retains normal failure and retry
behavior, while an allowed exit completes without retrying.

Control forwarding and capture independently for each process stream with `stdout` and `stderr`:

| Policy | Display live | Return in the output |
| --- | --- | --- |
| `inherit` | Yes | No; the output string is empty |
| `capture` | No | Yes |
| `tee` | Yes | Yes |
| `discard` | No | No; the output string is empty |

Both policies default independently to `tee`, preserving the standard behavior. For example,
capture a large JSON document without printing it while leaving diagnostics and failures visible:

```yaml
- id: deployments
  type: shell
  with:
    command: kubectl
    args: [get, deployments, --all-namespaces, -o, json]
    stdout: capture
    stderr: inherit
```

An omitted `stderr` also defaults to `tee`, so `stdout: capture` alone still displays and captures
stderr. Use `capture_limit` to bound each captured stream independently:

```yaml
with:
  command: generate-manifest
  stdout: capture
  capture_limit: 16MiB
```

Sizes are positive integers followed by `B`, `KiB`, `MiB`, `GiB`, or `TiB`. When a stream exceeds
the limit, Wuko retains its leading bytes without adding a marker, continues draining the process,
and sets its `stdout_truncated` or `stderr_truncated` output to `true`. Streaming continues beyond
the capture limit for `tee`. Without `capture_limit`, non-TTY capture is unlimited.

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

TTY mode cannot be combined with non-empty `stdin`. Terminal state is restored when the command
succeeds, fails, times out, or is canceled. `stdout`, `stderr`, and `capture_limit` cannot be set
with `tty: true`; TTY output remains a live merged stream with its existing 1 MiB capture.

### Scripted PTY interactions

Use `interactions` to write initial input, wait for prompts, inject dynamic values, and optionally
hand the live PTY to the user without an external `expect` executable:

```yaml
- id: jump
  type: shell
  with:
    tty: true
    argv:
      expr: steps.jump_argv.value
    interactions:
      - expect: 'iex[^\r\n]*>'
        send: Recruitee.Environment.current_env()
        newline: true
        timeout: 30s
    interact: true
```

| Field | Required | Default | Meaning |
| --- | --- | --- | --- |
| `interactions` | No | — | Non-empty ordered list of PTY writes and prompt responses. Requires `tty: true`. |
| `interactions[].send` | Yes | — | Text to write. An empty string is valid. Normal Wuko templates are rendered first. |
| `interactions[].expect` | No | — | Go regular expression matched against raw merged PTY output. Without it, `send` is immediate. |
| `interactions[].newline` | No | `false` | Append carriage return (`\r`) after `send`, equivalent to pressing Enter. |
| `interactions[].timeout` | No | `30s` | Positive bound for this `expect`. It is invalid without `expect`. |
| `interactions[].sensitive` | No | `false` | Redact `send` from diagnostics and suppress PTY echo while injecting it. |
| `interact` | No | `false` | Hand the PTY to the user immediately after the complete scripted sequence. |

Every entry requires `send`, but `expect` is optional. Consecutive send-only entries are written
immediately in declaration order, so startup input does not need an artificial prompt:

```yaml
with:
  command: setup-console
  tty: true
  interactions:
    - {send: select-project, newline: true}
    - {send: enable-feature, newline: true}
```

Immediate and prompt-driven entries may be mixed for login flows. Dynamic sends can use all normal
template roots, including `.inputs`, `.vars`, `.env`, `.steps`, `.dependencies`, active control
bindings, `.workflow`, and `.run`:

```yaml
with:
  command: ssh
  args: [gateway.example]
  tty: true
  interactions:
    - expect: 'Login:'
      send: "{{ .vars.username }}"
      newline: true
    - expect: 'Password:'
      send: "{{ .env.LOGIN_PASSWORD }}"
      newline: true
      sensitive: true
    - expect: 'workspace>'
      send: "use {{ .steps.workspace.value }} {{ .dependencies.build.artifact }}"
      newline: true
  interact: true
```

`send` uses template rendering, not the Expr language used by `argv.expr`. A sensitive send is
absent from debug configuration and Wuko suppresses the PTY line discipline's echo for that write.
A child program that independently prints the value can still expose it and must handle its own
output safely.

Expectations match the raw PTY byte stream. ANSI escapes and carriage returns are not removed, so
patterns should account for them when the target renders styled prompts. A match consumes through
the matched bytes; any bytes after it remain available to the next expectation. Wuko retains at
most 1 MiB of unmatched output for the active expectation.

When `interact: true`, workflow input is withheld during scripting. User control begins immediately
after the final send, including its optional carriage return, has been written. Without `interact`,
the command continues headlessly after scripting. Headless scripted PTYs use a 24×80 size and can
run without a file-backed workflow terminal, including non-interactive and browser-driven runs.
Plain `tty: true` without `interactions` preserves immediate handoff and still requires an
interactive file-backed terminal. Docker executor sessions reject TTY mode; local process wrappers
such as devenv may forward it.

### Live-console appearance

An interactive shell can identify the console by changing the outer terminal's colors and title
for the duration of the user handoff:

```yaml
- id: production_console
  type: shell
  with:
    command: ssh
    args: [production.example.com]
    tty: true
    terminal:
      background: "rgb(30, 30, 46)"
      foreground: white
      title: Production console
```

`terminal` accepts optional `background`, `foreground`, and `title` fields, and at least one must
be set. Colors accept `#RGB`, `#RRGGBB`, and decimal `rgb(r, g, b)` values from 0 through 255.
Names are case-insensitive and include `black`, `silver`, `gray`/`grey`, `white`, `maroon`, `red`,
`purple`, `fuchsia`/`magenta`, `green`, `lime`, `olive`, `yellow`, `navy`, `blue`, `teal`,
`aqua`/`cyan`, and `orange`. Normal Wuko templates are rendered before validation, so appearance
can depend on workflow values. Titles cannot contain terminal control characters.

The appearance is applied immediately before Wuko hands input to the child PTY. With scripted
interactions, this is after the last interaction succeeds and requires `interact: true`. When the
child exits, fails, or is canceled, Wuko resets configured colors to the terminal defaults and
restores the saved window title. Appearance control uses xterm-compatible OSC and CSI sequences;
terminals that do not support them may ignore them. Styling is best effort and never changes the
workflow result. Redirected or non-terminal output is left untouched.

If an expectation is not found, its send and every later interaction are skipped. The step fails
when that interaction's timeout expires, terminates the child process group, and does not hand the
PTY to the user. If the child exits first, failure is immediate. Interaction failures are
operational errors: `allowed_exit_codes` cannot accept them, while a step retry starts the entire
sequence again with fresh state. Step cancellation and outer deadlines take precedence.

| Symptom | Result |
| --- | --- |
| Regex never matches | Fail at that interaction's timeout; do not send or hand off. |
| Child exits before a match | Fail immediately with the interaction index and regex. |
| Rendered regex is invalid | Fail while building the rendered shell runner. |
| More than 1 MiB arrives before a match | Fail with an unmatched-output overflow. |
| `timeout` is used without `expect` | Reject the workflow configuration. |
| `interact: true` has no interactive terminal | Fail before starting the command. |
| Executor cannot create a PTY | Reject TTY execution in that executor. |

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

Use an `expr` binding when an argument must keep its runtime type instead of being rendered to a
string:

```yaml
- id: inspect
  type: lua
  with:
    source: |
      for _, deployment in ipairs(wuko.args.inventory) do
        print(deployment.metadata.name)
      end
    args:
      inventory:
        expr: steps.decode_deployments.value
```

Argument expressions use the normal `inputs`, `vars`, `env`, `steps`, `dependencies`, `batch`,
`foreach`, `matrix`, `finally`, `workflow`, and `run` Expr roots.

Use the host API for richer automation:

```lua
local response = wuko.http.request({method = "GET", url = wuko.args.url, timeout = 10})
wuko.fs.write("response.json", response.body)
local result = wuko.exec.run({command = "git", args = {"status", "--short"}})
wuko.output("clean", result.stdout == "")
```

`wuko.exec.run` accepts the same `stdout`, `stderr`, and `capture_limit` options as a shell step:

```lua
local result = wuko.exec.run({
  command = "kubectl",
  args = {"get", "deployments", "--all-namespaces", "-o", "json"},
  stdout = "capture",
  stderr = "inherit",
  capture_limit = "16MiB"
})
local deployments = wuko.json.decode(result.stdout)
```

The result contains `stdout`, `stderr`, `exit_code`, `error`, `stdout_truncated`, and
`stderr_truncated`. The policies and per-stream truncation behavior match `shell`; omitted policies
default independently to `tee`.

The trusted `wuko` API exposes `args`, variables, outputs, environment, JSON, shared helpers,
key-value stores, HTTP, filesystem operations, and direct command execution. It also exposes
snapshot tables for `wuko.inputs`, `wuko.steps`, `wuko.dependencies`, `wuko.workflow`, and
`wuko.run`. Changing these Lua tables does not change workflow state. Outputs may be nil, booleans,
strings, numbers, arrays, or string-keyed objects; cyclic and mixed-key tables are rejected.

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
