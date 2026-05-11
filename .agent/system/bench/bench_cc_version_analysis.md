---
name: bench-cc-version-analysis
description: Claude Code version is the single biggest bench performance variable — 2.1.72 is best, 2.1.78+ crashes
type: project
---

Terminal-bench performance is dominated by Claude Code version, not prompt or Pilot code changes.

## Version Performance Matrix (2026-03-19)

| Run | CC Version | Node | Pass Rate | Tasks | TypeErrors |
|-----|-----------|------|-----------|-------|------------|
| v5m | **2.1.72** | 18.x | **68.5%** | 61/89 | 0 |
| v10b | 2.1.74 | 18.x | 63.6% | 14/22* | 0 |
| v12 | 2.1.75 | 18.x | 55.6% | 10/18* | 0 |
| v16 | 2.1.74 | 18.x | 59.6% | 53/89 | 0 |
| v15 | 2.1.78 | 18.x | 41.2% | 7/17* | 5 |
| v17 | 2.1.79 | 24.x | 20.0% | 1/5* | 2 |

*killed early

## Key Findings

1. **CC 2.1.72 is the best version** — 68.5% (v5m), 14 tasks regressed in 2.1.74
2. **CC 2.1.78+ has TypeError crash** — `A.with is not a function` in cli.js:6892, NOT a Node.js compat issue (reproduces on Node 18, 22, 24). Filed: anthropics/claude-code#35934
3. **Node version doesn't matter** — v18 (Node 24 + CC 2.1.74) tracked identically to v16 (Node 18 + CC 2.1.74)
4. **Prompt v9 vs v10 made no difference** — same pass rates
5. **Effort routing blocked** — CC crashes on non-max effort levels, all routing set to max

## 14 Tasks That Regressed 2.1.72 → 2.1.74

adaptive-rejection-sampler, caffe-cifar-10, count-dataset-tokens, feal-differential-cryptanalysis, gpt2-codegolf, headless-terminal, large-scale-text-editing, model-extraction-relu-logits, sparql-university, sqlite-with-gcov, torch-pipeline-parallelism, torch-tensor-parallelism, tune-mjcf, write-compressor

## Install Script Config

- Pin CC version in `pilot-bench/pilot_agent/templates/install-pilot-agent.sh.j2` line 55
- Node upgrade via `n` package manager (nodesource doesn't work in Modal containers)
- Symlink override needed: `ln -sf /usr/local/bin/node /usr/bin/node` (n installs to /usr/local, but /usr/bin shadows it)

## Node Version Impact (2026-03-19)

| Run | CC | Node | Pass Rate | Note |
|-----|-----|------|-----------|------|
| v16 | 2.1.74 | 18 | **59.6%** | Best complete run |
| v18 | 2.1.74 | 24 | 60% (10 trials) | Same as v16, killed |
| v19 | 2.1.72 | 24 | 46.7% (15 trials) | **Worse** — Node 24 hurts |

Node 24 does NOT help and may hurt. Keep Node 18 (container default).

**Why:** CC version and Node version both affect task completion. Don't upgrade either without full 89-task validation.

## Best Config (confirmed 2026-03-19)

**CC 2.1.74 + Node 18** = v16's config = 59.6% on full 89 tasks.
v20 running to confirm reproducibility.

**How to apply:** Pin CC to 2.1.74 + Node 18. Don't upgrade Node (24 hurts). Don't downgrade CC (2.1.72 + Node 24 = 46.7%). The v5m 68.5% advantage was likely older Pilot binary/prompt, not CC version.
