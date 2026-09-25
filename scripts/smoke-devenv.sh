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

  tasks."smoke:prepare" = {
    exec = ''
      test "$DEVENV_TASK_INPUT" = '{"value":"ok"}'
      echo '{"prepared":true,"count":2}' > "$DEVENV_TASK_OUTPUT_FILE"
      export WUKO_TASK_EXPORT=task-export
    '';
    exports = [ "WUKO_TASK_EXPORT" ];
  };

  tasks."smoke:build" = {
    exec = ''
      test "$DEVENV_TASK_INPUT" = '{"value":"ok"}'
      echo '{"artifact":"dist/app"}' > "$DEVENV_TASK_OUTPUT_FILE"
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

cat >"$fixture/typed.yaml" <<'EOF'
version: 1
name: devenv-typed-output-smoke
steps:
  - executor:
      type: devenv
      with:
        directory: .
        profiles: [smoke-a, smoke-b]
        secrets: {mode: disabled}
    steps:
      - id: task
        type: devenv_task
        with:
          names: [smoke:prepare, smoke:build]
          mode: single
          inputs: {value: ok}
          show_output: false
          capture_limit: 1MiB
      - id: task_decoded
        type: shell
        with:
          command: sh
          args: [-c, 'test "$DECODED" = true']
          env:
            DECODED: '{{ .steps.task.value_decoded }}'
      - id: task_output
        type: shell
        with:
          command: sh
          args: [-c, 'test "$PREPARED" = true && test "$COUNT" = 2 && test "$ARTIFACT" = dist/app && test "$EXPORTED" = task-export && test -z "${WUKO_TASK_EXPORT:-}"']
          env:
            PREPARED: '{{ index .steps.task.value "smoke:prepare" "prepared" }}'
            COUNT: '{{ index .steps.task.value "smoke:prepare" "count" }}'
            ARTIFACT: '{{ index .steps.task.value "smoke:build" "artifact" }}'
            EXPORTED: '{{ index .steps.task.value "smoke:prepare" "devenv" "env" "WUKO_TASK_EXPORT" }}'
EOF

# The same typed-value assertions under runtime SecretSpec, which wraps the invocation as
# `devenv shell -- secretspec run -- devenv tasks run`. Kept separate from typed.yaml so a
# failure localizes itself: both red means typed values are broken generally, this one red
# alone means a wrapper writes to the stdout the typed value is decoded from.
cat >"$fixture/typed-secrets.yaml" <<'EOF'
version: 1
name: devenv-typed-output-secrets-smoke
steps:
  - executor:
      type: devenv
      with:
        directory: .
        profiles: [smoke-a, smoke-b]
        secrets: {mode: runtime, profile: smoke, provider: env}
    steps:
      - id: task
        type: devenv_task
        with:
          names: [smoke:prepare, smoke:build]
          mode: single
          inputs: {value: ok}
          show_output: false
          capture_limit: 1MiB
  - id: task_decoded
    type: assert
    with:
      expr: steps.task.value_decoded == true
      message: "devenv tasks run stdout was not decodable under devenv shell + secretspec run"
  - id: task_output
    type: assert
    with:
      expr: >-
        steps.task.value["smoke:prepare"]["prepared"] == true &&
        steps.task.value["smoke:prepare"]["count"] == 2 &&
        steps.task.value["smoke:build"]["artifact"] == "dist/app"
      message: "typed task values did not survive the runtime SecretSpec wrapper"
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
assert_no_secret_leak() {
  if grep -Fq "$WUKO_SMOKE_SECRET" <<<"$1"; then
    echo "secret value leaked into captured Wuko output" >&2
    return 1
  fi
}

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
  assert_no_secret_leak "$output"
}

run_typed_workflow() {
  local file="$1"
  local output
  if ! output="$(cd "$fixture" && "$binary" run --file "$file" 2>&1)"; then
    printf '%s\n' "$output" >&2
    return 1
  fi
  printf '%s\n' "$output"
  # Runtime SecretSpec puts the secret in the task environment and the typed value is
  # materialized into workflow state, so a leak here means devenv echoed the environment
  # or Wuko captured more than the JSON document.
  assert_no_secret_leak "$output"
}

run_workflow
run_typed_workflow typed.yaml
run_typed_workflow typed-secrets.yaml
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
