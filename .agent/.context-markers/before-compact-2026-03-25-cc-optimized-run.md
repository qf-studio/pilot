# Context Marker: Back to CC Backend — Optimized with Engine Learnings

**Date**: 2026-03-25 | **Branch**: `feat/pilot-bench-real`

## Why Back to CC

Custom `anthropic-api` Go backend was built and works, but:
1. **OAuth tokens rejected** by raw Anthropic API (401 "OAuth not supported")
2. **API credits depleted** ($57 burned on 12 trials with Python engine)
3. CC CLI handles OAuth internally through Anthropic's gateway — free on CC subscription

## What We Learned from Custom Engine

1. **83.3% score** at 12 trials — proves the task set is solvable
2. **Progressive thinking** helps but CC doesn't expose thinking budget control
3. **Model routing** saves 53% cost — now wired to CC via `--model` flag
4. **Effort routing** matters — `max` causes OOM, `high` is the ceiling
5. **Session resume** saves 40% tokens on self-review — now enabled
6. **15m heartbeat** needed for complex tasks — was 5m (false kills)
7. **Quality gates work** in LocalMode — deps are pre-installed, no OOM risk

## v33 Config (Current Run)

```yaml
executor:
  type: "claude-code"
  claude_code:
    use_session_resume: true      # NEW: 40% token savings
  heartbeat_timeout: 15m          # NEW: was 5m default
  model_routing:
    trivial: haiku                # NEW: was all-same model
    simple: sonnet                # NEW
    medium: opus
    complex: opus
  effort_routing: low/med/high/high  # KEPT: max causes OOM
  effort_classifier: enabled (Haiku)
quality:
  enabled: true                   # gates with pytest retry
memory:
  learning: enabled               # 17 patterns
```

## What Still Exists (for When Credits Come)

- `internal/executor/backend_anthropic.go` — Go direct API backend (tested, compiles)
- `pilot-bench/pilot_agent/engine.py` — Python standalone engine (proven 83%)
- `internal/executor/backend_factory.go` — `anthropic-api` case ready

Switch to direct API anytime: change `type: "anthropic-api"` + add `ANTHROPIC_API_KEY`.

## Score History

| Run | Backend | Score | Cost |
|-----|---------|-------|------|
| v24 (CC, k=1) | CC 2.1.74 | 65.9% | CC sub |
| v32 (CC, k=5) | CC 2.1.74 | 49.2% @58 | CC sub |
| engine-v9 | Python direct | 83.3% @12 | $57 API |
| v33 (CC optimized) | CC 2.1.74 | 100% @3 (validation) | CC sub ($0) |
| **v35 (CC stripped)** | **CC 2.1.74** | **83.1% first pass (74/89)** | **CC sub ($0)** |

## Resume

```bash
# Check v33 results
python3 -c "
import json
r = json.load(open('pilot-bench/jobs/pilot-cc-v33/result.json'))
s = r['stats']['evals']['pilot-real__claude-opus-4-6__terminal-bench']
print(f\"Score: {s['metrics'][0]['mean']:.1%}\")
"

# Full k=5 run (after v33 validates)
source .env && cd pilot-bench && harbor run \
  --job-name pilot-cc-v33-k5 -o jobs -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e modal -n 2 -k 5 \
  --agent-timeout-multiplier 9.0 --agent-setup-timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"
```
