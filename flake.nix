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
        
        # Build configuration
        version = "0.1.0";
        buildTime = "1970-01-01T00:00:00Z";
        
        # Common build function for all three tools
        buildTool = name: pkgs.buildGoModule rec {
          pname = name;
          inherit version;
          
          src = ./.;
          
          # Update this hash after running: nix build .#${name} --no-link 2>&1 | grep 'got:' | cut -d: -f2 | xargs
          vendorHash = null; # Dependencies are vendored
          
          subPackages = [ "cmd/${name}" ];
          
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.buildTime=${buildTime}"
          ];
          
          meta = with pkgs.lib; {
            description = 
              if name == "smart-lint" then "Claude Code smart linting hook"
              else if name == "smart-test" then "Claude Code smart testing hook"
              else "Claude Code status line generator";
            homepage = "https://github.com/Veraticus/cc-tools";
            license = licenses.mit;
            maintainers = with maintainers; [ ];
            platforms = platforms.unix;
          };
        };
        
        # Build all three tools
        smart-lint = buildTool "smart-lint";
        smart-test = buildTool "smart-test";
        statusline = buildTool "statusline";
        
        # Combined package with all three tools
        cc-tools = pkgs.symlinkJoin {
          name = "cc-tools-${version}";
          paths = [ smart-lint smart-test statusline ];
          meta = {
            description = "Claude Code smart hooks collection";
            homepage = "https://github.com/Veraticus/cc-tools";
            license = pkgs.lib.licenses.mit;
            platforms = pkgs.lib.platforms.unix;
          };
        };
        
      in
      {
        # Individual packages
        packages = {
          inherit smart-lint smart-test statusline;
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
          smart-lint = {
            type = "app";
            program = "${smart-lint}/bin/smart-lint";
          };
          smart-test = {
            type = "app";
            program = "${smart-test}/bin/smart-test";
          };
          statusline = {
            type = "app";
            program = "${statusline}/bin/statusline";
          };
        };
      }
    );
}