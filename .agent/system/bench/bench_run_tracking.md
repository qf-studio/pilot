---
name: bench-run-tracking
description: Complete tracking of all bench runs with configs, results, and lessons learned
type: project
---

## Run History (2026-03-17 to 2026-03-19)

### Runs executed this session:
- **v14** (CC 2.1.77, Node 18): 8/21 = 38% — baseline before changes
- **v15** (multiple attempts): effort routing experiments, all broke due to CC TypeError
- **v16** (CC 2.1.74, Node 18): **53/89 = 59.6%** — first complete 89-task run, 0 TypeErrors
- **v17** (CC 2.1.79, Node 24): 1/5 = 20% — killed, CC 2.1.79 broken
- **v18** (CC 2.1.74, Node 24): 6/10 = 60% — killed, same as v16 (Node doesn't matter)
- **v19** (CC 2.1.72, Node 18): running — testing v5m's CC version

### Previous best: v5m (CC 2.1.72, Node 18) = 61/89 = 68.5%

### Additional runs (2026-03-19):
- **v17** (CC 2.1.79, Node 24): 1/5 = 20% — killed, CC 2.1.79 broken even on Node 24
- **v18** (CC 2.1.74, Node 24): 6/10 = 60% — killed, same as v16
- **v19** (CC 2.1.72, Node 24): 7/15 = 46.7% — killed, Node 24 hurts
- **v20** (CC 2.1.74, Node 18): running — confirming v16 baseline

## What We Tried That Didn't Work

1. **Effort routing via cmd.Env** — replaces entire env, breaks CC
2. **Effort routing via `env` command prefix** — CC TypeError on non-max values
3. **Prompt v10** (test-first, tighter timebox) — no improvement over v9
4. **Enabling learning DB** — no measurable impact
5. **Node 22/24 upgrade** — doesn't affect pass rate
6. **CC 2.1.78/2.1.79** — TypeError regression, worse than 2.1.72-2.1.77

## What Actually Moved the Needle

1. **Pinning CC to 2.1.74** — recovered from 41% (v15) to 59.6% (v16)
2. **Eliminating TypeErrors** — from 29% crash rate to 0%
3. **All-max effort routing** — prevents CC crash on non-max values
4. **Keeping Node 18** — Node 24 caused regressions (v19 = 46.7% vs v16 = 59.6%)

## Analysis Infrastructure

- Data analysis scripts: `pilot-bench/.analysis/collect_data.py`
- Plots output: `pilot-bench/.analysis/output/`
- Python venv: `pilot-bench/.analysis/venv/`
- GitHub issue: anthropics/claude-code#35934

## Phase 2 Research Complete (2026-03-19)

Flaky task analysis on 429 results. 5 failure categories:
- MEMORY_CONSTRAINED: 5 tasks (OOM, random Modal scheduling)
- ALGORITHM_VARIANCE: 5 tasks (wrong output, non-deterministic approach)
- CC_BUG: 2 tasks (fixed by CC pin)
- EARLY_FAILURE: 2 tasks (dependency/approach crash)
- SLOW_FAILURE: 1 task (over-retries)

Theoretical ceiling: 78.7% (70/89 ever-passed). 19 tasks never passed in any run.

## Root Cause Found (2026-03-20)

**`CLAUDE_CODE_EFFORT_LEVEL=max` env var was the 10% gap.**
- Added in v15 to avoid CC 2.1.78 TypeError, never removed
- Overrode Pilot's effort routing for ALL tasks → effort=max → OOM
- v5m (68.5%) never had this env var → tasks got medium/high effort → worked
- Also: v5m used 32K output tokens, we used 128K (agent over-thinks)

**v24 COMPLETE: 58/88 = 65.9%** — new record for current codebase:
- No CLAUDE_CODE_EFFORT_LEVEL env override
- Effort routing: low/med/high/high
- **Haiku effort classifier enabled** (routes ~60% of tasks to medium, not high)
- Smart retry prompt (unproven — changed alongside classifier, can't isolate impact)

Key insight: Haiku classifier routes tasks to medium effort → less memory pressure → fewer OOM.
v16 (no classifier, all-max): 60.2% → v24 (classifier): 65.9% = +5.7 points.

**v25 KILLED at 22 trials: 54.5%** — quality gates HURT in bench:
- Retries consume memory + time in 2GB containers
- Tasks that pass on first attempt timeout/OOM when retried
- 54 retries fired, none flipped a failed task
- v24 without quality gates: 65.9% > v25 with: 54.5%

**CONFIRMED: quality gates should stay DISABLED in LocalMode.**

Effort pass rates from v24: low=100%, medium=80%, high=61%.
Medium fallback untested in isolation (bundled with quality gates in v25).

## CRITICAL: We've been running -k 1, leaderboard uses -k 5

Standard submission: `harbor run ... -k 5` (5 attempts per task, best counts)
Official Claude Code (Opus 4.6) = 58.0% with -k 5
Our v24 = 65.9% with -k 1 — already beats official CC on single attempt
With -k 5, math says 65.9% → ~80%+ (flaky tasks become near-certain)
Top leaderboard: ForgeCode 81.8%, TongAgents 80.2%

## What's Left to Try
- **Run v24 config with -k 5** — highest priority, expected 75-80%+
- CC 2.1.72 (v5m's version)
- Pre-analysis pipeline (Haiku strategy hints)

**Why:** Tracking prevents repeating failed experiments and provides data for optimization.

**How to apply:** Check this before starting new bench experiments. NEVER set CLAUDE_CODE_EFFORT_LEVEL in agent.py env — let Pilot's routing config control it.
