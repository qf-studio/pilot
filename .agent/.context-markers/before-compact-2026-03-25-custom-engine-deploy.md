# Context Marker: Custom Engine Built, Deploy Blocked

**Date**: 2026-03-25 | **Branch**: `feat/pilot-bench-real`

## What Was Built

### 1. Custom Python Engine (engine.py) — PROVEN 83% score
- Direct Anthropic API calls (no Claude Code)
- Progressive thinking (10K first 8 turns, 3K after)
- Tools: bash, read_file, write_file, edit_file
- Loop detection, auto-test, context pruning, prompt caching
- SSE streaming, retry with 30-180s backoffs
- **Validated**: 3/3 tasks passed, then 10/12 = 83.3% on full run
- **Killed**: API credits exhausted ($57 spent, gpt2-codegolf alone $31)

### 2. Go `anthropic-api` Backend (backend_anthropic.go) — BUILT, NOT TESTED
- Full Backend interface implementation
- SSE streaming parser, tool execution in Go
- Progressive thinking, effort-mapped budgets
- OAuth token support (Bearer auth for sk-ant-oat*)
- **Problem**: 28MB Go binary upload to Modal hangs
- Install script completes, but agent.py `upload_file()` never returns
- v32 (Claude Code era) uploaded the same binary fine — unclear what changed

## Deploy Blocker

Modal `upload_file()` for the Go binary hangs. The Python engine works because it's 15KB vs 28MB. Options:
1. **Use Python engine with OAuth token** — proven to work, bills to CC subscription
2. **Debug Modal upload** — might be a timeout or SDK version issue
3. **Install binary via install script** — wget from GitHub release instead of upload_file

## Key Commits

| Commit | Description |
|--------|-------------|
| `3445b733` | Go anthropic-api backend |
| `5ec2326a` | OAuth token Bearer auth |
| `f05e6f8e` | Robust API retry |
| `8f993884` | Python engine rewrite (all bugs fixed) |
| `8db45e85` | Setup logging for debugging |

## Costs & Scores

| Run | Engine | Score | Cost | Notes |
|-----|--------|-------|------|-------|
| v24 (CC, k=1) | Claude Code | 65.9% | CC subscription | Previous best |
| v32 (CC, k=5) | Claude Code | 49.2% @58 | CC subscription | Best CC k=5 |
| engine-v9 (k=5) | Python engine | 83.3% @12 | $57 API credits | Credits ran out |
| api-v1..v9 | Go binary | 0% | $0 | Binary upload hangs |

## Resume

```bash
# Python engine (works, needs API credits or OAuth):
source .env && cd pilot-bench && harbor run \
  --job-name pilot-engine-v10 -o jobs -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e modal -n 2 -k 5 \
  --agent-timeout-multiplier 9.0 --agent-setup-timeout-multiplier 5.0 \
  --ae "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY"

# To use OAuth token instead, switch agent.py back to Python engine mode
# and pass --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"

# Go binary (needs Modal upload fix):
# Debug: check if harbor's upload_file has a timeout parameter
# Alternative: host binary on GitHub and wget in install script
```

## Files

- `pilot-bench/pilot_agent/engine.py` — Python execution engine
- `internal/executor/backend_anthropic.go` — Go API backend
- `internal/executor/backend_factory.go` — Factory with anthropic-api type
- `pilot-bench/pilot_agent/agent.py` — Agent shim (currently wired to Go binary)
- `pilot-bench/pilot_agent/data/pilot.db` — 17 patterns learning DB
- `.agent/system/bench-engine.md` — Full engine docs
- `.agent/tasks/TASK-13-custom-bench-engine.md` — Task tracking
