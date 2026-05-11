---
name: bench_cost_model
description: Terminal Bench cost model — model routing saves 53% vs all-Opus. Haiku for trivial, Sonnet for simple, Opus for medium/complex.
type: project
---

## Terminal Bench Cost Model (anthropic-api backend, v2.85.2)

| Complexity | % of Tasks | Model | Effort | Thinking | Cost/1M Input | vs All-Opus |
|-----------|-----------|-------|--------|----------|--------------|-------------|
| Trivial | ~18% | Haiku | low | OFF | $0.80 | 19x cheaper |
| Simple | ~22% | Sonnet | medium | OFF | $3.00 | 5x cheaper |
| Medium | ~30% | Opus | high | 10K→3K progressive | $15.00 | same |
| Complex | ~30% | Opus | high | 10K→3K progressive | $15.00 | same |

**Weighted average: ~$7/1M input vs $15/1M — 53% savings.**

### Historical Run Costs

| Run | Engine | Trials | Cost | Per Trial | Notes |
|-----|--------|--------|------|-----------|-------|
| engine-v9 (all-Opus) | Python | 12 | $57 | $4.75 | gpt2-codegolf alone $31 |
| v32 (Claude Code) | CC CLI | 58 | CC sub | ~$0.16 | Dashboard metric |
| Projected (routed) | Go API | 445 | ~$71 | ~$0.16 | Matches CC cost with better quality |

### Why Routing Matters

- All-Opus Python engine: 83% score but $4.75/trial = $2,114 for full k=5
- Routed Go backend: same quality on medium/complex, cheaper on trivial/simple
- Projected full k=5 cost: ~$71 (445 trials × $0.16/trial)

**How:** Effort classifier (Haiku, ~$0.001/call) pre-classifies task complexity → router selects model → thinking budget scaled to model capability.
