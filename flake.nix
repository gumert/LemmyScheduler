{
  description = "Development environment for LemmyScheduler";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { nixpkgs, ... }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forEachSystem = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forEachSystem (system: {
        default = nixpkgs.legacyPackages.${system}.mkShell {
          packages = with nixpkgs.legacyPackages.${system}; [
            git
            go
            nodejs_22
            pnpm
            tilt
            just
            pre-commit
            figlet
            buf
            protoc-gen-go
            protoc-gen-connect-go
            protoc-gen-es
          ];

          shellHook = ''
            # The Nix-provided Go is the single source of truth for the Go
            # version. Fail loudly if go.mod asks for a newer one instead of
            # silently downloading a toolchain.
            export GOTOOLCHAIN=local
            export NEXT_TELEMETRY_DISABLED=1
            figlet LemmySched
          '';
        };
      });
    };
}
