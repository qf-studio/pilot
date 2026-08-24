# TASK-482: Authorize approval decisions — Telegram callback + Slack interaction (fixes #5149)

**Status**: 🚀 Dispatched to Pilot
**Last Updated**: 2026-08-24
**Source**: External report [#5149](https://github.com/qf-studio/pilot/issues/5149) (d3rowy, verified 2026-08-24). Research found the **same gap in Slack** — one PR fixes both.

## Context

Pre-merge approval decisions are recorded without verifying who pressed the button:

- **Telegram**: `internal/approval/telegram.go:471` `HandleCallback` parses `approve:`/`reject:` and calls `h.recorder.RecordDecision(...)` (~:552) unconditionally. `pending.Request.Approvers []string` is available at :486 but never read.
- **Slack**: `internal/approval/slack.go:399` `HandleInteraction` — identical shape, `RecordDecision` at ~:471, no identity check.
- The adapter-level `allowed_ids` gate applies to message handlers only; the callback dispatch (`internal/adapters/telegram/handler.go:404-467`) bypasses it entirely. `userID` arrives as the decimal string of the Telegram numeric user ID (`strconv.FormatInt(callback.From.ID, 10)`, handler.go:458).
- GitHub approval handler is NOT affected (GitHub's own PR-review permissions are the auth boundary) — no change there.

## Approach

Guard inside each approval handler, before any state mutation (mirror the existing not-found/expired early-return paths, which already answer via `AnswerCallback` without mutating):

1. **Strict allowlist when approvers set**: if `pending.Request.Approvers` is non-empty, require plain string equality of `userID` against an entry. `@handle` entries are destination overrides (`resolveDestChatID`, telegram.go:137-146) and will simply never match a numeric `userID` — document this in the config example comment: per-user auth requires numeric ID entries.
2. **Fallback when approvers empty**: check `userID` against a new allowlist injected at construction — add `WithAllowedIDs(ids []string)` builder on `TelegramHandler` (mirroring `WithStore`/`WithDecisionRecorder`, telegram.go:180-194); wire it from `cfg.Adapters.Telegram.AllowedIDs` (int64 → decimal strings) at both construction sites (`cmd/pilot/main.go` ~:2895-2958 chat mode, ~:3627 gateway). Empty fallback list = unrestricted (matches existing `allowed_ids` semantics, handler_test.go "no allowlist is unrestricted").
3. **Refusal path**: `h.client.AnswerCallback(ctx, callbackID, "⛔ Not an approver")` + `h.log.Warn` with `request_id`/`user` attrs (follow logging pattern at telegram.go:496-500). Return `true` (callback consumed). NO call to `RecordDecision`, no pending-map mutation.
4. **Slack**: same two-step guard in `HandleInteraction` using `pending.Request.Approvers`; fallback allowlist via equivalent `WithAllowedIDs` setter wired from Slack adapter config. Refusal via the interaction response (ephemeral "not an approver"), warn log, no mutation.

## Acceptance

- [ ] Telegram: approver tap accepted; non-approver tap → visible refusal via `AnswerCallback`, warn log, decision NOT recorded, pending request untouched (a subsequent authorized tap still works).
- [ ] Telegram: `Approvers` empty + fallback allowlist set → member accepted, non-member refused; both lists empty → current behavior (unrestricted).
- [ ] Slack: same three cases against `HandleInteraction`.
- [ ] Tests in `internal/approval/telegram_test.go` / `slack_test.go` following existing conventions (`mockTelegramClient`, `getAnsweredCallbacks()`, `newCapturingLogger()` for the warn-log assertion); include a Rehydrate + approver-gated tap case.
- [ ] `configs/pilot.example.yaml` approvers comment updated: numeric IDs required for per-user authorization.
- [ ] PR body includes `Fixes #5149`.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5153
- #5149 (external report; agreed semantics posted in maintainer comment 2026-08-24)
- Research: `Request.Approvers` propagation `manager.go:266-267`; `ApprovalCallbackHandler` seam `internal/adapters/telegram/handler.go:48-51`
