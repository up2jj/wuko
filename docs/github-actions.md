# GitHub Actions

[Back to the README](../README.md)

## Run a workflow

Use the repository's composite action to install a pinned Wuko release and run a workflow with
GitHub error annotations, a job summary, and typed outputs:

```yaml
permissions:
  contents: read
  pull-requests: read
  actions: read

steps:
  - uses: actions/checkout@v6

  - name: Run Wuko checks
    uses: up2jj/wuko@v0.18.0
    with:
      workflow: check
      vars: |
        package=./cmd/...
        race=true
    env:
      API_TOKEN: ${{ secrets.API_TOKEN }}
```

## Authentication

The action automatically exports the job's `github.token` as `GH_TOKEN` while Wuko runs, so
GitHub-backed steps and direct `gh` commands work without repeating token wiring. The token keeps
exactly the access granted to the job through `permissions`; the action does not broaden it. For
example, a workflow can discover its pull request without declaring authentication:

```yaml
version: 1
name: check
steps:
  - id: pull_request
    type: github_pr
    with:
      operation: find
```

Override the job token when an integration needs different credentials, such as access to another
repository:

```yaml
- name: Run cross-repository release workflow
  uses: up2jj/wuko@v0.18.0
  with:
    workflow: release
    token: ${{ secrets.WUKO_GITHUB_TOKEN }}
```

## Workflow outputs

The `outputs` output carries the workflow's complete return map as JSON, so a later step can read a
single value. Wuko writes it only for a successful run whose workflow ends in a `return`; any other
run leaves the output empty, and `fromJSON('')` fails the job. Guard the consuming step when the
return is conditional:

```yaml
- name: Build the release archive
  id: wuko
  uses: up2jj/wuko@v0.18.0
  with:
    workflow: build

- uses: actions/upload-artifact@v4
  if: steps.wuko.outputs.outputs != ''
  with:
    path: ${{ fromJSON(steps.wuko.outputs.outputs).artifact }}
```

## Execution metadata and failures

Execution metadata is separate from workflow return values. The action writes `status`,
`execution-id`, `duration-ms`, `failed-step`, and a versioned JSON `report` before a failed Wuko
invocation exits. `execution-id` identifies the complete invocation, including failures during
discovery, loading, or validation. `status` is `succeeded`, `failed`, `timed_out`, or `canceled`.
`failed-step` is empty unless an unsuccessful top-level step with an ID was recorded.

The metadata describes the Wuko invocation, not the action step. It is empty when the action fails
before Wuko starts - a failed install, both or neither of `workflow` and `definition`, or a missing
`working-directory` - and `status` can still read `succeeded` when the invocation itself succeeded
but the run reporters failed to publish their results. Treat a non-empty `status` other than
`succeeded` as a Wuko failure, and `steps.<id>.outcome` as the authoritative step result.

Use `continue-on-error` when later steps should inspect a failed run without failing the job at
that point:

```yaml
- name: Run Wuko checks
  id: wuko
  continue-on-error: true
  uses: up2jj/wuko@v0.18.0
  with:
    workflow: check

- name: Inspect the failure
  if: steps.wuko.outcome == 'failure' && steps.wuko.outputs.status != ''
  env:
    WUKO_REPORT: ${{ steps.wuko.outputs.report }}
    WUKO_FAILED_STEP: ${{ steps.wuko.outputs.failed-step }}
  run: |
    echo "Failed step: $WUKO_FAILED_STEP"
    jq . <<<"$WUKO_REPORT"
```

Guarding on `outcome` rather than `status == 'failed'` also covers a timed-out or canceled
invocation, and the `status != ''` test skips the step when the action failed before Wuko produced
any metadata.

Without `continue-on-error`, use `if: failure()` on a following diagnostic step. Workflow outputs
remain empty on failure even though execution metadata is available.

## Inputs and variables

`workflow` accepts a discovered name, a local file, an HTTPS URL, or a `github:` locator. Use
`target` to select a workflow target and `working-directory` to change the run directory. The
optional `vars` input accepts one `key=value` assignment per line. Wuko preserves JSON values
such as booleans, numbers, arrays, and objects; unquoted values are strings:

```yaml
vars: |
  package=./cmd/...
  race=true
  retries=3
  platforms=["linux","darwin"]
```

For generated or deeply nested values, pass one JSON object instead. A value whose first
non-whitespace character is `{` is loaded as a typed variable file:

```yaml
vars: |
  {
    "revision": ${{ toJSON(github.sha) }},
    "matrix": {"os":["linux","darwin"]}
  }
```

Pass secrets through the action's `env` block rather than interpolating them into variables or the
workflow definition.

## Inline definitions

For a small workflow constructed by the GitHub workflow itself, provide an inline definition:

```yaml
- name: Run inline Wuko workflow
  id: wuko
  uses: up2jj/wuko@v0.18.0
  with:
    definition: |
      version: 1
      name: checks
      steps:
        - id: tests
          type: shell
          with:
            command: go
            args: [test, ./...]
        - return:
            outputs:
              revision: 'vars.revision'
    vars: |
      revision=${{ toJSON(github.sha) }}
```

Exactly one of `workflow` and `definition` is required. Inline definitions use the working
directory as the base for relative `require` entries, templates, and local actions. The action runs
once even when the definition declares `cron`. Pin the action to a release tag or full commit SHA;
`version` can override the Wuko binary version recorded by that action revision.

## Without the composite action

When Wuko is already installed, the equivalent lower-level invocation remains available:

```yaml
- name: Run Wuko checks
  id: wuko
  run: wuko run check --once --reporter plain --reporter github
```

Write the same canonical report to a local file with `--report-json`. Wuko atomically replaces the
file when the invocation finishes, including failure paths; the parent directory must already
exist:

```sh
wuko run check --once --report-json ./wuko-report.json
```

The `github` reporter is never enabled automatically outside the composite action. With no
`--reporter` flags, `wuko run` uses only the default plain reporter.
