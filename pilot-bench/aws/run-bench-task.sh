#!/bin/bash
# run-bench-task.sh — Execute a single Terminal Bench task on an EC2 warm pool instance.
#
# Called via SSM RunCommand by the orchestrator. Each invocation:
# 1. Downloads assets from S3 (pilot binary, config, learning DB, task manifest)
# 2. Pulls the task's Docker image
# 3. Runs pilot binary inside the container
# 4. Runs the verifier (test.sh)
# 5. Uploads results to S3
#
# Parameters (passed as env vars or SSM document params):
#   TASK_NAME     — Terminal Bench task name (e.g., "chess-best-move")
#   TRIAL_ID      — Trial identifier (e.g., "trial-001")
#   RUN_ID        — Benchmark run identifier (e.g., "aws-v1-20260331")
#   S3_BUCKET     — S3 bucket for assets/results (default: pilot-s3-agent-data)
#   S3_PREFIX     — S3 prefix for bench assets (default: bench/assets)
#   SSM_PREFIX    — SSM parameter prefix (default: /pilot)
#   MODEL         — Model to use (default: claude-opus-4-6)
#   TB_REPO_URL   — Terminal Bench repo URL
#   TB_REPO_REF   — Terminal Bench repo ref/branch
#   DOCKER_TAG    — Docker image tag (default: 20251031)

set -euo pipefail
echo "DEBUG: run-bench-task.sh started"

# Defaults
S3_BUCKET="${S3_BUCKET:-pilot-s3-agent-data}"
S3_PREFIX="${S3_PREFIX:-bench/assets}"
S3_RUNS="${S3_RUNS_PREFIX:-bench/runs}"
SSM_PREFIX="${SSM_PREFIX:-/pilot}"
MODEL="${MODEL:-claude-opus-4-6}"
TB_REPO_URL="${TB_REPO_URL:-https://github.com/laude-institute/terminal-bench-2.git}"
TB_REPO_REF="${TB_REPO_REF:-main}"
DOCKER_TAG="${DOCKER_TAG:-20251031}"
MAIN_TIMEOUT="${MAIN_TIMEOUT:-5400}"

# Validate required params
if [ -z "${TASK_NAME:-}" ] || [ -z "${TRIAL_ID:-}" ] || [ -z "${RUN_ID:-}" ]; then
    echo "ERROR: TASK_NAME, TRIAL_ID, and RUN_ID are required"
    exit 1
fi

echo "=== AWS Bench Task Runner ==="
echo "Task:     $TASK_NAME"
echo "Trial:    $TRIAL_ID"
echo "Run:      $RUN_ID"
echo "Model:    $MODEL"
echo "Instance: $(curl -s http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null || echo unknown)"
echo "Started:  $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

# Working directories
WORKSPACE="/workspace/${TRIAL_ID}"
RESULTS_DIR="${WORKSPACE}/results"
LOGS_DIR="${WORKSPACE}/logs"
ASSETS_DIR="/opt/bench-assets"
TB_REPO="/opt/terminal-bench"

mkdir -p "$WORKSPACE" "$RESULTS_DIR" "$LOGS_DIR" "$ASSETS_DIR"

# Generate pilot config YAML for a given model
generate_pilot_config() {
    local model="$1"
    cat << PILOTCFG
version: "1.0"
orchestrator:
  model: "${model}"
executor:
  type: "claude-code"
  claude_code:
    command: claude
    use_structured_output: true
    use_session_resume: true
    use_from_pr: false
  hooks:
    enabled: true
    run_tests_on_stop: true
    block_destructive: true
    lint_on_save: false
  heartbeat_timeout: 15m
  model_routing:
    enabled: false
  timeout:
    default: 30m
    trivial: 15m
    simple: 25m
    medium: 30m
    complex: 60m
  effort_routing:
    enabled: true
    trivial: low
    simple: medium
    medium: high
    complex: high
  effort_classifier:
    enabled: false
  intent_judge:
    enabled: false
  retry:
    enabled: true
    rate_limit:
      max_attempts: 3
      initial_backoff: 30s
      backoff_multiplier: 2
    api_error:
      max_attempts: 3
      initial_backoff: 5s
      backoff_multiplier: 2
    timeout:
      max_attempts: 2
      initial_backoff: 0s
      backoff_multiplier: 0
      extend_timeout: true
      timeout_multiplier: 1.5
quality:
  # v5: gates ON. Enables the Build-Verify-Fix retry loop (runner.go:2147+)
  # that gives the agent up to 2 retries with a "try a different algorithm"
  # prompt when the test gate fails. For TB2 workspaces without a Makefile,
  # the test gate will fail-skip via DetectTestCommand once #2396 lands; until
  # then, the build gate (auto-detected) still runs and the test gate's
  # spurious failures convert into retries that may help or be no-ops.
  enabled: true
memory:
  path: /root/.pilot/data
  learning:
    enabled: true
    # Harbor bench compliance (TB2): disable prompt injection of learned
    # patterns + knowledge-graph nodes. The learning system still RECORDS
    # outcomes, but no task-targeted priors are injected into the agent
    # prompt — preserves "harness-only" claim for leaderboard submission.
    inject_patterns: false
PILOTCFG
}

