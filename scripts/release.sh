#!/usr/bin/env bash
# Prepare, tag, and push a Wuko release from a local checkout of master.
# The pushed tag starts .github/workflows/release.yml, which publishes the release.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/release.sh [--dry-run] [--yes] [--no-watch] <patch|minor|major|X.Y.Z>

Bumps the release version in action/version, cmd/plugin.go, and the documented
up2jj/wuko@vX.Y.Z references, runs the release checks, commits
"chore: prepare vX.Y.Z release", tags it, and pushes master and the tag together.

  --dry-run   show the version changes and run the checks without committing or pushing
  --yes       do not ask for confirmation before committing and pushing
  --no-watch  do not follow the GitHub release workflow after pushing

When action/version already names the requested version, for example after an
interrupted run, the prepare commit is skipped and HEAD is tagged.
EOF
}

die() {
    echo "error: $*" >&2
    exit 1
}

dry_run=0
assume_yes=0
watch=1
request=""
for arg in "$@"; do
    case "$arg" in
        --dry-run) dry_run=1 ;;
        --yes) assume_yes=1 ;;
        --no-watch) watch=0 ;;
        -h | --help) usage; exit 0 ;;
        -*) die "unknown option $arg" ;;
        *)
            [ -z "$request" ] || die "expected one version argument"
            request="$arg"
            ;;
    esac
done
[ -n "$request" ] || { usage >&2; exit 2; }

cd "$(git rev-parse --show-toplevel)"

semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
current="$(tr -d '[:space:]' < action/version)"
[[ "$current" =~ $semver ]] || die "action/version holds $current, which is not X.Y.Z"
major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}"

case "${request#v}" in
    patch) version="$major.$minor.$((patch + 1))" ;;
    minor) version="$major.$((minor + 1)).0" ;;
    major) version="$((major + 1)).0.0" ;;
    *)
        version="${request#v}"
        [[ "$version" =~ $semver ]] || die "$request is not patch, minor, major, or X.Y.Z"
        ;;
esac
tag="v$version"

# The newest released tag is the baseline; action/version equal to the request means the
# prepare commit already exists and only tagging remains.
if [ "$version" = "$current" ]; then
    prepare=0
else
    prepare=1
    newest="$(printf '%s\n%s\n' "$current" "$version" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)"
    [ "$newest" = "$version" ] || die "$tag is older than the current version v$current"
fi

[ "$(git symbolic-ref --quiet --short HEAD || true)" = master ] || die "releases are cut from master"
[ -z "$(git status --porcelain)" ] || die "working tree is not clean; commit or stash changes first"
# A local tag on HEAD is left over from an interrupted push and is pushed as is.
tagged=0
if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
    [ "$prepare" -eq 0 ] && [ "$(git rev-parse "$tag^{commit}")" = "$(git rev-parse HEAD)" ] ||
        die "tag $tag already exists locally and does not point at HEAD"
    tagged=1
fi

echo "Fetching origin..."
git fetch --quiet origin master
git merge-base --is-ancestor origin/master HEAD || die "master is behind origin/master; pull first"
[ -z "$(git ls-remote --tags origin "refs/tags/$tag")" ] || die "tag $tag already exists on origin"

restore() {
    git checkout --quiet -- .
}

if [ "$prepare" -eq 1 ]; then
    echo "Preparing $tag (from v$current)..."
    trap restore EXIT
    printf '%s\n' "$version" > action/version
    perl -pi -e 's/^const scaffoldSDKFallbackVersion = "v\Q'"$current"'\E"$/const scaffoldSDKFallbackVersion = "'"$tag"'"/' cmd/plugin.go
    { git grep -lF "up2jj/wuko@v$current" -- '*.md' || true; } | while IFS= read -r file; do
        perl -pi -e 's{up2jj/wuko\@v\Q'"$current"'\E\b}{up2jj/wuko\@'"$tag"'}g' "$file"
    done
    grep -qF "\"$tag\"" cmd/plugin.go || die "cmd/plugin.go has no scaffoldSDKFallbackVersion for v$current"
    git --no-pager diff --stat
else
    echo "action/version already names $version; tagging HEAD without a prepare commit."
fi

echo "Running release checks..."
go vet ./...
go test ./...
goreleaser check

if [ "$dry_run" -eq 1 ]; then
    [ "$prepare" -eq 0 ] || git --no-pager diff
    echo "Dry run complete; no commit, tag, or push was made."
    exit 0
fi

if [ "$assume_yes" -eq 0 ]; then
    read -r -p "Commit, tag, and push $tag to origin? [y/N] " answer
    case "$answer" in
        y | Y | yes) ;;
        *) die "release cancelled" ;;
    esac
fi

if [ "$prepare" -eq 1 ]; then
    git commit --quiet --all --message "chore: prepare $tag release"
    trap - EXIT
fi
[ "$tagged" -eq 1 ] || git tag --annotate "$tag" --message "Release $tag"
# Push both refs in one transaction so a rejected master never leaves a published tag behind.
git push --atomic origin master "$tag"
echo "Pushed $tag; the release workflow builds and publishes it."

if [ "$watch" -eq 0 ]; then
    exit 0
fi
if ! command -v gh >/dev/null 2>&1; then
    echo "gh is not installed; follow the release in the repository's Actions tab."
    exit 0
fi

sha="$(git rev-parse HEAD)"
run=""
for _ in $(seq 1 30); do
    run="$(gh run list --workflow release.yml --commit "$sha" --event push --limit 1 --json databaseId --jq '.[0].databaseId // empty')"
    [ -n "$run" ] && break
    sleep 2
done
[ -n "$run" ] || die "the release workflow for $tag did not start within a minute; check the Actions tab"
gh run watch "$run" --exit-status
gh release view "$tag" --json url --jq '"Released: " + .url'
