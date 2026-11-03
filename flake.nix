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
          vendorHash = "sha256-D/HFg3JpIgnnBCahXdsKGv7Ub5c+/41YjeuNeCd9Zc4=";
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
      };

      checks.${system} = self.packages.${system} // {
        lint = check "lint" "golangci-lint run";
        fmt = check "fmt" "golangci-lint fmt --diff";
        vet = check "vet" "go vet ./...";
        test = check "test" "go test -race ./...";
        tidy = check "tidy" "go mod tidy -diff";
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
