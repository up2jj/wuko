#!/bin/sh
# Keep local development, CI, and pre-push hooks on the same Staticcheck release.
set -eu

if [ "$#" -eq 0 ]; then
    set -- ./...
fi

# Staticcheck 2026.1 supports the project's Go 1.26 toolchain.
exec go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 "$@"
