{
  description = "Wuko — trusted workflow runner for everyday development tasks";

  # 26.05 is the last Nixpkgs release supporting Intel Macs.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-darwin"
        "x86_64-linux"
      ];
      lib = nixpkgs.lib;
      forAllSystems = lib.genAttrs systems;
      pkgsFor = system: nixpkgs.legacyPackages.${system};

      # Only Go sources and embedded assets reach the build, so documentation edits do not
      # rebuild Wuko and path: builds never copy untracked output such as dist/ into the store.
      # docs/ holds a separate demo module whose Go files must stay out of the main module.
      goFiles = lib.fileset.fileFilter (file: file.hasExt "go") ./.;
      src = lib.fileset.toSource {
        root = ./.;
        fileset = lib.fileset.unions [
          ./go.mod
          ./go.sum
          ./cmd
          ./scriptsdk
          ./skills
          (lib.fileset.difference goFiles ./docs)
        ];
      };

      # Match the RFC 3339 build date stamped by GoReleaser and just install.
      stamp = self.lastModifiedDate;
      part = start: length: builtins.substring start length stamp;
      date = "${part 0 4}-${part 4 2}-${part 6 2}T${part 8 2}:${part 10 2}:${part 12 2}Z";

      # A flake cannot tell a release tag from a later commit, so action/version only names the
      # base release; the commit in the build metadata identifies what was actually built.
      release = lib.removeSuffix "\n" (builtins.readFile ./action/version);
      shortRev = self.shortRev or self.dirtyShortRev or null;
      version = if shortRev == null then release else "${release}+${shortRev}";
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          wuko = pkgs.callPackage ./nix/package.nix {
            inherit src date version;
            commit = self.rev or self.dirtyRev or "unknown";
          };
        in
        {
          inherit wuko;
          default = wuko;
        }
      );

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.wuko}/bin/wuko";
          meta.description = self.packages.${system}.wuko.meta.description;
        };
        wuko = self.apps.${system}.default;
      });

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go_1_26
              pkgs.git
              pkgs.just
            ];
          };
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);

      checks = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          wuko = self.packages.${system}.wuko;
        in
        {
          package = wuko;
          smoke = pkgs.runCommand "wuko-smoke" { nativeBuildInputs = [ wuko ]; } ''
            wuko --version
            wuko --help > /dev/null
            test -s ${wuko}/share/bash-completion/completions/wuko.bash
            test -s ${wuko}/share/zsh/site-functions/_wuko
            test -s ${wuko}/share/fish/vendor_completions.d/wuko.fish
            wuko --env-loader none run --file ${./nix/smoke.yaml} --once --reporter plain > output
            grep -q 'nix-workflow-ready' output
            touch "$out"
          '';
        }
      );
    };
}
