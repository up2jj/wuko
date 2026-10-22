# Script SDK

Set `shell.with.sdk: node` to inject `globalThis.wuko` into Node.js scripts. Wuko bundles
the SDK in its binary and supplies it offline. Node.js 22+ must already be available;
no SDK package, npm install, global module, or project modification is needed.

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

## API

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
- Dynamic calls cannot be fully validated before JavaScript runs. The SDK does not sandbox trusted
  scripts, and it exposes the capabilities and secret access of the owning workflow.

[Run the example](../examples/node-sdk.yaml) with `wuko run --file examples/node-sdk.yaml`.
JavaScript editors can use the supplied [TypeScript declarations](../scriptsdk/node.d.ts).
The [bridge protocol](script-sdk-protocol.md) is language-independent, allowing a future Python
adapter to share the same host implementation.
