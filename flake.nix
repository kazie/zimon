{
  description = "zimon - a friendly CLI that manages Kiwix ZIM files";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = self.shortRev or self.dirtyShortRev or "dev";
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.buildGoModule {
          pname = "zimon";
          inherit version;
          src = pkgs.lib.cleanSource self;
          # Update after changing go.mod or importing a package not used before (only
          # imported packages are vendored): set to lib.fakeHash, build, copy the "got:" hash.
          vendorHash = "sha256-QlX9Gl9f7Wtxcx54J0DJXYdI7d1JdYXxkaDZx0ls2fY=";
          subPackages = [ "." ];
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];
          nativeBuildInputs = [ pkgs.installShellFiles ];
          postInstall = pkgs.lib.optionalString (pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform) ''
            installShellCompletion --cmd zimon \
              --bash <($out/bin/zimon completion bash) \
              --fish <($out/bin/zimon completion fish) \
              --zsh <($out/bin/zimon completion zsh)
            # Dated by the commit, not the build.
            SOURCE_DATE_EPOCH=${toString (self.lastModified or 315532800)} $out/bin/zimon man > zimon.1
            installManPage zimon.1
          '';
          meta = {
            description = "A friendly manager for Kiwix ZIM files";
            homepage = "https://github.com/kazie/zimon";
            mainProgram = "zimon";
          };
        };
        # The fake Kiwix catalog and mirrors, for trying zimon offline and for
        # the NixOS module's VM test.
        fakekiwix = self.packages.${pkgs.stdenv.hostPlatform.system}.default.overrideAttrs {
          pname = "fakekiwix";
          subPackages = [ "tools/fakekiwix" ];
          postInstall = "";
          meta.mainProgram = "fakekiwix";
        };
      });

      # services.zimon; see the README.
      nixosModules.default = import ./nix/module.nix self;

      # `nix flake check` runs the whole test suite, checks that the store's
      # generated code matches its SQL (`just generate`), and on Linux runs the
      # NixOS module in a VM.
      checks = forAllSystems (
        pkgs:
        {
          sqlc = pkgs.runCommand "zimon-sqlc" { nativeBuildInputs = [ pkgs.sqlc ]; } ''
            export HOME=$TMPDIR
            cd ${self}/internal/store
            sqlc diff
            touch $out
          '';
          tests = self.packages.${pkgs.stdenv.hostPlatform.system}.default.overrideAttrs (old: {
            pname = "zimon-tests";
            subPackages = [ "./..." ];
            doCheck = true;
            # The server tests run the real kiwix-serve when it is there.
            nativeCheckInputs =
              (old.nativeCheckInputs or [ ])
              ++ pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [ pkgs.kiwix-tools ];
          });
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          nixos = import ./nix/test.nix { inherit self pkgs; };
        }
      );

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages =
            with pkgs;
            [
              go
              gopls
              golangci-lint
              goreleaser
              govulncheck
              just
              sqlc
              sqlite
              vhs
            ]
            # kiwix-serve and zimdump, for `zimon serve` and poking at ZIM files.
            ++ lib.optionals stdenv.hostPlatform.isLinux [ kiwix-tools ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
