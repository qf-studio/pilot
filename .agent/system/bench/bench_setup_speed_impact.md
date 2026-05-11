---
name: bench_setup_speed_impact
description: Terminal Bench setup time directly impacts score — faster container setup = more execution time = higher pass rate. Correlation found across all runs.
type: project
---

## Setup Speed → Score Correlation

Discovered 2026-03-25 across 6+ bench runs. Container setup time eats into the 90-min task timeout. Tasks that barely fit in the window get killed with slow setup but pass with fast setup.

| Engine | Upload Size | Setup Time | Score | Notes |
|--------|------------|------------|-------|-------|
| Python engine (15KB) | 15KB | ~30s | **83.3%** | No binary, no CC, no Node.js |
| Go + CC (28MB uncompressed) | 28MB + CC | ~10-15 min | 49% | v32 |
| Go + CC (15MB compressed) | 15MB + CC | ~5-8 min | 66-71% | v34-k5 running |
| Go + CC (8MB stripped+compressed) | 8MB + CC | ~3-5 min (est) | untested | `-ldflags "-s -w"` |

**Why:** Every minute of setup = 1 minute less for task execution. Complex tasks (torch install, compilation) need 30-60 min. At 10 min setup, only 20-50 min remains.

**How to apply:**
- Always use stripped binary: `go build -ldflags "-s -w"` (28MB → 20MB, compressed 15MB → 8MB)
- Always compress: `gzip -k pilot-linux-amd64`
- CC install adds ~2-3 min (Node.js + npm + claude@2.1.74) — unavoidable with CC backend
- When switching to `anthropic-api` backend: drop CC install entirely, save 2-3 min

**Binary size breakdown:**
```
28MB  — original (debug symbols + DWARF)
20MB  — stripped (-s -w removes symbols)
15MB  — original gzipped
 8MB  — stripped + gzipped (best)
```

**Implication for `anthropic-api` backend (when credits available):**
- No CC install needed (save 2-3 min)
- 8MB stripped binary upload (save 5+ min vs uncompressed)
- Total setup: ~1-2 min vs ~8-15 min with CC
- Expected score boost: +5-10% from recovered execution time
