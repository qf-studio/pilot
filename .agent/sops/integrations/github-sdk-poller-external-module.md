# SOP: `component=github-sdk-poller` dispatch-loop code lives outside this repo

**Trigger**: a task asks you to change logging/dispatch-loop behavior for
`component=github-sdk-poller` (e.g. "before announcing dispatch...", "the
code that logs 'Dispatching issue for parallel execution'").

**The gotcha**: that log line, the candidate-filter loop, and the
`toDispatch` announce-then-call-Handler sequence are NOT in `pilot` — they
live in the vendored `github.com/qf-studio/studio-sdk` module
(`sdk/integrations/github/poller.go`), pinned in `go.mod` with no `replace`
directive. `cmd/pilot/poller_github.go` only *registers* the SDK poller
(config mapping, deps wiring, token resolution) — it does not implement the
poll/dispatch loop.

Confirm before assuming you can edit it:

```bash
grep -n "studio-sdk" go.mod                      # pinned version, no replace
grep -rn "Dispatching issue for parallel execution" \
  $(go env GOPATH)/pkg/mod/github.com/qf-studio/studio-sdk@<version>/
```

If the string is only found under `$(go env GOPATH)/pkg/mod/...`, it is
**read-only vendored code** — editing it does nothing (module cache) and
committing changes there is not a `pilot` PR.

## What you CAN fix from `pilot`

The SDK poller calls back into pilot via `sdkcore.IssueHandlerFunc` →
`handleGithubIssueEventSDK` (`cmd/pilot/handlers.go`) →
`handleIssueGeneric` (`cmd/pilot/handler_common.go`), which is the **single
shared choke point** every adapter (github, gitlab, jira, linear,
azuredevops, in-tree github) uses to call `Dispatcher.QueueTask`. The SDK
poller's dispatch loop does:

```go
result, err := p.onIssueWithResult(ctx, issue)
if err != nil {
    p.logger.Error("Failed to process issue", ...)   // <-- this is what fires
    ...
}
```

So: **any non-nil error your Handler returns becomes an ERROR log inside
studio-sdk.** You cannot suppress the loop's own INFO "Dispatching..."
announcement (it fires before your Handler is even called), but you CAN
prevent the paired ERROR by making `handleIssueGeneric` return a nil error
for conditions that are expected/benign (e.g. GH-4008: task already queued
or running — downgrade via `Dispatcher.IsActive()` pre-check +
`errors.Is(qErr, executor.ErrTaskAlreadyActive)` in the QueueTask
error-handling branch, return `(hr, nil)` instead of propagating).

## When the INFO-level announcement itself must change

That requires an actual `studio-sdk` change: edit the sibling checkout at
`~/Projects/startups/studio-sdk` (separate git repo), cut a release, then
bump `go.mod` in `pilot` to the new version. That is a cross-repo task, not
a single Pilot-issue scope — flag it as a follow-up rather than attempting
it inside a `pilot/GH-*` branch.

## Earliest controllable checkpoint: `ExecutionChecker`/`TaskChecker`, not `HandlerResult.Error`

If the task is "stop the poller from even attempting dispatch" (not just
suppress a log line after the fact), `handleIssueGeneric`'s returned
`HandlerResult.Error` is too late — the vendored poller's per-issue loop
(`sdk/integrations/github/poller.go`) only inspects `err` (from
`onIssueWithResult`) and `result.Success`/`result.PRNumber`; it never reads
`result.Error`. By the time your Handler runs, the poller has already
walked scope-overlap grouping, done a fresh-label GH API refresh, run the
pre-flight judge subprocess (~30s/~280MB), called `markProcessed`, and
logged the INFO "Dispatching issue for parallel execution" announcement.

The actual earliest host-controllable checkpoint in the loop is the
`core.ExecutionChecker` hook (`HasCompletedExecution(taskID, projectPath)`,
wired via `terminalCompletionChecker` in `cmd/pilot/main.go`) — it runs
*before* candidate filtering, the judge, and the claim insert. To make the
poller skip a task entirely for a tick (not just avoid one log line), gate
inside that checker rather than downstream in `handleIssueGeneric`.

Confirm the exact call order before assuming a hook fires early enough:

```bash
grep -n "hasCompletedExecution\|hasMergedWork\|hasPendingDependencies\|passesPreFlight\|markProcessed\|Dispatching issue for parallel execution" \
  $(go env GOPATH)/pkg/mod/github.com/qf-studio/studio-sdk@<version>/sdk/integrations/github/poller.go
```

GitLab's vendored poller (`sdk/integrations/gitlab/poller.go`) has **no**
`ExecutionChecker`/`TaskChecker`/`PreFlightJudge` hooks at all — this
early-checkpoint pattern is GitHub-only; GitLab must rely on the
`handleIssueGeneric` gates (downstream, post-announcement).

- GH-4469 (2026-07-20): GH-4391 looped 4,233 dispatch→reject cycles over
  ~2 days because the repick-backoff gate lived only in
  `handleIssueGeneric`, downstream of the judge subprocess and claim
  insert. Fix: `terminalCompletionChecker.HasCompletedExecution` now
  consults `repickBackoff` first and reports `true` (as if terminally
  complete) while a task is gated — the poller then treats it exactly like
  an already-completed issue and skips the rest of the loop for that tick.
  `ErrDispatchGated` (a new sentinel in `internal/executor/dispatcher.go`)
  was still added for `handleIssueGeneric`'s own gates, but it is
  introspection-only for the GitHub SDK poller (never read by it) — its
  value is for tests/logging and for adapters that DO consult
  `HandlerResult.Error`.

## History

- GH-4008 (2026-07-07): task asked to suppress the "Dispatching issue for
  parallel execution" INFO announcement itself for already-active tasks.
  Fixed the graded acceptance criterion (zero ERROR lines) entirely from
  `handler_common.go`; the INFO announcement suppression was left as an
  explicit out-of-repo-scope note (would require a `studio-sdk` release).
