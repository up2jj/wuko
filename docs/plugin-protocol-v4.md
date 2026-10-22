# Plugin protocol v4

[Back to plugins](plugins.md)

`wuko.plugin/v4` keeps all [v3](plugin-protocol-v3.md) framing, lifecycle, services, callbacks,
helpers, and actions. It adds what an execution environment needs when it runs in a plugin
instead of in Wuko itself, such as a container or a project shell:

- an executor filesystem;
- streamed stdin;
- TTY runs with full terminal handling;
- steps that borrow the user's terminal;
- steps that run inside their own plugin's executors;
- output while an executor opens;
- the outputs of a failed step.

Each capability is declared during `initialize`. Wuko accepts the declarations only from a plugin
that negotiated v4.

The Go SDK answers v4, and also answers v3 when the plugin registers no v4 capability. A plugin
whose `plugin.json` pins `wuko.plugin/v3` therefore keeps working after it is rebuilt against a
newer SDK. Declaring a v4 capability requires `"protocol": "wuko.plugin/v4"` in the release
manifest.

## Contents

- [Declarations](#declarations)
- [Executor filesystem](#executor-filesystem)
- [Streamed input](#streamed-input)
- [TTY runs](#tty-runs)
- [Borrowing the terminal](#borrowing-the-terminal)
- [Steps inside their own plugin's executors](#steps-inside-their-own-plugins-executors)
- [Executor sessions](#executor-sessions)
- [Failed steps](#failed-steps)
- [Go SDK](#go-sdk)
- [Not carried](#not-carried)

## Declarations

| Declaration | Field | Effect |
| --- | --- | --- |
| executor | `filesystem: true` | Sessions serve `executor.fs.*`, so `file`, `edit`, and `markdown_edit` run inside the executor's blocks. |
| executor | `stdin: true` | `executor.run` reads stdin through `host.input.read` instead of receiving it whole. |
| executor | `tty: true` | `executor.run` accepts TTY commands, including `interactions` and user handoff. |
| step | `executor: true` | The step may appear inside executor blocks of the same plugin's executors. |
| step | `host_callbacks: ["host.terminal.acquire"]` | The step may borrow the user's terminal. |

```json
{"protocol":"wuko.plugin/v4","namespace":"acme",
 "executors":[{"type":"acme.box","cancel_stops_process":false,"filesystem":true,"stdin":true,"tty":true}],
 "steps":[{"type":"acme.task","executor":true},{"type":"acme.attach","host_callbacks":["host.terminal.acquire"]}]}
```

The declarations apply to the executor type, not to one configuration. A configuration whose
sessions cannot use a declared capability fails when a step uses it, rather than during
validation.

## Executor filesystem

Paths arrive already resolved against the host run directory, as a command's `dir` does. The
session translates them into its own filesystem.

```json
{"id":"7","method":"executor.fs.stat","params":{"session":"session-1","path":"/work/run/app.yaml"}}
{"id":"7","result":{"size":12,"mode":420,"mod_time":"2026-10-17T12:00:00.123456789Z"}}

{"id":"8","method":"executor.fs.read","params":{"session":"session-1","path":"/work/run/app.yaml","maximum":1048576}}
{"id":"8","result":{"data":"dmVyc2lvbjogMQo="}}

{"id":"9","method":"executor.fs.write","params":{"session":"session-1","path":"/work/run/app.yaml","data":"dmVyc2lvbjogMgo=","mode":420,"replace":true}}
{"id":"10","method":"executor.fs.mkdir","params":{"session":"session-1","path":"/work/run/out","mode":493}}
```

**Fields**
- `mode` carries the bits of Go's `fs.FileMode`, so a symbolic link reports `fs.ModeSymlink`.
- `data` is base64.

**Reads**
- A positive `maximum` lets a read stop after `maximum + 1` bytes. Wuko trims to that bound
  whatever the plugin returns.

**Writes**
- A write must install the file atomically: either the complete new file or the old one.
- With `replace: false`, a write must not overwrite an existing file.

**Errors**
- A missing path is answered with error code `not_found`.
- A write that refuses to overwrite is answered with code `exists`.
- Wuko maps the two to `fs.ErrNotExist` and `fs.ErrExist`.

**Limit**
- One transfer carries at most about 7.5 MiB, the 10 MiB frame cap less base64 overhead.
  Larger files fail with an error.

## Streamed input

An executor declared with `stdin: true` receives `"stdin_stream": true` instead of a `stdin`
blob. It is also told whether its stdin outlives the command:

```json
{"id":"11","method":"executor.run","params":{"session":"session-1","command":"psql","args":[],"stdin_stream":true,"stdin_outlives_process":false}}
```

**Reading input**
- The plugin reads input with the `host.input.read` callback, scoped to the run by `parent_id`
  like every host callback.
- Each answer carries one chunk of at most 32 KiB, or `eof` once the input has ended.
- Because the plugin asks for input, Wuko never sends more than the command consumes.
- `sensitive` marks input from a `sensitive` interaction. Suppressing its echo is optional,
  because Wuko redacts the value from captured output itself.

```json
{"id":"plugin-4","method":"host.input.read","params":{"parent_id":"11"}}
{"id":"plugin-4","result":{"data":"U0VMRUNUIDE7Cg==","sensitive":false}}
{"id":"plugin-5","method":"host.input.read","params":{"parent_id":"11"}}
{"id":"plugin-5","result":{"eof":true}}
```

**When stdin outlives the command**
- With `stdin_outlives_process: true`, as for a managed service fed from a pipe, the stdin
  stays open after the command.
- The plugin reports the command's exit without waiting for that stdin to end.

## TTY runs

An executor declared with `tty: true` receives `tty` with the terminal's starting size:

```json
{"id":"12","method":"executor.run","params":{"session":"session-1","command":"sh","args":["-c","read name"],"tty":{"rows":40,"cols":120},"stdout_policy":1,"stderr_policy":1,"capture_limit":0}}
{"method":"resize","params":{"id":"12","rows":50,"cols":160}}
```

The plugin allocates the terminal in its own environment and moves data in three ways:
- It streams the terminal's single output stream as `stdout` events.
- It reads keystrokes and scripted input through `host.input.read`.
- It applies each `resize` notification to the terminal.

Wuko keeps everything on the user's side, exactly as for a local PTY:
- the user's terminal, raw mode, and restoring it afterwards;
- `interactions` and the handoff after them;
- capture, the output policy, and the capture limit.

This is why Wuko asks the plugin to stream all output and does not ask it to capture. A
canceled run is asked to stop with the usual `cancel` notification.

## Borrowing the terminal

A step that declares `host.terminal.acquire` may take over the user's terminal while it runs, for
example to attach to a container. The step context's `interactive` field tells it in advance
whether that can work.

```json
{"id":"plugin-6","method":"host.terminal.acquire","params":{"parent_id":"13","raw":true}}
{"id":"plugin-6","result":{"rows":40,"cols":120}}
{"id":"plugin-7","method":"host.terminal.release","params":{"parent_id":"13"}}
```

**When it is lent**
- Wuko lends the terminal only to an interactive run whose stdin is a terminal.
- Anywhere else, for example in CI, the call fails with code `terminal_unavailable`, and the step
  can fall back to plain output.

**While it is held**
- Keystrokes arrive through `host.input.read`.
- Size changes arrive as `resize` notifications for the step's request.
- The step's `stdout` events already reach the terminal.
- `raw: true` passes every keystroke through unprocessed. Without it the terminal keeps line
  editing and echo.

**Release**
- Wuko restores the terminal when the plugin releases it or when the step ends, whichever comes
  first.

## Steps inside their own plugin's executors

A step declared with `executor: true` may run inside executor blocks. Inside a block opened by
the same plugin, its context names the session:

```json
{"id":"14","method":"step.run","params":{"type":"acme.task","with":{"name":"build"},"context":{"step_id":"build","executor_session":"session-1"}}}
```

The plugin resolves the session itself. It can therefore use capabilities that no protocol method
carries, such as a native task graph.

**Where it may run**
- Outside executor blocks, the field is absent.
- Inside another plugin's executor, Wuko refuses to run the step instead of running it on the
  host.
- The check happens when the step runs. Validation precedes the session.

## Executor sessions

An executor's `executor.open` may stream `stdout` and `stderr` events, which reach the workflow's
output. This is for progress such as building an environment or pulling an image. The open
context also carries `workflow_dir`.

The open result may set `cancel_stops_process` for one session. That overrides the executor's
declaration when the answer depends on configuration:

```json
{"id":"15","result":{"session":"session-2","cancel_stops_process":true}}
```

## Failed steps

A step that fails can still return what it produced, such as an exit code. Workflows read those
values as `error.outputs`, and in conditions such as `error.exit_code == 75`. Under v4 the error
object carries that result:

```json
{"id":"16","error":{"code":"step_failed","message":"container exited with status 75","result":{"outputs":{"exit_code":75,"stderr":"rate limit"}}}}
```

The SDK sends `result` only to a v4 host. Earlier hosts reject unknown fields in an error.

Under every protocol, numbers in a plugin's results and helper values become ordinary workflow
numbers. Integers stay exact as 64-bit integers, so expressions compare them as numbers.

## Go SDK

| Capability | SDK |
| --- | --- |
| Executor flags | `ExecutorHandler.FileSystem`, `.Stdin`, `.TTY` |
| Filesystem | sessions implement `sdk.ExecutorFileSystem` |
| Input | `Command.Input`, streamed for `Stdin`/`TTY` executors; `Command.Stdin` keeps the whole input otherwise |
| TTY | `Command.TTY` (starting size), `Command.Resize` |
| Stdin lifetime | `Command.StdinOutlivesProcess` |
| Open output | `ExecutorRequest.Stdout`, `.Stderr` |
| Per-session cancel policy | sessions implement `sdk.CancelPolicy` |
| Terminal | `sdk.WithHostCallbacks(sdk.HostTerminal)`, `Host.Terminal`, `sdk.ErrTerminalUnavailable`, `StepContext.Interactive` |
| Executor access | `sdk.WithExecutorAccess()`, `RunRequest.Executor` |
| Failed outputs | return the `Result` alongside the error |

`github.com/up2jj/wuko/plugin/stepadapter` serves code written for Wuko's own registries without
changes:
- `Register` serves a native `step.Runner`, with its output schema, cleanup, executor access,
  and, when its configuration asks for it, the borrowed terminal as a file-backed interactive
  `Stdin`.
- `RegisterExecutor` serves a native `executor.Provider`. Its sessions keep every capability they
  have. TTY runs are bridged to a local PTY through `process.RemoteTerminal`.

`github.com/up2jj/wuko/plugin/plugintest` runs such a plugin in process against the real host:

```go
manager := plugintest.Connect(t, plugin)
plugintest.Install(manager, steps, executors)
```

Wuko's own docker and devenv tests use these to run the built-in packages through the plugin
path.

## Not carried

- **Static step references:** a built-in step's `References` hook, which lets validation check
  `steps.*` lookups in its configuration, has no protocol equivalent.
- **Output schemas:** they are static per step type, not per configuration.
- **Service steps:** they cannot run inside executor blocks.
- **Typed exit errors:** a failed step's exit status crosses as its outputs and message, not as
  Go's `process.ExitError`.
