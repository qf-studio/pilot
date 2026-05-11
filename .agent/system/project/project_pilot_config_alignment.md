---
name: Pilot config aligned to "Opus plans, Sonnet executes"
description: ~/.pilot/config.yaml state after 2026-04-30 alignment with commit 5a965bc5 — model routing, planning, hooks, quality gates
type: project
originSessionId: c55a7ac2-b7ba-4d85-9f36-ca6161c6e257
---
On 2026-04-30 aligned `~/.pilot/config.yaml` with commit `5a965bc5 feat(executor): "Opus plans, Sonnet executes"` (cuts ~70% token spend).

**Why:** config predated the commit and explicitly overrode the new cost-saving defaults back to Opus-everywhere + tests-on-stop. Effective spend was ~4× what the commit intended.

**How to apply:** when debugging Pilot model spend or quality regressions, this is the current effective config. Backup at `~/.pilot/config.yaml.bak-20260430-100726`.

## Edits applied

| Setting | Before | After |
|---|---|---|
| `executor.default_model` | `claude-opus-4-7` | `claude-sonnet-4-6` |
| `executor.model_routing.complex` | `claude-opus-4-7` | `claude-sonnet-4-6` |
| `executor.planning.model` | (missing) | `claude-opus-4-7` (new block) |
| `executor.claude_code.allowed_tools` | (missing) | `[Read,Write,Edit,Bash,Grep,Glob,Task]` (new) |
| `executor.hooks.run_tests_on_stop` | `true` | `false` |
| `quality.enabled` | `false` | `true` |

## Effective model attribution

| Stage | Model |
|---|---|
| Planning (epic decompose) | Opus 4.7 |
| Execution (trivial) | Haiku 4.5 |
| Execution (simple/medium/complex) | Sonnet 4.6 |
| Self-review | Same as execution (Sonnet) — `runner.go:3320` reuses modelRouter |
| Intent judge | Haiku 4.5 |
| Effort classifier | Haiku 4.5 |
| Quality gates retry-prompt | Same as execution (Sonnet) — no escalation to Opus on retry |
| Orchestrator | Sonnet 4.6 |

## Quality gates flow (now active for Pilot repo)

After Claude subprocess exits, before PR creation:
1. Sequential `make build` → `make test` → `make lint` (lint not required)
2. Inner loop per gate: command retried up to `MaxRetries+1` times with `RetryDelay` between (no model)
3. Outer loop: on gate failure, Pilot sends Sonnet a retry prompt with stderr + `failure_hint`. Up to `maxAutoRetries=2` outer loops.
4. Worst case per task: 1 initial + 2 quality retries = 3 Sonnet turns + 3× gate runs

## Watch for regressions
- `pilot-retry-1/2/exhausted` label velocity — Sonnet may need more retries on genuinely complex multi-file work
- CI fail rate on Pilot PRs (in-session test net thinner now: `run_tests_on_stop=false`, but quality gates compensate before PR push)
- Token spend per model: `sqlite3 ~/.pilot/data/pilot.db "SELECT model, COUNT(*), AVG(tokens_input+tokens_output) FROM executions WHERE created_at > date('now','-1 day') GROUP BY model;"`

## Rollback
Single line if quality drops: `model_routing.complex: claude-opus-4-7`. Or `cp ~/.pilot/config.yaml.bak-20260430-100726 ~/.pilot/config.yaml` for full revert.

**Restart required:** `pilot start` reads config once at boot.
