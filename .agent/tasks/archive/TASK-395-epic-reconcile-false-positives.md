# TASK-395: Epic reconcile shipped-check false positives — parent in own child set, post-close escalation, stale needs-clarification

**Status**: ✅ SHIPPED — PR [#4147](https://github.com/qf-studio/pilot/pull/4147) MERGED 2026-07-10; spec live-validated twice before merge
**Created**: 2026-07-09
**Assignee**: Pilot (when dispatched)

---

## Context

**Problem** (incident 2026-07-09, epic GH-4127 → children #4128–#4131, all shipped
same day via merged PRs #4133/#4136/#4137/#4138):

The epic-parent reconcile sweep (`internal/autopilot/epic_reconcile.go`) posted
"⚠️ Epic auto-close is permanently blocked" **three times** on an epic whose every
child had (or was minutes from having) a merged PR, and left a permanent
`pilot-needs-clarification` label on the successfully-closed parent. Four distinct
defects, observed live:

1. **Parent appears in its own child set.** One escalation names "child #4127" —
   the parent itself. `getSubIssuesByTextSearch` (`epic_reconcile.go:124`) finds
   issues whose text references the parent; the parent's own body/comments match
   (the escalation comment literally contains "GH-4127", making this
   **self-amplifying** — each escalation strengthens the match). The parent (closed,
   no directly-linked merged PR — PRs reference children) then fails
   `verifyChildrenShippedForClose` → veto against itself.
2. **Eventually-consistent PR search produces false "no merged PR" vetoes.**
   `verifyChildrenShippedForClose` (`epic_reconcile.go:171-199`) uses
   `SearchPRsForIssue` — GitHub search-API backed, minutes of indexing lag. During
   the ~1 h window when children were closing and PRs merging (#4131 closed;
   #4138 merged 18:00), consecutive passes saw "closed child, no merged PR" and the
   identical veto signature reached `epicCloseVetoBreakerThreshold` → escalation.
   The veto also conflates "PR exists but not yet merged/still in CI" with
   "no PR at all".
3. **Reconcile passes run on already-closed parents.** One "permanently blocked"
   comment landed AFTER the auto-close success comment. Candidates from
   `discoverBodyMarkerEpicParents` (`epic_reconcile.go:278`, recently-closed
   children's "Parent: GH-N" markers) aren't filtered on parent state, so a closed
   parent keeps getting vetoed/escalated.
4. **`pilot-needs-clarification` is added but never removed.**
   `escalateEpicCloseVeto` (`epic_reconcile.go:546`) adds the label;
   `clearEpicCloseVeto` (`epic_reconcile.go:534`) clears only the in-memory streak.
   When a later pass refutes the veto (children verify shipped), or the parent
   closes successfully, the label — which blocks dispatch — stays forever.
5. *(cosmetic)* Auto-close summary listed "Merged PRs: #4137, #4138" — 2 of 4;
   same search-lag root cause as (2) at close time.

**Interaction with TASK-394 / #4140**: missing sub-issue `executions` rows starve
the `isChildNoOp` ledger path, making the search-API result the only evidence —
#4140 reduces exposure but does not fix defects 1, 3, 4, which are
reconciler-local. No file conflict expected (`epic.go`/`store.go` vs
`epic_reconcile.go`), but land after #4140 to avoid merge friction.

**Goal**: the reconcile sweep never vetoes a genuinely-shipped epic, never
processes itself as its own child, never touches closed parents, and cleans up
its own escalation label when the blocking condition resolves.

---

## Known Pitfalls & Patterns

- **PITFALL** (90%, mem-100): this incident — full evidence timeline on GH-4127.
- **PITFALL** (mem-058 family): gate every reconcile/dispatch entry point on
  parent state — defect 3 is this pattern missed at the sweep entry.
- **PATTERN** (mem-065): "done without shipped code" gates must verify a merged
  PR for the exact `pilot/GH-N` branch — the same evidence standard applies here
  in reverse: use the direct branch/timeline lookup, not eventually-consistent
  search, before declaring "no merged PR".
- **PATTERN** (mem-093 lesson 2): test the drain/sweep path, not just the happy
  handler — the existing tests never exercised a closed parent or a
  self-referencing child set.

---

## Acceptance Criteria

- [ ] `getSubIssuesByTextSearch` and `getAllSubIssueNumbers` never return the
      parent's own number in the child set
- [ ] `reconcileEpicParent` returns early (no veto, no escalation, no comment)
      when the parent issue is already closed; closed parents with a lingering
      `pilot-needs-clarification` from a refuted veto get the label removed
- [ ] `verifyChildrenShippedForClose` verifies via direct PR lookup for branch
      `pilot/GH-N` (timeline/linked-PR API, not search) before vetoing; search
      remains a secondary source. A child whose PR exists but is unmerged (open /
      in CI) defers (no veto count) rather than vetoing
- [ ] `clearEpicCloseVeto` path (veto refuted on a later pass) also removes
      `pilot-needs-clarification` if this reconciler added it
- [ ] Close-summary comment lists the merged PR of every child
- [ ] Table-driven tests: self-referencing child set; closed parent in candidate
      list; closed-child-with-open-PR (defers); closed-child-with-merged-PR-via-
      branch-lookup-but-not-search (no veto); refuted veto removes label
- [ ] GH-4127 replay: running the fixed sweep against the recorded state produces
      zero vetoes

---

## Out of Scope

- Sub-issue `executions` ledger rows (TASK-394 / #4140, dispatched)
- `maybeCloseParentIssue` reactive-close path beyond the summary-comment fix
- Alert-engine changes (`epic_close_vetoed` alert stays; it just stops firing falsely)
- GH-4006 breaker mechanism itself (threshold/streak logic is sound; its inputs were wrong)

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| "No merged PR" evidence source | search API only; ledger only; direct branch/linked-PR lookup with search fallback | direct lookup first | Search is eventually consistent — the root of defects 2 and 5; ledger starved until #4140 |
| Closed-parent handling | skip silently; skip + label cleanup | skip + cleanup | Defect 4 leaves dispatch-blocking labels on closed issues; cleanup is one API call on an already-terminal object |
| Unmerged-PR child | veto; defer (no count) | defer | A child in the close→merge window is in-flight, not broken; vetoing it is exactly the GH-4127 false positive |

---

## Verify

```bash
make build && make test && make lint
go test ./internal/autopilot/ -run "TestReconcileEpic|TestVerifyChildrenShipped|TestEscalateEpicCloseVeto" -v
```

---

## Done

- [ ] All four defects have failing-then-passing tests
- [ ] GH-4127's stale `pilot-needs-clarification` removed (manual or by the new cleanup path)
- [ ] Next live epic closes with a complete PR list and zero false "blocked" comments

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4143
- Incident evidence: https://github.com/qf-studio/pilot/issues/4127 (3 escalation
  comments, self-reference, post-close escalation, 2/4 PR list)
- Related dispatched fix: TASK-394 → https://github.com/qf-studio/pilot/issues/4140 (ledger rows)
- Prior art: GH-4006 (veto breaker), GH-3939 (reconcile sweep), GH-3780 (no_op children),
  GH-4099 (body-marker discovery), GH-3513 (unlinked children)
- Parent plan: `.agent/tasks/TASK-393-throughput-acceleration.md` (incident surfaced
  during Phase 1 delivery)
- Memory: mem-100

---

**Last Updated**: 2026-07-09
