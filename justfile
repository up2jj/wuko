# wuko task runner — run `just` to list recipes.

# Show available recipes.
default:
    @just --list

# Build the binary into ./wuko.
build:
    go build -o wuko ./

# Install wuko into $GOBIN (or $GOPATH/bin, i.e. ~/go/bin) so it is on your PATH.
# Stamp the same build metadata as GoReleaser.
install:
    go install -ldflags "-X github.com/up2jj/wuko/cmd.version=$(git describe --tags --always) -X github.com/up2jj/wuko/cmd.commit=$(git rev-parse --short HEAD) -X github.com/up2jj/wuko/cmd.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./
    @echo "installed wuko to $(go env GOBIN GOPATH | awk 'NR==1{b=$0} NR==2{p=$0} END{print (b!="" ? b : p"/bin")"/wuko"}')"

# Run the test suite.
test:
    go test ./...

# Run the test suite without the tests that compile generated plugin modules.
test-short:
    go test -short ./...

# Measure script bridge lifecycle, runtime invocation and sequential SDK calls.
[positional-arguments]
bench-script-sdk *args: build
    WUKO_BENCH_EXECUTABLE="{{justfile_directory()}}/wuko" go test ./scriptsdk -run '^$' -bench 'Benchmark(BridgeLifecycle|ScriptInvocation|SDKCall)$' -benchtime=1s -count=5 "$@"

# Run the test suite under the race detector (what CI and the pre-push hook run).
test-race:
    go test -race ./...

# Run the devenv executor end-to-end tests against a fake devenv CLI (no nix required).
test-devenv:
    go test -race -count=1 ./steps/devenv

# Run the opt-in real-devenv smoke test. Requires devenv 2.2+ and the secretspec CLI.
smoke-devenv:
    WUKO_SMOKE_REQUIRED=1 go test -tags devenv_smoke -count=1 ./smoke

# Fuzz one target locally; fuzz tests are excluded from CI by the fuzz build tag.
# Example: just fuzz ./expression FuzzEval 2m
fuzz package target time="1m":
    go test -tags fuzz {{package}} -run '^$' -fuzz '^{{target}}$' -fuzztime {{time}}

# Run every fuzz target's seed corpus as ordinary tests.
fuzz-seeds:
    go test -tags fuzz -run '^Fuzz' ./cmd ./expression ./form ./fswatch ./validation ./workflow

# Run go vet.
vet:
    go vet ./...

# Run staticcheck (configured in staticcheck.conf).
[positional-arguments]
lint *args:
    sh scripts/lint.sh "$@"

# Format all Go sources.
fmt:
    gofmt -w .

# Tidy module dependencies.
tidy:
    go mod tidy

# Build a persistent Wuko binary and install pre-commit + pre-push workflows.
hooks: build
    ./wuko git hook install

# Run the commit checks on every tracked file, without staged isolation.
hooks-check: build
    ./wuko --env-loader none run --file .wuko/workflows/repository-checks.yaml all --once --reporter plain

# Run wuko in the current directory, e.g. `just run --help`.
[positional-arguments]
run *args:
    go run . "$@"

# Render the checked-in VHS demos. Requires vhs, ttyd, and ffmpeg.
screenshots: build
    #!/usr/bin/env bash
    set -euo pipefail
    for tape in docs/demos/*.tape; do
        vhs "$tape"
    done

# Render one VHS demo, e.g. `just screenshot gallery-retry`.
screenshot name: build
    vhs docs/demos/{{ name }}.tape

# Validate VHS tapes without rendering or modifying generated media.
validate-screenshots:
    vhs validate 'docs/demos/*.tape'

# Validate the GoReleaser config.
check:
    goreleaser check

# Build a local release into ./dist without publishing.
snapshot:
    goreleaser release --snapshot --clean

# Prepare, check, tag, and push a release; the pushed tag triggers the GitHub release workflow.
# Usage: just release minor   (or patch, major, 0.21.0; add --dry-run to preview)
[positional-arguments]
release *args:
    bash scripts/release.sh "$@"
