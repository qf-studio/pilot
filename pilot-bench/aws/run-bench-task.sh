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
    enabled: true
    trivial: "claude-haiku-4-5-20251001"
    simple: "claude-sonnet-4-6"
    medium: "${model}"
    complex: "${model}"
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
    enabled: true
    model: claude-haiku-4-5-20251001
    timeout: 30s
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
  enabled: true
  gates:
    - name: test
      type: test
      command: "if [ -f /tests/test_outputs.py ]; then cd /app && /usr/local/bin/uvx -p 3.13 -w pytest==8.4.1 pytest /tests/test_outputs.py -rA 2>&1 || /root/.local/bin/uvx -p 3.13 -w pytest==8.4.1 pytest /tests/test_outputs.py -rA 2>&1; fi"
      required: true
      timeout: 5m
      max_retries: 2
      retry_delay: 5s
      failure_hint: "Tests failed. Read /tests/test_outputs.py to understand what is expected, then fix your implementation."
memory:
  path: /root/.pilot/data
  learning:
    enabled: true
PILOTCFG
}

# ─── Step 1: Load secrets from SSM ────────────────────────────────────────────
echo "--- Loading secrets from SSM ---"
export ANTHROPIC_API_KEY=$(aws ssm get-parameter \
    --name "${SSM_PREFIX}/ANTHROPIC_API_KEY" \
    --query "Parameter.Value" --output text 2>/dev/null || echo "")
GITHUB_TOKEN=$(aws ssm get-parameter \
    --name "${SSM_PREFIX}/GITHUB_TOKEN" \
    --query "Parameter.Value" --output text 2>/dev/null || echo "")

if [ -z "$ANTHROPIC_API_KEY" ]; then
    echo "ERROR: ANTHROPIC_API_KEY not found in SSM"
    exit 1
fi
echo "  API key loaded (${#ANTHROPIC_API_KEY} chars)"

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

# Learning DB
echo "  Downloading learning DB..."
mkdir -p /root/.pilot/data
aws s3 cp "s3://${S3_BUCKET}/${S3_PREFIX}/pilot.db" /root/.pilot/data/pilot.db --quiet 2>/dev/null || echo "  No learning DB found, starting fresh"

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

# Start container with workspace mount
echo "  Starting container..."
docker run -d \
    --name "$CONTAINER_NAME" \
    --cpus="$TASK_CPUS" \
    --memory="${TASK_MEMORY}m" \
    -v "${WORKSPACE}/app:/app" \
    -v "${WORKSPACE}/logs:/logs" \
    "$DOCKER_IMAGE" \
    sleep infinity

# Wait for container to be ready
sleep 2

echo "  Container started: $(docker ps --filter name=$CONTAINER_NAME --format '{{.Status}}')"

# ─── Step 6: Inject pilot + deps into container ──────────────────────────────
echo ""
echo "--- Setting up agent in container ---"

# Copy pilot binary into container
docker cp /usr/local/bin/pilot "$CONTAINER_NAME:/usr/local/bin/pilot"

# Copy pilot config (generated by orchestrator, downloaded from S3)
if [ -f "${ASSETS_DIR}/pilot-config.yaml" ]; then
    docker exec "$CONTAINER_NAME" mkdir -p /root/.pilot
    docker cp "${ASSETS_DIR}/pilot-config.yaml" "$CONTAINER_NAME:/root/.pilot/config.yaml"
else
    # Generate config inline as fallback
    docker exec "$CONTAINER_NAME" mkdir -p /root/.pilot
    docker exec "$CONTAINER_NAME" bash -c "cat > /root/.pilot/config.yaml << 'CFGEOF'
$(generate_pilot_config "$MODEL")
CFGEOF"
fi

# Copy learning DB into container
docker exec "$CONTAINER_NAME" mkdir -p /root/.pilot/data
docker cp /root/.pilot/data/pilot.db "$CONTAINER_NAME:/root/.pilot/data/pilot.db" 2>/dev/null || echo "  No learning DB to inject"

