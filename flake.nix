{
  description = "medewi: Go tooling for the leva! espresso-machine controller";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { nixpkgs, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      packages.${system} =
        let
          build =
            name:
            pkgs.buildGoModule {
              pname = name;
              version = "0.1.0";
              src = ./.;
              vendorHash = "sha256-8dBsmXBZ9rFk0XxnBO8qFMzsaGfAGpT744Mvk9tVGxA=";
              subPackages = [ "cmd/${name}" ];
              meta.mainProgram = name;
            };
        in
        {
          levacap = build "levacap";
          levasim = build "levasim";
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
