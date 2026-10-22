# Loaded by noninteractive Bash before executing user code. Only Bash builtins
# and the existing Wuko executable are needed, even with an empty PATH.
wuko_output()       { "$WUKO_SDK_EXECUTABLE" __script-sdk output "$@"; }
wuko_output_json()  { "$WUKO_SDK_EXECUTABLE" __script-sdk output-json "$@"; }
wuko_set_var()      { "$WUKO_SDK_EXECUTABLE" __script-sdk set-var "$@"; }
wuko_set_var_json() { "$WUKO_SDK_EXECUTABLE" __script-sdk set-var-json "$@"; }
wuko_get()          { "$WUKO_SDK_EXECUTABLE" __script-sdk get "$@"; }
wuko_get_json()     { "$WUKO_SDK_EXECUTABLE" __script-sdk get-json "$@"; }
wuko_helper()       { "$WUKO_SDK_EXECUTABLE" __script-sdk helper "$@"; }
wuko_helper_json()  { "$WUKO_SDK_EXECUTABLE" __script-sdk helper-json "$@"; }
wuko_secret()       { "$WUKO_SDK_EXECUTABLE" __script-sdk secret "$@"; }
wuko_render()       { "$WUKO_SDK_EXECUTABLE" __script-sdk render "$@"; }
wuko_step()         { "$WUKO_SDK_EXECUTABLE" __script-sdk step "$@"; }
wuko_run_workflow() { "$WUKO_SDK_EXECUTABLE" __script-sdk run-workflow "$@"; }
wuko_call()         { "$WUKO_SDK_EXECUTABLE" __script-sdk call "$@"; }
