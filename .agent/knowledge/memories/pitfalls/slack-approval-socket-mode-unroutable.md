---
name: slack-approval-socket-mode-unroutable
description: Slack approval buttons can never work on Socket Mode deployments — approval.SlackHandler.HandleInteraction is wired only to the HTTP interactivity webhook; socket clicks fall through to comms "No pending task to confirm". FIXED by #4431.
type: pitfall
---

# Slack approval buttons unroutable in Socket Mode (HTTP-webhook-only wiring)

**Status: fixed (#4431).** `internal/adapters/slack/handler.go`'s `HandleMessage`
now intercepts `Action:"callback"` events whose action ID/value match
`approve`/`reject` (or the `approve:`/`reject:` value prefix) and routes them
directly to a `slack.ApprovalCallbackHandler` (implemented by
`*approval.SlackHandler`) before the comms fallthrough — mirroring Telegram's
GH-3825 routing. Wired at both the `cmd/pilot/main.go` polling-mode call site
and `internal/pilot/pilot.go`'s gateway-mode call site (each nil-guards the
handler so approval-disabled deployments don't wrap a nil pointer in a non-nil
interface). `comms/handler.go`'s callback fallthrough also now replies
`"Unknown action: <id>"` for any ActionID that isn't a recognized
execute/cancel value, instead of silently treating it as a cancel of whatever
task happens to be pending.

**What happened (2026-07-17, #4411/#4431):** Every founder click on a Pre-Merge
Approval button failed with "No pending task to confirm" — before AND after the
#4426 persistence fix, restarts irrelevant.

## Root cause
`internal/pilot/pilot.go` wires `slackApprovalHdlr.HandleInteraction` only into
`slack.NewInteractionHandler(signingSecret)` — an HTTP interactivity endpoint
that requires a public Request URL. The founder box runs Slack in **Socket
Mode** with no public endpoint, so that webhook never fires. Socket button
clicks arrive as `core.MessageEvent{Action:"callback"}` →
`adapters/slack/handler.go` → `comms.Handler.handleConfirmation` →
`pendingTasks` miss → the misleading error. The approval handler is
unreachable **by construction**, not by state loss.

## Debugging lesson
"No pending task to confirm" comes from `comms/handler.go`, NOT the approval
package — seeing that exact string means the click was ROUTED WRONG, not that
approval state is missing. The #4426 persistence work fixed a real but
secondary gap; the primary gap is routing (#4431).

## How to apply
- Fix: route `approve:`/`reject:` action values (or approval action IDs) to
  `HandleInteraction` inside the socket callback path before the comms
  fallthrough (Telegram does this — GH-3825 precedent).
- Any interactive feature must be tested on the transport the deployment
  actually uses (socket vs HTTP webhook) — they are disjoint code paths.
- Related: [[founder-priority-pointer-first-saas-parked]], #4411, #4426, #4431.
