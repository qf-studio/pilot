# Pilot Cloud Makefile

.PHONY: build dev test lint fmt clean docker-build docker-push deploy-staging deploy-prod migrate

# Variables
BINARY_NAME=pilot-cloud
BUILD_DIR=./bin
DOCKER_REGISTRY=ghcr.io/alekspetrov
VERSION=$(shell git describe --tags --always --dirty)

# Build
build:
	@echo "Building $(BINARY_NAME)..."
	@go build -ldflags="-X main.Version=$(VERSION)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/pilot-cloud

# Development
dev:
	@echo "Running in development mode..."
	@go run ./cmd/pilot-cloud

# Test
test:
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...

# Lint
lint:
	@echo "Running linter..."
	@golangci-lint run ./...

# Format
fmt:
	@echo "Formatting code..."
	@go fmt ./...
	@goimports -w .

# Clean
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out

# Docker
docker-build-api:
	@echo "Building API Docker image..."
	@docker build -f infra/docker/Dockerfile.api -t $(DOCKER_REGISTRY)/pilot-api:$(VERSION) .
	@docker tag $(DOCKER_REGISTRY)/pilot-api:$(VERSION) $(DOCKER_REGISTRY)/pilot-api:latest

docker-build-executor:
	@echo "Building Executor Docker image..."
	@docker build -f infra/docker/Dockerfile.executor -t $(DOCKER_REGISTRY)/pilot-executor:$(VERSION) .
	@docker tag $(DOCKER_REGISTRY)/pilot-executor:$(VERSION) $(DOCKER_REGISTRY)/pilot-executor:latest

docker-build: docker-build-api docker-build-executor

docker-push:
	@echo "Pushing Docker images..."
	@docker push $(DOCKER_REGISTRY)/pilot-api:$(VERSION)
	@docker push $(DOCKER_REGISTRY)/pilot-api:latest
	@docker push $(DOCKER_REGISTRY)/pilot-executor:$(VERSION)
	@docker push $(DOCKER_REGISTRY)/pilot-executor:latest

# Database
migrate:
	@echo "Running migrations..."
	@psql $(DATABASE_URL) -f migrations/001_initial_schema.sql

migrate-down:
	@echo "Rolling back migrations..."
	@echo "Manual rollback required - check migrations folder"

# Deployment
deploy-staging:
	@echo "Deploying to staging..."
	@kubectl config use-context pilot-staging
	@kubectl apply -k infra/k8s/overlays/staging

deploy-prod:
	@echo "Deploying to production..."
	@kubectl config use-context pilot-prod
	@kubectl apply -k infra/k8s/overlays/prod

# Infrastructure
infra-plan:
	@echo "Planning infrastructure changes..."
	@cd infra/terraform && terraform plan

infra-apply:
	@echo "Applying infrastructure changes..."
	@cd infra/terraform && terraform apply

# Local development
local-db:
	@echo "Starting local PostgreSQL..."
	@docker run --name pilot-postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=pilot_cloud -p 5432:5432 -d postgres:15

local-redis:
	@echo "Starting local Redis..."
	@docker run --name pilot-redis -p 6379:6379 -d redis:7

local-up: local-db local-redis
	@echo "Local services started"

local-down:
	@echo "Stopping local services..."
	@docker stop pilot-postgres pilot-redis || true
	@docker rm pilot-postgres pilot-redis || true

# Generate
generate:
	@echo "Generating code..."
	@go generate ./...

# Help
help:
	@echo "Available targets:"
	@echo "  build          - Build the binary"
	@echo "  dev            - Run in development mode"
	@echo "  test           - Run tests"
	@echo "  lint           - Run linter"
	@echo "  fmt            - Format code"
	@echo "  clean          - Clean build artifacts"
	@echo "  docker-build   - Build Docker images"
	@echo "  docker-push    - Push Docker images"
	@echo "  migrate        - Run database migrations"
	@echo "  deploy-staging - Deploy to staging"
	@echo "  deploy-prod    - Deploy to production"
	@echo "  local-up       - Start local development services"
	@echo "  local-down     - Stop local development services"
