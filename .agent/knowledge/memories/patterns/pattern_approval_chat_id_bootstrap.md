---
name: Approval-channel bootstrap deadlock
description: When approval Telegram chat_id is unreachable, both the smoke PR and the fix PR (route to approver DM) get stuck at awaiting_approval. The fix can't ship through the broken channel it fixes. Resolved 2026-05-05 via temp chat_id swap + manual gh pr merge.
type: project
originSessionId: d0fafba1-9001-4881-8900-e3d17b306b13
---
2026-05-05 incident. Pre-merge approval was wired to global `adapters.telegram.chat_id: "-4502060100"` (group), but bot returned `chat not found (400)` on every send. Same bot DMs `283716179` reliably for daily briefs (different config path: `briefs.daily_brief.channels[].channel`).

**Why:** the approval handler is constructed with `cfg.Adapters.Telegram.ChatID` at startup (`cmd/pilot/main.go:423,1304`), conflating "where the bot lives" with "who must approve." Per-PR routing to `req.Approvers[0]` was never implemented (PR #2635 fixes this).

**Bootstrap deadlock pattern:** the fix PR (#2635) itself got stuck in pre-merge approval with the same chat-not-found error, because the broken channel is the only path to merge it. Same as `pattern_hot_upgrade_bootstrap.md` but for approval routing.

**How to apply:**
- If `autopilot_pr_state.error LIKE 'approval request failed%chat not found%'` for the PR that fixes the approval channel → break out of the loop with `gh pr merge --squash`. `controller.checkExternalMergeOrClose` (`controller.go:2121`) handles externally-merged PRs gracefully (adds `pilot-done`, closes issue, advances release pipeline). Don't bother with config rollback gymnastics.
- Useful intermediate unblock: temporarily set `adapters.telegram.chat_id` to the approver's user ID (e.g. `283716179`) to receive the message in DM. After daemon restart this works because the bot already has a live DM with that user (briefs prove it).
- After restart, the in-memory `prFailures` map is hydrated from `autopilot_pr_failures` — clearing the DB rows BEFORE restart (or restarting again after the DB clear) is required to release the per-PR circuit breaker (`controller.go:1597-1615`). Auto-reset only kicks in after `FailureResetTimeout` (default 30min since `LastFailureTime`).

**Sister facts:**
- `removePR()` does NOT delete the row from `autopilot_pr_state` — only the in-memory map. Stale `stage='awaiting_approval'` rows on closed/merged PRs are cosmetic, not functional.
- Global `autopilot_metadata.consecutive_failures` is a leftover key with no current readers (production code only calls `SaveMetadata`/`GetMetadata` for nothing as of v2.118.x). Ignore values there.

**Refs:**
- Failing PRs: #2631 (smoke), #2635 (the fix), both stuck 3/3 retries.
- Fix lands in v2.118.2 (released after manual merge of #2635 on 2026-05-05).
- Cross-ref: `reference_approval_telegram_wiring.md`, `pattern_hot_upgrade_bootstrap.md`.
