# Loaded by noninteractive Bash before executing user code. Calls use only Bash
# builtins; the executable client remains a fallback for builds without /dev/tcp.
__wuko_sdk_call() {
    # Probe on first use, so Bash processes that never call the SDK open no
    # connection. Probing before a request avoids retrying one that might have
    # performed external work. Older builds can disable /dev/tcp, and the host
    # leaves the port empty when it has no IPv4 loopback.
    if [[ -n $__wuko_sdk_port ]] && { builtin : 9<>/dev/tcp/127.0.0.1/"$__wuko_sdk_port"; } 2>/dev/null; then
        __wuko_sdk_call() { __wuko_sdk_builtin_call "$@"; }
    else
        __wuko_sdk_call() { "$WUKO_SDK_EXECUTABLE" __script-sdk "$@"; }
    fi
    __wuko_sdk_call "$@"
}

__wuko_sdk_builtin_call() {
    # Keep the private bearer token out of xtrace while preserving user tracing.
    local __wuko_trace= __wuko_result __wuko_connected= __wuko_attempts=0
    case $- in *x*) __wuko_trace=1; set +x ;; esac
    # A failed connect sends nothing, so retrying it is safe. Bash 3.2 does not
    # restart connect after a signal, such as SIGCHLD from a finished job. The
    # transport reports through fd 8 so connect errors can be silenced.
    while :; do
        if __wuko_sdk_transport "$@" 8>&2 2>/dev/null; then __wuko_result=0; else __wuko_result=$?; fi
        [[ -n $__wuko_connected ]] && break
        if (( ++__wuko_attempts == 5 )); then
            builtin printf '%s\n' 'connecting to Bash SDK bridge failed' >&2
            break
        fi
    done
    if [[ $__wuko_trace == 1 ]]; then set -x; fi
    return "$__wuko_result"
}

__wuko_sdk_transport() {
    # Function redirections restore the caller's descriptor on return, including
    # on Bash 3.2. Each call owns its connection, including background jobs.
    __wuko_connected=1
    local LC_ALL=C
    local __wuko_arg __wuko_length=0 __wuko_header __wuko_line __wuko_status __wuko_out __wuko_err
    for __wuko_arg in "$@"; do
        __wuko_length=$((__wuko_length + ${#__wuko_arg} + 1))
    done
    if (( __wuko_length > 10485760 )); then
        builtin printf '%s\n' 'Bash SDK request exceeds 10 MiB' >&8
        return 1
    fi
    builtin printf -v __wuko_header 'POST /v1/bash HTTP/1.0\r\nAuthorization: Bearer %s\r\nContent-Length: %s\r\n\r\n' "$__wuko_sdk_token" "$__wuko_length"
    if ! builtin printf '%s\0' "$__wuko_header$@" >&9; then
        builtin printf '%s\n' 'writing Bash SDK request failed' >&8
        return 1
    fi
    if ! IFS= builtin read -r __wuko_line <&9 || [[ $__wuko_line != HTTP/*' 200 '* ]]; then
        builtin printf '%s\n' 'Bash SDK bridge request failed' >&8
        return 1
    fi
    while IFS= builtin read -r __wuko_line <&9; do
        [[ $__wuko_line == $'\r' ]] && break
    done
    if ! IFS= builtin read -r -d '' __wuko_status <&9 ||
       ! IFS= builtin read -r -d '' __wuko_out <&9 ||
       ! IFS= builtin read -r -d '' __wuko_err <&9 ||
       [[ $__wuko_status != 0 && $__wuko_status != 1 ]]; then
        builtin printf '%s\n' 'reading Bash SDK response failed' >&8
        return 1
    fi
    builtin printf '%b' "$__wuko_out" || return 1
    builtin printf '%b' "$__wuko_err" >&8 || return 1
    return "$__wuko_status"
} 9<>/dev/tcp/127.0.0.1/"$__wuko_sdk_port"

wuko_output()       { __wuko_sdk_call output "$@"; }
wuko_output_json()  { __wuko_sdk_call output-json "$@"; }
wuko_set_var()      { __wuko_sdk_call set-var "$@"; }
wuko_set_var_json() { __wuko_sdk_call set-var-json "$@"; }
wuko_get()          { __wuko_sdk_call get "$@"; }
wuko_get_json()     { __wuko_sdk_call get-json "$@"; }
wuko_helper()       { __wuko_sdk_call helper "$@"; }
wuko_helper_json()  { __wuko_sdk_call helper-json "$@"; }
wuko_secret()       { __wuko_sdk_call secret "$@"; }
wuko_render()       { __wuko_sdk_call render "$@"; }
wuko_step()         { __wuko_sdk_call step "$@"; }
wuko_run_workflow() { __wuko_sdk_call run-workflow "$@"; }
wuko_call()         { __wuko_sdk_call call "$@"; }
