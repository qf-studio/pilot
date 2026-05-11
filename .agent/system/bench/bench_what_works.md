---
name: bench-what-works
description: Definitive list of what helps vs hurts bench performance, based on 600+ task results across 20 runs
type: project
---

## HELPS (confirmed with data)

1. **Haiku effort classifier** — routes tasks to medium effort, saves memory. +5.7 points (v16→v24)
2. **No CLAUDE_CODE_EFFORT_LEVEL env var** — overrides routing. Removing it = v5m behavior
3. **CC 2.1.74 pinned** — 2.1.78+ has TypeError crash. 2.1.72 may be slightly better but untested cleanly
4. **Effort routing low/med/high/high** — static fallback when classifier fails
5. **Node 18** — container default, stable

## HURTS (confirmed with data)

1. **Quality gates in LocalMode** — retries consume memory/time, net negative. v25: 54.5% vs v24: 65.9%
2. **CLAUDE_CODE_EFFORT_LEVEL=max env var** — overrides all routing, wastes memory
3. **effort=max for all tasks** — OOMs in 2GB containers. Medium is better (80% vs 61% pass rate)
4. **Node 24** — v19: 46.7% vs v16: 59.6%
5. **CC 2.1.78+** — TypeError crash bug

## NO EFFECT (confirmed with data)

1. **Prompt v9 vs v10** — identical results
2. **CLAUDE_CODE_MAX_OUTPUT_TOKENS** — CC ignores it, always uses 32K
3. **Smart retry prompt** — quality gates never fired, code was dead
4. **Learning DB** — enabled in later runs, no measurable impact
5. **4GB memory** — v22 tracked same as 2GB runs at 16 trials

## TESTED SINCE (confirmed)

1. **Medium fallback for all** — v26: 54% at 13 trials. HURTS. Tasks need differentiated effort.
2. **complex: max** — v27: 64% (41/64, 21 tasks hit Modal limit). Within noise of v24 (66%). Neutral.
3. **Quality gates in LocalMode** — v25: 55% at 22 trials. HURTS. Retries eat memory/time.

## TESTED SINCE (k=5 submissions)

1. **Classifier + strategy hints + k5** (v2): 63.6% at 11 — classifier OOMs light tasks
2. **No classifier + k5** (v3): 52.6% at 19 — WORSE, high effort OOMs deterministically
3. **Classifier + no hints + k5** (v4): running — 62.5% at 8
4. **Strategy hints**: add memory to classifier subprocess, removed
5. **Evolved DB** (53 patterns from v24+v5m): live, injected via PatternContext

## COMMITTED BUT UNTESTED

1. **Skip classifier for <500 char tasks** — defaults to medium, no subprocess OOM
2. **Classifier timeout 10s** (was 30s) — less memory hold time

## UNTESTED

1. **CC 2.1.72 with current code + Node 18** — v19 used Node 24 (confounded)
2. **Direct API call classifier** — HTTP to Haiku API instead of CC subprocess (~1MB vs 500MB). Needs ANTHROPIC_API_KEY.

## CRITICAL: k=5 DOES NOT HELP — Our Failures Are Deterministic

Leaderboard uses k=5 but our failures are deterministic OOM (same effort → same OOM × 5).
k=5 only helps random failures. We tested 8 k=5 submissions — ALL scored worse than v24 k=1.
- v24 k=1: **65.9%** (best)
- Best k=5: 63% (all-medium, no classifier)
- Worst k=5: 25% (medium fallback)
- Official CC k=5: 58%

The classifier gives same answer 5 times → same OOM 5 times. k=5 is a waste of 5× compute.

## Best Config (v24)

CC 2.1.74, Node 18, prompt v9, Haiku effort classifier, effort low/med/high/high, no CLAUDE_CODE_EFFORT_LEVEL env var, no quality gates in LocalMode. **Run with -k 5 for submission.**

**Why:** Prevents repeating failed experiments. Check this before ANY bench change.
**How to apply:** Start from v24 config with -k 5. Only change ONE variable per run.