# ─── Step 1: Load secrets from SSM ────────────────────────────────────────────
# v5: Claude Code subscription auth (no API). Real Opus 4.7 / Sonnet 4.6 via
# the official Anthropic endpoint, model routed by complexity (router on).
echo "--- Loading secrets from SSM ---"
# Remove any stale debug log that might exist from an older build (security).
rm -f /tmp/ssm-auth-debug.log 2>/dev/null
echo "Loading CLAUDE_CODE_OAUTH_TOKEN from SSM..."
CLAUDE_CODE_OAUTH_TOKEN=$(aws ssm get-parameter \
    --name "${SSM_PREFIX}/CLAUDE_CODE_OAUTH_TOKEN" --with-decryption \
    --query "Parameter.Value" --output text 2>/dev/null)
if [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ] || [ "$CLAUDE_CODE_OAUTH_TOKEN" = "None" ]; then
    echo "ERROR: CLAUDE_CODE_OAUTH_TOKEN not found in SSM (${SSM_PREFIX}/CLAUDE_CODE_OAUTH_TOKEN)"
    echo "Hint: claude setup-token  →  aws ssm put-parameter --type SecureString \\"
    echo "        --name ${SSM_PREFIX}/CLAUDE_CODE_OAUTH_TOKEN --value <token>"
    exit 1
fi
export CLAUDE_CODE_OAUTH_TOKEN
# Default to Anthropic endpoint by leaving ANTHROPIC_BASE_URL unset; Claude Code
# will use api.anthropic.com via the OAuth subscription. NOT setting it here is
# intentional — historical Z.AI proxy (api.z.ai) is removed for v5.
export ANTHROPIC_MODEL="${MODEL:-claude-opus-4-7}"
GITHUB_TOKEN=$(aws ssm get-parameter \
    --name "${SSM_PREFIX}/GITHUB_TOKEN" \
    --query "Parameter.Value" --output text 2>/dev/null || echo "")

echo "  OAuth token loaded (${#CLAUDE_CODE_OAUTH_TOKEN} chars)"
echo "  Base URL: (default api.anthropic.com — subscription)"
echo "  Model: $ANTHROPIC_MODEL"

# ─── Step 2: Download assets from S3 ─────────────────────────────────────────
echo ""
echo "--- Downloading assets from S3 ---"

# Pilot binary (compressed)
if [ ! -x /usr/local/bin/pilot ]; then
    echo "  Downloading pilot binary..."
    aws s3 cp "s3://${S3_BUCKET}/${S3_PREFIX}/pilot-linux-amd64.gz" "${ASSETS_DIR}/pilot.gz" --quiet
    gunzip -f "${ASSETS_DIR}/pilot.gz"
    mv "${ASSETS_DIR}/pilot" /usr/local/bin/pilot
    chmod +x /usr/local/bin/pilot
    echo "  Pilot binary installed: $(pilot version 2>/dev/null || echo 'unknown version')"
else
    echo "  Pilot binary already installed"
fi

# Task manifest
echo "  Downloading task manifest..."
aws s3 cp "s3://${S3_BUCKET}/${S3_PREFIX}/tasks-manifest.json" "${ASSETS_DIR}/tasks-manifest.json" --quiet

# Pilot config (generated by orchestrator)
echo "  Downloading pilot config..."
aws s3 cp "s3://${S3_BUCKET}/${S3_PREFIX}/pilot-config.yaml" "${ASSETS_DIR}/pilot-config.yaml" --quiet 2>/dev/null || echo "  No config found, will generate inline"

# Learning DB — Harbor bench compliance: we deliberately do NOT seed a DB.
# learning.inject_patterns=false in the generated pilot-config.yaml is the
# primary guard; starting with an empty DB is belt-and-suspenders. Any stale
# pilot.db from a prior trial on the same warm-pool host is removed.
echo "  Clearing any stale learning DB (Harbor H2 compliance)..."
mkdir -p /root/.pilot/data
rm -f /root/.pilot/data/pilot.db 2>/dev/null || true

# ─── Step 3: Get task metadata from manifest ──────────────────────────────────
echo ""
echo "--- Parsing task metadata ---"

