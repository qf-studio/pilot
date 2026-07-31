# fix(github-poller): pre-dispatch fresh GetIssue is discarded — dispatched issue keeps the stale list-snapshot body

**Status**: ✅ Delivered (studio-sdk GH-105 → PR#106 merged 07-31, released in **v0.31.2**) · **Created**: 2026-07-30 · **Last Updated**: 2026-07-31
**Target repo**: qf-studio/studio-sdk · SDK-side twin of qf-studio/pilot#4624

## Context

The GitHub poller's parallel dispatch path re-fetches each candidate issue immediately before dispatch — then throws the fresh object away and dispatches the stale list snapshot.

`sdk/integrations/github/poller.go:1099-1117` (v0.31.2 lineage):

```go
for _, issue := range toDispatch {
    // Refresh labels before dispatch to avoid stale snapshot races.
    if fresh, ferr := p.client.GetIssue(ctx, p.owner, p.repo, issue.Number); ferr == nil && fresh != nil {
        if HasLabel(fresh, LabelDone) || HasLabel(fresh, LabelInProgress) {
            ...
            continue
        }
    }
    ...
    go func(issue *Issue) {           // <- the ORIGINAL list-snapshot object, not `fresh`
        ...
        result, err := p.onIssueWithResult(ctx, issue)
```

`fresh` is consulted only for a done/in-progress label check. The dispatched `issue` — converted to `core.IssueEvent` via `toIssueEvent` (`adapter.go:169-192`, `Body: issue.Body`) — carries the body/labels/title from `fetchCandidates`' `ListIssues` snapshot, which can be arbitrarily older than the dispatch moment under queue depth or semaphore backpressure.

**Production impact (2026-07-30, Pilot daemon)**: an operator fixed an issue body at ~10:04Z; the poller dispatched at 10:05:35Z with the pre-edit list-snapshot body; the host's spec validator judged the stale body and silently escalated the issue to `pilot-blocked` (4-day block/unblock loop, see qf-studio/pilot#4624 for the host-side analysis). The host side is fixing its own discard of the same fresh read, but the poller handing consumers a knowingly-stale object after having a fresh one in hand is the SDK's half of the defect.

## Implementation

In the pre-dispatch loop, when the fresh GET succeeds, dispatch `fresh` instead of the list-snapshot `issue`:

- Replace the object passed to the dispatch goroutine (and thus to `toIssueEvent`/`onIssueWithResult`) with `fresh` when `fresh != nil`; keep the list-snapshot fallback when the GET failed (current fail-open behavior unchanged).
- Apply to every dispatch mode that does the pre-dispatch refresh (parallel/auto; check whether the sequential path has the same shape and fix it symmetrically if so).
- No API/signature changes — `fresh` is already an `*Issue` in scope.

## Acceptance

- [ ] When the pre-dispatch `GetIssue` succeeds, the handler receives the fresh issue's Body/Title/Labels, not the list snapshot's (test: list snapshot with body A, fresh GET returns body B → `onIssueWithResult` sees B)
- [ ] When the pre-dispatch `GetIssue` fails, behavior is unchanged (list-snapshot dispatch, fail-open)
- [ ] Done/in-progress label skip behavior unchanged
- [ ] Existing poller tests green

## Refs

- Pilot issue: https://github.com/qf-studio/studio-sdk/issues/105
- Host-side twin + full incident analysis: https://github.com/qf-studio/pilot/issues/4624
- Same-shape host defect: pilot `cmd/pilot/handlers.go` fetches fresh and discards `.Body` (fixed under #4624)
