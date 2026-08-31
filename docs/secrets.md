# Secrets

Wuko can resolve secret values lazily through the 1Password and Bitwarden CLIs. Secret references
work in Go templates and Expr expressions. A value is fetched only when the containing template or
expression runs, and each successful reference is cached for one workflow occurrence.

```yaml
version: 1
name: publish

secrets:
  ensure_auth:
    - provider: op
      login:
        native: true
    - provider: bw
      login:
        native: true

env:
  API_TOKEN: '{{ secret "op://Production/API/token" }}'

steps:
  - id: credentials
    type: set
    with:
      variable: registry_password
      expr: 'secret("bw://password/Container%20Registry")'
```

## References

1Password references use the native secret-reference URI and are passed to `op read --no-newline`:

```gotemplate
{{ secret "op://Production/API/token" }}
```

Bitwarden references use `bw://<selector>/<item>`. Supported selectors are `password`, `username`,
`uri`, `totp`, and `notes`. Percent-encode spaces and other URI characters in item names:

```text
bw://password/Container%20Registry
bw://username/Container%20Registry
```

The helper also accepts a dynamically constructed reference:

```yaml
if: 'secret("bw://password/" + vars.item) != ""'
```

## Authentication preflight

The preflight runs only for commands that execute a workflow: `wuko run`, `wuko ui`, and the
`install` and `uninstall` lifecycles. Read-only inspection (`wuko validate`, `wuko tree`) never
authenticates, so it cannot prompt for a login or run a fallback login command; a `secret()` call
made while validating reaches the provider with whatever session the environment already has.

Each `ensure_auth` entry first checks the provider (`op whoami` or `bw status`). Omitting `login`
makes the entry check-only. Native login is attempted only in an interactive terminal:

```yaml
secrets:
  ensure_auth:
    - provider: op
      login:
        native: true
```

A native `op signin` that returns a session token stores it as `OP_SESSION_<account>`, the variable
the 1Password CLI reads. The account is resolved from `op account list`, so it requires exactly one
configured account; with several accounts, sign in through a fallback command that sets
`session_env` itself. The desktop app integration manages its own session and needs neither.

For automation or organization-specific authentication, declare a fallback command. When
`session_env` is present, Wuko captures trimmed stdout into the provider's private environment. It
is not added to workflow `env` and is not inherited by workflow steps.

```yaml
secrets:
  ensure_auth:
    - provider: bw
      login:
        native: true
        fallback:
          command: company-vault-login
          args: [bitwarden]
          session_env: BW_SESSION
```

A shell script is also supported. The default shell is `/bin/sh`. Entries in `args` are passed to
the script as `$1`, `$2`, and so on:

```yaml
secrets:
  ensure_auth:
    - provider: bw
      login:
        fallback:
          script: 'bw unlock --passwordenv BW_PASSWORD --raw'
          session_env: BW_SESSION
```

After either login method, Wuko checks authentication again before continuing. In a headless run,
native login is skipped; the fallback must succeed or workflow preparation fails. A provider check
that cannot report a state is treated as unauthenticated so the configured fallback still runs;
a missing provider binary fails immediately.

## Sessions

One session covers a workflow occurrence and every workflow it depends on: authentication happens
once, the resolve cache is shared, and one redaction set covers the whole run. The session is
detached from run cancellation so a `finally` or cleanup step can still resolve a secret after
Ctrl-C; for the same reason, a step `timeout` does not bound the provider CLI call it makes.

Secret values and captured session tokens are kept in memory and are redacted from error messages
and `--debug` diagnostics. Values shorter than six characters are not redacted: substitution is
textual, and a short value would rewrite unrelated output. Avoid printing a resolved value or
placing it in an ordinary step output: workflow stdout, files, and intentional outputs are not
rewritten.