# Parse all fields in a single python3 invocation
eval "$(python3 -c "
import json, sys, shlex
manifest = json.load(open('${ASSETS_DIR}/tasks-manifest.json'))
for t in manifest['tasks']:
    if t['task_name'] == '${TASK_NAME}':
        print(f'DOCKER_IMAGE={shlex.quote(t.get(\"docker_image\",\"\"))}')
        print(f'INSTRUCTION={shlex.quote(t.get(\"instruction\",\"\"))}')
        print(f'TASK_CPUS={t.get(\"cpus\",1)}')
        print(f'TASK_MEMORY={t.get(\"memory_mb\",2048)}')
        print(f'TASK_AGENT_TIMEOUT={int(t.get(\"agent_timeout_sec\",900))}')
        sys.exit(0)
sys.exit(1)
" 2>/dev/null)" || {
    echo "ERROR: Task '$TASK_NAME' not found in manifest"
    exit 1
}

echo "  Image:  $DOCKER_IMAGE"
echo "  CPUs:   $TASK_CPUS"
echo "  Memory: ${TASK_MEMORY}MB"

# ─── Step 4: Clone Terminal Bench repo (for test files) ───────────────────────
echo ""
echo "--- Preparing task environment ---"

if [ ! -d "$TB_REPO/.git" ]; then
    echo "  Cloning terminal-bench repo..."
    git clone --depth 1 --branch "$TB_REPO_REF" "$TB_REPO_URL" "$TB_REPO" 2>/dev/null || {
        echo "  Clone with --branch failed, trying without..."
        git clone --depth 1 "$TB_REPO_URL" "$TB_REPO"
    }
else
    echo "  Terminal-bench repo already cloned"
fi

# Find task directory in repo
TASK_DIR=""
for candidate in "tasks/$TASK_NAME" "benchmarks/$TASK_NAME" "$TASK_NAME"; do
    if [ -d "$TB_REPO/$candidate" ]; then
        TASK_DIR="$TB_REPO/$candidate"
        break
    fi
done

if [ -z "$TASK_DIR" ]; then
    # Recursive search
    TASK_DIR=$(find "$TB_REPO" -type d -name "$TASK_NAME" 2>/dev/null | head -1)
fi

echo "  Task dir: ${TASK_DIR:-NOT FOUND}"

# ─── Step 5: Pull and start Docker container ─────────────────────────────────
echo ""
echo "--- Starting task container ---"

CONTAINER_NAME="bench-${TASK_NAME}-${TRIAL_ID}"

# Clean up any existing container
docker rm -f "$CONTAINER_NAME" 2>/dev/null || true

echo "  Pulling $DOCKER_IMAGE..."
docker pull "$DOCKER_IMAGE" 2>&1 | tail -3

# Start container (no /app volume mount — image ships task files at /app)
echo "  Starting container..."
docker run -d \
    --name "$CONTAINER_NAME" \
    --cpus="$TASK_CPUS" \
    --memory="${TASK_MEMORY}m" \
    -v "${WORKSPACE}/logs:/logs" \
    "$DOCKER_IMAGE" \
    sleep infinity

# Wait for container to be ready
sleep 2

echo "  Container started: $(docker ps --filter name=$CONTAINER_NAME --format '{{.Status}}')"

# Ensure verifier + agent output directories exist
# - /logs/verifier/reward.txt ← TB2 test.sh writes here
# - /logs/agent/pilot-result.json ← pilot writes here
docker exec -w / "$CONTAINER_NAME" mkdir -p /logs/verifier /logs/agent

# ─── Step 6: Inject pilot + deps into container ──────────────────────────────
echo ""
echo "--- Setting up agent in container ---"

# Copy pilot binary into container
docker cp /usr/local/bin/pilot "$CONTAINER_NAME:/usr/local/bin/pilot"

# Copy pilot config (generated by orchestrator, downloaded from S3)
if [ -f "${ASSETS_DIR}/pilot-config.yaml" ]; then
    docker exec -w / "$CONTAINER_NAME" mkdir -p /root/.pilot
    docker cp "${ASSETS_DIR}/pilot-config.yaml" "$CONTAINER_NAME:/root/.pilot/config.yaml"
else
    # Generate config inline as fallback
    docker exec -w / "$CONTAINER_NAME" mkdir -p /root/.pilot
    docker exec -w / "$CONTAINER_NAME" bash -c "cat > /root/.pilot/config.yaml << 'CFGEOF'
$(generate_pilot_config "$MODEL")
CFGEOF"
fi

# Copy learning DB into container
docker exec -w / "$CONTAINER_NAME" mkdir -p /root/.pilot/data
docker cp /root/.pilot/data/pilot.db "$CONTAINER_NAME:/root/.pilot/data/pilot.db" 2>/dev/null || echo "  No learning DB to inject"

