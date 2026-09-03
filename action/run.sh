#!/usr/bin/env bash
set -euo pipefail

die() {
  echo "wuko action: $*" >&2
  exit 1
}

[[ -x "$WUKO_BINARY" ]] || die "Wuko binary is not executable: $WUKO_BINARY"

has_workflow=false
has_definition=false
[[ -n "$INPUT_WORKFLOW" ]] && has_workflow=true
[[ -n "$INPUT_DEFINITION" ]] && has_definition=true
if [[ "$has_workflow" == "$has_definition" ]]; then
  die "exactly one of workflow or definition must be provided"
fi

if [[ "$INPUT_WORKING_DIRECTORY" = /* ]]; then
  run_dir="$INPUT_WORKING_DIRECTORY"
else
  run_dir="${GITHUB_WORKSPACE:?GITHUB_WORKSPACE is required}/$INPUT_WORKING_DIRECTORY"
fi
[[ -d "$run_dir" ]] || die "working directory does not exist: $INPUT_WORKING_DIRECTORY"
cd "$run_dir"

temp_dir=""
cleanup() {
  if [[ -n "$temp_dir" ]]; then
    rm -rf "$temp_dir"
  fi
}
trap cleanup EXIT

command=("$WUKO_BINARY" run)
if [[ "$has_definition" == true ]]; then
  command+=(--file -)
elif [[ -f "$INPUT_WORKFLOW" ]]; then
  command+=(--file "$INPUT_WORKFLOW")
else
  command+=("$INPUT_WORKFLOW")
fi
if [[ -n "$INPUT_TARGET" ]]; then
  command+=("$INPUT_TARGET")
fi
if [[ -n "$INPUT_VARS" ]]; then
  trimmed_vars="${INPUT_VARS#"${INPUT_VARS%%[![:space:]]*}"}"
  if [[ "$trimmed_vars" == \{* ]]; then
    temp_dir="$(mktemp -d "${RUNNER_TEMP:?RUNNER_TEMP is required}/wuko-action-run.XXXXXX")"
    printf '%s' "$INPUT_VARS" > "$temp_dir/vars.json"
    command+=(--var-file "$temp_dir/vars.json")
  else
    while IFS= read -r entry || [[ -n "$entry" ]]; do
      entry="${entry%$'\r'}"
      [[ -z "$entry" ]] && continue
      command+=(--var "$entry")
    done <<< "$INPUT_VARS"
  fi
fi
command+=(--once --reporter plain --reporter github)
if [[ "$has_definition" == true ]]; then
  printf '%s' "$INPUT_DEFINITION" | "${command[@]}"
else
  "${command[@]}"
fi
