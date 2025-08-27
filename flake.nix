{
  description = "CC-Tools - Go implementations of Claude Code smart hooks";

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
        
        # Build unified cc-tools binary
        cc-tools = pkgs.buildGoModule rec {
          pname = "cc-tools";
          inherit version;
          
          src = ./.;
          
          # Update this hash after running: nix build . --no-link 2>&1 | grep 'got:' | cut -d: -f2 | xargs
          vendorHash = "sha256-qbzor2DVDqLCuNqAWNxgr8xHCljrQEm+fRh8iH5tmKc=";
          
          subPackages = [ "cmd/cc-tools" ];
          
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];
          
          meta = with pkgs.lib; {
            description = "Claude Code Tools - unified binary for smart hooks";
            homepage = "https://github.com/Veraticus/cc-tools";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };
        
      in
      {
        # Packages
        packages = {
          inherit cc-tools;
          default = cc-tools;
        };
        
        # Development shell
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go_1_24
            gopls
            golangci-lint
            deadcode
            gnumake
            git
            
            # For testing the tools
            jq
            bash
          ];
          
          shellHook = ''
            echo "CC-Tools development environment"
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
            program = "${cc-tools}/bin/cc-tools";
          };
          cc-tools = {
            type = "app";
            program = "${cc-tools}/bin/cc-tools";
          };
        };
      }
    );
}