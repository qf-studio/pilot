# TASK-384: GH-3789 — Blocked-by gating via in-memory candidate resolution (no per-blocker API call)

**Status**: ✅ Completed 2026-07-05 — [#3882](https://github.com/qf-studio/pilot/issues/3882) → PR [#3883](https://github.com/qf-studio/pilot/pull/3883) merged (`407a4e27`, +194/−90), released v2.214.1
**Created**: 2026-07-05
**Assignee**: Pilot (first-attempt merge after 4 failed autonomous tries on #3789)

---

## Context

**Problem** (Defect D6, TASK-382 register):
`Blocked by: #N` gating runs at poll time in both poller modes via
`hasPendingDependencies` (`internal/adapters/github/poller.go:2345`), but it
resolves each blocker with a **live `p.client.GetIssue` call**
(`poller.go:2354`). Observed defect: GH-3759 (`Blocked by: #3754`) queued and
ran while its blocker was open.

Four autonomous fix attempts (PRs #3802, #3822, #3824, #3835) all failed
identically: `FAIL github.com/qf-studio/pilot/stress 600.0s` — the `stress/`
package's fake GitHub servers (`stress/concurrent_test.go:59-76`,
`stress/memory_test.go`) serve only the issues-**list** route, so any
per-blocker `GET /repos/{o}/{r}/issues/{n}` in the poll/dispatch path burns
the full timeout in retry loops (`internal/adapters/github/retry.go:31`,
exponential backoff via `doRequest`, `client.go:210`). Attempts #3824/#3835
additionally tripped the PR size guard (>200 additions).

**Goal** (fix direction settled on the issue thread, final comment 2026-07-04):
Resolve `Blocked by: #N` / `Depends on: #N` **against the already-fetched
candidate list** returned by `fetchCandidates` (`poller.go:904`) — the full
open, `pilot`-labeled issue set fetched once per poll cycle. Blocker **present**
in that list ⇒ open ⇒ skip candidate. Blocker **absent** (closed, unlabeled,
or nonexistent) ⇒ not blocking. Zero new GitHub API calls ⇒ stress-safe by
construction.

---

## Acceptance Criteria

- [ ] `hasPendingDependencies` (or its replacement) makes **no**
      `p.client.GetIssue` call — blockers are resolved against the
      `fetchCandidates` result for the current poll cycle.
- [ ] Both call sites thread the fetched slice:
      sequential `findOldestUnprocessedIssue` (`poller.go:1068`) and
      parallel `checkForNewIssues` (`poller.go:1459`).
- [ ] A candidate whose blocker is present (open) in the fetched list is
      skipped and re-evaluated next cycle — never waited on (non-blocking).
- [ ] Skip is recorded via `skipreason.ReasonPendingDependency` in **both**
      modes (sequential mode currently records nothing — gap noted in #3835).
- [ ] Blocker absent from the fetched list ⇒ candidate proceeds
      (deliberate fail-open direction change from today's
      fail-closed-on-API-error at `poller.go:2363`; documented in code).
- [ ] `go test ./stress/ -timeout 600s` passes — run **locally before push**;
      this is the gate all four prior attempts died on.
- [ ] `go test ./internal/adapters/github/` passes; existing
      `TestPoller_HasPendingDependencies*` tests (`poller_test.go:1068-1180`)
      reworked from per-issue httptest routes to list-based fixtures.
- [ ] New test: D6 reproduction — issue with `Blocked by: #N` where #N is open
      and present in the fetched list is skipped in BOTH poller modes, with
      skip reason recorded.
- [ ] Diff ≤ 200 additions (CI size guard killed #3824 at 328 and #3835 at 251).

---

## Implementation

### Phase 1: In-memory blocker resolution
**Goal**: Replace the API lookup with candidate-list lookup.

**Tasks**:
- [ ] Change `hasPendingDependencies(ctx, issue)` to
      `hasPendingDependencies(issue, fetched []*Issue)` (or add a sibling and
      delete the old one — do not keep both paths).
- [ ] Build a `map[int]struct{}` of fetched issue numbers once per poll cycle;
      `ParseDependencies` (`poller.go:1835`) output checked against it.
- [ ] Thread the fetched slice from `fetchCandidates` into both call sites
      (it is already a local var `issues` at `poller.go:922` and
      `poller.go:1318` — no new plumbing).
- [ ] Record `skipreason.ReasonPendingDependency` in the sequential path.

**Files**:
- `internal/adapters/github/poller.go` — gate rewrite + call-site threading
- `internal/adapters/github/poller_test.go` — rework existing tests, add D6 repro

### Phase 2: Verification
- [ ] `go test ./internal/adapters/github/ ./stress/ -timeout 600s`
- [ ] `make lint && make build`
- [ ] Confirm diff size < 200 additions before opening PR.

---

## Out of Scope

- **Dispatch-time recheck** — formally descoped on the issue thread
  (2026-07-04). The race window (blocker reopens while a task sits queued
  behind the semaphore / pre-flight judge) is accepted and documented.
- Extending the `stress/` fake GitHub server with `GET /issues/{n}`
  (guidance option 2 — rejected in favor of zero-new-calls).
- Project-board-sourced path (`FindIssuesFromProject`, `poller.go:910`)
  semantics beyond what the shared gate change gives it for free.
- Any new GitHub API endpoint usage anywhere in the poll cycle.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Blocker state source | (a) per-blocker `GetIssue`; (b) in-memory fetched list; (c) extend stress fake + keep API call | (b) | (a) deadlocks stress suite (4 failed PRs); (c) keeps a per-blocker API call per poll cycle at runtime. (b) is zero-cost and stress-safe by construction. |
| Absent-blocker semantics | fail-closed (block) vs fail-open (proceed) | fail-open | Absent from open+`pilot`-labeled fetch ⇒ overwhelmingly means closed. Fail-closed on absence would permanently wedge issues whose blockers are closed or unlabeled. Settled on issue thread. |
| Enforcement point | poll-time only vs poll+dispatch | poll-time only | Dispatch-time variants deadlocked stress; residual race accepted per issue decision. |

---

## Verify

```bash
go test ./internal/adapters/github/ -run 'Dependencies|PendingDependencies|SkipsBlocked' -v
go test ./stress/ -timeout 600s
make lint && make build
```

---

## Done

- [x] `grep -n "GetIssue" internal/adapters/github/poller.go` shows no call
      inside the dependency-gating path (verified on `origin/main` @ `407a4e27`;
      remaining `GetIssue` calls at :1517/:2240 are pre-existing other features).
- [x] All Verify commands pass in CI (PR #3883 merged through autopilot gates).
- [x] PR ≤ 200 additions (+194), merged; GH-3789 closed.
- [x] TASK-382 register row D6 flipped (done inside PR #3883 itself).

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/3882
- Issue: [#3789](https://github.com/qf-studio/pilot/issues/3789) — full RCA +
  scope decisions in comments (authoritative).
- Failed attempts: PRs #3802, #3822 (stress timeout), #3824, #3835
  (stress timeout + size guard).
- Parent register: `.agent/tasks/TASK-382-restart-epic-defect-burndown.md` (D6).
- Note: `pilot` label was removed from #3789 to stop autonomous retries —
  "re-add only after the stress-suite question is settled." This spec settles
  it (zero new API calls). #3789 also carries a stale `pilot-done` label that
  may make the daemon skip it; if dispatching to Pilot, prefer a fresh issue
  from this doc with `Fixes #3789` linkage.

---

**Last Updated**: 2026-07-05 (dispatched → #3882)
