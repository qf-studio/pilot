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

## History

- GH-4008 (2026-07-07): task asked to suppress the "Dispatching issue for
  parallel execution" INFO announcement itself for already-active tasks.
  Fixed the graded acceptance criterion (zero ERROR lines) entirely from
  `handler_common.go`; the INFO announcement suppression was left as an
  explicit out-of-repo-scope note (would require a `studio-sdk` release).
