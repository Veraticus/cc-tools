.PHONY: build clean test lint

# Build unified cc-tools binary
build:
	@echo "Building cc-tools..."
	@mkdir -p build
	go build -o build/cc-tools cmd/cc-tools/main.go
	@echo "Binary built: build/cc-tools"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf build/ coverage.out coverage.html

# Test with coverage
test:
	@echo "Running tests..."
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated in coverage.html"

# Lint check
lint:
	@echo "Running linters..."
	gofmt -w .
	golangci-lint run
	deadcode -test ./...

# Install cc-tools binary
install: build
	@echo "Installing cc-tools..."
	@mkdir -p ~/bin
	cp build/cc-tools ~/bin/
	@echo "cc-tools installed to ~/bin/"
	@echo "Make sure ~/bin is in your PATH"

# Run lint subcommand
run-lint: build
	./build/cc-tools lint

# Run test subcommand
run-test: build
	./build/cc-tools test

# Run statusline subcommand
run-statusline: build
	./build/cc-tools statusline

# Nix build
nix-build:
	@echo "Building with Nix..."
	@if command -v nix >/dev/null 2>&1; then \
		nix build .#default -L; \
		echo "Nix build completed. Binaries in ./result/bin/"; \
	else \
		echo "Nix not installed, skipping nix build"; \
	fi

# Test nix build
test-nix:
	@echo "Testing nix build..."
	@if command -v nix >/dev/null 2>&1; then \
		CURRENT_SYSTEM=$$(nix eval --raw --impure --expr builtins.currentSystem); \
		echo "Building for current system ($$CURRENT_SYSTEM)..."; \
		nix build .#packages.$$CURRENT_SYSTEM.default -L --no-link || exit 1; \
		echo "Testing cc-tools binary..."; \
		nix build .#packages.$$CURRENT_SYSTEM.cc-tools -L --no-link || exit 1; \
		echo "Nix build succeeded for $$CURRENT_SYSTEM!"; \
	else \
		echo "Nix not installed, skipping nix build test"; \
	fi

# Enter nix development shell
nix-shell:
	@if command -v nix >/dev/null 2>&1; then \
		nix develop; \
	else \
		echo "Nix not installed"; \
	fi

.PHONY: help nix-build test-nix nix-shell
help:
	@echo "Available targets:"
	@echo "  build         - Build cc-tools binary"
	@echo "  clean         - Remove build artifacts"
	@echo "  test          - Run tests with coverage"
	@echo "  lint          - Run linters"
	@echo "  install       - Install commands to ~/bin"
	@echo "  run-lint      - Run the lint subcommand"
	@echo "  run-test      - Run the test subcommand"  
	@echo "  run-statusline - Run the statusline subcommand"
	@echo "  nix-build     - Build with Nix"
	@echo "  test-nix      - Test Nix builds"
	@echo "  nix-shell     - Enter Nix development shell"
