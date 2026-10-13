# CLI cheatsheet

[Back to the README](../README.md)

Every command accepts `--help`. Global flags: `--debug` traces loading, validation, and execution
to stderr with diagnostic redaction; `--env-loader auto|none|mise|asdf|direnv` loads the
invocation environment.

## Find and inspect workflows

```sh
wuko                              # searchable picker (non-interactive: prints workflows)
wuko list                         # effective workflows: name, scope, description, path
wuko tree check                   # execution tree
wuko validate                     # validate every discovered workflow
wuko validate check --format json # versioned machine-readable diagnostics
wuko validate check --reporter github
```

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
wuko run check --report-json report.json # versioned execution report
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
wuko marketplace plugin --help       # scaffold and manage marketplace plugins
```

`install` and `uninstall` also accept `--var`, `--var-file`, and `--env` for lifecycle hooks. See
[Workflow installation](execution.md#workflow-installation) and
[Workflow marketplaces](execution.md#workflow-marketplaces).

## Plugins

```sh
wuko plugin install --global --package acme https://github.com/acme/wuko-marketplace
wuko plugin uninstall --help
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
