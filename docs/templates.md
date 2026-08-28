# Templates

Wuko renders strings with Go's standard [`text/template`](https://pkg.go.dev/text/template)
package. Templates are strict: referring to a missing map key fails with an error instead of
silently producing an empty value.

## Template data

Every rendered string can access these roots:

| Root | Contents |
| --- | --- |
| `.vars` | Workflow variables, including values written by earlier sequential steps. |
| `.env` | The effective workflow environment. |
| `.steps` | Outputs committed by earlier sequential steps. |
| `.workflow.name` | The owning workflow or action name. |
| `.workflow.dir` | The directory containing the owning workflow or materialized action. |
| `.run.dir` | The active run directory: initially where Wuko was invoked, or the directory established by an enclosing `working_directory` or `worktree` block. |
| `.inputs` | Composite-action inputs; empty in a top-level workflow. |
| `.batch` | Active zero-based batch index and current item chunk; available only inside a batch body. |
| `.foreach` | Active foreach item and zero-based index; available only inside a foreach body. |
| `.matrix` | Active named axis values; available only inside a matrix body. |

Strings can use standard template actions and built-in functions such as `if`, `range`, `with`,
`index`, `len`, and `printf`:

```yaml
with:
  args:
    - "{{ .vars.application }}"
    - "{{ index .vars.regions 0 }}"
```

Wuko also provides deterministic string, defaulting, collection, indentation, JSON, and YAML
helpers. See [Template, Expr, and Lua functions](template-functions.md) for the complete reference
and equivalent syntax in each language.

Lua `source` is deliberately not rendered. Pass dynamic values through the Lua step's typed
`args` instead.

## Named templates

Declare reusable templates at the top level of a workflow:

```yaml
version: 1
name: deploy

vars:
  registry: ghcr.io/acme
  application: payments
  version: v1.8.0

templates:
  image: '{{ .vars.registry }}/{{ .vars.application }}:{{ .vars.version }}'
  message: 'Deploying {{ template "image" . }}'

steps:
  - id: deploy
    type: shell
    with:
      command: deploy
      args:
        - '{{ template "message" . }}'
```

Invoke a named template with `{{ template "name" . }}`. Passing `.` gives the named template the
same data roots as the string invoking it. Named templates may invoke other declared templates.
Names must start with a letter or underscore and contain only letters, digits, and underscores.

Wuko validates static data references before constructing or running any step. Variable names must
be declared under `vars`, supplied by the invocation, or written by an earlier step that names the
variable it assigns; `lua` and `import_vars` name their variables only at run time, so they end
variable checking for the steps after them. Step IDs must be visible at the point where the
template is used. Environment names are not checked, because the effective environment inherits the
host process environment and would make the same workflow validate on one machine and fail on
another. Constant-key forms such as `{{ index .vars "application" }}` are checked too; genuinely
dynamic keys remain allowed, as does the key a `hasKey` presence test asks about.

Named templates are checked both as declarations and in the scope of each invocation. Output
fields below a visible step remain open because step output shapes are runtime-defined. A
reference to an earlier step is therefore valid even when that step has an `if`; if the step is
skipped, strict rendering still reports the missing runtime value when the consumer starts.

## File-backed templates

A named template can load its body from a file:

```yaml
templates:
  deployment:
    file: templates/deployment.yaml.tmpl

steps:
  - id: write_deployment
    type: file
    with:
      operation: write
      path: deployment.yaml
      overwrite: true
      content: '{{ template "deployment" . }}'
```

`templates/deployment.yaml.tmpl` contains the body directly:

```gotemplate
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .vars.application }}
spec:
  replicas: {{ .vars.replicas }}
  template:
    spec:
      containers:
        - name: {{ .vars.application }}
          image: {{ template "image" . }}
```

Do not wrap the file in `{{ define }}`. Each entry in `templates` already supplies the template
name, and nested definitions are rejected to keep the namespace explicit.

Template file paths must be relative. Local workflow paths resolve from the directory containing
the workflow file. A template file is read once while the workflow loads and is limited to 1 MiB;
editing it during a run does not change that run.

## Remote workflows and actions

Remote workflow archives may include template files alongside their root `wuko.yaml` or
`wuko.yml`. Paths cannot escape the extracted archive:

```text
wuko.yaml
templates/
  deployment.yaml.tmpl
```

A workflow fetched as a single YAML document has no companion files. Publish a ZIP or gzip tar
archive when it uses file-backed templates.

Composite actions may declare their own inline and file-backed templates. A local action reads
template files from its manifest directory and keeps them within that action root. A remote or
command-fetched action must include file-backed templates in an archive alongside `action.yml` or
`action.yaml`; a standalone fetched manifest cannot carry companion template files.

Caller and action template namespaces are isolated. Templates used in the caller's `with`
bindings come from the caller workflow. Templates used by the action's internal steps come from
the action manifest:

```yaml
# action.yml
version: 1
name: package

inputs:
  target:
    type: string
    required: true

templates:
  command:
    file: templates/package.sh.tmpl

steps:
  - id: package
    type: shell
    with:
      script: '{{ template "command" . }}'
```

Inside the action template, the bound value is available as `.inputs.target`.

## Typed action inputs

Go templates always render strings. They work directly for string inputs:

```yaml
with:
  target: '{{ template "target" . }}'
```

Use `expr` when a dynamic input must remain a boolean, number, array, or object:

```yaml
with:
  enabled:
    expr: vars.enabled
  targets:
    expr: steps.configuration.value.targets
```

Expr roots omit the leading dot: `vars`, `env`, `steps`, `workflow`, `run`, and `inputs`.

## Execution order

Named templates are parsed when the workflow or action loads, but each consuming string is
executed at its normal lifecycle point. A sequential step can therefore use an earlier result:

```yaml
templates:
  artifact: '{{ .steps.build.stdout }}'

steps:
  - id: build
    type: shell
    with:
      command: ./build

  - id: publish
    type: shell
    with:
      args: ['{{ template "artifact" . }}']
```

Concurrent children share the state snapshot taken before their group starts and cannot consume
one another's results. Batch, foreach, and matrix iterations also use isolated snapshots; see
[Workflow controls](workflow-control.md). Action sources are rendered before execution and
therefore cannot depend on `.steps`, `.batch`, `.foreach`, or `.matrix`.

## Safety

Templates do not provide shell or HTML escaping automatically. Use the documented JSON or YAML
serialization helpers where appropriate, quote values for their destination format, and avoid
treating untrusted values as executable shell source. Wuko does not add filesystem,
command-execution, network, clock, random, or environment-lookup functions; templates receive
only the documented data roots and [side-effect-free helpers](template-functions.md).
