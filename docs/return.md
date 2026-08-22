# Early successful return

[Back to execution and composition](execution.md)

Use the anonymous `return` control to finish a workflow or composite action successfully before
the end of its main step list. A return has no step ID and publishes explicit outputs instead of
adding an entry beneath `steps`:

```yaml
- return:
    outputs:
      artifact: steps.package.path
      cached: steps.restore.hit
  if: steps.restore.hit
```

The optional `if` is a boolean Expr expression. Each output value is also an Expr expression, so
booleans, numbers, arrays, objects, and null remain typed. Quote Expr string literals inside YAML:

```yaml
- return:
    outputs:
      status: '"up-to-date"'
      successful: "true"
      exit_code: "0"
      files: '[steps.build.archive, steps.build.checksum]'
      metadata: '{"target": vars.target, "cached": steps.restore.hit}'
```

Output names must be Wuko identifiers and expressions must be non-empty strings. The complete map
is evaluated atomically against the state available at the return position. An evaluation error or
non-JSON/YAML-compatible value fails the run and publishes no return outputs.

## Cached result and successful no-op

Return a cached artifact without running the build or save steps:

```yaml
steps:
  - id: prepare_dist
    type: file
    with: {operation: mkdir, path: dist, recursive: true}

  - id: restore
    type: cache
    with:
      operation: restore
      cache_dir: .wuko/cache
      key_files: [go.mod, go.sum]
      paths: [dist]

  - return:
      outputs:
        artifact: '"dist/app.tar.gz"'
        cached: "true"
    if: steps.restore.hit

  - id: build
    type: shell
    with: {command: ./build}

  - id: save
    type: cache
    with:
      operation: save
      cache_dir: .wuko/cache
      key_files: [go.mod, go.sum]
      paths: [dist]

  - return:
      outputs:
        artifact: '"dist/app.tar.gz"'
        cached: "false"
```

An explicitly empty output map is valid when “nothing to do” is itself the successful result:

```yaml
- id: source_changed
  type: changed
  with:
    key: build-inputs
    root: .
    files: [go.mod, go.sum, "**/*.go"]

- return:
    outputs: {}
  if: "!steps.source_changed.changed"

- id: build
  type: shell
  with: {command: go, args: [build, ./...]}
```

Embedders receive top-level workflow values through `engine.State.Outputs`. The `wuko run` command
does not print them automatically.

## Sequential blocks

A return may appear directly in the main step list or inside transparent conditional and
working-directory blocks. It propagates through those wrappers and finishes the whole current
workflow or action:

```yaml
steps:
  - if: vars.use_existing
    steps:
      - working_directory: packages/api
        steps:
          - id: manifest
            type: file
            with: {operation: read, path: dist/manifest.json}

          - return:
              outputs:
                manifest: steps.manifest.content

  - id: build
    type: shell
    with: {command: ./build-api}
```

Steps following a triggered return do not run. Ordinary and concurrent leaves are reported
skipped; a later foreach or matrix parent is skipped without expanding its iterations. Previously
committed state remains available to cleanup.

## Concurrent, foreach, and matrix boundaries

`return` may follow a completed parallel control and consume its committed outputs:

```yaml
steps:
  - concurrent:
      steps:
        - id: frontend
          type: shell
          with: {command: ./build-frontend}
        - id: backend
          type: shell
          with: {command: ./build-backend}

  - return:
      outputs:
        frontend: steps.frontend.stdout
        backend: steps.backend.stdout
```

It can likewise return an ordered fan-out aggregate after every iteration succeeds:

```yaml
steps:
  - id: deployments
    foreach:
      items: vars.targets
      collect: '{"target": foreach.item, "stdout": steps.deploy.stdout}'
      max_concurrency: 4
      steps:
        - id: deploy
          type: shell
          with: {command: ./deploy, args: ["{{ .foreach.item }}"]}

  - return:
      outputs:
        count: steps.deployments.count
        deployments: steps.deployments.results
```

A return cannot appear inside `concurrent`, `foreach`, or `matrix`. Parallel branches or
iterations could otherwise race to publish conflicting results:

```yaml
# Invalid
- concurrent:
    steps:
      - return: {outputs: {result: '"frontend"'}}
      - {id: backend, type: shell}

# Invalid
- id: processing
  foreach:
    items: vars.items
    steps:
      - return: {outputs: {selected: foreach.item}}
```

A composite action invoked inside one of these controls remains its own boundary. An internal
return finishes only that action invocation and supplies its caller-step outputs; it does not
finish the surrounding iteration, control, or workflow.

## Composite actions

Every return in a composite action must provide exactly the keys declared by the action's
`outputs` contract. A triggered return supplies those values directly. If no return triggers, the
normal output expressions are evaluated as before:

```yaml
version: 1
name: resolve-artifact
inputs:
  target: {type: string, required: true}
outputs:
  path: {value: steps.build.stdout}
  cached: {value: "false"}
steps:
  - id: prepare_dist
    type: file
    with: {operation: mkdir, path: dist, recursive: true}

  - id: restore
    type: cache
    with:
      operation: restore
      cache_dir: .wuko/cache
      key_files: [go.mod, go.sum]
      paths: [dist]

  - return:
      outputs:
        path: '"dist/artifact.tar.gz"'
        cached: "true"
    if: steps.restore.hit

  - id: build
    type: shell
    with: {command: ./build, args: ["{{ .inputs.target }}"]}
```

The caller consumes either result through the same action step:

```yaml
- id: artifact
  uses: https://actions.example.test/resolve-artifact
  with: {target: linux}

- id: upload
  type: shell
  with: {command: ./upload, args: ["{{ .steps.artifact.path }}"]}
```

## Cleanup and status

An early return is successful, so workflow progress finishes with `succeeded`. The complete
`finally` list still runs with `finally.status == "succeeded"` and an empty `finally.errors` list:

```yaml
steps:
  - id: acquire
    type: shell
    with: {command: ./acquire-lock}

  - return:
      outputs: {result: '"already-complete"'}
    if: vars.already_complete

  - id: process
    type: shell
    with: {command: ./process}

finally:
  - id: release
    type: shell
    with: {command: ./release-lock}
```

A cleanup failure still fails the complete run. `return` itself is not allowed inside `finally`.
