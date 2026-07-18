# SOP: a delivered PR is ground truth — it must outrank any later non-completed classification

**Category:** Quality / execution-ledger correctness
**Implemented:** 2026-07-18
**Source incident:** GH-4404 (pointer GH-16/GH-15 — poller re-picked a task
whose PR was already open and risked a duplicate-PR dispatch), one causal
chain with GH-4407 (intent judge's truncated-diff false veto)

## Problem

`ExecutionLifecycle.Classify` (`internal/executor/lifecycle.go`) derives an
execution's terminal status from `TerminalStatus(result)` (or `execErr`
directly). Nothing in that derivation consulted whether a PR had actually
been created. On pointer GH-16, a run completed and opened PR #18, but
something downstream of delivery — GH-4407's truncated-diff intent-judge
veto is the incident that exposed this, though the invariant below holds
regardless of which downstream signal causes it — still classified the
attempt as non-completed. The ledger row landed "failed" while the PR sat
open on GitHub. `HasTerminalCompletion` (which requires `status='completed'`
plus a PR/commit) correctly read that row as "not done," so the poller
re-dispatched the issue at the next claim generation — a full re-execution
of a task whose PR already existed, i.e. the exact duplicate-PR class
TASK-407/#4349 was built to prevent, reached through a different door.

## Root cause

The classification pipeline trusted whatever the *last* signal said
(intent-judge verdict, self-review, an error string) as the final word on
"was this done," instead of treating GitHub PR existence as authoritative.
Any advisory/soft check that runs after a PR is created can, by construction,
produce this disagreement unless something explicitly checks for delivery
evidence first.

## Fix

`ExecutionLifecycle.Classify` now promotes a non-completed classification to
`ExecStatusCompleted` whenever `result.PRUrl != ""` — **unless** the caller
passed an explicit `override` status. The override escape hatch matters:
epic.go's stranded-work guard (`executeSubIssuesTracked`) forces `failed` for
a sub-issue that has commits but **no PR** — the mirror case, which never
collides with this fix since it has no PRUrl to promote on. If a future
caller ever needs to override *despite* a PR existing (e.g. a PR later found
to be garbage), the override parameter already provides that escape hatch —
don't try to special-case it inside the PR check itself.

This lives in `Classify` (not `Persist`) so callers that record
`execution_events` between `Classify` and `Persist` (GH-4259 ordering) see
the corrected status too — the event ledger and the row status stay
consistent.

## Prevention

Any new signal that can mark an execution non-completed *after* PR/commit
creation (a new quality gate, a new judge, a new async check) must be
evaluated against this invariant: **can this fire after delivery evidence
exists?** If yes, it must not be allowed to make `HasTerminalCompletion`
disagree with GitHub reality. Prefer feeding the signal in as an advisory
warning (see `ExecutionResult.IntentWarning` — surfaced as a PR/issue
comment, never as ledger status) over letting it drive terminal status
directly, unless the signal specifically wants to invoke the `override`
parameter with full knowledge of this invariant.

## Test coverage

- `TestExecutionLifecycle_Finish_PRExistsPromotesToCompleted` — the GH-4404
  regression: a `Success: false` result with a PRUrl set still resolves to
  `completed`, and `HasTerminalCompletion` reports the row as done.
- `TestExecutionLifecycle_Finish_OverrideWinsOverPRSelfHeal` — explicit
  override still wins over the PR self-heal.
- `TestExecutionLifecycle_Finish_Override` (pre-existing) — the mirror case,
  commits with no PR, still gets a caller-forced `failed`.
