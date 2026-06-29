{
  description = "movielily - a minimal, notebook-style video editor (mpv + ffmpeg)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f:
        nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      # Project-local dev shell: `nix develop` (or `direnv allow`).
      # Kept deliberately tiny — the whole dependency graph is Go + mpv + ffmpeg.
      devShells = forAllSystems (system: pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            delve

            mpv
            ffmpeg

            just
            git
          ];
          shellHook = ''
            echo "movielily dev shell — $(go version | awk '{print $1, $3}'), mpv, ffmpeg"
          '';
        };
      });

      # `nix build` / `nix run .` — builds the CLI with mpv + ffmpeg on PATH.
      packages = forAllSystems (system: pkgs: {
        default = pkgs.buildGoModule {
          pname = "movielily";
          version = "0.1.0";
          src = ./.;
          # Deps are vendored (see vendor/), so no hash needed and the build
          # works offline and reproducibly.
          vendorHash = null;
          nativeBuildInputs = [ pkgs.makeWrapper ];
          postInstall = ''
            wrapProgram $out/bin/movielily \
              --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.mpv pkgs.ffmpeg ]}
          '';
          meta = {
            description = "Minimal, notebook-style video editor (mpv + ffmpeg)";
            mainProgram = "movielily";
          };
        };
      });

      apps = forAllSystems (system: pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/movielily";
        };
      });
    };
}
