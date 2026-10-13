# Contributing to Wuko

Thanks for helping improve Wuko. This page covers the development loop; see
[`AGENTS.md`](AGENTS.md) for the full repository guidelines on layout, style, and testing.

## Setup

Wuko needs Go 1.26 or newer and [`just`](https://github.com/casey/just).

```sh
just build        # build ./wuko
just test         # go test ./...
just test-race    # what CI and the pre-push hook run
just vet          # go vet ./...
just fmt          # gofmt -w .
just run -- --help
```

Run `just hooks` once to install the [prek](https://github.com/j178/prek) pre-commit and pre-push
hooks, and `prek run --all-files` before broad changes. `just snapshot` builds local release
archives into `dist/`; do not commit them.

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
- Add regression tests for behavior changes. `go test ./...`, `go vet ./...`, and a clean
  `gofmt -l .` are required by CI.
- Update `README.md`, `docs/`, or `examples/` when workflow syntax or CLI behavior changes.
- For visible TUI changes, include terminal output or screenshots and re-record affected demos with
  `just screenshots`. See [Screenshot and demo maintenance](docs/screenshots.md).
