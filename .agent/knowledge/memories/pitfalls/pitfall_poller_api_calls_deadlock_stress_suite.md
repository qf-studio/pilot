---
name: poller-api-calls-deadlock-stress-suite
description: Any new GitHub API endpoint call added inside the poller's poll/dispatch cycle deadlocks the stress/ suite — its fake servers only implement the issues-LIST route. Resolve state in-memory from the per-cycle fetchCandidates result instead. Cost 4 failed PRs on GH-3789.
type: pitfall
---

**Never add a new GitHub API endpoint call inside the poller's poll or dispatch
path** (`internal/adapters/github/poller.go`). The `stress/` package's fake
GitHub servers (`stress/concurrent_test.go`, `stress/memory_test.go`) implement
ONLY the issues-list route (`/repos/{o}/{r}/issues`); any other route (e.g.
per-issue `GetIssue`) falls through unserved, and the retrying client
(`retry.go` exponential backoff via `doRequest`) burns the full 600s package
timeout with goroutines parked in `httptest Server.Accept`.

**Why:** GH-3789 (Blocked-by gating) burned **4 autonomous Pilot attempts**
(PRs #3802, #3822, #3824, #3835) — every one failed identically on
`FAIL github.com/qf-studio/pilot/stress 600.0s`, both dispatch-time and
poll-time variants, because each resolved blocker state via a per-blocker
`GetIssue` call. Two also tripped the 200-addition PR size guard from cascade
churn.

**How to apply:**
1. Need state about other issues during a poll cycle? Resolve it **in-memory
   from the already-fetched `fetchCandidates` slice** (the full open,
   `pilot`-labeled set, one API call per cycle). That's how the fix landed
   (TASK-384 → #3882 → PR #3883, merged first-attempt, v2.214.1):
   `hasPendingDependencies(issue, fetched)` checks blocker numbers against a
   set built from the fetched list; absent ⇒ fail-open (closed/unlabeled).
2. If a new endpoint in the cycle is truly unavoidable, extend the stress
   fakes with that route **in the same PR**, and run
   `go test ./stress/ -timeout 600s` locally before pushing — it is the gate
   that killed every prior attempt.
3. Gating must be non-blocking: skip with a `skipreason`, re-evaluate next
   poll. Never wait on a blocker inside the cycle.

Related: [[verify-branch-and-working-tree-before-destructive-ops]] (same repo
discipline family); TASK-382 D6 register row; issue #3789 thread holds the
full RCA and the formal decision that dispatch-time recheck is out of scope.
