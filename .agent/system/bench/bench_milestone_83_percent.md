---
name: bench_milestone_83_percent
description: Terminal Bench 2.0 milestone — 83-85% first pass, tracking 84.7% at 98 trials. Above ForgeCode #1 (81.8%). Steps to 91%+ identified.
type: project
---

## Terminal Bench 2.0 Milestone (2026-03-26)

**First pass: 83.1% (74/89)** — above ForgeCode #1 (81.8%)
**Final k=5: 82.0% (365/445)** — validated, submitted to leaderboard
**PR #108**: https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard/discussions/108

### What Got Us Here (v32 49% → v35 84.7%)
1. Model routing: Haiku/Sonnet/Opus by complexity (53% cost savings)
2. Session resume: 40% token savings on self-review
3. Stripped binary: 8MB compressed (was 28MB) — faster setup = more execution time
4. Heartbeat timeout: 15m (was 5m) — fewer false kills
5. Compressed upload: gzip binary before Modal upload
6. n=3 concurrency: safe with CC OAuth (no rate limit)
7. Quality gates re-enabled: pytest retry between iterations
8. 17 learned patterns injected into prompt
9. Phased prompt: recon/implement/recover structure

### Path to 91%+
- `anthropic-api` Go backend ready (needs API credits)
- Progressive thinking control: 10K planning, 3K execution
- No CC install overhead: 1 min setup vs 8 min
- Can crack 5-8 of the 14 always-fail tasks with deep thinking
- Theoretical max with all 14 cracked: ~100%
- Realistic target with credits: 91-95%

### Key: Setup Speed → Score
Every minute saved on container setup = more execution time for the task.
Python engine (15KB, 30s setup) → 83.3%
CC + stripped binary (8MB, 5 min setup) → 84.7%
API backend + no CC (8MB, 1 min setup) → projected 90%+
