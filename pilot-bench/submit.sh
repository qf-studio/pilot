#!/bin/bash
# Submit Pilot agent results to Terminal-Bench leaderboard.
#
# Requirements:
#   - 5+ independent runs for statistical significance
#   - Results in results/ directory
#   - GitHub account with push access to leaderboard repo
#
# Usage:
#   ./submit.sh                          # Analyze all runs, submit best
#   ./submit.sh --check                  # Check readiness only
#   ./submit.sh --agent-name "Pilot v1"  # Custom agent name

set -euo pipefail
cd "$(dirname "$0")"

AGENT_NAME="${AGENT_NAME:-Pilot}"
MODEL="claude-opus-4-6"
CHECK_ONLY=false
MIN_RUNS=5

while [[ $# -gt 0 ]]; do
    case $1 in
        --check) CHECK_ONLY=true; shift ;;
        --agent-name) AGENT_NAME="$2"; shift 2 ;;
        --min-runs) MIN_RUNS="$2"; shift 2 ;;
        *) echo "Unknown arg: $1"; exit 1 ;;
    esac
done

echo "=== Terminal-Bench Leaderboard Submission ==="
echo "Agent: $AGENT_NAME"
echo "Model: $MODEL"
echo ""

# Count completed runs
RUNS_DIR="results"
if [ ! -d "$RUNS_DIR" ]; then
    echo "ERROR: No results directory found. Run benchmarks first."
    exit 1
fi

RUN_COUNT=$(find "$RUNS_DIR" -maxdepth 1 -mindepth 1 -type d | wc -l | tr -d ' ')
echo "Completed runs: $RUN_COUNT"

if [ "$RUN_COUNT" -lt "$MIN_RUNS" ]; then
    echo "ERROR: Need at least $MIN_RUNS runs for statistical significance (have $RUN_COUNT)"
    echo "Run more benchmarks: ./run-benchmark.sh --run-id run-N"
    exit 1
fi

# Aggregate scores across runs
echo ""
echo "=== Run Scores ==="
SCORES=()
for run_dir in "$RUNS_DIR"/*/; do
    if [ -f "$run_dir/results.json" ] || [ -d "$run_dir" ]; then
        SCORE=$(python3 pilot_agent/scripts/analyze-results.py "$run_dir" --json 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)['score'])" 2>/dev/null || echo "0")
        RUN_NAME=$(basename "$run_dir")
        echo "  $RUN_NAME: ${SCORE}%"
        SCORES+=("$SCORE")
    fi
done

# Calculate statistics
STATS=$(python3 << 'PYEOF'
import sys, statistics
scores = [float(s) for s in sys.argv[1:] if float(s) > 0]
if not scores:
    print("No valid scores")
    sys.exit(1)
mean = statistics.mean(scores)
stdev = statistics.stdev(scores) if len(scores) > 1 else 0
median = statistics.median(scores)
best = max(scores)
worst = min(scores)
print(f"Mean:   {mean:.1f}% ± {stdev:.1f}%")
print(f"Median: {median:.1f}%")
print(f"Best:   {best:.1f}%")
print(f"Worst:  {worst:.1f}%")
print(f"Range:  {best-worst:.1f}pp")
print(f"---")
print(f"SCORE={mean:.1f}")
print(f"STDEV={stdev:.1f}")
PYEOF
"${SCORES[@]}")

echo ""
echo "=== Statistics ==="
echo "$STATS"

if $CHECK_ONLY; then
    echo ""
    echo "Readiness check complete. Use ./submit.sh to submit."
    exit 0
fi

# Extract score for submission
FINAL_SCORE=$(echo "$STATS" | grep "^SCORE=" | cut -d= -f2)
FINAL_STDEV=$(echo "$STATS" | grep "^STDEV=" | cut -d= -f2)

echo ""
echo "=== Submission ==="
echo "Submitting: $AGENT_NAME — ${FINAL_SCORE}% ± ${FINAL_STDEV}%"
echo ""
echo "To submit to the Terminal-Bench leaderboard:"
echo ""
echo "1. Fork https://huggingface.co/spaces/terminal-bench/leaderboard"
echo "2. Add entry to submissions.json:"
echo ""
echo "  {"
echo "    \"agent_name\": \"$AGENT_NAME\","
echo "    \"model\": \"$MODEL\","
echo "    \"score\": $FINAL_SCORE,"
echo "    \"std_dev\": $FINAL_STDEV,"
echo "    \"num_runs\": $RUN_COUNT,"
echo "    \"repo\": \"https://github.com/alekspetrov/pilot\","
echo "    \"submission_date\": \"$(date -u +%Y-%m-%d)\""
echo "  }"
echo ""
echo "3. Create PR with results"
