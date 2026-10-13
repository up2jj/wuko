# Trust model

[Back to the README](../README.md)

Wuko is a local automation tool, not a sandbox. Treat every workflow, composite action, and plugin
you run as trusted code.

## What workflows can do

- Lua, shell, agent, Docker, and command-based action sources can access local resources with the
  permissions granted to Wuko.
- Local file workflows and local path actions may explicitly run `shell` and `process` steps as root
  with `with.elevated: true`. This is arbitrary sudo execution, not a restricted helper. Remote,
  stdin, and executor-scoped definitions cannot elevate. See
  [Elevated shell and process commands](steps-automation.md#elevated-shell-and-process-commands).
- Safe archive extraction is not an execution sandbox.
- Do not mount the Docker socket for untrusted workflows.

## Remote workflows, actions, and plugins

- Review publishers before running remote workflows or installing marketplace packages.
- Pin immutable remote action releases with SHA-256, and pin GitHub-hosted actions to a commit
  reference when reproducibility matters. See [Composite actions](execution.md#composite-actions).
- Executable plugins run as local processes; see the
  [plugin trust model](plugins.md) before installing one.
- Pin the [GitHub Action](github-actions.md) to a release tag or full commit SHA.

## Secrets

Wuko does not provide a secrets store. It can resolve values lazily through the 1Password and
Bitwarden CLIs, and diagnostic output is redacted. Pass CI secrets through environment variables
rather than interpolating them into variables or workflow definitions. See [Secrets](secrets.md).
