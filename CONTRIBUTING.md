# Contributing to Wuko

Thanks for helping improve Wuko. This page covers the development loop; see
[`AGENTS.md`](AGENTS.md) for the full repository guidelines on layout, style, and testing.

## Setup

Wuko needs Go 1.26.10 or newer and [`just`](https://github.com/casey/just).

```sh
just build        # build ./wuko
just test         # go test ./...
just test-race    # what CI and the pre-push hook run
just vet          # go vet ./...
just lint         # pinned Staticcheck, including test files
just fuzz ./expression FuzzEval 2m  # local only; fuzz tests are not run in CI
just fmt          # gofmt -w .
just run --help
```

`just run` preserves quoted arguments, for example
`just run run check --var 'message=hello world'`.

Staticcheck uses the checks and style exceptions in `staticcheck.conf`. Run
`just lint ./engine/...` to check a single package tree, or `just lint -version`
to see the pinned release. The version is pinned in `scripts/lint.sh`, which is
shared by `just lint`, CI, and the pre-push hook; no separate linter installation
is needed. The first run downloads the tool with Go.

Run `just hooks` once to build `./wuko` and install its pre-commit and pre-push workflows. The
dispatcher records that persistent binary, so keep it in place and rebuild it after changing Wuko.
Run `just hooks-check` before broad changes to apply the commit checks to every tracked file.
If this checkout still has prek hooks, remove them once with
`prek uninstall --hook-type pre-commit --hook-type pre-push` before installing Wuko's hooks.

The versioned bindings are in `.wuko/git-hooks.yaml`; check ordering and conditions are in
`.wuko/steps/repository-checks.yaml`. They preserve the previous file checks, Go
formatting, vet, tidy, and push-only Staticcheck and race tests. Each target selects its files with
the native `git_files` step, which excludes generated `dist/` and `completions/` files and
classifies the rest as text or binary. File policies run through the local
`.wuko/actions/file-check/action.yaml` action, where one Lua pass per check parses YAML and TOML
with `wuko.yaml` and `wuko.toml` and scans content; `batch` groups Go formatting calls. Inline `lua` steps handle case comparisons,
and `go vet` package scoping; the native `text_fix` step fixes whitespace and final newlines. The fixers and gofmt run in
order, then the remaining checks run concurrently; every check still runs and reports after a
failure, and formatters reject the operation after making fixes so they can be reviewed and staged.
Pre-commit isolates staged tracked content before loading the workflows; see
[staged isolation](docs/git-hooks.md#staged-isolation).
Push checks select files from Git's outgoing ref updates. The large-file policy checks the files a
commit or push adds, allows up to 500 KiB, and exempts Git LFS files. Staticcheck's configuration
filter also covers the replacement Wuko configuration.
CI runs the same all-files workflow, followed by Staticcheck and the full race test suite.

`just snapshot` builds local release archives into `dist/`; do not commit them.

## Where things live

| Path | Contents |
| --- | --- |
| `main.go`, `cmd/` | Cobra commands and dependency wiring |
| `engine/` | Workflow execution |
| `workflow/` | Parsing, discovery, and loading |
| `step/` | Shared step contracts and the registry |
| `steps/<name>/` | Built-in steps, one package each |
| `tui/` | Terminal UI |
| `docs/` | User documentation; `docs/demos/` holds the VHS gallery |
| `examples/` | Runnable example workflows |
| `skills/` | Embedded coding-agent skills |

## Pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) such as `feat(docker): add ...`
  or `fix: ...`.
- Add regression tests for behavior changes. `go test ./...`, `go vet ./...`, `just lint`, and a clean
  `gofmt -l .` are required by CI.
- Update `README.md`, `docs/`, or `examples/` when workflow syntax or CLI behavior changes.
- For visible TUI changes, include terminal output or screenshots and re-record affected demos with
  `just screenshots`. See [Screenshot and demo maintenance](docs/screenshots.md).
