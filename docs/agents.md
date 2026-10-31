# Using Wuko from an agent

Discover the interface shipped with the installed binary before constructing commands or workflows.
Help and reference lookups are offline, ignore local configuration, and never execute workflow
code, plugins, or examples.

## Command discovery

```sh
wuko help --format json
wuko help run --format json
wuko help plugin install --format json
wuko reference step --format json
wuko reference search "HTTP request" --format json
wuko reference step http --format json
wuko reference topic templates --format json
```

Read a compact index first, then fetch the relevant command, step, or topic. `--help` and
`wuko help COMMAND` provide equivalent text help. JSON command metadata describes built-in
defaults, not configuration resolved from the current repository. Root help points to `run`
for the flags accepted by a configured bare invocation.

## Examples

Every built-in step has a named `basic` example. Additional named examples demonstrate alternate
modes and unattended prompts. Examples include a complete workflow, supporting files, prerequisites,
and expected results. Looking one up does not create files or run it.

```sh
wuko reference example set
wuko reference example set basic --format yaml
wuko reference example file basic --format json
```

Save the workflow as `workflow.yaml`. For examples with supporting files, use the JSON bundle and
save each `files` entry relative to that workflow. YAML output is available only for single-file
examples. Run the commands from the directory containing the workflow and supporting files:

```sh
wuko --no-config validate --file ./workflow.yaml --format json
wuko --no-config tree --file ./workflow.yaml
wuko --no-config run --file ./workflow.yaml --dry-run
```

Review effects and prerequisites before execution. Validation and dry-run do not run steps, but
loading arbitrary workflows can execute command-based source resolvers or plugin helpers. The
built-in example catalog does not declare plugins or command-based action sources.

## Discover and run existing workflows

Use `wuko list --format json` (workflow-list `schema_version: 1`) and choose an entry with
`invokable: true`. `--all` includes shadowed sources; do not silently substitute one for the
effective workflow. Preserve explicit names, targets, file paths, and the invocation directory.

Supply required variables explicitly. `--var` parses valid JSON: preserve JSON quotes when a
numeric-looking value must remain text, for example `--var 'code="123"'`. Keep credentials in
the environment or configured secret providers, rather than command arguments or workflow text.

For an intended execution of a reviewed workflow:

```sh
wuko --no-config run --file ./workflow.yaml --once --report-json -
```

`--once` prevents a declared cron schedule from repeating the invocation. It does not disable
workflow retries. `--yes` answers confirmation prompts only; `--defaults` uses declared prompt
defaults. Neither supplies all possible missing inputs. Interactive steps also support explicitly
documented unattended behavior; see the step reference.

With `--report-json -`, stdout contains one execution report (`schema_version: 2`). Workflow
output, progress, and prompts go to stderr. Require `status: succeeded` and `dry_run: false`
before treating declared `outputs` as execution results. Exit status is 0 on success, 1 on
failure, and 130 on cancellation. A missing or incomplete report is not evidence of success.
Do not automatically repeat failed operations: completed effects are not rolled back.

## Help and reference JSON contract

Help and reference JSON use this envelope:

```json
{"schema_version":1,"kind":"command","data":{}}
```

`kind` identifies `command`, `index`, `steps`, `step`, `topics`, `topic`, `search`, `examples`,
or `example`. Index and search data are arrays of entries with `id`, `kind`, `summary`, and an
offline lookup `command`. Empty searches return an empty array and succeed.

- Commands expose canonical `path`, `summary`, `description`, `usage`, `aliases`, `arguments`,
  immediate child `commands`, `examples`, local `flags`, inherited `global_flags`, `behavior`,
  and `related` commands. Flag defaults retain pflag's string representation; the `type` field
  describes how to interpret them. Hidden commands and flags are excluded.
- Steps expose `fields`, `rules`, structural `outputs`, complete Markdown `documentation`, its repository
  `source`, a compact named `examples` index, and related topic IDs. Field structure comes from native config
  types. Reviewed unconditional requirements, defaults, and choices annotate fields where known;
  absence of an annotation means unspecified, not false or zero. Conditional constraints and custom YAML forms are
  described by `rules` and the documentation; `omitempty` is not a requiredness rule. `any` and open outputs
  deliberately preserve dynamic values instead of inventing exact types.
- Topics expose complete Markdown `documentation` and its repository `source`.
- Examples expose `id`, `title`, `description`, complete `workflow` YAML, supporting `files`,
  `prerequisites`, and the `expected` result. A `check`, when present, is the Expr assertion
  included in the workflow itself; it is not a command to execute separately.

Output order is deterministic and independent of terminal width. Unknown names, invalid formats,
and invalid requests fail with a nonzero status, an error on stderr, and no JSON on stdout.
Accept additive fields; incompatible changes require a new schema version. These documents are
independent of the existing list, validation, and execution-report schemas.
