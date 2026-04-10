# Context Marker: Terminal Bench 82.0% — Leaderboard Submitted

**Date**: 2026-03-28 | **Branch**: `feat/pilot-bench-real` | **Version**: v2.85.4

## Milestone

**Terminal Bench 2.0: 82.0%** — validated, PR #108 submitted, ready to merge.
- 365 pass, 74 fail, 6 errors (439/445 trials)
- Above ForgeCode #1 (81.8%)
- $0 cost (CC subscription)
- HF PR: https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard/discussions/108

## What's Built & Working

### Bench Infrastructure
- **CC backend** with optimized config (model routing, session resume, 15m heartbeat)
- **Stripped binary** (8MB compressed, eliminates upload timeouts)
- **17 learned patterns** in seed DB (injected into prompt)
- **Phased prompt** (recon/implement/recover)
- **Quality gates** re-enabled (pytest retry)
- **Pattern persistence** across containers (batch learning)
- **Effort classifier** (Haiku pre-classifies complexity)

### Go `anthropic-api` Backend (Ready, Needs Credits)
- `internal/executor/backend_anthropic.go` — SSE streaming, tool execution, progressive thinking
- `internal/executor/backend_factory.go` — `anthropic-api` type registered
- Proven 83.3% in 12 trials (Python engine equivalent)
- Blocked: OAuth tokens don't work on raw Anthropic API, needs `ANTHROPIC_API_KEY`

### Self-Improvement Pipeline
- ROAD-01: Anti-pattern injection — verified working
- ROAD-02: Patterns injected into self-review prompt — shipped
- Pattern DB persistence across containers — shipped
- Knowledge graph, meta-improvement, prompt archive — planned

## Roadmap (Next Steps)

### Immediate (This Week)
1. AIM Innovation Week presentation (2026-03-30/31)
2. Wait for leaderboard merge
3. Post results on X/LinkedIn/Threads

### Short-Term (API Credits)
4. Get Anthropic API credits (startup program or purchase)
5. Switch to `anthropic-api` backend (one config change)
6. Run v36 with direct API: target 85-90%
7. Submit updated score

### Medium-Term (AWS Infrastructure)
8. Deploy AWS sandbox infra (CloudFormation at `.agent/system/aws-sandbox-infra.md`)
9. Golden AMI with pre-baked deps (zero setup time)
10. n=10 concurrency (full run in ~8-12 hours)
11. Eliminate setup timeout errors → +1.5% score

### Self-Improvement Roadmap (Hyperagents-Inspired)
12. ROAD-10: Activate knowledge graph queries
13. Quality gate feedback → retry prompt improvement
14. Outcome → complexity re-classification
15. Bench DB → main DB sync
16. Prompt variant archive (evolutionary selection)
17. Meta-improvement: auto-prompt optimization
18. Eval suite + regression detection

### Commercial
19. Pattern DB sync as paid feature (S3/R2 backend)
20. Free=local SQLite, Pro=cloud sync, Enterprise=self-hosted

## Key Files

| File | Purpose |
|------|---------|
| `internal/executor/backend_anthropic.go` | Direct API backend (ready) |
| `internal/executor/prompt_builder.go` | Phased prompt + pattern injection |
| `internal/executor/runner.go` | Quality gates, self-review, pattern extraction |
| `pilot-bench/pilot_agent/agent.py` | Harbor agent shim + pattern persistence |
| `pilot-bench/pilot_agent/engine.py` | Python standalone engine (fallback) |
| `pilot-bench/pilot_agent/data/pilot.db` | 17 learned patterns |
| `.agent/system/bench-engine.md` | Full engine architecture docs |
| `.agent/system/aws-sandbox-infra.md` | AWS CloudFormation + tech requirements |

## Resume Commands

```bash
# Check leaderboard status
open https://www.tbench.ai/leaderboard/terminal-bench/2.0

# Check PR status
open https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard/discussions/108

# Next run with API credits (when available)
source .env && cd pilot-bench && harbor run \
  --job-name pilot-api-v36-k5 -o jobs -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e modal -n 2 -k 5 \
  --agent-timeout-multiplier 9.0 --agent-setup-timeout-multiplier 5.0 \
  --ae "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY"

# Next run with CC (free)
source .env && cd pilot-bench && harbor run \
  --job-name pilot-cc-v36-k5 -o jobs -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e modal -n 3 -k 5 \
  --agent-timeout-multiplier 9.0 --agent-setup-timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"
```

## Score Trajectory

| Run | Score | Notes |
|-----|-------|-------|
| v32 (CC, k=5) | 49.2% @58 | Before optimizations |
| engine-v9 (Python) | 83.3% @12 | Direct API, credits exhausted |
| v34 (CC, k=1 validation) | 100% 3/3 | Optimized config proven |
| **v35 (CC, k=5 final)** | **82.0% 445 trials** | **SUBMITTED — PR #108** |
| v36 (API, projected) | 85-90% | Needs credits |
| v37 (AWS, projected) | 84%+ | Needs AWS infra |
