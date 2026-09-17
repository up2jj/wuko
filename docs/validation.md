# Validation diagnostics

Wuko validates a workflow graph before command-specific work begins. `run`, `run --dry-run`,
`validate`, `tree`, browser runs, Git-hook checks, lifecycle commands, and scheduled reloads share
the same preflight path. Parsing and structural validation gate later phases; independent workflows,
dependency branches, and top-level step configurations are collected when it is safe to continue.
Cancellation and unrecoverable filesystem, network, or plugin transport failures still stop
immediately.

Terminal diagnostics include a stable issue code, logical YAML source, source span, YAML path,
redacted excerpt, suggestion, related declarations, and the total issue count. Machine consumers can
request one versioned document:

```sh
wuko validate --format json
wuko validate --reporter github
```

The JSON document never contains source excerpts or wrapped causes. URL credentials, queries, and
fragments are removed from presented source names. The GitHub reporter emits one deduplicated
annotation per issue and uses repository-relative spans when available.

## Package ownership

- `validation` owns issue codes, common wording, paths, spans, collection, source indexing,
  redaction, suggestions, extraction, and the safe JSON projection.
- Workflow, engine, control, and step packages own semantic rules and return structured facts.
- `cmd` owns preflight orchestration; command implementations do not reinterpret error strings.
- Terminal, GitHub, browser, Git-hook, debug, and JSON adapters only render the shared issue model.
- Step output schemas live in the registration beside their builders. Composite actions derive
  schemas from declared outputs; plugin declarations may provide a recursive schema and otherwise
  remain open for compatibility.

`step.Register` remains available to external integrations as an open-schema compatibility
shorthand. New steps should use `step.RegisterDefinition` and choose a closed schema whenever their
keys are statically known. Wuko checks produced result keys against that registration at runtime.
