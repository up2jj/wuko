# Repository Guidelines

## Project Structure & Module Organization

Wuko is a Go 1.26 CLI for trusted YAML workflows. `main.go` is the entry point; `cmd/` defines Cobra commands and wires dependencies. Execution lives in `engine/`; `workflow/` parses definitions; `step/` provides shared contracts. Built-in operations are feature packages under `steps/` (for example, `steps/http/`). Terminal UI code belongs in `tui/`. Documentation is in `docs/`, runnable YAML in `examples/`, embedded agent skills in `skills/`, and CI configuration in `.github/`.

## Build, Test, and Development Commands

Use the repository's `justfile` as the command entry point:

- `just build` builds `./wuko`.
- `just run -- --help` runs the CLI from source with arguments.
- `just test` runs `go test ./...`.
- `just vet` runs Go's static checks.
- `just fmt` formats all Go sources with `gofmt`.
- `just snapshot` creates local release artifacts in `dist/`.
- `just hooks` installs pre-commit and pre-push checks; run `prek run --all-files` before broad changes.

## Coding Style & Naming Conventions

Follow idiomatic Go and let `gofmt` control tabs and layout. Keep packages flat and named by responsibility. Exported names use `PascalCase`; unexported names use `camelCase`; filenames use lowercase words, with underscores when helpful (for example, `working_directory.go`). Wrap errors with context and `%w`. Keep Cobra code focused on arguments and wiring; put reusable behavior in domain packages and write command output through Cobra's streams.

## Testing Guidelines

Tests use the standard `testing` package and are colocated as `*_test.go`. Name tests `TestBehaviorScenario` and prefer table-driven subtests. Use `t.TempDir()`, injected dependencies, and buffers to isolate filesystem and CLI behavior. Add regression tests for behavior changes. There is no coverage threshold, but `go test ./...`, `go vet ./...`, and a clean `gofmt -l .` are required by CI.

## Commit & Pull Request Guidelines

History follows Conventional Commits such as `feat(docker): add ...`, `fix: ...`, and `refactor: ...`; keep subjects imperative and focused. Pull requests should explain the user-visible change, note tests run, link relevant issues, and update `README.md`, `docs/`, or examples when workflow syntax or CLI behavior changes. Include terminal output or screenshots for meaningful TUI changes, and avoid committing generated `dist/` artifacts.

## Security & Configuration

Treat workflows as trusted local code: shell, HTTP, Docker, Lua, and agent steps can access developer resources. Never commit credentials or private keys; the hooks include secret detection. Preserve diagnostic redaction when adding logs, and pin remote workflow/action references when reproducibility matters.
