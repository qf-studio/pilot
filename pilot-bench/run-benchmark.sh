#!/bin/bash
# Full Terminal-Bench 2.0 benchmark run with logging.
#
# Usage:
#   ./run-benchmark.sh                  # Full run, 8 parallel
#   ./run-benchmark.sh --parallel 16    # More parallelism
#   ./run-benchmark.sh --dry-run        # Show command only
#   ./run-benchmark.sh --run-id v1      # Custom run ID
#
# Results saved to: results/<run-id>/

set -euo pipefail
cd "$(dirname "$0")"

# Defaults
PARALLEL=8
MODEL="${MODEL:-anthropic/claude-opus-4-6}"
RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
DRY_RUN=false

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --parallel) PARALLEL="$2"; shift 2 ;;
        --model) MODEL="$2"; shift 2 ;;
        --run-id) RUN_ID="$2"; shift 2 ;;
        --dry-run) DRY_RUN=true; shift ;;
        *) echo "Unknown arg: $1"; exit 1 ;;
    esac
done

RESULTS_DIR="results/$RUN_ID"
mkdir -p "$RESULTS_DIR"

echo "=== Terminal-Bench 2.0 Full Benchmark ==="
echo "Run ID:    $RUN_ID"
echo "Model:     $MODEL"
echo "Parallel:  $PARALLEL"
echo "Results:   $RESULTS_DIR/"
echo "Started:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

# Save run metadata
cat > "$RESULTS_DIR/metadata.json" << EOF
{
    "run_id": "$RUN_ID",
    "model": "$MODEL",
    "parallel": $PARALLEL,
    "agent": "pilot_agent:PilotAgent",
    "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "hostname": "$(hostname)",
    "agent_version": "$(python3 -c 'import tomllib; print(tomllib.load(open("pyproject.toml","rb"))["project"]["version"])' 2>/dev/null || echo 'unknown')"
}
EOF

CMD="harbor run \
    -d terminal-bench@2.0 \
    -a pilot_agent:PilotAgent \
    --model $MODEL \
    --env docker \
    --parallel $PARALLEL \
    --output-dir $RESULTS_DIR"

if $DRY_RUN; then
    echo "[dry-run] Would execute:"
    echo "  $CMD"
    exit 0
fi

echo "Running benchmark..."
echo "$CMD" > "$RESULTS_DIR/command.txt"

START=$(date +%s)
$CMD 2>&1 | tee "$RESULTS_DIR/run.log"
END=$(date +%s)

DURATION=$((END - START))
echo ""
echo "Completed in $((DURATION / 60))m $((DURATION % 60))s"

# Update metadata with completion
python3 -c "
import json
with open('$RESULTS_DIR/metadata.json') as f:
    meta = json.load(f)
meta['completed_at'] = '$(date -u +%Y-%m-%dT%H:%M:%SZ)'
meta['duration_seconds'] = $DURATION
with open('$RESULTS_DIR/metadata.json', 'w') as f:
    json.dump(meta, f, indent=2)
"

# Analyze results
echo ""
echo "=== Analyzing results ==="
python3 pilot_agent/scripts/analyze-results.py "$RESULTS_DIR"

echo ""
echo "Full results: $RESULTS_DIR/"
echo "To compare with another run: python3 pilot_agent/scripts/analyze-results.py $RESULTS_DIR --compare results/<other>"