# Setup: copy Golden AMI tool bundle (or fallback to runtime install)
if [ -d "/opt/pilot-tools" ] && [ -x "/opt/pilot-tools/bin/node" ]; then
    echo "  Using Golden AMI tool bundle..."
    docker cp /opt/pilot-tools "$CONTAINER_NAME:/opt/pilot-tools"

    docker exec -w / "$CONTAINER_NAME" bash -c '
        set -e
        chmod +x /usr/local/bin/pilot

        # Ensure git and curl are present (bundle does not include them)
        if ! command -v git &>/dev/null || ! command -v curl &>/dev/null; then
            echo "  Installing base deps (git, curl)..."
            if command -v apt-get &>/dev/null; then
                apt-get update -qq 2>&1 && apt-get install -y -qq git curl ca-certificates 2>&1
            elif command -v apk &>/dev/null; then
                apk add --no-cache git curl ca-certificates 2>&1
            fi
        fi
        echo "  Git: $(git --version 2>/dev/null || echo MISSING)"
        echo "  Curl: $(curl --version 2>/dev/null | head -1 || echo MISSING)"

        # Persist PATH for all subsequent docker exec calls
        export PATH="/opt/pilot-tools/bin:/root/.local/bin:/usr/local/bin:$PATH"
        echo "export PATH=\"/opt/pilot-tools/bin:/root/.local/bin:/usr/local/bin:\$PATH\"" >> /root/.bashrc
        echo "PATH=/opt/pilot-tools/bin:/root/.local/bin:/usr/local/bin:$PATH" >> /etc/environment

        echo "  Node: $(node --version 2>/dev/null || echo MISSING)"
        echo "  Claude: $(claude --version 2>/dev/null || echo MISSING)"
        echo "  uv: $(uv --version 2>/dev/null || echo MISSING)"
        echo "  uvx: $(uvx --version 2>/dev/null || echo MISSING)"
        echo "  pilot: $(pilot version 2>/dev/null || echo installed)"
    '
else
    echo "  WARNING: /opt/pilot-tools not found on host, falling back to runtime install..."
    docker exec -w / "$CONTAINER_NAME" bash -c '
        set -e
        chmod +x /usr/local/bin/pilot

        # Base deps (curl, git — many containers lack both)
        if ! command -v git &>/dev/null || ! command -v curl &>/dev/null; then
            echo "  Installing base deps (git, curl)..."
            if command -v apt-get &>/dev/null; then
                apt-get update -qq 2>&1 && apt-get install -y -qq git curl ca-certificates 2>&1
            elif command -v apk &>/dev/null; then
                apk add --no-cache git curl ca-certificates 2>&1
            fi
        fi
        echo "  Git: $(git --version 2>/dev/null || echo MISSING)"
        echo "  Curl: $(curl --version 2>/dev/null | head -1 || echo MISSING)"

        # Node.js (required by Claude Code) — direct binary, .tar.gz (no xz dependency)
        if ! command -v node &>/dev/null; then
            echo "  Installing Node.js via binary tarball..."
            NODE_VERSION=22.14.0
            curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.gz" \
                | tar -xz -C /usr/local --strip-components=1 2>&1
            hash -r
        fi
        echo "  Node: $(node --version 2>/dev/null || echo MISSING)"

        # Claude Code
        if ! command -v claude &>/dev/null; then
            echo "  Installing Claude Code..."
            npm install -g @anthropic-ai/claude-code 2>&1
        fi
        echo "  Claude: $(claude --version 2>/dev/null || echo MISSING)"

        # uv/uvx (verifiers need it) — download script first, then execute (avoids nested pipe issues)
        if ! command -v uv &>/dev/null; then
            echo "  Installing uv..."
            curl -LsSf https://astral.sh/uv/install.sh -o /tmp/uv-install.sh 2>&1
            sh /tmp/uv-install.sh 2>&1
            rm -f /tmp/uv-install.sh
        fi

        # Persist PATH for all subsequent docker exec calls
        export PATH="/root/.local/bin:/usr/local/bin:$PATH"
        echo "export PATH=\"/root/.local/bin:/usr/local/bin:\$PATH\"" >> /root/.bashrc
        echo "PATH=/root/.local/bin:/usr/local/bin:$PATH" >> /etc/environment

        echo "  uv: $(uv --version 2>/dev/null || echo MISSING)"
        echo "  uvx: $(uvx --version 2>/dev/null || echo MISSING)"
        echo "  pilot: $(pilot version 2>/dev/null || echo installed)"
    '
fi

