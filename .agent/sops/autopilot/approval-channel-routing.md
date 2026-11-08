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

## Destination (Slack) — decision 2026-09-06

Platform routing above picks *which handler*; the Slack handler then posts to
`adapters.slack.approval.channel` → fallback `adapters.slack.channel` → fallback DM to
`Approvers[0]` (`internal/approval/slack.go resolveChannel`, GH-4772 — that fallback once
made asks invisible: PR#4806 sat 50 min in an unwatched bot DM).

**Founder decision 09-06:** asks go back to the founder's bot DM **explicitly** —
`adapters.slack.approval.channel: 'D09HGS3BR4J'` on the box (not via the empty-key fallback,
so the choice is visible in config). Alerts moved off `#pointer` to `#pilot-reports` at the
same time so the DM/`#pointer` split is clean. Consequences:
- Second approver (the infra contractor, `U0ADEMJ7J83`) does **not** see DM-routed asks. Founder-accepted.
- Buttons in a DM reach the daemon through **Socket Mode**. The box config says
  `socket_mode: false`, but the daemon starts with `--slack`, and that flag sets
  `SocketMode = true` (`cmd/pilot/main.go`, `cmd.Flags().Changed("slack")` block). The flag
  wins over the config file. Settled 2026-10-19 (memory
  `box-slack-socket-mode-config-false-but-socket-mode-starts`). The HTTP interactions
  route exists only in gateway mode (`pilot.New`); polling mode without `--slack` has no
  click ingress at all (TASK-576 leg C, #5758). The 10-16 lost click is still unexplained.
- Full channel map: `.agent/system/references/reference_slack_notifications_routing.md`.

## Per-project routing and approver identity (2026-10-19)

Resolution order, most specific first:

| What | Keys (first set wins) |
|------|-----------------------|
| Channel | `projects[].approval.approval_source` → `environments.<env>.approval_source` → `autopilot.approval_source` |
| Approvers | `projects[].approval.approvers` → `approval.<stage>.approvers_by_channel.<channel>` → `approval.<stage>.approvers` |

- One PR goes to exactly one channel (`slack` · `telegram` · `github-review`). There is no
  fan-out to several channels.
- Slack and Telegram user IDs are different identity systems. A flat approver list of Slack IDs
  rejects every Telegram tap. Use `approvers_by_channel` (PR#5754, #5753).
- **Telegram caveat until #5755 ships:** a valid Telegram ID as the first approver moves the ask
  from `adapters.telegram.chat_id` to that user's DM (`resolveDestChatID` is approver-first;
  Slack has been channel-first since GH-4808).
- `github-review` needs `adapters.github.approval.enabled: true` (null on the box). Each request
  resolves its own owner/repo from the project and fails closed when it cannot (#5716). An
  approval counts only on an open PR and on the current head SHA (#5749).
- No registered handler for an enabled stage currently returns success, and the controller clock
  then applies `default_action` → fix #5756 (TASK-576 leg A).
