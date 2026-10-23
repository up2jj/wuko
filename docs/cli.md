# CLI cheatsheet

[Back to the README](../README.md)

Every command accepts `--help`. Global flags: `--debug` traces loading, validation, and execution
to stderr with diagnostic redaction; `--env-loader auto|none|mise|asdf|direnv` loads the
invocation environment.

## Contents

- [Find and inspect workflows](#find-and-inspect-workflows)
- [Run workflows](#run-workflows)
- [Install and share workflows](#install-and-share-workflows)
- [Plugins](#plugins)
- [Git hooks](#git-hooks)
- [Coding-agent skills](#coding-agent-skills)
- [Shell completion](#shell-completion)

## Find and inspect workflows

```sh
wuko                              # searchable picker (non-interactive: prints workflows)
wuko list                         # effective workflows: name, scope, description, path
wuko list --all                   # include shadowed workflows, with effective/shadowed status
wuko list --format json           # versioned workflow discovery document
wuko tree check                   # execution tree
wuko validate                     # validate every discovered workflow
wuko validate check --format json # versioned machine-readable diagnostics
wuko validate check --reporter github
wuko validate --file ./draft.yaml # validate a file outside discovery
./generate-workflow | wuko validate --file - --format json
./generate-workflow | wuko tree --file -
```

`validate --file PATH [TARGET]` and `tree --file PATH [TARGET]` inspect the selected file without
executing steps. Use `--file -` to read from stdin; relative resources resolve from the current
directory, and the inspection is non-interactive. Use `--file ./-` for a file named `-`.

When discovery finds no workflows, `wuko validate` prints `No workflows found.` and succeeds
unless Git hook validation fails. JSON output keeps its normal diagnostics document.

Misspelled workflow names suggest a close discovered match, for example `wuko run depoly`
suggests `deploy`. Unknown-name errors also point to `wuko list`; Wuko never runs the suggestion
automatically.

In the picker, Enter runs the selected workflow and `a` opens its action palette: open a browser
form, validate, show the tree, dry-run, print a reproducible command, edit, pin, change sorting, or
manage marketplace installs. See [Workflow discovery](workflow-discovery.md) for search order,
shadowing, and picker behavior.

## Run workflows

```sh
wuko run check
wuko run deploy staging                  # select a workflow target
wuko run check --var package=./cmd/...   # typed variables (repeatable)
wuko run check --var-file vars.json      # JSON or TOML variables
wuko run check --env API_URL=http://localhost:8080
wuko run check --dry-run                 # validate and print steps without running them
wuko run check --once                    # ignore a declared cron schedule
wuko run check --cpus 1.5 --memory 512MiB # shared budget for the local invocation
wuko run check --report-json report.json # versioned execution report
wuko run check --once --report-json -    # report JSON on stdout; terminal output on stderr
wuko run check --reporter plain --reporter github
wuko ui check                            # run through the local browser form
```

Workflows can also come from a file, standard input, a URL, or a GitHub repository:

```sh
wuko run --file ./workflows/release.yaml
./generate-workflow | wuko run --file -
wuko run https://github.com/acme/automation/blob/main/workflows/build/wuko.yaml
wuko run --pick https://github.com/acme/automation/tree/main/workflows
```

See [Run reporters](execution.md#run-reporters), [Validation diagnostics](validation.md), and
[Browser forms](forms.md).

### Local CPU and memory limits

`--cpus` and `--memory` supervise the entire `wuko run` invocation without Docker. Either flag
can be used alone. CPU counts are relative to one core (`0.5` is half a core, `2` is two cores),
from `0.01` through `1000000`. Memory accepts a positive integer followed by `B`, `KiB`, `MiB`,
`GiB`, or `TiB`. Omitting a flag leaves that resource unlimited. `--dry-run` validates the values
without launching a supervisor or requiring its platform facilities.

The runner, local shell commands, managed processes, plugins, prerequisites, and child workflows
share the budget. Concurrent branches do not receive separate allowances. A scheduled invocation
or `--pick` session keeps the same budget for its lifetime. The small supervising process stays
outside the budget. These are invocation flags; they do not add a YAML `resources` field or apply
to `wuko ui` and install hooks.

| Platform | Enforcement | Requirements |
| --- | --- | --- |
| Linux | A cgroup v2 scope enforces CPU quota and aggregate memory, with swap disabled. An OOM event kills the whole scope. | systemd 254+, `systemd-run`, `systemctl`, and a running user manager with the requested controllers delegated. |
| macOS | A supervisor samples the descendant process tree every 50ms, throttles aggregate CPU with stop/continue signals, and kills the observed tree when summed resident memory exceeds the budget. | Permission to enumerate, measure, and signal the workflow's processes. |

Linux checks the actual cgroup settings before loading or executing the workflow. Missing
controllers, unavailable user sessions, and unsupported systemd options fail the run; Wuko never
falls back to unlimited execution. Linux uses systemd's [CPU and memory controls](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html)
and [scope OOM policy](https://www.freedesktop.org/software/systemd/man/latest/systemd.scope.html#OOMPolicy=).

macOS limits are approximate and print a notice on stderr. CPU and memory can overshoot between
samples. Processes that start and finish between samples may be missed, and children that detach
before discovery may escape tracking. Shared resident pages can be counted more than once;
swapped or compressed pages are not a hard allocation limit. Previously observed descendants
remain tracked after reparenting, and process start times protect against PID reuse. CPU
accounting converts XNU's [Mach time counters](https://github.com/apple-oss-distributions/xnu/blob/main/osfmk/kern/bsd_kern.c)
using the kernel timebase on both Intel and Apple Silicon.

Memory termination may prevent `finally` cleanup and final JSON reports from being produced on
either platform. Ordinary cancellation resumes CPU-throttled processes before graceful shutdown.
Force-killing the supervisor can bypass cleanup, especially on macOS where suspended descendants
may need to be killed manually. These controls assume trusted workflows: privileged processes
and work submitted to external daemons, remote executors, or Docker containers can escape the
local budget. Use the Docker executor's own resource settings for container workloads.

## Install and share workflows

```sh
wuko install ./release.yaml                        # into ./.wuko/workflows/
wuko install --global https://example.com/workflows/release.yaml
wuko install --global github:acme/workflows@main:release.yaml
wuko install https://github.com/acme/wuko-marketplace   # pick from a marketplace
wuko install --global --package release --package lint https://github.com/acme/wuko-marketplace
wuko uninstall release
wuko uninstall --global --yes release

wuko marketplace init                # create a marketplace manifest
wuko marketplace build               # publish new or changed packages and plugins
wuko marketplace workflow --help     # add or remove workflow package sources
wuko marketplace plugin --help       # scaffold and manage marketplace plugins
```

`install` and `uninstall` also accept `--var`, `--var-file`, and `--env` for lifecycle hooks. See
[Workflow installation](execution.md#workflow-installation) and
[Workflow marketplaces](execution.md#workflow-marketplaces).

## Plugins

```sh
wuko plugin install --global --package acme https://github.com/acme/wuko-marketplace
wuko plugin uninstall --help
wuko plugin lock build.yaml test.yaml # generate shared local pins without starting plugins
wuko plugin lock build.yaml --update # intentionally refresh selected pins
wuko plugin lock legacy.yaml --migrate # move legacy inline SHA pins into wuko.lock
wuko run --file build.yaml --frozen-lockfile # CI: require existing pins
```

See [Executable plugins](plugins.md).

## Git hooks

```sh
wuko git hook init        # starter .wuko/git-hooks.yaml and example workflows
wuko git hook install     # install dispatchers; --chain keeps an existing hook
wuko git hook status
wuko git hook run pre-commit -- "$@"
wuko git hook uninstall
```

See [Git hooks](git-hooks.md).

## Coding-agent skills

```sh
wuko agent list            # supported agent CLIs found on PATH
wuko agent install claude  # install the bundled skills for one agent
wuko agent install         # install for every available agent
```

Claude skills are installed under `~/.claude/skills/`; Codex skills under `~/.agents/skills/`.
Reinstalling replaces the bundled skill files.

## Shell completion

```sh
wuko completion zsh > "${fpath[1]}/_wuko"
wuko completion --help
```

Completion reads workflow metadata without preparing workflows or starting plugins. Malformed
YAML drafts are skipped so other workflow names and targets remain available. With `--file`, it suggests
targets from that file, for example after `wuko run --file ./deploy.yaml `, without preparing the
workflow or starting plugins. Stdin (`--file -`) and remote workflows do not offer target
completion. `--file` filters filenames to YAML; `--var-file` filters to JSON and TOML.
`validate --format` and `validate --reporter` suggest their supported values.
