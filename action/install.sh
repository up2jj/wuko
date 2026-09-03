#!/usr/bin/env bash
set -euo pipefail

die() {
  echo "wuko action: $*" >&2
  exit 1
}

if [[ -n "${WUKO_ACTION_TEST_BINARY:-}" ]]; then
  [[ -x "$WUKO_ACTION_TEST_BINARY" ]] || die "test binary is not executable: $WUKO_ACTION_TEST_BINARY"
  printf 'path=%s\n' "$WUKO_ACTION_TEST_BINARY" >> "$GITHUB_OUTPUT"
  exit 0
fi

version="${INPUT_VERSION#v}"
if [[ -z "$version" ]]; then
  version="$(tr -d '[:space:]' < "$WUKO_ACTION_PATH/action/version")"
fi
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || die "invalid Wuko version: $version"

case "${RUNNER_OS:-}" in
  Linux) os=linux ;;
  macOS) os=darwin ;;
  *) die "unsupported runner operating system: ${RUNNER_OS:-unknown}; Wuko supports Linux and macOS" ;;
esac

case "${RUNNER_ARCH:-}" in
  X64) arch=amd64 ;;
  ARM64) arch=arm64 ;;
  *) die "unsupported runner architecture: ${RUNNER_ARCH:-unknown}; Wuko supports X64 and ARM64" ;;
esac

cache_root="${RUNNER_TOOL_CACHE:-${RUNNER_TEMP:?RUNNER_TEMP is required}/wuko-tool-cache}"
install_dir="$cache_root/wuko/$version/$arch"
binary="$install_dir/wuko"
if [[ -x "$binary" ]]; then
  printf 'path=%s\n' "$binary" >> "$GITHUB_OUTPUT"
  exit 0
fi

archive="wuko_${version}_${os}_${arch}.tar.gz"
release_url="https://github.com/up2jj/wuko/releases/download/v${version}"
temp_dir="$(mktemp -d "${RUNNER_TEMP:?RUNNER_TEMP is required}/wuko-action.XXXXXX")"
trap 'rm -rf "$temp_dir"' EXIT

curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 --retry 3 \
  --output "$temp_dir/$archive" "$release_url/$archive"
curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 --retry 3 \
  --output "$temp_dir/checksums.txt" "$release_url/checksums.txt"

expected="$(awk -v archive="$archive" '$2 == archive { print $1 }' "$temp_dir/checksums.txt")"
[[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "release checksum is missing or invalid for $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$temp_dir/$archive" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$temp_dir/$archive" | awk '{ print $1 }')"
fi
[[ "$actual" == "$expected" ]] || die "checksum verification failed for $archive"

mkdir -p "$temp_dir/extract" "$install_dir"
tar -xzf "$temp_dir/$archive" -C "$temp_dir/extract"
[[ -f "$temp_dir/extract/wuko" ]] || die "release archive does not contain the wuko binary"
candidate="$install_dir/.wuko.$$"
install -m 0755 "$temp_dir/extract/wuko" "$candidate"
mv -f "$candidate" "$binary"
printf 'path=%s\n' "$binary" >> "$GITHUB_OUTPUT"
