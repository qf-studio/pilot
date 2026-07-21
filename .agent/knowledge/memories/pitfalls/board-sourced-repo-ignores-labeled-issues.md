---
name: board-sourced-repo-ignores-labeled-issues
description: With project_board.source_enabled true (pointer/board #2), board sourcing REPLACES label discovery — a pilot-labeled issue not on the board (or not in source_status Todo) is silently invisible; poller silence is indistinguishable from poller death
type: pitfall
---

# Board-sourced repos silently ignore pilot-labeled issues

**What happened (2026-07-21):** pointer#136 (TASK-38) filed with the `pilot`
label sat undispatched for hours. Label-cycling did nothing. A watch session
misdiagnosed a dead pointer-poller goroutine (silence since 09:10Z + a
`context canceled` inside "list project board items" at shutdown) and
restarted the daemon — no effect. Real cause: pointer has
`project_board.source_enabled: true` → the board IS the queue
(`internal/adapters/github/types.go:42` — "Pull work FROM the board instead
of by label"). #136 was never added to board #2. Added card + status Todo →
dispatched within one 30s tick (10:13:49Z).

## Mechanism

- `source_enabled: true` + `source_status: Todo` (config `projects[].github.project_board`):
  poller lists board items in Todo via projectV2 GraphQL; the dispatch label
  is NOT a discovery path for that repo.
- A labeled-but-boardless issue produces **zero log lines** — healthy-idle
  and dead poller look identical in the daemon log.
- Companion trap: board status-sync (card → In Progress/Done) fails
  WARN-only with INSUFFICIENT_SCOPES (daemon token lacks read:project), so
  shipped issues keep "In Progress" cards — pointer#129 (TASK-37) read as a
  second stuck task when it had shipped at 08:38Z. Sourcing (read) worked
  while sync (update) failed — token/scope paths differ; unresolved.

## How to avoid

1. Repo dispatch checklist for board-sourced repos: issue must be ON the
   board AND in `source_status` column. Label alone does nothing.
2. Debugging "pilot won't pick up issue X": check `projectItems` on the
   issue (`gh issue view N --json projectItems`) BEFORE theorizing about
   poller death; check config for `source_enabled` per repo.
3. Don't trust board card status as execution state while the daemon token
   lacks project scopes — trust the ledger (`executions`) and issue state.
4. Durable fix filed: [#4488](https://github.com/qf-studio/pilot/issues/4488)
   (WARN + gauge for unsourced labeled issues; alert on scope failures;
   document semantics).

Related: [[founder-priority-pointer-first-saas-parked]] (pointer flow),
[[require-approval-flip-doesnt-release-held-prs]] (same class: config
semantics invisible at runtime).
