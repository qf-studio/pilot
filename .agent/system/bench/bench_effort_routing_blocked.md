---
name: bench-effort-routing-blocked
description: Claude Code crashes on non-max effort levels — per-task effort routing blocked until upstream fix
type: project
---

Per-task effort routing via CLAUDE_CODE_EFFORT_LEVEL env var is blocked by a Claude Code bug.

**Bug**: Any effort level other than "max" triggers `TypeError: A.with is not a function` in Claude Code's cli.js. Likely `Array.prototype.with()` (ES2023) unsupported in container's Node.js version. Claude Code only hits this code path on non-max effort levels.

**Attempts that failed:**
1. `cmd.Env = append(os.Environ(), ...)` — replaces entire env, breaks Claude Code
2. `env CLAUDE_CODE_EFFORT_LEVEL=high claude ...` — env command works but non-max values crash Claude Code
3. Both approaches confirmed: 6/6 tasks crashed in v15 vs 0/21 in v14

**Current state**: Global `CLAUDE_CODE_EFFORT_LEVEL=max` in agent.py, all effort routing set to max. Config validation accepts "max" (committed).

**Why:** Anthropic needs to fix Array.with() usage in Claude Code for non-max effort paths, or container needs Node.js 20+.

**UPDATE (2026-03-20):** The `--effort` flag DOES work on CC 2.1.74. v5m used it successfully with `routed_effort=medium`. The env var override was masking this.

**CRITICAL FINDING:** v5m had **Haiku effort classifier enabled** which routed most tasks to `medium` effort instead of heuristic's `high`. Medium uses less memory → tasks pass. v23 without classifier got 57%; v5m with classifier got 68.5%.

**How to apply:**
1. Enable effort classifier: `effort_classifier.enabled: true, model: claude-haiku-4-5-20251001`
2. Use `--effort` flag via Pilot's routing config (low/med/high/high)
3. Pin CC to 2.1.74
4. NEVER set `CLAUDE_CODE_EFFORT_LEVEL` env var — it overrides routing
