# Demo gallery

[Back to the README](../README.md)

Every recording below is generated with [VHS](https://github.com/charmbracelet/vhs) from an offline
workflow in [`docs/demos`](demos). Expand a demo to see its complete YAML, copy it into
`.wuko/workflows/`, and run it with `wuko run NAME`. See
[Screenshot and demo maintenance](screenshots.md) to re-record or add a demo.

- [Workflow picker](#workflow-picker)
- [Guided release](#guided-release)
- [Validated prompts](#validated-prompts)
- [Validated form](#validated-form)
- [Pick files](#pick-files)
- [Browse data as a table](#browse-data-as-a-table)
- [Live dashboard](#live-dashboard)
- [Parallel dependency graph](#parallel-dependency-graph)
- [Matrix fan-out](#matrix-fan-out)
- [Retries with backoff](#retries-with-backoff)
- [Recover and clean up](#recover-and-clean-up)
- [Typed data pipeline](#typed-data-pipeline)
- [Validation before execution](#validation-before-execution)
- [Conventional commit](#conventional-commit)

## Workflow picker

Run bare `wuko` to search every discovered workflow. Enter runs the selection; `a` opens an action palette to validate, show the tree, dry-run, edit, or pin it.

![Workflow picker](assets/workflow-picker.gif)

Learn more: [Workflow discovery](workflow-discovery.md).

## Guided release

A text input with validation, a choice, and a colored diff review collect everything a release needs, then the run continues with the answers. A prerequisite workflow runs first.

![Guided release](assets/interactive-workflow.gif)

<details>
<summary>Workflow: <code>docs/demos/fixtures/.wuko/workflows/release-demo.yaml</code></summary>

```yaml
version: 1
name: release-demo
description: Prepare and approve a local release

depends_on:
  preparation: prepare-demo

steps:
  - id: release_name
    type: tui_input
    with:
      variable: release_name
      message: Enter the release name
      validation:
        pattern: '^[a-z][a-z0-9-]+$'
        message: Use lowercase letters, numbers, and hyphens

  - id: environment
    type: tui_choice
    with:
      variable: environment
      message: Select the deployment environment
      choices:
        - label: Staging
          description: Shared verification environment
          value: staging
          default: true
        - label: Production
          description: Customer-facing environment
          value: production

  - id: review
    type: tui_review
    with:
      variable: approved
      message: Review the release plan
      format: diff
      default: false
      content: |
        diff --git a/release-plan b/release-plan
        --- a/release-plan
        +++ b/release-plan
        @@ release plan @@
        - environment: pending
        + environment: selected above
        + release: approved after review

  - id: publish
    type: shell
    with:
      command: printf
      args:
        - 'Release %s approved for %s\n'
        - '{{ .vars.release_name }}'
        - '{{ .vars.environment }}'

  - return:
      outputs:
        status: '"approved"'
```

</details>

Learn more: [Interactive steps](steps-interactive.md).

## Validated prompts

Input is validated as you type, passwords are masked, and the deploy step runs only after confirmation. Pass `--var service=... --var token=... --var approved=true` to run it unattended.

![Validated prompts](assets/gallery/prompts.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/prompts.yaml</code></summary>

```yaml
version: 1
name: prompts
description: Collect validated input, a secret, and a confirmation

steps:
  - id: service
    type: tui_input
    with:
      variable: service
      message: Service name
      validation:
        pattern: '^[a-z][a-z0-9-]+$'
        message: Use lowercase letters, numbers, and hyphens

  - id: token
    type: tui_password
    with:
      variable: token
      message: Deploy token

  - id: confirm
    type: tui_confirm
    with:
      variable: approved
      message: Deploy {{ .vars.service }} now?
      default: false

  - id: deploy
    type: shell
    if: vars.approved
    with:
      command: printf
      args: ['Deploying %s with a %d-character token\n', '{{ .vars.service }}', '{{ len .vars.token }}']
```

</details>

Learn more: [Interactive steps](steps-interactive.md).

## Validated form

`tui_form` collects every field on one screen. Errors appear as you move between fields, a rule can depend on another field, and each value becomes a typed step output.

![Validated form](assets/gallery/form.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/form.yaml</code></summary>

```yaml
version: 1
name: form
description: Collect deployment settings in one validated form

steps:
  - id: settings
    type: tui_form
    with:
      title: Deploy a service
      description: Every field is checked before the workflow continues
      fields:
        - id: service
          type: text
          label: Service name
          required: true
          validation:
            pattern: '^[a-z][a-z0-9-]+$'
            message: Use lowercase letters, numbers, and hyphens
        - id: environment
          type: select
          label: Environment
          default: staging
          required: true
          choices:
            - {label: Staging, value: staging}
            - {label: Production, value: production}
        - id: regions
          type: multiselect
          label: Regions
          default: [west]
          required: true
          choices:
            - {label: West, value: west}
            - {label: East, value: east}
        - id: replicas
          type: number
          label: Replicas
          default: 1
          validation:
            min: 1
            max: 20
            integer: true
            expr: fields.environment != "production" || value >= 2
            message: Production needs at least two replicas
        - id: notify
          type: checkbox
          label: Notify the team
          default: true

  - id: deploy
    type: shell
    with:
      command: printf
      args:
        - 'Deploying %s to %s (%s) with %s replicas\n'
        - '{{ .steps.settings.service }}'
        - '{{ .steps.settings.environment }}'
        - '{{ .steps.settings.regions | join ", " }}'
        - '{{ .steps.settings.replicas }}'
```

</details>

Learn more: [`tui_form`](steps-interactive.md#tui_form).

## Pick files

`tui_path` browses a rooted directory with multi-select. `foreach` then copies each selection with the portable `file` step.

![Pick files](assets/gallery/pick-files.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/pick-files.yaml</code></summary>

```yaml
version: 1
name: pick-files
description: Pick files interactively and back them up

steps:
  - id: sources
    type: tui_path
    with:
      variable: sources
      message: Select files to back up
      root: project
      multiple: true

  - id: backup_dir
    type: file
    with: {operation: mkdir, path: backup, recursive: true}

  - id: backup
    foreach:
      items: vars.sources
      steps:
        - id: copy
          type: file
          with:
            operation: copy
            path: project/{{ .foreach.item }}
            destination: backup/{{ .foreach.item }}

  - id: listing
    type: shell
    with: {command: ls, args: [-1, backup]}
```

</details>

Learn more: [`tui_path`](steps-interactive.md#tui_path), [`foreach`](workflow-control.md#foreach), [Filesystem operations](filesystem-operations.md).

## Browse data as a table

JSON is decoded into typed data, filtered with `transform`, and shown in a paginated table.

![Browse data as a table](assets/gallery/table.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/table.yaml</code></summary>

```yaml
version: 1
name: table
description: Decode JSON, filter it, and browse it as a table

steps:
  - id: releases
    type: decode
    with:
      format: json
      path: releases.json

  - id: stable
    type: transform
    with:
      from: steps.releases.value
      operations:
        - filter: item.channel != "nightly"

  - id: browse
    type: tui_table
    with:
      message: Stable releases
      from: steps.stable.value
      columns:
        - {header: Version, field: version}
        - {header: Channel, field: channel}
        - {header: Downloads, field: downloads}
        - {header: Notes, field: notes}
```

</details>

Learn more: [`tui_table`](steps-interactive.md#tui_table), [`decode`](steps-data.md#decode), [`transform`](steps-data.md#transform).

## Live dashboard

A dashboard refreshes its widgets from ordinary workflow steps every second. The sparkline fills its widget, and widget actions run with a confirmation.

![Live dashboard](assets/gallery/dashboard.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/dashboard.yaml</code></summary>

```yaml
version: 1
name: dashboard
description: Monitor live data with refreshing widgets

steps:
  - id: monitor
    type: dashboard
    with:
      title: Release health
      groups:
        - id: service
          title: Service
          columns: 2
          refresh:
            every: 1s
            steps:
              - id: clock
                type: shell
                with: {script: date +%s}
              - id: sample
                type: lua
                with:
                  args: {t: "{{ .steps.clock.stdout }}"}
                  source: |
                    local t = tonumber(wuko.args.t)
                    local samples = {}
                    for i = 1, 40 do samples[i] = 50 + math.floor(40 * math.sin((t + i) / 3)) end
                    wuko.output("samples", samples)
                    wuko.output("latency", samples[40])
            outputs:
              samples: steps.sample.samples
              latency: steps.sample.latency
          widgets:
            - id: latency
              title: Latency (ms)
              template: |-
                {{ .data.samples | sparkline .widget.width .widget.height }}
            - id: status
              title: Status
              template: |-
                Version   0.17.0
                Replicas  3/3 ready
                Latency   {{ .data.latency }} ms
                Last action: {{ (index .actions "restart").status }}
              actions:
                - id: restart
                  label: Restart a replica
                  confirm: Restart one replica?
                  steps:
                    - id: restart
                      type: shell
                      with: {script: sleep 1}
```

</details>

Learn more: [`dashboard`](steps-interactive.md#dashboard).

## Parallel dependency graph

`wuko tree` shows the graph, then `concurrent` runs independent checks in parallel as soon as their `needs` finish.

![Parallel dependency graph](assets/gallery/concurrent.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/concurrent.yaml</code></summary>

```yaml
version: 1
name: concurrent
description: Run a dependency graph of checks in parallel

steps:
  - concurrent:
      max_concurrency: 4
      steps:
        - id: deps
          type: shell
          with: {script: sleep 1 && echo modules ready}
        - id: lint
          needs: [deps]
          type: shell
          with: {script: sleep 2 && echo lint ok}
        - id: unit
          needs: [deps]
          type: shell
          with: {script: sleep 3 && echo 128 tests passed}
        - id: fuzz
          needs: [deps]
          type: shell
          with: {script: sleep 2.5 && echo no crashers}
        - id: build
          needs: [lint, unit, fuzz]
          type: shell
          with: {script: sleep 1 && echo dist/app built}
```

</details>

Learn more: [Concurrency](execution.md#concurrency).

## Matrix fan-out

A `matrix` expands every OS and architecture pair, runs three at a time, and collects the results in order for the next step.

![Matrix fan-out](assets/gallery/matrix.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/matrix.yaml</code></summary>

```yaml
version: 1
name: matrix
description: Fan out over every OS and architecture

steps:
  - id: builds
    matrix:
      axes:
        os: [linux, darwin, windows]
        arch: [amd64, arm64]
      max_concurrency: 3
      collect: trim(steps.build.stdout)
      steps:
        - id: build
          type: shell
          with:
            script: sleep 1 && echo dist/app-{{ .matrix.os }}-{{ .matrix.arch }}

  - id: summary
    type: shell
    with:
      script: |
        {{- range .steps.builds.results }}
        echo "built {{ . }}"
        {{- end }}
```

</details>

Learn more: [`matrix`](workflow-control.md#matrix).

## Retries with backoff

`attempt` retries a failing upload with exponential backoff and reports every attempt and the total retry wait.

![Retries with backoff](assets/gallery/retry.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/retry.yaml</code></summary>

```yaml
version: 1
name: retry
description: Retry a flaky step with backoff until it succeeds

steps:
  - id: counter
    type: temp
    with: {kind: file}

  - id: publish
    attempt:
      max_attempts: 4
      initial_delay: 500ms
      backoff_multiplier: 2
      steps:
        - id: upload
          type: shell
          with:
            script: |
              echo x >> '{{ .steps.counter.path }}'
              n=$(wc -l < '{{ .steps.counter.path }}' | tr -d ' ')
              if [ "$n" -lt 3 ]; then echo "upload attempt $n: 503 Service Unavailable" >&2; exit 1; fi
              echo "upload attempt $n: published"
```

</details>

Learn more: [Attempts](execution.md#attempts).

## Recover and clean up

A failed deploy inside `try` triggers the `catch` rollback, the workflow continues as recovered, and `finally` cleanup always runs.

![Recover and clean up](assets/gallery/recover.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/recover.yaml</code></summary>

```yaml
version: 1
name: recover
description: Roll back a failed deployment and always clean up

steps:
  - id: deployment
    try:
      steps:
        - id: migrate
          type: shell
          with: {script: sleep 0.6 && echo 'migrations applied'}
        - id: deploy
          type: shell
          with: {script: sleep 0.8 && echo 'health check failed' >&2 && exit 1}
    catch:
      steps:
        - id: rollback
          type: shell
          with:
            script: sleep 0.6 && echo 'rolling back after {{ .error.step }} failed'

  - id: report
    type: shell
    with:
      command: printf
      args: ['recovered: %s\n', '{{ .steps.deployment.recovered }}']

finally:
  - id: cleanup
    type: shell
    with: {script: echo 'removed temporary credentials'}
```

</details>

Learn more: [`try`/`catch`](workflow-control.md#try-and-catch), [Finally cleanup](finally.md).

## Typed data pipeline

Lists stay typed from step to step: `transform` filters and sorts, `jsonpath` selects one value, and `semver` bumps it.

![Typed data pipeline](assets/gallery/data.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/data.yaml</code></summary>

```yaml
version: 1
name: data
description: Query, reshape, and version typed data

vars:
  services:
    - {name: api, version: 1.4.2, healthy: true}
    - {name: worker, version: 2.0.0-rc.1, healthy: false}
    - {name: web, version: 0.9.7, healthy: true}

steps:
  - id: healthy
    type: transform
    with:
      from: vars.services
      operations:
        - filter: item.healthy
        - map: item.name
        - sort

  - id: api_version
    type: jsonpath
    with:
      from: vars.services
      query: $[?@.name == 'api'].version
      result: one

  - id: next
    type: semver
    with:
      operation: increment
      version: "{{ .steps.api_version.value }}"
      part: minor

  - id: report
    type: shell
    with:
      command: printf
      args:
        - 'healthy: %s\nnext api release: %s\n'
        - '{{ .steps.healthy.value | join ", " }}'
        - '{{ .steps.next.version }}'
```

</details>

Learn more: [Data steps](steps-data.md).

## Validation before execution

A typo in a variable name is caught before anything runs, with its location and a suggested fix. After the fix, `--dry-run` previews the plan.

![Validation before execution](assets/gallery/validate.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/broken/.wuko/workflows/deploy.yaml</code></summary>

```yaml
version: 1
name: deploy
description: Deploy the current build

vars:
  target: staging

steps:
  - id: build
    type: shell
    with: {script: echo dist/app}

  - id: deploy
    type: shell
    if: vars.targt == "production"
    with:
      command: echo
      args: [deploying, "{{ .steps.build.stdout }}"]
```

</details>

Learn more: [Validation diagnostics](validation.md).

## Conventional commit

Review the working-tree diff, choose a commit type, and type a subject. Wuko builds a Conventional Commit message and commits.

![Conventional commit](assets/gallery/commit.gif)

<details>
<summary>Workflow: <code>docs/demos/gallery/.wuko/workflows/commit.yaml</code></summary>

```yaml
version: 1
name: commit
description: Review changes and create a Conventional Commit

steps:
  - id: changes
    type: git_diff
    with: {source: unstaged}

  - id: patch
    type: shell
    with: {command: git, args: [diff, --no-color]}

  - id: review
    type: tui_review
    with:
      variable: approved
      message: "Review {{ .steps.changes.count }} changed file(s)"
      content: "{{ .steps.patch.stdout }}"
      format: diff
      default: true

  - id: kind
    type: tui_choice
    if: vars.approved
    with:
      variable: kind
      message: Commit type
      choices:
        - {label: feat, description: A new feature, value: feat}
        - {label: fix, description: A bug fix, value: fix}
        - {label: docs, description: Documentation only, value: docs}

  - id: subject
    type: tui_input
    if: vars.approved
    with:
      variable: subject
      message: Commit subject
      required: true

  - id: message
    type: git_conventional_commit
    if: vars.approved
    with:
      operation: create
      type: "{{ .vars.kind }}"
      scope: api
      subject: "{{ .vars.subject }}"

  - id: commit
    type: git_commit
    if: vars.approved
    with:
      message: "{{ .steps.message.message }}"
      paths: [.]

  - id: history
    type: shell
    with: {command: git, args: [log, --oneline, --no-decorate, -3]}
```

</details>

Learn more: [Git steps](steps-system.md#git_conventional_commit), [Git change recipes](git-changes.md).