# ─── Step 6b: Configure Claude Code auth ─────────────────────────────────────
echo ""
echo "--- Configuring Claude Code auth ---"

# v5: subscription via CLAUDE_CODE_OAUTH_TOKEN. Claude Code uses the OAuth
# token to route to api.anthropic.com (no proxy). Model selection is governed
# by Pilot's model router based on task complexity (Opus 4.7 / Sonnet 4.6).
echo "  Auth: Claude Code OAuth (subscription)"
echo "  CLAUDE_CODE_OAUTH_TOKEN: loaded (${#CLAUDE_CODE_OAUTH_TOKEN} chars)"
echo "  ANTHROPIC_MODEL: $ANTHROPIC_MODEL  (router will downgrade to Sonnet for low/medium complexity)"

# ─── Step 6c: Validate critical dependencies ─────────────────────────────────
echo ""
echo "--- Validating dependencies ---"

DEP_CHECK=$(docker exec -w / -e PATH="/opt/pilot-tools/bin:/root/.local/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    "$CONTAINER_NAME" bash -c '
    MISSING=""
    command -v git     >/dev/null 2>&1 || MISSING="$MISSING git"
    command -v node    >/dev/null 2>&1 || MISSING="$MISSING node"
    command -v claude  >/dev/null 2>&1 || MISSING="$MISSING claude"
    command -v uvx     >/dev/null 2>&1 || MISSING="$MISSING uvx"
    command -v pilot   >/dev/null 2>&1 || MISSING="$MISSING pilot"
    if [ -n "$MISSING" ]; then
        echo "FATAL: Missing dependencies:$MISSING"
        exit 1
    fi
    echo "OK: node=$(node -v) claude=$(claude --version 2>&1 | head -1) uvx=$(uvx --version) pilot=$(pilot version 2>/dev/null || echo ok)"
' 2>&1) || {
    echo "ERROR: Container dependency check failed:"
    echo "$DEP_CHECK"
    echo ""
    echo "Aborting trial — writing failure result"
    mkdir -p "${RESULTS_DIR}"
    echo "0.0" > "${RESULTS_DIR}/reward.txt"
    echo "Dependency install failed: $DEP_CHECK" > "${RESULTS_DIR}/verifier-output.txt"
    cat > "${RESULTS_DIR}/trial-meta.json" << METAEOF
{
    "task_name": "${TASK_NAME}",
    "trial_id": "${TRIAL_ID}",
    "run_id": "${RUN_ID}",
    "model": "${MODEL}",
    "docker_image": "${DOCKER_IMAGE}",
    "reward": 0.0,
    "duration_sec": 0,
    "instance_id": "$(curl -s http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null || echo unknown)",
    "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "completed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "error": "dependency_install_failed"
}
METAEOF
    S3_DEST="s3://${S3_BUCKET}/${S3_RUNS}/${RUN_ID}/${TASK_NAME}/${TRIAL_ID}"
    aws s3 cp --recursive "${RESULTS_DIR}/" "$S3_DEST/" --sse aws:kms --quiet
    docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
    exit 1
}
echo "  $DEP_CHECK"

# NOTE: Test files are NOT copied before execution — that's oracle access (Harbor violation).
# Tests are copied AFTER pilot exits, only for the verifier step.

# ─── Step 7: Run environment bootstrap ────────────────────────────────────────
echo ""
echo "--- Environment bootstrap ---"

# Some TB2 images use /workspace (or other dirs) as WORKDIR instead of /app.
# All downstream steps (oracle removal, git init, agent exec, verifier) assume /app.
# If /app is missing, symlink it to the image's WORKDIR so everything resolves.
# This is Harbor-safe: oracle-file removal and canary-grep still operate on the
# real workdir contents through the symlink; no test access is introduced.
docker exec -w / "$CONTAINER_NAME" bash -c '
    if [ ! -e /app ]; then
        for cand in /workspace /home/user /home/agent /srv; do
            if [ -d "$cand" ]; then
                ln -s "$cand" /app
                echo "  Symlinked /app -> $cand (image uses non-standard WORKDIR)"
                break
            fi
        done
        [ -e /app ] || { mkdir -p /app; echo "  Created /app (no known WORKDIR found)"; }
    fi
'

docker exec -w / "$CONTAINER_NAME" bash -c '
    (
        echo "=== FILES ==="
        ls /app/ 2>/dev/null | head -30
        echo "=== PYTHON PACKAGES ==="
        python3 -c "import torch; print(\"torch=\"+torch.__version__)" 2>/dev/null || echo "torch=missing"
        python3 -c "import scipy; print(\"scipy=\"+scipy.__version__)" 2>/dev/null || echo "scipy=missing"
        python3 -c "import pandas; print(\"pandas=\"+pandas.__version__)" 2>/dev/null || echo "pandas=missing"
        echo "=== SYSTEM ==="
        free -m 2>/dev/null | grep Mem || echo "free: N/A"
        echo "CPUs: $(nproc 2>/dev/null || echo N/A)"
    ) > /app/.pilot-env-context.txt 2>&1
