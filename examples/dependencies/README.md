# Workflow dependency examples

Run these examples from this directory so Wuko discovers `.wuko/workflows/`:

```sh
wuko validate release
wuko tree release
wuko run release --dry-run
wuko run release
wuko run build --var cached=true
wuko run nightly-release --once
```

`release` demonstrates both a dependency chain and a diamond. `prepare` is required by `build` and
`checks`, but runs once. `release` consumes the typed outputs of its direct dependencies. `build`
shows an early return satisfying its declared output contract, and `nightly-release` shows a
scheduled root invoking its prerequisites immediately.
