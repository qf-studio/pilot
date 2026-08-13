# TASK-465: C16 — console GET /api/v1/dashboard aggregation for the v4 register

**Status**: ✅ MERGED + REVIEWED 2026-08-11 → console PR#116 (12:29Z). **Review verdict: APPROVED, tenancy clean, no blocking defects** (posted on the PR). Non-blocking: activity array spans window+1 days (UI must not assume len==days) · no upper time bound on aggregates (midnight clock-skew edge) · 3 nits. → **READY TO ARCHIVE.**
**Created**: 2026-08-11
**Assignee**: Pilot

## Summary

One aggregated fetch for the v4 dashboard register: `GET /api/v1/dashboard?window=30` → `{window, stats{shipped, needsYou}, activity[day × connection], projects[connection + top done cards]}`. New `internal/dashboardapi` package (boardapi conventions), first aggregation queries in the store (COUNT/GROUP BY day, real-Postgres tests).

## Key decision — honest re-scope (research 2026-08-11)

**The console has no PR-merge records** (cards model issues; `approval_mirror.pr_number` is deleted on decision). So:

- "Shipped" = cards reaching `status='done'` in window — NOT merged PRs; chart series likewise.
- `doneAt` ≈ `cards.updated_at` (no status-transition journal) — documented approximation.
- `deliveryRate` / `costPerShipped` deliberately ABSENT from the endpoint — daemon-ledger metrics; the SPA pulls them via the existing C13 passthrough (`status`/`metrics` tails). The endpoint makes zero daemon calls.
- "Project" = connection (UNIQUE(org_id, tracker) ⇒ max one GitHub project per org today — the founder's 3-project mock is not representable for one org without connection-model changes; endpoint groups by connection and does not pretend otherwise). Unlinked cards → synthetic `board` bucket.

Future-work flags (NOT filed): status-transition journal if `doneAt` precision matters; shipped-PR journal if the card wants true merged-PR counts; connection-model change for multi-repo orgs.

## Refs

- Design: `design/dashboard-v4-spec.md:34-36` (data contract) + `dashboard-v4.html` @ pilot-console-ui
- Vocabulary parity: pilot GH-4735 (30d window) · spec model: C14 PR#111