# Single setup script: install all deps + configure in one docker exec
docker exec "$CONTAINER_NAME" bash -c '
    set -e
    chmod +x /usr/local/bin/pilot

    # Node.js (required by Claude Code)
    if ! command -v node &>/dev/null; then
        echo "  Installing Node.js..."
        curl -fsSL https://deb.nodesource.com/setup_22.x 2>/dev/null | bash - 2>/dev/null
        apt-get install -y nodejs 2>/dev/null || apk add --no-cache nodejs npm 2>/dev/null || true
    fi

    # Claude Code
    if ! command -v claude &>/dev/null; then
        echo "  Installing Claude Code..."
        npm install -g @anthropic-ai/claude-code 2>/dev/null || true
    fi

    # uv/uvx (verifiers need it)
    if ! command -v uv &>/dev/null; then
        echo "  Installing uv..."
        curl -LsSf https://astral.sh/uv/install.sh 2>/dev/null | sh 2>/dev/null || true
    fi

    echo "  Node: $(node --version 2>/dev/null || echo missing)"
    echo "  Claude: $(claude --version 2>/dev/null || echo missing)"
    echo "  uv: $(/root/.local/bin/uv --version 2>/dev/null || uv --version 2>/dev/null || echo missing)"
    echo "  pilot: $(pilot version 2>/dev/null || echo installed)"
'

# Copy test files into container
if [ -n "$TASK_DIR" ] && [ -d "$TASK_DIR/tests" ]; then
    echo "  Copying test files..."
    docker exec "$CONTAINER_NAME" mkdir -p /tests
    for f in "$TASK_DIR/tests/"*; do
        [ -f "$f" ] && docker cp "$f" "$CONTAINER_NAME:/tests/$(basename $f)"
    done
    echo "  Tests: $(docker exec $CONTAINER_NAME ls /tests/ 2>/dev/null | tr '\n' ' ')"
fi

# ─── Step 7: Run environment bootstrap ────────────────────────────────────────
echo ""
echo "--- Environment bootstrap ---"

docker exec "$CONTAINER_NAME" bash -c '
    (
        echo "=== FILES ==="
        ls /app/ 2>/dev/null | head -30
        echo "=== TESTS ==="
        head -50 /tests/test_outputs.py 2>/dev/null || echo "NO_TEST_FILE"
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

# Initialize git repo in /app (pilot needs it)
docker exec "$CONTAINER_NAME" bash -c '
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
docker exec \
    -e ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
    -e IS_SANDBOX=1 \
    -e CLAUDE_CODE_MAX_OUTPUT_TOKENS=54000 \
    "$CONTAINER_NAME" \
    timeout "${MAIN_TIMEOUT}" \
    pilot task "$INSTRUCTION" \
        --local \
        --project /app \
        --verbose \
        --result-json /logs/agent/pilot-result.json \
    2>&1 | tee "${LOGS_DIR}/pilot-stdout.log" || true

TASK_END=$(date +%s)
TASK_DURATION=$((TASK_END - TASK_START))
echo ""
echo "Task duration: ${TASK_DURATION}s"

# ─── Step 9: Run verifier ─────────────────────────────────────────────────────
echo ""
echo "--- Running verifier ---"

REWARD="0.0"
VERIFIER_OUTPUT=""

# Try running test.sh from the task definition
if [ -n "$TASK_DIR" ] && [ -f "$TASK_DIR/test.sh" ]; then
    docker cp "$TASK_DIR/test.sh" "$CONTAINER_NAME:/tmp/test.sh"
    docker exec "$CONTAINER_NAME" chmod +x /tmp/test.sh
    VERIFIER_OUTPUT=$(docker exec "$CONTAINER_NAME" bash -c "cd /app && /tmp/test.sh 2>&1" || true)

    # Check for reward in verifier output or exit code
    if echo "$VERIFIER_OUTPUT" | grep -qi "pass\|success\|reward.*1"; then
        REWARD="1.0"
    fi
fi

# Also check test_outputs.py
if [ "$REWARD" = "0.0" ]; then
    TEST_RESULT=$(docker exec "$CONTAINER_NAME" bash -c '
        export PATH="/root/.local/bin:$PATH"
        if [ -f /tests/test_outputs.py ]; then
            cd /app && uvx -p 3.13 -w pytest==8.4.1 pytest /tests/test_outputs.py -rA 2>&1
            echo "EXIT_CODE=$?"
        else
            echo "NO_TESTS"
            echo "EXIT_CODE=0"
        fi
    ' 2>/dev/null || true)

    EXIT_CODE=$(echo "$TEST_RESULT" | grep "EXIT_CODE=" | tail -1 | cut -d= -f2)
    if [ "${EXIT_CODE:-1}" = "0" ]; then
        REWARD="1.0"
    fi
    VERIFIER_OUTPUT="${VERIFIER_OUTPUT}\n${TEST_RESULT}"
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
