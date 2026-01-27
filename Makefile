.PHONY: build run test test-e2e clean install lint fmt deps dev

# Variables
BINARY_NAME=pilot
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
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

# Run end-to-end tests
test-e2e: build
	@echo "Running E2E tests..."
	./scripts/test-e2e.sh

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
	@echo "  make test-e2e       Run end-to-end tests"
	@echo "  make test-e2e-live  Run E2E tests with live Claude"
	@echo "  make lint           Run linter"
	@echo "  make fmt            Format code"
	@echo "  make clean          Clean build artifacts"
	@echo "  make install        Install to GOPATH/bin"
	@echo "  make install-global Install to /usr/local/bin"
	@echo ""
