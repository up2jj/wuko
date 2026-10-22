# Script SDK

Set `shell.with.sdk: node` to inject `globalThis.wuko` into Node.js scripts, or
[`sdk: bash`](#bash-sdk) to inject Bash functions. Wuko bundles the adapters in its binary
and supplies them offline. The selected runtime must already be available; no SDK package,
global module, or project modification is needed.

## Node SDK

Node.js 22+ must already be available; no npm installation is needed.

```yaml
- id: metadata
  type: shell
  with:
    sdk: node
    command: node
    args: [scripts/metadata.mjs]
    sdk_args:
      name: "{{ .vars.name }}"
      packages:
        expr: vars.packages
```

```javascript
// scripts/metadata.mjs
const label = await wuko.helpers.upper(wuko.args.name);
console.log(`Preparing ${wuko.args.packages.length} packages`);
await wuko.output('label', label);
await wuko.output('packages', wuko.args.packages);
await wuko.setVar('release_label', label);
```

Read outputs through `steps.metadata.data.label` and variables through `vars.release_label`.
The usual shell outputs `stdout`, `stderr`, `exit_code`, `stdout_truncated`, and
`stderr_truncated` remain available. Console output and process stdin are separate from the bridge.
Normal capture policies, exit-code policies, TTY operation, cancellation, and retries still apply.

`sdk_args` supports ordinary values and templates. A top-level object containing only `expr`
evaluates an Expr expression and preserves its type. These references are checked during workflow
validation. The shell's existing `args` remain command-line arguments.

The preload works with CommonJS, ESM, inline Node code, and inherited Node subprocesses. Existing
`NODE_OPTIONS` are preserved; Wuko's preload runs first. `script`, `command`, and `argv.expr`
entry points all support injection. For example:

```yaml
- id: inspect
  type: shell
  with:
    sdk: node
    script: |
      node --input-type=module <<'JS'
      await wuko.output('platform', process.platform);
      JS
```

Shell configuration is still templated, including inline source and `node -e` arguments.
Use a script file when the JavaScript itself contains literal Go template delimiters,
such as templates passed to `wuko.render`. Script files are loaded normally by Node and are
not templated by Wuko. Relative file arguments resolve from the step's working directory.

### Node API

All methods return Promises; await them and handle errors normally. Data properties are deeply
frozen snapshots from the start of the shell attempt:

| Property | Contents |
| --- | --- |
| `args` | Resolved `sdk_args` |
| `vars`, `inputs`, `steps`, `dependencies` | Workflow state |
| `env` | Effective shell environment before bridge injection |
| `providers`, `bindings` | Provider values and active control bindings |
| `workflow` | `name`, `dir`, `timezone` |
| `run` | `dir`, `environment_loaders`, `os`, `arch` |
| `attempt` | `number`, `max_attempts`, `operation_id` |

| Method | Behavior |
| --- | --- |
| `output(name, value)` | Stage a value under this shell step's `data` outputs |
| `setVar(name, value)` | Stage a workflow variable |
| `helpers[name](...args)` | Call a shared template helper or workflow plugin helper |
| `secret(reference)` | Resolve a reference using the current workflow's secret session |
| `render(template, extra?)` | Render with workflow templates and optional extra roots |
| `step(type, config?)` | Execute one isolated, noninteractive step and return its outputs |
| `runWorkflow(name, {target?, vars?}?)` | Invoke a discovered workflow and return its outputs |

Helper names and argument order follow [shared template helpers](template-functions.md), for example
`await wuko.helpers.upper('hello')`. Resolve secrets with `wuko.secret`, rather than a helper call.

```javascript
const result = await wuko.step('shell', {
  command: 'git', args: ['rev-parse', '--short', 'HEAD'], stdout: 'capture',
});
const build = await wuko.runWorkflow('build', {
  vars: { revision: result.stdout.trim() },
});
await wuko.output('build', build);
```

Step configurations follow normal Wuko template and expression semantics and are validated when
called. Child calls use the parent state captured at the start of the shell step and own their
cleanup lifecycles. They do not publish child variables or steps into the parent. Pass computed
values explicitly through configuration or workflow variables. `setVar` does not modify the frozen
snapshot or the state seen by child calls.

## Bash SDK

Set `sdk: bash` on a local shell step. Inline `script` defaults to Bash with this SDK;
`shell`, `command`, and `argv.expr` may also select an explicit Bash executable. Bash 3.2+
must already be available. No curl, jq, Python, Node, or separately installed SDK is required:
every function calls a private client command inside the running Wuko executable using its
absolute path. JSON encoding, decoding, authentication, and socket communication run in Go.

```yaml
- id: metadata
  type: shell
  with:
    sdk: bash
    sdk_args:
      name: release
      packages:
        expr: vars.packages
    script: |
      set -euo pipefail
      name=$(wuko_get args.name)
      label=$(wuko_helper upper "$name")
      packages=$(wuko_get_json args.packages)
      wuko_output label "$label"
      wuko_output_json packages "$packages"
      wuko_set_var release_label "$label"
```

For a file, use `command: bash` and `args: [scripts/metadata.bash]`. Script files are not
templated. Inline source and arguments follow the usual Wuko template rules.

| Function | Arguments and behavior |
| --- | --- |
| `wuko_get [DOT_PATH]` | Read the initial context, e.g. `args.name`, `vars.items.0`, `attempt.number`; omit path for the whole snapshot |
| `wuko_get_json [DOT_PATH]` | Same lookup, always return JSON |
| `wuko_output NAME STRING` | Stage a string under `steps.<id>.data` |
| `wuko_output_json NAME JSON` | Stage a JSON value, including booleans, numbers, arrays, objects, or null |
| `wuko_set_var NAME STRING` | Stage a string workflow variable |
| `wuko_set_var_json NAME JSON` | Stage a typed workflow variable |
| `wuko_helper NAME [STRING ...]` | Call a helper with string arguments |
| `wuko_helper_json NAME JSON_ARRAY` | Call a helper with typed arguments; return JSON |
| `wuko_secret REFERENCE` | Resolve a secret string |
| `wuko_render TEMPLATE [EXTRA_JSON_OBJECT]` | Render a template with optional extra roots |
| `wuko_step TYPE [CONFIG_JSON_OBJECT]` | Execute an isolated step; return its outputs as JSON |
| `wuko_run_workflow NAME [VARS_JSON_OBJECT [TARGET]]` | Execute a child workflow; return its outputs as JSON |
| `wuko_call METHOD PARAMS_JSON_OBJECT` | Call the underlying bridge protocol; return JSON |

Functions complete synchronously and return a nonzero exit status on transport, host, or argument
errors, writing diagnostics to stderr. Output and variable functions produce no stdout. Other
functions print strings as text and other values as JSON, followed by a newline; JSON variants
always print JSON. Use `wuko_get_json` for keys containing dots by retrieving their enclosing
object. Context snapshots, child isolation, staging, and retries have the same semantics as Node.

```bash
set -euo pipefail
repeated=$(wuko_helper_json repeat '[3,"x"]')
revision=$(wuko_step shell '{"command":"git","args":["rev-parse","HEAD"],"stdout":"capture"}')
build=$(wuko_run_workflow build '{"revision":"abc"}' release)
wuko_output_json repeated "$repeated"
wuko_output_json revision "$revision"
wuko_output_json build "$build"
```

Assign results separately as shown so `set -e` can observe call failures. Bash can hide the
exit status of a failing command substitution when it is nested in another successful command.

The bootstrap uses `BASH_ENV`. An existing `BASH_ENV` file is sourced after SDK functions are
defined; its value is treated as a literal file path. Inherited, noninteractive Bash processes
load the same functions and share the attempt's bridge. Run Bash directly: `sh`, interactive
Bash, privileged mode, and POSIX mode do not support this injection. Clearing `BASH_ENV` or the
SDK environment removes injection. All calls must finish before the owning step exits; wait for
background jobs explicitly.

Shell variables hold strings. Quote expansions, use JSON variants for structured values, and
encode binary data as text. Command substitution strips trailing newlines; redirect a function's
stdout to a file when exact trailing newlines matter (the function adds one output newline).
Each SDK call starts a Wuko process and rereads its descriptor, so very tight loops have more
overhead than Node's in-process client. Calls preserve script stdin. The helper functions also
work with an empty `PATH` once Bash has started. Arguments are subject to the operating system's
process argument size limits, which may be substantially smaller than the bridge's 10 MiB limit.

[Run the Bash example](../examples/bash-sdk.yaml) with `wuko run --file examples/bash-sdk.yaml`.

## Limits and dependencies

- Injection is supported for local shell steps; executor scopes, `user`, and `elevated` are rejected.
- Run scripts through Wuko to receive the SDK. Launchers that replace `NODE_OPTIONS` or clear the
  environment can remove injection. Node subprocesses share the owning attempt's bridge and staged
  writes; detached processes cannot use it after that attempt ends.
- Dependencies resolve normally from the project and runtime environment. Prepare dependencies from
  lockfiles in a preceding step or use an existing package-manager runner. Wuko does not install
  project dependencies, edit lockfiles, or download a runtime. Offline execution also requires those
  dependencies to be available locally.
- Values must be JSON-compatible: null, booleans, strings, finite numbers, arrays, and plain objects.
  The Node client rejects cycles, undefined values, BigInt, custom objects, and unsafe integers.
  Encode binary data and large integers as strings. Each bridge message is limited to 10 MiB;
  at most 64 host calls run concurrently per attempt; excess calls wait for a free slot.
- Repeated writes replace the previous value of the same key; concurrent writes have no ordering
  guarantee. Outputs and variables follow the engine's normal commit policy, so failed attempts do
  not publish writes to workflow state. External side effects are not rolled back and can repeat
  on retry. A nonzero exit accepted by `allowed_exit_codes` counts as success.
- Dynamic calls cannot be fully validated before the script runs. The SDK does not sandbox trusted
  scripts, and it exposes the capabilities and secret access of the owning workflow.

[Run the example](../examples/node-sdk.yaml) with `wuko run --file examples/node-sdk.yaml`.
JavaScript editors can use the supplied [TypeScript declarations](../scriptsdk/node.d.ts).
The [bridge protocol](script-sdk-protocol.md) is language-independent, allowing a future Python
adapter to share the same host implementation.
