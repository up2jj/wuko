# Nix and devenv

[Back to the README](../README.md)

Wuko's flake builds from source on Linux and macOS, on x86-64 and ARM64. It includes Bash, Zsh,
and Fish completions. Enable Nix's `nix-command` and `flakes` experimental features before
using these commands.

The flake uses Nixpkgs 26.05 to retain Intel Mac support; later Nixpkgs releases dropped that target.

## Run or install

```sh
nix run github:up2jj/wuko -- --help
nix profile add github:up2jj/wuko
wuko --version
```

`nix profile add` requires Nix 2.26 or newer; older releases name it `nix profile install`.

The default package and app are also available by the name `wuko`, for example
`nix build github:up2jj/wuko#wuko`. Pin a Git revision in the URL when an installation must stay
on a particular version: `github:up2jj/wuko/<commit>`.

Wuko uses tools from its environment. Add Git, language runtimes, Docker, or other tools required
by your workflows to the same shell. Installing Wuko does not install those tools or start services.

## Use Wuko with devenv

Add Wuko as a flake input in your project's `devenv.yaml`:

```yaml
inputs:
  wuko:
    url: github:up2jj/wuko
```

Then include the package in `devenv.nix`:

```nix
{ inputs, pkgs, ... }:
{
  packages = [
    inputs.wuko.packages.${pkgs.stdenv.hostPlatform.system}.default
    pkgs.git
  ];
}
```

Keep any existing inputs and packages in these files. Wuko's input uses its own pinned Nixpkgs
and Go toolchain; it does not need to follow the project's Nixpkgs input.

```sh
devenv shell -- wuko --version
devenv shell -- wuko run check --once
```

The second command assumes your project has a workflow named `check`. Wuko and its local shell
steps inherit the active devenv environment. Commit `devenv.yaml`, `devenv.nix`, and the generated
`devenv.lock` to pin the environment, including Wuko. Use `devenv update wuko` to update that input.

Devenv's [inputs](https://devenv.sh/inputs/) and [packages](https://devenv.sh/packages/)
documentation describes the configuration in more detail.

### Run steps through the devenv executor

Wuko can also enter a project's devenv environment for a block of steps. This requires devenv 2.2
or newer on the invoking PATH. For example, create `.wuko/workflows/check.yaml`:

```yaml
version: 1
name: check
steps:
  - executor:
      type: devenv
      with:
        directory: .
        secrets: {mode: disabled}
    steps:
      - id: git_version
        type: shell
        with:
          command: git
          args: [--version]
```

Run it with `wuko run check --once`, or through `devenv shell -- wuko run check --once` when Wuko
is installed only in that environment. The executor enters `devenv shell` when needed and reuses
a matching active environment. A requested directory or profile set that differs from the active
environment is rejected.

SecretSpec is disabled in this example so no secret configuration is needed. The
[devenv executor guide](executors.md#devenv-executor) covers profiles, tasks, managed processes,
and secrets. Only supported steps execute through devenv; HTTP, persistence, TUI, and Lua steps
remain on the host.

## Use Wuko in another flake

Add the input alongside your existing flake inputs:

```nix
inputs.wuko.url = "github:up2jj/wuko";
```

Include `wuko` in your `outputs` arguments, then add its package to a development shell:

```nix
devShells.${system}.default = pkgs.mkShell {
  packages = [ wuko.packages.${system}.default ];
};
```

Here `system` and `pkgs` are the system name and Nixpkgs instance from your existing flake.
Commit the generated `flake.lock` to pin Wuko. Update it with `nix flake update wuko`.

## Develop and check Wuko

From a checkout:

```sh
nix develop
just build
just test
just vet
just lint
```

The development shell provides Go 1.26, Git, and `just`. The flake pins Nixpkgs to a revision whose
Go toolchain satisfies `go.mod`; the package build disables automatic toolchain downloads.

```sh
nix build .#wuko
nix run . -- --version
nix flake check -L
nix fmt -- --check flake.nix nix/package.nix
```

New files must be tracked by Git for a local Git flake to see them. To check an unstaged addition,
use a path input instead, for example `nix build path:.#wuko`.

`nix flake check` builds Wuko, verifies installed completions, and executes a small workflow.
The full Go test suite runs separately with `just test`, since it includes plugin builds and
external-service integration tests. The package has no runtime dependency on the Go compiler.

When Go dependencies change, update `vendorHash` in `nix/package.nix`: replace it temporarily with
`lib.fakeHash`, run `nix build .#wuko`, and copy the reported `got: sha256-...` hash into the file.
Rebuild and run `nix flake check` to verify it. Update the Nixpkgs pin with `nix flake update nixpkgs`
when a newer Go toolchain is required. Build version metadata comes from `action/version` and the
flake's source revision and timestamp. Because a flake cannot tell a release tag from a later
commit, the version names the last release plus the commit, for example `v0.20.0+3b4f4d9`.
