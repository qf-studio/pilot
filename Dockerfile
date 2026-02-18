# Pilot — autonomous AI development pipeline
# Multi-stage build for standalone Pilot binary (cmd/pilot)
#
# Runtime uses Ubuntu (not Alpine) because Pilot executes Claude Code CLI,
# which requires Node.js, git, and gh CLI at runtime.

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary using modernc.org/sqlite (pure-Go, no CGO needed)
ARG VERSION=dev
ARG BUILD_TIME
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}" \
    -o /pilot \
    ./cmd/pilot

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM ubuntu:22.04

# Avoid interactive prompts during apt installs
ENV DEBIAN_FRONTEND=noninteractive

# Install runtime dependencies:
# - git, gh: required for git operations and GitHub API calls
# - curl, ca-certificates: HTTPS requests, certificate validation
# - nodejs, npm: required for Claude Code CLI execution
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    gnupg \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends \
    gh \
    nodejs \
    npm \
    && npm install -g @anthropic-ai/claude-code \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user pilot (UID 1000)
RUN useradd -m -u 1000 -s /bin/bash pilot

# Create directories for SQLite data and workspace execution
RUN mkdir -p /home/pilot/.pilot/data /workspace \
    && chown -R pilot:pilot /home/pilot /workspace

# Copy binary from builder
COPY --from=builder /pilot /usr/local/bin/pilot
RUN chmod 755 /usr/local/bin/pilot

# Switch to non-root user
USER pilot

WORKDIR /workspace

# Gateway port
EXPOSE 9090

# Health check using /health endpoint (requires gateway to be running)
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -sf http://localhost:9090/health || exit 1

ENTRYPOINT ["/usr/local/bin/pilot"]
CMD ["start", "--github", "--autopilot=stage"]
