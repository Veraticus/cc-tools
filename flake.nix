{
  description = "CC-Tools - Go implementations of Claude Code smart hooks";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      # Define modules outside of eachDefaultSystem since they're system-independent
      nixosModule = { config, lib, pkgs, ... }:
        let
          cfg = config.services.cc-tools;
          
          # Build tools that hooks might need
          serverPath = lib.makeBinPath (with pkgs; [
            # Build systems
            gnumake
            just
            cmake
            ninja
            
            # Language tools - Go
            go
            golangci-lint
            
            # Language tools - Python
            python3
            python3Packages.flake8
            python3Packages.mypy
            python3Packages.black
            python3Packages.pytest
            ruff
            
            # Language tools - Rust
            rustc
            cargo
            clippy
            
            # Language tools - Node.js
            nodejs
            
            # Common tools
            git
            coreutils
            findutils
            gnugrep
            gnused
            gawk
          ]);
        in
        {
          options.services.cc-tools = {
            enable = lib.mkEnableOption "cc-tools server";
            
            socketPath = lib.mkOption {
              type = lib.types.str;
              default = "/run/user/%U/cc-tools.sock";
              description = "Path to the Unix socket for cc-tools server";
            };
            
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.cc-tools;
              description = "The cc-tools package to use";
            };
          };
          
          config = lib.mkIf cfg.enable {
            systemd.user.services.cc-tools-server = {
              Unit = {
                Description = "Claude Code Tools Server";
                After = [ "graphical-session-pre.target" ];
                PartOf = [ "graphical-session.target" ];
              };
              
              Service = {
                Type = "simple";
                ExecStart = "${cfg.package}/bin/cc-tools serve -socket ${cfg.socketPath}";
                Restart = "on-failure";
                RestartSec = 5;
                Environment = [ "PATH=${serverPath}" ];
              };
              
              Install = {
                WantedBy = [ "default.target" ];
              };
            };
          };
        };
        
      homeManagerModule = { config, lib, pkgs, ... }:
        let
          cfg = config.services.cc-tools;
          
          # Build tools that hooks might need  
          serverPath = lib.makeBinPath (with pkgs; [
            # Build systems
            gnumake
            just
            cmake
            ninja
            
            # Language tools - Go
            go
            golangci-lint
            
            # Language tools - Python
            python3
            python3Packages.flake8
            python3Packages.mypy
            python3Packages.black
            python3Packages.pytest
            ruff
            
            # Language tools - Rust
            rustc
            cargo
            clippy
            
            # Language tools - Node.js
            nodejs
            
            # Common tools
            git
            coreutils
            findutils
            gnugrep
            gnused
            gawk
          ]);
        in
        {
          options.services.cc-tools = {
            enable = lib.mkEnableOption "cc-tools server";
            
            socketPath = lib.mkOption {
              type = lib.types.str;
              default = "/run/user/\${UID}/cc-tools.sock";
              description = "Path to the Unix socket for cc-tools server";
            };
            
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.cc-tools;
              description = "The cc-tools package to use";
            };
          };
          
          config = lib.mkIf cfg.enable {
            systemd.user.services.cc-tools-server = {
              Unit = {
                Description = "Claude Code Tools Server";
                After = [ "graphical-session-pre.target" ];
                PartOf = [ "graphical-session.target" ];
              };
              
              Service = {
                Type = "simple";
                ExecStart = "${cfg.package}/bin/cc-tools serve -socket ${cfg.socketPath}";
                Restart = "on-failure";
                RestartSec = 5;
                Environment = [ "PATH=${serverPath}" ];
              };
              
              Install = {
                WantedBy = [ "default.target" ];
              };
            };
          };
        };
    in
    {
      # Export modules at the flake level
      nixosModules.default = nixosModule;
      homeManagerModules.default = homeManagerModule;
    } // flake-utils.lib.eachDefaultSystem (system:
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
        
        # NixOS/Home Manager module
        nixosModules.default = { config, lib, pkgs, ... }:
          let
            cfg = config.services.cc-tools;
            
            # Build tools that hooks might need
            serverPath = lib.makeBinPath (with pkgs; [
              # Build systems
              gnumake
              just
              cmake
              ninja
              
              # Language tools - Go
              go
              golangci-lint
              
              # Language tools - Python
              python3
              python3Packages.flake8
              python3Packages.mypy
              python3Packages.black
              python3Packages.pytest
              ruff
              
              # Language tools - Rust
              rustc
              cargo
              clippy
              
              # Language tools - Node.js
              nodejs
              
              # Common tools
              git
              coreutils
              findutils
              gnugrep
              gnused
              gawk
            ]);
          in
          {
            options.services.cc-tools = {
              enable = lib.mkEnableOption "cc-tools server";
              
              socketPath = lib.mkOption {
                type = lib.types.str;
                default = "/run/user/%U/cc-tools.sock";
                description = "Path to the Unix socket for cc-tools server";
              };
              
              package = lib.mkOption {
                type = lib.types.package;
                default = cc-tools;
                description = "The cc-tools package to use";
              };
            };
            
            config = lib.mkIf cfg.enable {
              systemd.user.services.cc-tools-server = {
                Unit = {
                  Description = "Claude Code Tools Server";
                  After = [ "graphical-session-pre.target" ];
                  PartOf = [ "graphical-session.target" ];
                };
                
                Service = {
                  Type = "simple";
                  ExecStart = "${cfg.package}/bin/cc-tools serve -socket ${cfg.socketPath}";
                  Restart = "on-failure";
                  RestartSec = 5;
                  Environment = [ "PATH=${serverPath}" ];
                };
                
                Install = {
                  WantedBy = [ "default.target" ];
                };
              };
            };
          };
        
        homeManagerModules.default = { config, lib, pkgs, ... }:
          let
            cfg = config.services.cc-tools;
            
            # Build tools that hooks might need
            serverPath = lib.makeBinPath (with pkgs; [
              # Build systems
              gnumake
              just
              cmake
              ninja
              
              # Language tools - Go
              go
              golangci-lint
              
              # Language tools - Python
              python3
              python3Packages.flake8
              python3Packages.mypy
              python3Packages.black
              python3Packages.pytest
              ruff
              
              # Language tools - Rust
              rustc
              cargo
              clippy
              
              # Language tools - Node.js
              nodejs
              
              # Common tools
              git
              coreutils
              findutils
              gnugrep
              gnused
              gawk
            ]);
          in
          {
            options.services.cc-tools = {
              enable = lib.mkEnableOption "cc-tools server";
              
              socketPath = lib.mkOption {
                type = lib.types.str;
                default = "/run/user/\${UID}/cc-tools.sock";
                description = "Path to the Unix socket for cc-tools server";
              };
              
              package = lib.mkOption {
                type = lib.types.package;
                default = cc-tools;
                description = "The cc-tools package to use";
              };
            };
            
            config = lib.mkIf cfg.enable {
              systemd.user.services.cc-tools-server = {
                Unit = {
                  Description = "Claude Code Tools Server";
                  After = [ "graphical-session-pre.target" ];
                  PartOf = [ "graphical-session.target" ];
                };
                
                Service = {
                  Type = "simple";
                  ExecStart = "${cfg.package}/bin/cc-tools serve -socket ${cfg.socketPath}";
                  Restart = "on-failure";
                  RestartSec = 5;
                  Environment = [ "PATH=${serverPath}" ];
                };
                
                Install = {
                  WantedBy = [ "default.target" ];
                };
              };
            };
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