.PHONY: build run test test-e2e clean install lint fmt deps dev install-hooks check-secrets gate check-integration auto-fix test-short test-integration test-chaos package release

# Variables
BINARY_NAME=pilot
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

# Default target
all: build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/pilot

# Build for all platforms
build-all:
	@echo "Building for all platforms..."
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 ./cmd/pilot
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 ./cmd/pilot
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/pilot
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 ./cmd/pilot

# Package binaries into tar.gz archives for release
# Binary inside tar is named "pilot" (not pilot-darwin-arm64) to match upgrade code.
# COPYFILE_DISABLE=1 prevents macOS tar from adding ._* resource fork entries.
package: build-all
	@echo "📦 Packaging binaries..."
	@for arch in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do \
		cp bin/$(BINARY_NAME)-$$arch bin/$(BINARY_NAME) && \
		COPYFILE_DISABLE=1 tar czf bin/$(BINARY_NAME)-$$arch.tar.gz -C bin $(BINARY_NAME) && \
		rm bin/$(BINARY_NAME); \
	done
	@shasum -a 256 bin/*.tar.gz > bin/checksums.txt
	@echo "✅ Packages created"

# Run the daemon
run: build
	./bin/$(BINARY_NAME) start

# Run in development mode with auto-reload
dev:
	@echo "Running in development mode..."
	go run ./cmd/pilot start

# Install dependencies
deps:
	go mod download
	go mod tidy

# Run tests
test:
	go test -v -race ./...

# Run tests with coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Run end-to-end tests (Go-based workflow tests + shell tests)
test-e2e: build
	@echo "Running E2E workflow tests..."
	go test -v -count=1 -timeout 60s ./e2e/...
	@echo "Running E2E shell tests..."
	./scripts/test-e2e.sh

# Run only Go-based E2E workflow tests (faster, no external deps)
test-e2e-go:
	@echo "Running E2E workflow tests..."
	go test -v -count=1 -timeout 60s ./e2e/...

# Run end-to-end tests with live Claude Code execution
test-e2e-live: build
	@echo "Running E2E tests (including live Claude Code)..."
	RUN_LIVE_TESTS=true ./scripts/test-e2e.sh

# Lint the code
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, skipping..."; \
	fi

# Format the code
fmt:
	go fmt ./...
	@if command -v goimports >/dev/null 2>&1; then \
		goimports -w .; \
	fi

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html

# Install to ~/go/bin (or GOBIN)
install:
	go install $(LDFLAGS) ./cmd/pilot

# Install to /usr/local/bin (requires sudo)
install-global: build
	sudo cp bin/$(BINARY_NAME) /usr/local/bin/

# Generate mocks for testing
mocks:
	@if command -v mockgen >/dev/null 2>&1; then \
		go generate ./...; \
	else \
		echo "mockgen not installed, skipping..."; \
	fi

# Run the orchestrator tests (Python)
test-orchestrator:
	cd orchestrator && python -m pytest -v

# Install git hooks (pre-commit for secret pattern detection)
install-hooks:
	@./scripts/install-hooks.sh

# Check for realistic secret patterns in test files
check-secrets:
	@./scripts/check-secret-patterns.sh

# Run short tests (for pre-push gate)
test-short:
	go test -short -race ./...

# Run integration tests (build tag separated from unit tests)
test-integration:
	go test -v -race -tags=integration ./...

# Run chaos tests for fault injection scenarios
# Tests system behavior under adverse conditions: network failures, API errors, timeouts
test-chaos:
	@echo "🔥 Running chaos tests..."
	go test -v -race -timeout 5m ./internal/chaos/...

# Run integration checks (orphan commands, build tags, etc.)
check-integration:
	@./scripts/check-integration.sh

# Auto-fix common issues (formatting, imports, lint)
auto-fix:
	@./scripts/auto-fix.sh

# Pre-push validation gate - runs all checks
gate:
	@./scripts/pre-push-gate.sh

# Release - creates tag, builds, packages, and publishes to GitHub
# Usage: make release V=0.14.6 NOTES="Release notes here"
release:
ifndef V
	$(error V is required. Usage: make release V=0.14.6 NOTES="Release notes")
endif
	@echo "🚀 Creating release v$(V)..."
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "❌ Error: Working directory not clean. Commit or stash changes first."; \
		exit 1; \
	fi
	@if [ "$$(git branch --show-current)" != "main" ]; then \
		echo "❌ Error: Must be on main branch to release."; \
		exit 1; \
	fi
	@echo "📌 Creating and pushing git tag v$(V)..."
	git tag v$(V)
	git push origin v$(V)
	@echo "🔨 Building and packaging binaries..."
	$(MAKE) package VERSION=v$(V)
	@echo "📦 Creating GitHub release..."
	gh release create v$(V) \
		bin/$(BINARY_NAME)-darwin-amd64.tar.gz \
		bin/$(BINARY_NAME)-darwin-arm64.tar.gz \
		bin/$(BINARY_NAME)-linux-amd64.tar.gz \
		bin/$(BINARY_NAME)-linux-arm64.tar.gz \
		bin/checksums.txt \
		--title "pilot v$(V)" \
		--notes "$(if $(NOTES),$(NOTES),Release v$(V))"
	@echo "✅ Released v$(V)"
	@echo "   Run 'pilot upgrade' to update"

# Help
help:
	@echo "Pilot Makefile Commands:"
	@echo ""
	@echo "  make build          Build the binary"
	@echo "  make build-all      Build for all platforms"
	@echo "  make run            Build and run the daemon"
	@echo "  make dev            Run in development mode"
	@echo "  make deps           Install dependencies"
	@echo "  make test           Run unit tests"
	@echo "  make test-coverage  Run tests with coverage"
	@echo "  make test-e2e       Run end-to-end tests (Go + shell)"
	@echo "  make test-e2e-go    Run Go-based E2E workflow tests"
	@echo "  make test-e2e-live  Run E2E tests with live Claude"
	@echo "  make lint           Run linter"
	@echo "  make fmt            Format code"
	@echo "  make clean          Clean build artifacts"
	@echo "  make install        Install to GOPATH/bin"
	@echo "  make install-global Install to /usr/local/bin"
	@echo "  make install-hooks  Install git pre-commit/pre-push hooks"
	@echo "  make check-secrets  Check for secret patterns in tests"
	@echo "  make check-integration  Check for orphan code"
	@echo "  make gate           Run pre-push validation gate"
	@echo "  make auto-fix       Auto-fix common issues"
	@echo "  make test-short     Run tests in short mode"
	@echo "  make test-integration Run integration tests"
	@echo "  make test-chaos     Run chaos/fault injection tests"
	@echo "  make package        Package binaries into tar.gz archives"
	@echo "  make release        Create release (V=0.x.x required)"
	@echo ""
