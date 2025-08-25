.PHONY: build clean test lint

# Build all three commands
build:
	@echo "Building cc-tools commands..."
	@mkdir -p build
	go build -o build/smart-lint cmd/smart-lint/main.go
	go build -o build/smart-test cmd/smart-test/main.go
	go build -o build/statusline cmd/statusline/main.go
	@echo "Commands built in build/"

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

# Install commands locally
install: build
	@echo "Installing cc-tools commands..."
	@mkdir -p ~/bin
	cp build/smart-lint ~/bin/
	cp build/smart-test ~/bin/
	cp build/statusline ~/bin/
	@echo "Commands installed to ~/bin/"
	@echo "Make sure ~/bin is in your PATH"

# Run smart-lint
run-smart-lint: build
	./build/smart-lint

# Run smart-test
run-smart-test: build
	./build/smart-test

# Run statusline
run-statusline: build
	./build/statusline

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
		echo "Testing individual tools..."; \
		nix build .#packages.$$CURRENT_SYSTEM.smart-lint -L --no-link || exit 1; \
		nix build .#packages.$$CURRENT_SYSTEM.smart-test -L --no-link || exit 1; \
		nix build .#packages.$$CURRENT_SYSTEM.statusline -L --no-link || exit 1; \
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
	@echo "  build         - Build all three commands"
	@echo "  clean         - Remove build artifacts"
	@echo "  test          - Run tests with coverage"
	@echo "  lint          - Run linters"
	@echo "  install       - Install commands to ~/bin"
	@echo "  run-smart-lint - Run the smart-lint command"
	@echo "  run-smart-test - Run the smart-test command"  
	@echo "  run-statusline - Run the statusline command"
	@echo "  nix-build     - Build with Nix"
	@echo "  test-nix      - Test Nix builds"
	@echo "  nix-shell     - Enter Nix development shell"
