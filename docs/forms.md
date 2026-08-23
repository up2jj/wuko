# Browser forms

[Back to the README](../README.md)

An optional `form` lets `wuko ui` collect workflow variables in a local browser. The form is an
adapter around one normal workflow run: standard `wuko run`, validation, schedules, templates, and
the execution engine ignore it.

```yaml
version: 1
name: greet

vars:
  person: Ada
  enthusiastic: true

form:
  title: Greeting
  description: Choose how to greet someone
  fields:
    - variable: person
      label: Person
      type: string
      required: true
    - variable: enthusiastic
      label: Add enthusiasm
      type: boolean
  result:
    success:
      title: Greeting complete
      template: '<p>{{ .outputs.message }}</p>'

steps:
  - return:
      outputs:
        message: 'vars.person + (vars.enthusiastic ? "!" : ".")'
```

Run it with `wuko ui greet`, `wuko ui --file ./greet.yaml`, or press `u` on it in the interactive
`wuko` picker. Wuko listens on a random `127.0.0.1` port and opens the default browser. Use
`--no-open` to print the URL without opening it.

## Fields

Every field binds to an existing top-level variable. Initial values use the normal precedence
`vars`, then `--var-file`, then `--var`; a submitted value has final precedence for the UI run.

Supported types are `string`, `boolean`, `number`, `array`, and `object`. Arrays and objects without
choices are entered as JSON. Strings may declare `min_length`, `max_length`, `pattern`, and
`validation_message`. Set `secret: true` on a string to render a password control. Existing secret
values are never sent to the browser; leaving the field blank retains a supplied value.

Static choices are declared inline:

```yaml
- variable: environment
  label: Environment
  type: string
  choices:
    - {label: Staging, value: staging}
    - {label: Production, value: production}
```

Choices may instead come from effective variables or form data:

```yaml
from: vars.available_regions
```

```yaml
from: data.pods
label_field: name
value_field: uid
description_field: status
disabled_field: unavailable
reason_field: reason
```

Scalar source items serve as both label and value. Object mappings default to `label` and `value`.
Choices are frozen before display and submissions are checked against that snapshot. Sources rooted
at `steps`, templates, expressions, environment variables, commands, or URLs are not accepted.

## Dynamic form data

Use `form.load` to fetch data once before fields appear. Its steps run as an isolated,
non-interactive workflow. Only declared outputs cross into the form's `data` root:

```yaml
vars:
  namespace: default
  pod: null

form:
  title: Select a pod
  load:
    steps:
      - id: pods
        type: shell
        with:
          command: kubectl
          args: [get, pods, --namespace, '{{ .vars.namespace }}', --output, json]
      - id: names
        type: lua
        with:
          source: |
            local response = wuko.json.decode(wuko.args.payload)
            local names = {}
            for _, item in ipairs(response.items) do
              table.insert(names, item.metadata.name)
            end
            wuko.output("values", names)
          args:
            payload: '{{ .steps.pods.stdout }}'
    outputs:
      pods: steps.names.values
  fields:
    - variable: pod
      label: Pod
      type: string
      required: true
      from: data.pods
```

Load steps receive initial vars, environment, run directory, executors, templates, and normal
diagnostics. They cannot contain a `return`; `load.outputs` is the explicit boundary. Variables and
step outputs created while loading do not enter the main workflow. A loading error displays an error
page and prevents the main run.

## Progress and results

The browser receives structured workflow and step progress in real time using server-sent events.
Terminal reporters continue normally. Browser events intentionally exclude stdout, stderr,
variables, environment values, diagnostics, and raw errors.

Success and failure templates use Go `html/template`; dynamic values are escaped. They can access
only `.workflow.name`, `.workflow.description`, `.status`, `.outputs`, `.stats`, `.duration`, and
`.error`. `.outputs` contains explicit workflow outputs, making `return` or declared workflow
outputs the presentation boundary.

The server accepts one valid submission, displays the final page, then shuts down. Workflow errors
still produce a non-zero command exit. UI invocations run scheduled workflows once.
