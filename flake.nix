{
  description = "medewi: Go tooling for the leva! espresso-machine controller";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      inherit (pkgs) lib;

      # Only what the build, lint and tests read: a docs-only change keeps
      # every check cached.
      goSrc = lib.fileset.toSource {
        root = ./.;
        fileset = lib.fileset.unions [
          ./go.mod
          ./go.sum
          ./.golangci.yml
          ./testdata
          ./daemon/panel.html # //go:embed
          (lib.fileset.fileFilter (f: f.hasExt "go") ./.)
        ];
      };

      build =
        name:
        pkgs.buildGoModule {
          pname = name;
          version = "0.1.0";
          src = goSrc;
          # proxyVendor: goModules is a module cache, so the checks can also
          # run `go mod tidy -diff` offline against it.
          proxyVendor = true;
          # tidy also resolves the test dependencies of dependencies, which a
          # plain build never downloads; fetch them into the same cache.
          overrideModAttrs = _: {
            postBuild = "go mod tidy";
          };
          vendorHash = "sha256-jVcBrwms/45DAg6SaBDVWvK7OtL5if17fYD3B/hXK10=";
          subPackages = [ "cmd/${name}" ];
          meta.mainProgram = name;
        };

      # One sandboxed derivation per gate step; it builds iff the step passes.
      check =
        name: script:
        pkgs.stdenv.mkDerivation {
          name = "medewi-${name}";
          src = goSrc;
          nativeBuildInputs = with pkgs; [
            go
            golangci-lint
            gofumpt
          ];
          dontConfigure = true;
          buildPhase = ''
            runHook preBuild
            export HOME=$TMPDIR GOPATH=$TMPDIR/go GOCACHE=$TMPDIR/go-cache
            export GOLANGCI_LINT_CACHE=$TMPDIR/lint-cache
            export GOPROXY=file://${self.packages.${system}.levacap.goModules}
            export GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=mod CGO_ENABLED=1
            ${script}
            runHook postBuild
          '';
          installPhase = "touch $out";
        };
    in
    {
      packages.${system} = {
        levacap = build "levacap";
        levasim = build "levasim";
        medewid = build "medewid";
        medewictl = build "medewictl";
      };

      nixosModules.medewid = import ./nix/module.nix self;

      checks.${system} = self.packages.${system} // {
        lint = check "lint" "golangci-lint run";
        fmt = check "fmt" "golangci-lint fmt --diff";
        vet = check "vet" "go vet ./...";
        test = check "test" "go test -race ./...";
        tidy = check "tidy" "go mod tidy -diff";

        # Boots the module against levasim; needs KVM.
        medewid-vm = import ./nix/vm-test.nix { inherit self pkgs; };

        # Forces evaluation of the unit without building a system closure.
        # Also checks that a path literal for a secret is refused: it would
        # copy the key or the vendor XML into the world-readable store.
        medewid-module =
          let
            evalWith =
              extra:
              nixpkgs.lib.nixosSystem {
                inherit system;
                modules = [
                  self.nixosModules.medewid
                  {
                    services.medewid.enable = true;
                    services.medewid.environmentFile = "/dev/null";
                    services.medewid.xmlFile = pkgs.lib.mkDefault "/run/secrets/leva!-65FCB201.xml";
                    services.medewid.tlsCert = pkgs.lib.mkDefault "/run/secrets/medewid.crt";
                    services.medewid.tlsKey = pkgs.lib.mkDefault "/run/secrets/medewid.key";
                    services.medewid.tlsClientCA = pkgs.lib.mkDefault "/run/secrets/clients-ca.crt";
                    fileSystems."/" = {
                      device = "none";
                      fsType = "tmpfs";
                    };
                    boot.loader.grub.enable = false;
                    system.stateVersion = "25.11";
                  }
                  extra
                ];
              };
            eval = evalWith { };
            refused =
              opt:
              !(builtins.tryEval (
                builtins.deepSeq
                  (evalWith { services.medewid.${opt} = ./flake.nix; })
                  .config.systemd.services.medewid.serviceConfig.LoadCredential
                  true
              )).success;
            pathLiteralsRefused = pkgs.lib.all refused [
              "xmlFile"
              "tlsCert"
              "tlsKey"
              "tlsClientCA"
            ];
          in
          pkgs.runCommand "medewid-module-eval"
            {
              json = builtins.toJSON eval.config.systemd.services.medewid.serviceConfig;
              passAsFile = [ "json" ];
              pathLiteralsRefused = if pathLiteralsRefused then "yes" else "no";
            }
            ''
              grep -q '"DynamicUser":true' "$jsonPath"
              ! grep -q -- '-insecure-http' "$jsonPath"
              if [ "$pathLiteralsRefused" != yes ]; then
                echo "a path literal for xmlFile/tlsCert/tlsKey/tlsClientCA was accepted" >&2
                exit 1
              fi
              cp "$jsonPath" $out
            '';
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = with pkgs; [
          go
          gopls
          golangci-lint
          gofumpt
          govulncheck
        ];
      };
    };
}
