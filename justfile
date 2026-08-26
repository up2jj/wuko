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

# Run the test suite under the race detector (what CI and the pre-push hook run).
test-race:
    go test -race ./...

# Run go vet.
vet:
    go vet ./...

# Format all Go sources.
fmt:
    gofmt -w .

# Tidy module dependencies.
tidy:
    go mod tidy

# Install git hooks via prek (pre-commit + pre-push).
hooks:
    prek install --hook-type pre-commit --hook-type pre-push

# Run wuko in the current directory (pass flags after --, e.g. `just run -- --help`).
run *args:
    go run . {{ args }}

# Render the checked-in VHS demos. Requires vhs, ttyd, and ffmpeg.
screenshots: build
    #!/usr/bin/env bash
    set -euo pipefail
    for tape in docs/demos/*.tape; do
        vhs "$tape"
    done

# Validate VHS tapes without rendering or modifying generated media.
validate-screenshots:
    vhs validate 'docs/demos/*.tape'

# Validate the GoReleaser config.
check:
    goreleaser check

# Build a local release into ./dist without publishing.
snapshot:
    goreleaser release --snapshot --clean

# Tag and push a release, triggering the GitHub Actions release workflow.
# Usage: just release 0.2.0   (creates and pushes tag v0.2.0)
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    version="{{ version }}"
    version="${version#v}"
    tag="v${version}"
    if [ -n "$(git status --porcelain)" ]; then
        echo "error: working tree is not clean; commit or stash changes first" >&2
        exit 1
    fi
    if git rev-parse "$tag" >/dev/null 2>&1; then
        echo "error: tag $tag already exists" >&2
        exit 1
    fi
    echo "Running pre-release checks..."
    go test ./...
    goreleaser check
    echo "Tagging and pushing $tag..."
    git tag -a "$tag" -m "Release $tag"
    git push origin "$tag"
    echo "Pushed $tag — the release workflow will now build and publish it."
