# TASK-342: TASK-322 Wave 3 kickoff — decompose the 12 verified-live mediums

**Status:** ✅ DISPATCHED 2026-06-01 — 9 `pilot` issues filed (#3344–#3352), all spec-guard'd (passed). · **Created:** 2026-06-01 · **Source of truth:** `.agent/tasks/TASK-322-remediation-roadmap.md` (§ Wave 3) + findings ledger `.agent/tasks/TASK-322-security-audit-findings.md`

Waves 0–2 are complete (all 3 crit + 14 high merged, v2.166.1). TASK-341 (open-PR phantom-block guard) shipped manually via PR #3343. This task captures the **verified** Wave-3 starting state so decomposition can begin cold.

## Re-verified against `main` (2026-05-31)

**Already fixed in Wave 2 — DO NOT re-file (re-filing triggers phantom-block churn):**
- B5 merge-retry-cap → `controller.go:1315` `MaxMergeAttempts` ✅
- E5 SuppressDuplicates → `engine.go:649` ✅
- C4 board CreatedAt → `project_source.go:24` ✅
- SMTP STARTTLS/ctx (E2-twin) → `smtp.go` `DialContext`+`tlsConfig` ✅
- parallel merged-candidate test → `poller_task321_pr4_test.go` ✅

**12 confirmed-live Wave-3 mediums:**

| Code | File | Note |
|---|---|---|
| A3 watchdog 30s hardcoded | `executor/watchdog.go:24` | verified bug present |
| B4 premature CIFailure | `autopilot/ci_monitor.go` | hasFailure precedence over hasPending |
| C6 ListIssues no `per_page` | `github/client.go:393` | verified — 30-item cap |
| C7 allowlist fail-open | `github/issue_create.go` | nil allow → ALLOW (vs executor fail-closed) |
| D3 SelfHeal task_id cross-repo | `memory/store.go` | **store.go cluster** |
| D4 KG non-atomic write | `memory/graph.go` | |
| D5 execution_logs unbounded | `memory/store.go` | **store.go cluster** |
| D6 RecordPatternFeedback non-atomic | `memory/store.go` | **store.go cluster** |
| D7 `rows.Err()` sweep | `memory/store.go`, `metrics.go`(0), `metering.go`(0) | **store.go cluster** |
| E4 Telegram parse_mode | `alerts/channels.go:150` | verified `"Markdown"` |
| E6 rotation cleanup race | `logging/rotation.go` | |
| E8 engine_test 48 sleeps flaky | `alerts/engine_test.go` | test-only |

## Dispatch plan (gates)

1. **`store.go` cluster (D3/D5/D6/D7) — serialize or batch into ONE issue.** Four parallel `pilot` issues on the same file → conflict-merge → phantom-block churn.
2. **Batch A (parallel, distinct files):** A3, B4, C6, C7, D4, E4, E6, E8.
3. **Spec-guard headers are mandatory** on every `pilot` issue body: lead with `## Context / ## Approach / ## Acceptance / ## Refs` — `## Problem`/`## Fix` auto-blocks within 2 polls. See `learnings/learning_pilot_issue_spec_guard_headers`.
4. **Poller-touching items (none in Wave 3 directly, but watch C6/`client.go`):** any change adding per-candidate API calls in `poller.go` starves `stress/TestMemory_ProcessedMapGrowth` (1000 fresh issues, unbounded busy-wait, 30s ctx) → 10m CI timeout. This bit TASK-341's first cut; fix is architectural (scope to re-dispatch path), not a test tweak.
5. 24–48h soak between Pilot waves (roadmap gate). Archive each task on merge; tick the finding in TASK-322.

## Doc-collision flag
Stray worktree `fix/task319-wire-boardsync` also edits the roadmap + findings ledger — Wave-3 task-doc edits there will conflict on merge (that branch rebases, not us).

## Dispatched (2026-06-01)
1 batched `store.go` issue (D3/D5/D6/D7) + 8 individual issues (Batch A), all spec-guard'd, filed with `pilot` label:

| Task | Finding | Issue |
|---|---|---|
| TASK-343 | store.go cluster D3/D5/D6/D7 | #3344 |
| TASK-344 | A3 watchdog interval | #3345 |
| TASK-345 | B4 premature CIFailure | #3346 |
| TASK-346 | C6 ListIssues pagination | #3347 |
| TASK-347 | C7 allowlist fail-closed | #3348 |
| TASK-348 | D4 KG atomic write | #3349 |
| TASK-349 | E4 Telegram parse_mode | #3350 |
| TASK-350 | E6 rotation cleanup race | #3351 |
| TASK-351 | E8 engine_test flaky | #3352 |

## Next action
Watch-and-merge loop (90s `gh pr list`/`gh issue list`): merge green PRs, clear any phantom
`pilot-blocked`+open-PR (TASK-341 guard is live so this should be rare). Archive each TASK-3XX on merge;
tick the finding in TASK-322 findings ledger. 24–48h soak before Wave 4 (lows).
