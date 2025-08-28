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
        
        # Build all cc-tools binaries
        cc-tools-validate = pkgs.buildGoModule rec {
          pname = "cc-tools-validate";
          inherit version;
          
          src = ./.;
          
          # Update this hash after running: nix build . --no-link 2>&1 | grep 'got:' | cut -d: -f2 | xargs
          vendorHash = "sha256-qbzor2DVDqLCuNqAWNxgr8xHCljrQEm+fRh8iH5tmKc=";
          
          subPackages = [ "cmd/cc-tools-validate" ];
          
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];
          
          meta = with pkgs.lib; {
            description = "Claude Code Tools - validate binary";
            homepage = "https://github.com/Veraticus/cc-tools";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };

        cc-tools-main = pkgs.buildGoModule rec {
          pname = "cc-tools";
          inherit version;
          
          src = ./.;
          
          vendorHash = "sha256-qbzor2DVDqLCuNqAWNxgr8xHCljrQEm+fRh8iH5tmKc=";
          
          subPackages = [ "cmd/cc-tools" ];
          
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];
          
          meta = with pkgs.lib; {
            description = "Claude Code Tools - main CLI";
            homepage = "https://github.com/Veraticus/cc-tools";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };

        cc-tools-statusline = pkgs.buildGoModule rec {
          pname = "cc-tools-statusline";
          inherit version;
          
          src = ./.;
          
          vendorHash = "sha256-qbzor2DVDqLCuNqAWNxgr8xHCljrQEm+fRh8iH5tmKc=";
          
          subPackages = [ "cmd/cc-tools-statusline" ];
          
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];
          
          meta = with pkgs.lib; {
            description = "Claude Code Tools - statusline binary";
            homepage = "https://github.com/Veraticus/cc-tools";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };

        # Combined package that includes all binaries
        cc-tools = pkgs.symlinkJoin {
          name = "cc-tools-${version}";
          paths = [ cc-tools-main cc-tools-validate cc-tools-statusline ];
          meta = with pkgs.lib; {
            description = "Claude Code Tools - all binaries";
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
          inherit cc-tools cc-tools-main cc-tools-validate cc-tools-statusline;
          default = cc-tools;
        };
        
        # Development shell
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go_1_24
            gopls
            golangci-lint
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
            program = "${cc-tools-main}/bin/cc-tools";
          };
          validate = {
            type = "app";
            program = "${cc-tools-validate}/bin/cc-tools-validate";
          };
          statusline = {
            type = "app";
            program = "${cc-tools-statusline}/bin/cc-tools-statusline";
          };
        };
      }
    );
}