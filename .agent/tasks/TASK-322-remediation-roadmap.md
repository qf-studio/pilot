# TASK-322 Remediation Roadmap

Companion to `TASK-322-security-audit-findings.md` (the findings ledger). This file is the
execution plan: 47 confirmed findings → Pilot-sized task files, grouped by code-area track,
sequenced into severity-ordered waves.

**Source plan:** `~/.claude/plans/well-it-feels-like-ancient-island.md` (approved 2026-05-30).

## Delivery model
- **Pilot waves (1–4):** one `/nav-task` file per item → `gh issue create --label pilot --body-file <task>.md`.
- **Wave 0 (manual):** the retry-path critical + `*PRState` race are human-implemented via `/nav-loop`
  task mode — Pilot must not edit its own execution/concurrency core (TASK-320 B2 precedent).
- **Folds:** parallel `hasMergedWork` guard → TASK-321 track; the 3 board findings → TASK-319 track.

## Wave 0 — MANUAL (gates the rest)
| ID | Finding | File | Status |
|---|---|---|---|
| TASK-323 | Retry runs in `task.ProjectPath` not worktree (critical) | `runner.go:2197,2561` | ✅ **shipped — PR #3293** |
| TASK-324 | `*PRState` cross-goroutine race (critical + 1 high) | `controller.go` | ✅ **shipped — PR #3301** (workflow-built, 4-reviewer verified, `-race` green) |

## Wave 1 — Criticals + top security (Pilot)
| ID | Finding | File | Note |
|---|---|---|---|
| TASK-325 | Scope/size merge-gate dead code (critical) | `controller.go`,`scope_guard.go` | **unblocked** — file as `pilot` issue once PR #3301 merges |
| TASK-326 | Webhook fail-closed + jira/asana dead verifiers (high) | `pilot.go`,`*/webhook.go` | filed |
| TASK-327 | Slack interaction webhook 0% test coverage (high) | `slack/webhook.go` | filed |
| TASK-328 | `PRAGMA foreign_keys=ON` never set (high, XS) | `store.go` | filed |
| TASK-321 PR-4 | Parallel `hasMergedWork` guard (high) | `poller.go` | filed (T321 fold) |

## Wave 2 — Highs (Pilot)
**Filed (parallel-safe, distinct files):** A2 #3302 · C5 #3303 · E2 #3304 · E3 #3305 · F2 #3306 · D2 #3307.
**SHIPPED:** F2 raw-body body-HMAC (TASK-333, #3306) → merged **manual** as #3325 (gateway buffers
raw jira/asana body before decode; pilot.go verifies HMAC over exact bytes; dead `marshalWebhookPayload`
removed). **This was the last open Wave 0–2 remediation item — Waves 0–2 complete.**
**Held batch — FILED + ✅ ALL SHIPPED 2026-05-31** (Wave 2 COMPLETE):
- B3 CI-commit-status fallback (`ci_monitor.go`) → #3326/TASK-335 → ✅ Pilot PR #3332
- B5 merge-retry hard cap (`controller.go`) → #3327/TASK-336 → ✅ Pilot PR #3333 (`MaxMergeAttempts` default 5)
- C3 ExecuteGraphQL retry (`client.go`) → #3330/TASK-339 → ✅ Pilot PR #3336
- C4 board CreatedAt oldest-first (`project_source.go`) → #3331/TASK-340 → ✅ Pilot PR #3337
- C2 board-source-in-parallel-mode (`poller.go`) → #3329/TASK-338 → ✅ **manual** PR #3339 (Pilot no-op'd)
- E1 alert-loop decouple **+** E5 SuppressDuplicates (`engine.go`) → #3328/TASK-337 → ✅ **manual** PR #3341 (hang-adjacent; Pilot no-op'd)

**Notes from execution:** B3/B5/C3/C4 all hit the **phantom `pilot-blocked`** bug — Pilot produced a green
PR then a redundant re-dispatch no-op'd ("no new commit produced") and false-flagged the issue; cleared the
label + merged each. C2/E1 produced **no branch at all** (genuine no-op) → taken manually. Follow-up still
open: executor should treat "no new commit + an OPEN pilot PR exists" as benign-awaiting-merge, not blocked
(the TASK-321 guard only covers already-*merged* PRs) → **✅ shipped manually, PR #3343 (TASK-341)**: Layer 1
(`handlers.go`) classifies the no-op-with-open-PR as awaiting-merge (no `pilot-blocked`); Layer 2 (`poller.go`)
skips the re-dispatch in the processed-retry path via `FindOpenPRByBranch` + `ReasonHasOpenPR`. Filing all 6
also surfaced the **spec-guard header requirement** (`## Context|Approach|Acceptance|…`) — see
`learnings/learning_pilot_issue_spec_guard_headers`.

## Wave 3 — Mediums (Pilot)
Re-verified against `main` 2026-06-01: B5/E5/C4 + SMTP-twin already shipped in Wave 2 (NOT re-filed).
12 confirmed-live mediums decomposed (TASK-342 kickoff) and **FILED 2026-06-01** with `pilot` label,
all spec-guard'd (passed — `pilot` only, no `-spec-incomplete`):

| Task | Finding | File(s) | Issue |
|---|---|---|---|
| TASK-343 | D3 task_id scope · D5 log-retention · D6 feedback-tx · D7 rows.Err — **batched** (same-file) | `memory/store.go`,`metrics.go`,`metering.go` | #3344 |
| TASK-344 | A3 watchdog-interval from stallTimeout | `executor/watchdog.go` | #3345 |
| TASK-345 | B4 premature-CIFailure debounce (`hasFailure && !hasPending`) | `autopilot/ci_monitor.go` | #3346 |
| TASK-346 | C6 ListIssues pagination (`per_page=100`) | `github/client.go` | #3347 |
| TASK-347 | C7 allowlist fail-closed on nil | `github/issue_create.go` | #3348 |
| TASK-348 | D4 KG atomic write + batch + `.bak` | `memory/graph.go` | #3349 |
| TASK-349 | E4 Telegram MarkdownV2 parse_mode | `alerts/channels.go` | #3350 |
| TASK-350 | E6 rotation cleanup serialize | `logging/rotation.go` | #3351 |
| TASK-351 | E8 engine_test deterministic sync (test-only) | `alerts/engine_test.go` | #3352 |

Watch-and-merge per the standard loop; clear phantom `pilot-blocked`+open-PR (TASK-341 guard shipped).

## Wave 4 — Lows + tests (Pilot)
A4 hook-tmp + subagent-argv · B6 recordedMerges/discoveryStart eviction · E7 retryTracker-TTL ·
G1 cleanup-return · G2 %w-wrap · G3 discord-heartbeat · G4 transcription-tests.

## Resolved / no-action
- `IsTaskShipped` error-guard — already fixed in `main` (`task_shipped.go:21`); only D2 test-row remains.
- 3 positive "no-action" findings + 5 refuted findings — nothing to file.

## Gates
Wave 0 merges & soaks first (TASK-325/B5 share `controller.go` with TASK-324). 24–48h soak between
Pilot waves. Archive each task to `.agent/tasks/archive/` on merge; tick the finding off in TASK-322;
re-audit ~2 weeks after Wave 3.
