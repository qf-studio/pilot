#!/bin/bash
# run-aws-bench.sh — Run Terminal Bench 2.0 on AWS warm pool infrastructure.
#
# Usage:
#   ./run-aws-bench.sh                                          # 3-task validation
#   ./run-aws-bench.sh --tasks all                              # Full 89-task run
#   ./run-aws-bench.sh --tasks all --k 5 --parallel 10          # Leaderboard run
#   ./run-aws-bench.sh --tasks chess-best-move                  # Single task
#   ./run-aws-bench.sh --extract-only                           # Only generate manifest
#
# Prerequisites:
#   - AWS credentials configured (profile: quantflow or env vars)
#   - Pilot binary built: make bench-binary
#   - Task manifest generated: python3 extract_tasks.py (auto-runs if missing)
#   - boto3 installed: pip install boto3

set -euo pipefail
cd "$(dirname "$0")"

# Defaults
TASKS="break-filter-js-from-html,chess-best-move,gcode-to-text"  # Validation set
K_TRIALS=1
PARALLEL=5
MODEL="${MODEL:-claude-opus-4-6}"
RUN_ID="${RUN_ID:-aws-$(date +%Y%m%d-%H%M%S)}"
EXTRACT_ONLY=false

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --tasks) TASKS="$2"; shift 2 ;;
        --k) K_TRIALS="$2"; shift 2 ;;
        --parallel) PARALLEL="$2"; shift 2 ;;
        --model) MODEL="$2"; shift 2 ;;
        --run-id) RUN_ID="$2"; shift 2 ;;
        --extract-only) EXTRACT_ONLY=true; shift ;;
        -h|--help)
            head -12 "$0" | tail -10
            exit 0
            ;;
        *) echo "Unknown arg: $1"; exit 1 ;;
    esac
done

echo "=== AWS Bench Runner ==="
echo "Run ID:    $RUN_ID"
echo "Tasks:     $TASKS"
echo "Trials:    $K_TRIALS"
echo "Parallel:  $PARALLEL"
echo "Model:     $MODEL"
echo ""

# Check prerequisites
if ! command -v aws &>/dev/null; then
    echo "ERROR: AWS CLI not found. Install: brew install awscli"
    exit 1
fi

if ! python3 -c "import boto3" 2>/dev/null; then
    echo "ERROR: boto3 not found. Install: pip install boto3"
    exit 1
fi

# Always regenerate task manifest to avoid stale data
MANIFEST="tasks-manifest.json"
echo "Generating task manifest..."
python3 extract_tasks.py --output "$MANIFEST" --force

if [ "$EXTRACT_ONLY" = true ]; then
    echo "Manifest generated. Exiting (--extract-only)."
    exit 0
fi

# Validate manifest task count
TASK_COUNT=$(python3 -c "import json; print(len(json.load(open('$MANIFEST')).get('tasks',[])))")
echo "Manifest: $TASK_COUNT tasks"
if [ "$TASK_COUNT" -lt 80 ]; then
    echo "ERROR: Manifest has only $TASK_COUNT tasks (expected ~89 for TB 2.0). Aborting."
    exit 1
fi

# Check pilot binary
BENCH_DIR="$(dirname "$0")/.."
if [ ! -f "$BENCH_DIR/bin/pilot-linux-amd64.gz" ] && [ ! -f "$BENCH_DIR/bin/pilot-linux-amd64" ]; then
    echo "WARNING: Pilot binary not found at $BENCH_DIR/bin/"
    echo "  Run 'make bench-binary' from project root to build it."
    echo "  Proceeding anyway — instance must have pilot pre-installed."
fi

# Upload manifest to S3 (orchestrator needs it)
echo "Uploading manifest to S3..."
aws s3 cp "$MANIFEST" "s3://pilot-s3-agent-data/bench/assets/tasks-manifest.json" --sse aws:kms --quiet

# Run orchestrator
echo ""
echo "Starting orchestrator..."
python3 orchestrator.py \
    --run-id "$RUN_ID" \
    --tasks "$TASKS" \
    --k-trials "$K_TRIALS" \
    --max-parallel "$PARALLEL" \
    --model "$MODEL"

EXIT_CODE=$?

# Run analysis if results exist
RESULTS_DIR="../results/$RUN_ID"
if [ -d "$RESULTS_DIR" ]; then
    echo ""
    echo "Running results analysis..."
    python3 ../pilot_agent/scripts/analyze-results.py "$RESULTS_DIR" 2>/dev/null || true
fi

echo ""
echo "Done. Results at: results/$RUN_ID/"
exit $EXIT_CODE