'
echo "  Bootstrap written to /app/.pilot-env-context.txt"

# Remove oracle test files BEFORE agent runs (Harbor H1/H4 compliance).
# Docker images may ship test_outputs.py, test.sh, tests/ at /app/, /workspace/,
# /tests/ (root), or other WORKDIRs. Be exhaustive — the verifier phase will
# deliberately `docker cp` tests into /tests AFTER pilot exits.
docker exec -w / "$CONTAINER_NAME" bash -c '
    set +e
    REMOVED=0
    # Known oracle file names at all common task dirs + container root.
    for base in /app /workspace /tests /home/user /home/agent /srv /root; do
        for f in test_outputs.py test.sh conftest.py pytest.ini tests; do
            target="$base/$f"
            if [ -e "$target" ]; then
                rm -rf "$target" 2>/dev/null && REMOVED=$((REMOVED+1))
                echo "  [oracle-guard] removed: $target"
            fi
        done
    done
    # Canary-grep across all plausible task dirs (scoped — no system-wide grep).
    for scan in /app /workspace /tests /home /srv /opt; do
        [ -d "$scan" ] || continue
        grep -rl --include="*" "terminal-bench-canary" "$scan" 2>/dev/null | while read -r hit; do
            rm -f "$hit" 2>/dev/null && echo "  [oracle-guard] canary file removed: $hit"
        done
    done
    echo "  [oracle-guard] pre-agent cleanup complete (removed: $REMOVED direct targets)"
'
echo "  Oracle files removed (broadened scan: /app /workspace /tests /home/user /home/agent /srv /root)"

# Initialize git repo in /app (pilot needs it)
docker exec -w / "$CONTAINER_NAME" bash -c '
    cd /app
    if [ ! -d .git ]; then
        git init -q
        git add -A 2>/dev/null || true
        git commit -q -m "initial" --allow-empty 2>/dev/null || true
    fi
'

# ─── Step 8: Execute pilot task ───────────────────────────────────────────────
echo ""
echo "=== Executing pilot task ==="
echo "Instruction (first 200 chars): ${INSTRUCTION:0:200}..."
echo ""

TASK_START=$(date +%s)

