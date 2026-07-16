# SOP: Approval channel routing (GH-4380)

## Problem

`environments.<env>.approval_source: slack` (or any non-default channel)
had zero effect. Every async approval request silently fell through to
whichever handler won Go map iteration order, and — if that happened to
be Telegram — could dial an `Approvers[0]` value meant for a different
channel, producing a per-tick `chat not found` 400 with no alert, no
comment, no metric. PRs sat in `awaiting_approval` for hours
(#4373/#4374, v2.241.0 regression).

## Root cause

Three independent gaps stacked:

1. **Config field never read.** `EnvironmentConfig.ApprovalSource` existed
   in the config struct and YAML schema, but nothing in
   `internal/autopilot/controller.go`'s `submitAsyncApprovalRequest` ever
   read it — `approval.Request.PreferredChannel` was never set. The
   wiring (`req.PreferredChannel = string(cfg.ApprovalSource)`) lived in
   `auto_merger.go:requestApproval` per TASK-26, but that function was
   deleted when TASK-36 migrated approval submission off the legacy
   blocking path into `controller.go` — and the PreferredChannel
   assignment was never carried over to the new call site.
2. **Silent nondeterministic fallback.** `Manager.SubmitApprovalRequest`
   treated "PreferredChannel set but not registered" the same as
   "PreferredChannel unset": WARN + pick any registered handler via map
   iteration. A `PreferredChannel` that's always non-empty (which it now
   is, since the controller always resolves *some* approval source) makes
   this branch always active whenever the intended channel isn't wired
   up — never a hard failure.
3. **No destination validation.** `TelegramHandler` used
   `req.Approvers[0]` verbatim as the Telegram chat id when present,
   without checking it looked like a Telegram destination at all. Since
   `Approvers` is a config-level, channel-agnostic list, a value meant for
   a different channel landing here (via gap #2) produced a live 400 on
   every retry instead of one diagnosable warning.

## Fix

- `autopilot.Config.EffectiveApprovalSource()` resolves per-env override →
  top-level default, and `submitAsyncApprovalRequest` sets
  `req.PreferredChannel` from it on every request.
- `Manager.SubmitApprovalRequest`: an explicit, unregistered
  `PreferredChannel` now returns a named error — no fallback. Fallback is
  preserved only when `PreferredChannel` is genuinely unset.
- `TelegramHandler.resolveDestChatID` validates `Approvers[0]` looks like
  a Telegram destination (`isValidTelegramChatID`) before using it;
  otherwise falls back to the configured `chat_id` and logs once (deduped
  per bad value).
- `Controller.alertApprovalSubmitFailureOnce`: on submit failure, increments
  `ApprovalSubmitFailures` (exposed as `pilot_approval_submit_failures_total`),
  fires an `alerts.EventTypeTaskFailed` event, and posts a PR comment
  mentioning the repo owner — all deduped per PR so a retried tick doesn't
  spam either.

## Prevention

When adding a new per-environment config knob that's supposed to override
routing/behavior (anything under `EnvironmentConfig`), grep for every call
site that constructs the object it's meant to influence
(`approval.Request{...}`, etc.) and confirm the field is actually read —
struct field + YAML tag existing is not proof of wiring. This class of bug
(field defined, never consumed) is easy to reintroduce during a refactor
that moves the one call site that used to read it (as TASK-36 did here).
