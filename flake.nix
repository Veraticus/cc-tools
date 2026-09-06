{
  description = "Steward - Go implementations of Claude Code utilities";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        # Get git revision or use placeholder
        gitRevision = if (self ? rev) then self.rev else "dirty";
        shortRev = if (self ? shortRev) then self.shortRev else "dirty";

        # Build configuration
        version = shortRev;
        buildTime = "1970-01-01T00:00:00Z";

        # Update this hash after running: nix build . --no-link 2>&1 | grep 'got:' | cut -d: -f2 | xargs
        vendorHash = "sha256-Cbk/8dREiEvXGESrhZ9dg2N1gHzdaipeghWoIH8wSGs=";

        steward-main = pkgs.buildGoModule rec {
          pname = "steward";
          inherit version vendorHash;

          src = ./.;

          subPackages = [ "cmd/steward" ];

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];

          meta = with pkgs.lib; {
            description = "Steward - main CLI";
            homepage = "https://github.com/joshsymonds/steward";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };

        steward-statusline = pkgs.buildGoModule rec {
          pname = "steward-statusline";
          inherit version vendorHash;

          src = ./.;

          subPackages = [ "cmd/steward-statusline" ];

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];

          meta = with pkgs.lib; {
            description = "Steward - statusline binary";
            homepage = "https://github.com/joshsymonds/steward";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };

        steward-pi-runtime = pkgs.callPackage ./nix/pi-runtime.nix {
          inherit version steward-main;
        };

        # Combined package that includes all binaries
        steward = pkgs.symlinkJoin {
          name = "steward-${version}";
          paths = [ steward-main steward-statusline steward-pi-runtime ];
          meta = with pkgs.lib; {
            description = "Steward - all binaries";
            homepage = "https://github.com/joshsymonds/steward";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };

      in
      {
        # Packages
        packages = {
          inherit steward steward-main steward-statusline steward-pi-runtime;
          default = steward;
        };

        # Development shell
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            golangci-lint
            gnumake
            git

            # For testing the tools
            jq
            bash
          ];

          shellHook = ''
            echo "Steward development environment"
            echo "Available commands:"
            echo "  make build    - Build all tools"
            echo "  make test     - Run tests"
            echo "  make lint     - Run linters"
            echo "  nix build     - Build with Nix"
            echo ""
            echo "Go version: $(go version)"
          '';
        };

        # Apps for nix run
        apps = {
          default = {
            type = "app";
            program = "${steward-main}/bin/steward";
          };
          statusline = {
            type = "app";
            program = "${steward-statusline}/bin/steward-statusline";
          };
        };
      }
    );
}

