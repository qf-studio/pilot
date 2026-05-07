# TASK-31: Replace blocking approval receive with callback-driven stage advance

**Status:** Planned (depends on TASK-30 landing first)
**Priority:** P1 (queue starvation under stalled approvals)
**Type:** Architectural refactor
**Effort:** L (~150-250 LoC + careful tests)

## Problem

`internal/approval/manager.go:requestApproval` returns a `<-chan *Response`,
and the caller (`autopilot/controller.go:handleAwaitApproval`) does
`<-responseCh` — a hard block up to the configured timeout (24h default).

`processAllPRs` (`controller.go:2059-2114`) is a sequential `for _, pr :=
range prs` loop. Every other PR — earlier-stage, post-merge, anything —
waits behind the blocked one. A single user who doesn't tap the approval
button starves the entire queue for 24h.

The intended UX is asynchronous: approval message goes out → user taps
sometime later → that tap should drive the stage forward independently of
the controller loop's cadence.

## Fix Shape

Make approval truly asynchronous: persist decision into PR state on tap;
have the controller's normal tick check `await_approval` PRs against PR
state and advance when a decision is recorded. Remove the blocking
receive.

### Design

**State machine addition** — PR state already has stage tracking; add:
- `approval_request_id` (the request submitted)
- `approval_decision` (pending|approved|rejected|expired)
- `approval_decision_at` (timestamp)
- `approval_decision_by` (user ID)

**Two paths converge into the controller tick:**

1. **Submission path** (`handleAwaitApproval`):
   - If PR has no `approval_request_id` yet → call
     `manager.SubmitApprovalRequest(...)` (new, non-blocking; returns
     `requestID`, no channel) → store on PR state → return
     `StageAwait` (stay in stage, controller will recheck next tick).
   - If PR has `approval_request_id` and decision is still `pending` →
     check expiry; if expired, mark `expired` and apply `default_action`.
     Otherwise return `StageAwait`.
   - If decision is `approved` → advance stage. If `rejected`/`expired`
     w/ default_action=reject → close/abort PR per existing logic.

2. **Decision path** (`approval.TelegramHandler.HandleCallback` and Slack/GitHub equivalents):
   - On approve / reject tap → manager looks up the request, writes the
     decision into PR state via injected `PRStateWriter` interface, then
     deletes from pending. **No channel send.** Returns immediately.

**Manager interface change:**
```go
type PRStateWriter interface {
    SetApprovalDecision(ctx context.Context, requestID string, d Decision, by string) error
}
```
Inject into `Manager` at construction. Each handler is given a callback
that the manager wires up, OR each handler calls back into the manager
which routes to the writer.

**Backward-compat note:** drop the `<-chan *Response` API entirely; no
external consumers besides controller. Single-PR migration.

### Code changes

**1. `internal/autopilot/state.go`** (or wherever PR state lives)
- Add fields to PR state struct + persistence
- Add `SetApprovalDecision` method on the store

**2. `internal/approval/manager.go`**
- Replace `RequestApproval(ctx, req) (<-chan *Response, error)` with
  `SubmitApprovalRequest(ctx, req) (requestID string, error)`
- Replace internal handler dispatch to use new non-blocking signature on handlers
- Add `RecordDecision(ctx, requestID, decision, by)` — handlers call this
  on tap; manager writes via injected `PRStateWriter`

**3. `internal/approval/telegram.go` / `slack.go` / `github.go`**
- `HandleCallback` (and Slack/GH equivalents) → on decision call
  `manager.RecordDecision(ctx, requestID, decision, userID)` instead of
  pushing to a channel.

**4. `internal/autopilot/controller.go`**
- `handleAwaitApproval` becomes non-blocking — the two-path logic above.
- Remove `<-responseCh` and timeout select.
- Approval timeout is now checked at tick time against
  `approval_decision_at == 0 && now - request_at > timeout`; controller
  applies `default_action`.

### Tests

- `TestController_AwaitApproval_StaysInStageUntilDecision` — first tick
  submits, second tick (no decision) stays, third tick (after manual
  decision write) advances.
- `TestManager_RecordDecision_WritesToState` — stub writer, verify call.
- `TestController_AwaitApproval_AppliesDefaultActionAtTimeout`
- `TestController_QueueNotStarved` — two PRs both at await_approval; one
  gets a decision, the other waits; verify the decided PR advances on the
  next tick without the second blocking it. (The whole point.)
- Migration tests for state schema additions.

### Migration / rollout

- This is a single load-bearing change to a critical path (release
  pipeline). Strongly prefer:
  - Land behind a config flag `approval.async_dispatch: true` (default
    `true` after a release of bake time).
  - Keep both code paths for one release cycle, then remove the blocking
    path.
- Coordinate with TASK-30 landing first so persisted approvals exist for
  rehydration on restart — without TASK-30, an in-flight async approval
  is lost on restart and PR sits indefinitely. (TASK-30 is the safety net.)

### Verification

- All existing approval tests pass under new manager surface.
- Manual smoke: open two stage releases simultaneously → tap approve on
  the second one → it advances while the first still waits → tap approve
  on the first → it advances. (Currently impossible.)
- 24h timeout still applied via tick-time check.

## Acceptance Criteria

- [ ] `Manager.RequestApproval` channel API removed; `SubmitApprovalRequest` + `RecordDecision` in place
- [ ] `handleAwaitApproval` non-blocking; PR stays in `await_approval` stage until state shows decision
- [ ] Telegram, Slack, GitHub handlers all call `manager.RecordDecision` on tap
- [ ] Approval timeout enforced at tick time, not via blocking select
- [ ] Two-PR queue test: stalled approval on one PR does NOT block the other
- [ ] All existing approval tests updated and green
- [ ] `go build ./...` + `go vet ./...` clean
- [ ] Conventional PR title: `refactor(approval): callback-driven stage advance, remove blocking receive (GH-NNNN)`

## References

- `internal/approval/manager.go:205-213` — current blocking `<-responseCh`
- `internal/autopilot/controller.go:2059-2114` — sequential `processAllPRs`
- `internal/autopilot/controller.go:handleAwaitApproval` — current blocking caller
- TASK-29 (archived) — dispatch wiring this depends on
- TASK-30 — SQLite persistence (recommended to land first)
- Memory: `bug_telegram_approval_callback_unwired.md` § "Outstanding follow-ups"
