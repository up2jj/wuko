#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

required="${WUKO_SMOKE_REQUIRED:-}"
if [[ -z "$required" && -n "${CI:-}" ]]; then
  required=1
fi

for profile_bin in "${HOME:-}/.nix-profile/bin" "${HOME:-}/.local/state/nix/profile/bin" "/nix/var/nix/profiles/default/bin"; do
  if [[ -x "$profile_bin/devenv" || -x "$profile_bin/secretspec" ]]; then
    PATH="$profile_bin:$PATH"
    export PATH
    break
  fi
done

skip_or_fail() {
  if [[ -n "$required" ]]; then
    echo "FAIL: $1" >&2
    exit 1
  fi
  echo "SKIP: $1" >&2
  exit 0
}

if ! command -v devenv >/dev/null 2>&1; then
  skip_or_fail "devenv is not installed"
fi

if ! devenv --version >/dev/null 2>&1; then
  skip_or_fail "devenv is unavailable"
fi

if ! command -v secretspec >/dev/null 2>&1; then
  skip_or_fail "SecretSpec CLI is not installed"
fi

fixture="$(mktemp -d "${TMPDIR:-/tmp}/wuko-devenv-smoke.XXXXXX")"
temporary_cache=""
if [[ -z "${GOCACHE:-}" ]]; then
  temporary_cache="$(mktemp -d "${TMPDIR:-/tmp}/wuko-go-cache.XXXXXX")"
  export GOCACHE="$temporary_cache"
fi
cleanup() {
  if command -v devenv >/dev/null 2>&1; then
    (cd "$fixture" && devenv --profile smoke-a --profile smoke-b processes stop smoke-process >/dev/null 2>&1) || true
  fi
  rm -rf "$fixture"
  if [[ -n "$temporary_cache" ]]; then
    rm -rf "$temporary_cache"
  fi
}
trap cleanup EXIT

cat >"$fixture/devenv.nix" <<'EOF'
{ ... }:
{
  profiles = {
    smoke-a.module = { env.WUKO_SMOKE_A = "active"; };
    smoke-b.module = { env.WUKO_SMOKE_B = "active"; env.WUKO_SMOKE_ORDER = "b"; };
  };

  tasks."smoke:task" = {
    exec = ''
      test "$DEVENV_TASK_INPUT" = '{"value":"ok"}'
      printf '%s' task-ok
    '';
  };

  processes.smoke-process = {
    exec = "sleep 30";
    ready.exec = "true";
  };
}
EOF

cat >"$fixture/secretspec.toml" <<'EOF'
[project]
name = "wuko-devenv-smoke"
revision = "1.0"

[providers]
injected = "env"

[profiles.smoke]
WUKO_SMOKE_SECRET = { description = "Smoke-test secret", required = true, providers = ["injected"] }
EOF

cat >"$fixture/workflow.yaml" <<'EOF'
version: 1
name: devenv-smoke
steps:
  - executor:
      type: devenv
      with:
        directory: .
        profiles: [smoke-a, smoke-b]
        processes: [smoke-process]
        secrets:
          mode: runtime
          profile: smoke
          provider: env
    steps:
      - id: tool
        type: require_tool
        with: {tool: sh}
      - id: environment
        type: shell
        with:
          command: sh
          args: [-c, 'test "$WUKO_SMOKE_A" = active && test "$WUKO_SMOKE_B" = active && test "$WUKO_SMOKE_ORDER" = b && test "$WUKO_SMOKE_SECRET" = smoke-secret']
      - id: task
        type: devenv_task
        with:
          name: smoke:task
          mode: single
          inputs: {value: ok}
EOF

cat >"$fixture/mismatch.yaml" <<'EOF'
version: 1
name: devenv-smoke-mismatch
steps:
  - executor:
      type: devenv
      with:
        directory: .
        profiles: [smoke-a]
    steps:
      - id: check
        type: shell
        with: {command: true}
EOF

binary="$fixture/wuko"
go build -o "$binary" "$repo_root"

export WUKO_SMOKE_SECRET=smoke-secret
export SECRETSPEC_REASON="wuko devenv smoke test"
run_workflow() {
  local output
  local -a command
  if [[ "${1:-}" == active ]]; then
    command=(devenv --profile smoke-a --profile smoke-b shell -- "$binary" run --file workflow.yaml)
  else
    command=("$binary" run --file workflow.yaml)
  fi
  if ! output="$(cd "$fixture" && "${command[@]}" 2>&1)"; then
    printf '%s\n' "$output" >&2
    return 1
  fi
  printf '%s\n' "$output"
  if grep -Fq "$WUKO_SMOKE_SECRET" <<<"$output"; then
    echo "secret value leaked into captured Wuko output" >&2
    return 1
  fi
}

run_workflow
if (cd "$fixture" && devenv --profile smoke-a --profile smoke-b processes status smoke-process 2>/dev/null | grep -Eqi 'smoke-process.*(running|ready|started)'); then
  echo "Wuko-owned process was not cleaned up" >&2
  exit 1
fi

(cd "$fixture" && devenv --profile smoke-a --profile smoke-b processes start smoke-process >/dev/null)
run_workflow active
if ! (cd "$fixture" && devenv --profile smoke-a --profile smoke-b processes status smoke-process 2>/dev/null | grep -qi smoke-process); then
  echo "smoke process was unexpectedly stopped after reuse" >&2
  exit 1
fi
(cd "$fixture" && devenv --profile smoke-a --profile smoke-b processes stop smoke-process >/dev/null)

if (cd "$fixture" && devenv --profile smoke-a --profile smoke-b shell -- "$binary" run --file mismatch.yaml >/dev/null 2>&1); then
  echo "profile mismatch was not rejected" >&2
  exit 1
fi

echo "devenv smoke test passed"
