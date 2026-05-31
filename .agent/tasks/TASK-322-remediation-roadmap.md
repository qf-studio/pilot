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
**Held batch — FILED 2026-05-31** (blockers all merged; grouped by file to avoid parallel-Pilot collisions):
- B3 CI-commit-status fallback (`ci_monitor.go`) → **#3326 / TASK-335**
- B5 merge-retry hard cap (`controller.go`) → **#3327 / TASK-336**
- E1 alert event-loop decouple **+** E5 SuppressDuplicates (both `engine.go`, **combined**) → **#3328 / TASK-337**
- C2 board-source-in-parallel-mode (`poller.go`) → **#3329 / TASK-338**
- C3 ExecuteGraphQL retry (`client.go`) → **#3330 / TASK-339**
- C4 board CreatedAt oldest-first (`project_source.go`) → **#3331 / TASK-340**

⚠️ E1/#3328 is timeout/hang-adjacent (deliberately-blocking channel under test) — if Pilot self-stalls
(see process learning), take it manually like E2/SMTP. All distinct files; rebase on main before push.

## Wave 3 — Mediums (Pilot)
A3 watchdog-interval · B4 premature-CIFailure · B5 merge-retry-cap · C6 ListIssues-pagination ·
C7 allowlist-fail-closed · D3 project_path-scope · D4 KG-atomic-write · D5 log-retention ·
D6 feedback-tx · D7 rows.Err-sweep · E4 Telegram-markdown · E5 SuppressDuplicates · E6 rotation-race ·
E8 alert-test-sync.

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