# Run pilot inside container
# When OAuth token is available, DON'T pass ANTHROPIC_API_KEY — it takes precedence
# over apiKeyHelper in settings.json and the API key account may have no credits.
# Pilot's effort classifier will fall back to static mapping (acceptable).
EXEC_ENV_ARGS=(-e IS_SANDBOX=1 -e CLAUDE_CODE_MAX_OUTPUT_TOKENS=54000 -e PATH="/opt/pilot-tools/bin:/root/.local/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
# v5: pass Claude Code OAuth token (subscription) + default model. ANTHROPIC_BASE_URL
# is intentionally NOT set so Claude Code uses api.anthropic.com (no Z.AI proxy).
EXEC_ENV_ARGS+=(
    -e CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_CODE_OAUTH_TOKEN"
    -e ANTHROPIC_MODEL="$ANTHROPIC_MODEL"
)
echo "  Auth: CLAUDE_CODE_OAUTH_TOKEN (subscription)"
echo "  Model: $ANTHROPIC_MODEL  (router-driven downgrade to Sonnet for low/medium complexity)"
set +e  # handle pilot exit code ourselves
docker exec -w /app \
    "${EXEC_ENV_ARGS[@]}" \
    "$CONTAINER_NAME" \
    timeout "${TASK_AGENT_TIMEOUT:-${MAIN_TIMEOUT}}" \
    pilot task \
        --local \
        --project /app \
        --verbose \
        --result-json /logs/agent/pilot-result.json \
        -- "$INSTRUCTION" \
    2>&1 | tee "${LOGS_DIR}/pilot-stdout.log"
PILOT_EXIT=${PIPESTATUS[0]}
set -e
echo "  Pilot exit code: $PILOT_EXIT"

TASK_END=$(date +%s)
TASK_DURATION=$((TASK_END - TASK_START))
echo ""
echo "Task duration: ${TASK_DURATION}s"

# ─── Trial validity checks (detect fake wins) ────────────────────────────────
# Fake-win scenarios to catch:
#   1. Pilot crashed (PILOT_EXIT != 0)
#   2. Claude Code hit rate limit (markers in pilot-stdout.log)
#   3. Claude did nothing — /app unchanged vs baseline
# In any of these cases, skip the verifier and mark the trial as non-scoring.

PILOT_RATE_LIMITED=0
RESET_TIME=""
if grep -qE "You've hit your limit|rate_limit_error|overloaded_error" \
    "${LOGS_DIR}/pilot-stdout.log" 2>/dev/null; then
    PILOT_RATE_LIMITED=1
    RESET_TIME=$(grep -oE "resets [0-9apm:]+ \([A-Z]+\)" "${LOGS_DIR}/pilot-stdout.log" | head -1)
    echo "  Rate limit detected: ${RESET_TIME:-unknown reset}"
fi

# Count /app changes since the initial git commit (Step 7 init baseline).
# Excludes .pilot-env-context.txt which the bootstrap writes itself.
CHANGES=$(docker exec -w /app "$CONTAINER_NAME" \
    bash -c "git -C /app status --porcelain 2>/dev/null | grep -v '.pilot-env-context.txt' | wc -l" 2>/dev/null || echo "0")
CHANGES=$(echo "$CHANGES" | tr -d '[:space:]')
echo "  Files changed in /app: ${CHANGES:-0}"

# ─── Step 9: Run verifier ─────────────────────────────────────────────────────
echo ""
echo "--- Running verifier ---"

REWARD="0.0"
VERIFIER_OUTPUT=""
TRIAL_STATUS="real"  # real | pilot_failed | rate_limited | no_op | no_tests

# Guard: skip verifier entirely if pilot failed, rate-limited, or did nothing.
# This prevents scoring against the Docker image's untouched /app state.
if [ "${PILOT_EXIT:-0}" -ne 0 ]; then
    TRIAL_STATUS="pilot_failed"
    VERIFIER_OUTPUT="SKIPPED: pilot exited with status ${PILOT_EXIT}"
    echo "  $VERIFIER_OUTPUT"
elif [ "${PILOT_RATE_LIMITED:-0}" -eq 1 ]; then
    TRIAL_STATUS="rate_limited"
    VERIFIER_OUTPUT="SKIPPED: Claude Code rate-limited (${RESET_TIME:-unknown reset})"
    echo "  $VERIFIER_OUTPUT"
elif [ "${CHANGES:-0}" -eq 0 ]; then
    TRIAL_STATUS="no_op"
    VERIFIER_OUTPUT="SKIPPED: pilot made zero changes to /app"
    echo "  $VERIFIER_OUTPUT"
else
    # Pilot ran successfully and modified /app — run the real verifier.

    # Copy test files NOW (after pilot exits) — NOT before execution (oracle violation)
    if [ -n "$TASK_DIR" ] && [ -d "$TASK_DIR/tests" ]; then
        echo "  Copying test files for verifier..."
        docker exec -w / "$CONTAINER_NAME" mkdir -p /tests
        for f in "$TASK_DIR/tests/"*; do
            [ -f "$f" ] && docker cp "$f" "$CONTAINER_NAME:/tests/$(basename $f)"
        done
    fi

    # Try running test.sh from the task definition
    # TB2 task structure: $TASK_DIR/tests/test.sh (NOT $TASK_DIR/test.sh)
    TEST_SH_PATH=""
    if [ -n "$TASK_DIR" ] && [ -f "$TASK_DIR/tests/test.sh" ]; then
        TEST_SH_PATH="$TASK_DIR/tests/test.sh"
    elif [ -n "$TASK_DIR" ] && [ -f "$TASK_DIR/test.sh" ]; then
        TEST_SH_PATH="$TASK_DIR/test.sh"
    fi

    if [ -n "$TEST_SH_PATH" ]; then
        echo "  Running canonical test.sh: $TEST_SH_PATH"
        docker cp "$TEST_SH_PATH" "$CONTAINER_NAME:/tmp/test.sh"
        docker exec -w / "$CONTAINER_NAME" chmod +x /tmp/test.sh
        VERIFIER_OUTPUT=$(docker exec -w / "$CONTAINER_NAME" bash -c "cd /app && /tmp/test.sh 2>&1" || true)

        # TB2 canonical protocol: test.sh writes reward to /logs/verifier/reward.txt
        REWARD_FILE=$(docker exec -w / "$CONTAINER_NAME" cat /logs/verifier/reward.txt 2>/dev/null | tr -d '[:space:]' || echo "")
        if [ "$REWARD_FILE" = "1" ]; then
            REWARD="1.0"
        fi
        # Fallback: grep stdout for non-standard test.sh scripts
        if [ "$REWARD" = "0.0" ] && echo "$VERIFIER_OUTPUT" | grep -qi "pass\|success\|reward.*1"; then
            REWARD="1.0"
        fi
    fi

    # Also check test_outputs.py
    if [ "$REWARD" = "0.0" ]; then
        TEST_RESULT=$(docker exec -w / "$CONTAINER_NAME" bash -c '
            export PATH="/opt/pilot-tools/bin:/root/.local/bin:/usr/local/bin:$PATH"
            if [ -f /tests/test_outputs.py ]; then
                cd /app
                pip install -q pytest 2>/dev/null || pip3 install -q pytest 2>/dev/null || true
                python3 -m pytest /tests/test_outputs.py -rA 2>&1
                PYTEST_EXIT=$?
                if [ $PYTEST_EXIT -ne 0 ]; then
                    # Fallback to uvx only if python3 pytest failed
                    uvx -p 3.13 --with pytest pytest /tests/test_outputs.py -rA 2>&1
                    PYTEST_EXIT=$?
                fi
                echo "EXIT_CODE=$PYTEST_EXIT"
            else
                echo "NO_TESTS"
                echo "EXIT_CODE=99"  # missing verifier = failure, not success
            fi
        ' 2>/dev/null || true)

        EXIT_CODE=$(echo "$TEST_RESULT" | grep "EXIT_CODE=" | tail -1 | cut -d= -f2)
        if [ "${EXIT_CODE:-99}" = "0" ]; then
            REWARD="1.0"
        fi
        VERIFIER_OUTPUT="${VERIFIER_OUTPUT}\n${TEST_RESULT}"

        # If no verifier existed at all (neither test.sh nor test_outputs.py), flag it
        if [ -z "$TEST_SH_PATH" ] && echo "$TEST_RESULT" | grep -q "NO_TESTS"; then
            TRIAL_STATUS="no_tests"
        fi
    fi
fi

echo "Reward: $REWARD"

# ─── Step 10: Collect results ─────────────────────────────────────────────────
echo ""
echo "--- Collecting results ---"

# Copy pilot result JSON
docker cp "$CONTAINER_NAME:/logs/agent/pilot-result.json" "${RESULTS_DIR}/pilot-result.json" 2>/dev/null || echo '{}' > "${RESULTS_DIR}/pilot-result.json"

# Copy learned patterns DB
docker cp "$CONTAINER_NAME:/root/.pilot/data/pilot.db" "${RESULTS_DIR}/pilot-patterns.db" 2>/dev/null || true

# Write reward file
echo "$REWARD" > "${RESULTS_DIR}/reward.txt"

# Write trial metadata
cat > "${RESULTS_DIR}/trial-meta.json" << EOF
{
    "task_name": "${TASK_NAME}",
    "trial_id": "${TRIAL_ID}",
    "run_id": "${RUN_ID}",
    "model": "${MODEL}",
    "docker_image": "${DOCKER_IMAGE}",
    "reward": ${REWARD},
    "trial_status": "${TRIAL_STATUS}",
    "pilot_exit": ${PILOT_EXIT:-0},
    "changes_in_app": ${CHANGES:-0},
    "rate_limited": ${PILOT_RATE_LIMITED:-0},
    "reset_time": "${RESET_TIME}",
    "duration_sec": ${TASK_DURATION},
    "instance_id": "$(curl -s http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null || echo unknown)",
    "started_at": "$(date -u -d @${TASK_START} +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)",
    "completed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

# Write verifier output
echo -e "$VERIFIER_OUTPUT" > "${RESULTS_DIR}/verifier-output.txt"

# Save pilot stdout log
cp "${LOGS_DIR}/pilot-stdout.log" "${RESULTS_DIR}/" 2>/dev/null || true

# ─── Step 11: Upload results to S3 ───────────────────────────────────────────
echo ""
echo "--- Uploading results to S3 ---"

S3_DEST="s3://${S3_BUCKET}/${S3_RUNS}/${RUN_ID}/${TASK_NAME}/${TRIAL_ID}"
aws s3 cp --recursive "${RESULTS_DIR}/" "$S3_DEST/" --sse aws:kms --quiet
echo "  Uploaded to $S3_DEST"

# ─── Step 12: Cleanup ────────────────────────────────────────────────────────
echo ""
echo "--- Cleanup ---"

docker stop "$CONTAINER_NAME" 2>/dev/null || true
docker rm "$CONTAINER_NAME" 2>/dev/null || true
rm -rf "$WORKSPACE"

# Prune unused images to save disk (keep last 3)
docker image prune -f --filter "until=24h" 2>/dev/null || true

echo ""
echo "=== Task Complete ==="
echo "Task:     $TASK_NAME"
echo "Trial:    $TRIAL_ID"
echo "Reward:   $REWARD"
echo "Duration: ${TASK_DURATION}s"
echo "Results:  $S3_DEST"
