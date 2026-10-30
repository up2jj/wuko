{
  lib,
  buildGo126Module,
  installShellFiles,
  stdenv,
  src,
  version,
  commit ? "unknown",
  date ? "unknown",
}:

buildGo126Module {
  pname = "wuko";
  inherit src version;

  vendorHash = "sha256-AuMDilAzQT/RIPqOB/3BnT8WcCPMTaJI1jdVhATlTd0=";
  subPackages = [ "." ];
  env.CGO_ENABLED = "0";

  ldflags = [
    "-s"
    "-w"
    "-X github.com/up2jj/wuko/cmd.version=v${version}"
    "-X github.com/up2jj/wuko/cmd.commit=${commit}"
    "-X github.com/up2jj/wuko/cmd.date=${date}"
  ];

  # The Go suite includes plugin builds and external services; run it with just test.
  # flake checks exercise the installed binary and its shell completions instead.
  doCheck = false;

  nativeBuildInputs = [ installShellFiles ];
  postInstall = lib.optionalString (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
    installShellCompletion --cmd wuko \
      --bash <("$out/bin/wuko" completion bash) \
      --zsh <("$out/bin/wuko" completion zsh) \
      --fish <("$out/bin/wuko" completion fish)
  '';

  meta = {
    description = "Trusted workflow runner for everyday development tasks";
    homepage = "https://github.com/up2jj/wuko";
    mainProgram = "wuko";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}
