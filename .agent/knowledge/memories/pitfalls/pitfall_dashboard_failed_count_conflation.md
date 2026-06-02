---
name: pitfall_dashboard_failed_count_conflation
description: Dashboard QUEUE "failed" count inflated because the dispatcher collapsed every non-success outcome into status='failed'; plus a TUI truncateVisual ANSI bug that blanked the styled line
type: pitfall
---
TASK-358 (v2.166.10–11, 2026-06-02). The QUEUE card showed `✗ 784 failed` when only ~234 were genuine failures ("many showed failed but were done correctly").

**Bug 1 — outcome conflation (write path).** `executor/dispatcher.go` recorded `status='failed'` for *every* `result.Success==false` outcome — declined, no-op ("no new commit produced" / "no_changes" / "made no code changes"), stalled, budget, OOM/SIGKILL, push/PR/worktree/branch failures, rate-limit, stale-queued, context-canceled. `memory.GetLifetimeTaskCounts` then `SUM(CASE WHEN status='failed')`, so the card counted all of them. `result.Declined` was never even honored by the dispatcher.
Fix: `executor.TerminalStatus(result)` — precedence Success→Declined→explicit `result.Outcome` tag→ordered error-signature table (no_op → rate_limited → skipped → stalled → infra)→`failed`. Distinct statuses persisted; `Store.reclassifyLegacyOutcomes()` (idempotent, runs in `migrate()`) backfills historical rows by the same signatures; heal-on-merge scope (`UpdateExecutionStatusByTaskID`, `SelfHealExecutionAfterMerge`) widened to the non-failure statuses so a reclassified row still promotes to `completed` if its PR merges. Live: 784→234 (infra 305 · no-op 120 · skipped 81 · rate-limited 34 · stalled 10).

**Bug 2 — TUI truncate blanks styled lines.** `dashboard/truncateVisual` iterated the *styled* string rune-by-rune counting ANSI escape bytes (`\x1b[38;5;…m`) as visible width → broke mid-escape → rendered the whole line blank. Latent until the TASK-358 breakdown suffix (`✗ N failed (120 no-op · 305 infra · …)`, ~75 chars) became the first detail line wide enough to need truncation in the ~17-char mini-card. Symptom (v2.166.10): succeeded line present, failed row empty, border corrupted. Fix: only append the suffix when `lipgloss.Width(line) <= ciw`; make `truncateVisual` ANSI-aware (escape sequences copied through at zero width).

**Why:** "not succeeded" ≠ "failed" — no-op (already-merged / no edits), infra/plumbing, rate-limit, stale-queued, and stalled are distinct from a genuine task failure and must not inflate the headline. And any width-bounded TUI helper that truncates *pre-styled* strings must be ANSI-aware.

**How to apply:** Classify execution outcomes at the single write site, not by reading `status='failed'` everywhere. When adding a dashboard detail suffix, guard on card width and never feed styled multi-segment strings to a non-ANSI-aware truncator. Cross-ref [[bug_goreleaser_cancellation]], [[learn_restart_vs_rebuild_stale_binary]]; related [[TASK-320]] [[TASK-321]] [[TASK-355]].
