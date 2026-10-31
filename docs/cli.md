# CLI cheatsheet

[Back to the README](../README.md)

Every command accepts `--help`. Global flags: `--debug` traces loading, validation, and execution
to stderr with diagnostic redaction; `--env-loader auto|none|mise|asdf|direnv` loads the
invocation environment. `--config PATH` selects a CLI defaults file; `--no-config` ignores CLI
configuration entirely.

## Contents

- [Local configuration](#local-configuration)
- [Find and inspect workflows](#find-and-inspect-workflows)
- [Run workflows](#run-workflows)
- [Install and share workflows](#install-and-share-workflows)
- [Plugins](#plugins)
- [Git hooks](#git-hooks)
- [Coding-agent skills](#coding-agent-skills)
- [Shell completion](#shell-completion)

## Local configuration

Place `wuko.yml` or `wuko.yaml` in the directory where you invoke Wuko to save CLI defaults.
Wuko checks only the current directory; it does not search parents, your home directory, or
workflow directories for CLI configuration. Workflow discovery keeps its existing search rules.

```yaml
flags:
  debug: false
  env-loader: [mise, direnv]

default:
  workflow: build
  target: production # optional
  flags:
    once: true
    reporter: [plain]
    var: [stage=production]

commands:
  run:
    flags:
      reporter: [plain]
      cpus: "1.5"
      memory: 512MiB
  list:
    flags:
      format: json
  marketplace:
    commands:
      plugin:
        commands:
          release:
            flags:
              skip-build: true
```

Use long flag names without `--`. Root `flags` holds global options. A command's `flags` holds
only options declared by that command; nest command groups under `commands`. Booleans use YAML
`true` or `false`, string options use strings, and repeatable options use lists of strings.
Quote numeric-looking strings such as `cpus: "1.5"`. `var` and `env` entries keep their usual
`key=value` syntax.

The optional `default` section changes bare `wuko` into a workflow invocation. `workflow` is
required in that section and accepts the same name, URL, or GitHub locator as `wuko run`.
`target` is optional. `default.flags` accepts run-specific options. Bare invocations also accept
run flags, such as `wuko --dry-run` or `wuko --once=false`; `wuko --help` leaves them out, and
`wuko run --help` lists them. Existing run-option validation still applies. Use
`wuko run NAME [TARGET]` to select a different workflow explicitly.

For bare invocations, run-option precedence is:

1. Explicit CLI flags.
2. `default.flags`.
3. `commands.run.flags`.
4. Built-in defaults.

Explicit subcommands ignore `default` and use their own configured flags. CLI flags override
configuration even when set to `false` or an empty string. Supplying a repeatable flag replaces
its entire configured list; entries are not appended or merged by key. A configured empty list
also replaces lower-priority lists. `WUKO_ENV_LOADERS`, when nonempty, overrides configured
`flags.env-loader`; explicit `--env-loader` flags override both.

Without `default`, bare `wuko` retains the picker (or prints available workflows without a
terminal). Picker actions use global configuration and retain their existing launch behavior;
they do not inherit `commands.run.flags` or other command defaults. Run-specific flags on bare
`wuko` require `default.workflow`.

```sh
wuko                                  # configured default workflow, or the picker
wuko --dry-run                        # preview the configured default workflow
wuko --config ./settings/local.yml run build
wuko --no-config                      # ignore all config and open the picker
wuko --no-config run build            # explicit flags and built-in defaults
```

`--config PATH` selects exactly one file instead of automatic discovery; its extension need not
be `.yml` or `.yaml`, but its contents must be YAML. Relative paths in configured options resolve
from the invocation directory, including when the selected config is elsewhere. Configuration
does not change the working directory.

`--no-config` skips loading entirely, including malformed files. It cannot be combined with
`--config`. If both automatic filenames exist, select one with `--config` or use `--no-config`.
Missing automatic files are normal; an explicitly selected missing file fails. Empty files
supply no defaults. Unknown fields, commands, flags, duplicate keys, invalid types, null option
values, and multiple YAML documents are errors, including in inactive command sections.
Config cannot set `config`, `no-config`, generated help/version flags, or private command options.
It also cannot set flags that confirm or scope a removal (`--yes` on `uninstall`,
`plugin uninstall`, and `marketplace workflow|plugin remove`, and `--global` on both uninstall
commands), so a checked-in config never removes anything without a prompt.
Help, version, shell completion, and the private script SDK bypass config loading.

Workflow packages also use the filenames `wuko.yaml` and `wuko.yml`. When invoking Wuko from
a directory containing such a workflow manifest, use `wuko --no-config run --file ./wuko.yaml`
so it is not interpreted as CLI config. CLI configuration files are trusted local settings;
the `default` section can run a workflow immediately.

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
suggests `deploy`. Misspelled targets also suggest a close match, such as `stagng` → `staging`,
and list available targets. Unknown workflow names also point to `wuko list`; Wuko never runs
the suggestion automatically.

Local YAML paths use `--file`, for example `wuko run --file ./workflow.yaml`. If a selector
matches an existing YAML file but no named workflow, the error points to `--file`. The same
guidance applies to `tree`, `validate`, and `ui`.

In the picker, workflows with targets appear once; Enter or `t` opens their target picker,
where Enter runs the selected target and Escape returns to workflows. Workflows without targets
run directly with Enter. Press `a` to open the action palette: open a browser
form, validate, show the tree, dry-run, print a reproducible command, edit, pin, change sorting, or
manage marketplace installs. See [Workflow discovery](workflow-discovery.md) for search order,
shadowing, and picker behavior.

`wuko list` and the picker skip invalid workflow definitions and warn on stderr. Valid workflows
remain available, and JSON listings keep stdout parseable. A broken local workflow still shadows
global workflows with the same name; `list --all` marks those global sources as shadowed, and the
picker hides them. Use `wuko validate` to inspect invalid definitions. Filesystem and ambiguous
identity errors still fail discovery.

The editor action uses `$VISUAL`, then `$EDITOR`, with single or double quotes for paths or
arguments containing spaces. For example: `VISUAL='"/path/to/My Editor" --wait'`. It launches
the editor directly; environment variables and shell commands inside the value are not expanded.

## Run workflows

```sh
wuko run check
wuko run deploy staging                  # select a workflow target
wuko run check --var package=./cmd/...   # typed variables (repeatable)
wuko run check --var-file vars.json      # JSON or TOML variables
wuko run check --env API_URL=http://localhost:8080
wuko run check --dry-run                 # validate and print steps without running them
wuko run check --once                    # ignore a declared cron schedule
wuko run bootstrap --yes                 # answer every tui_confirm with true
wuko run bootstrap --defaults            # answer every prompt that declares a default with it
wuko run check --cpus 1.5 --memory 512MiB # shared budget for the local invocation
wuko run check --report-json report.json # versioned execution report
wuko run check --once --report-json -    # report JSON on stdout; terminal output on stderr
wuko run check --once --report-json - --report-json-include outputs,vars # add redacted step outputs and variables
wuko run check --reporter plain --reporter github
wuko ui check                            # run through the local browser form
```

Canceled runs, including graceful Ctrl+C cancellation, exit with status 130. Other failures
exit with status 1.

`--var` parses valid JSON values, so `--var code=123` is a number and `--var enabled=true` is
a boolean. To keep numeric-looking or boolean-looking text as a string, preserve JSON quotes:
`--var 'code="123"'` or `--var 'label="true"'`. Replace `code` or `label` with your variable name.

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
`--var` and `--env` do not suggest filenames; enter a `key=value` override directly.
`wuko ui` suggests only invokable workflows and targets with an effective browser form,
including forms inherited from the workflow declaration.
`wuko uninstall` and `wuko plugin uninstall` suggest names from their local installation
directory, or the home-global directory with `--global`. Completion does not run uninstall
hooks or launch plugins.
