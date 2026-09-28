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

## 2026-09-28 — default model → Claude Sonnet 5.5 (box config, live)

Sonnet 5.5 released 2026-09-28 (`claude-sonnet-5-5`, $2/$10 per MTok, same as Sonnet 5; adaptive thinking, default effort `high`; breaking vs Sonnet 5: forced `tool_choice` any/tool → 400, `thinking.disabled` → use `between_tools`, thinking blocks model-bound — none touch Pilot's claude-code backend path). Box (`i-0e0c1ca34e7b561f9`) `~/.pilot/config.yaml` edited via SSM and daemon restarted 20:41Z (queue was idle, 0 in-flight). Claude Code CLI on the box 2.1.104 passes the model string through.

| Setting | Before | After |
|---|---|---|
| `orchestrator.model` | `claude-sonnet-5` | `claude-sonnet-5-5` |
| `executor.model_routing.simple/medium/complex` | `claude-sonnet-5` | `claude-sonnet-5-5` |
| `executor.default_model` | `claude-sonnet-5` | `claude-sonnet-5-5` |
| `bot.answer_model` | `claude-sonnet-5` | `claude-sonnet-5-5` |
| `executor.opencode.model` (unused backend) | `anthropic/claude-sonnet-5` | `anthropic/claude-sonnet-5-5` |

Unchanged: `model_routing.trivial`, `bot.model`, classifiers on `claude-haiku-4-5-20251001`; `effort_routing` (low/medium/high/high). Boot log confirms `LLM effort classifier initialized model=claude-sonnet-5-5` (it followed the executor default before, too). First executor run on the new model still to be observed: look for `Using routed model model=claude-sonnet-5-5` in `daemon.log`.

**Rollback:** on the box `cp /home/ec2-user/.pilot/config.yaml.bak-sonnet55-202609282040 /home/ec2-user/.pilot/config.yaml` then restart (safe-daemon-restart SOP). The laptop `~/.pilot/config.yaml` copy still says `claude-sonnet-5` — update it before any future "ship config verbatim" step.
