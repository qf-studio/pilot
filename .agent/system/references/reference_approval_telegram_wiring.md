---
name: Pre-merge approval via Telegram (config wiring)
description: Pre-merge approvals flow via Telegram inline-button taps. Wired by enabling approval.enabled + approval.pre_merge.enabled + adding the user as approver. Was disabled by default; activated 2026-05-05 to resolve stage env deadlock.
type: reference
originSessionId: a45a0b36-53c9-4751-93ff-3cd0d8b24386
---
Stage env has `require_approval: true` (hardened post-cascade-2). Pre-merge approvals flow via Telegram, NOT GitHub-native review.

## Config wiring (in `~/.pilot/config.yaml`)

Three blocks must align:
1. **`orchestrator.autopilot.approval_source: telegram`** (line ~148) — points autopilot at Telegram
2. **`adapters.telegram.allowed_ids: [<user-ids>]`** (line ~46) — Telegram user IDs that the bot accepts commands from
3. **Top-level `approval:` block** (line ~472):
   ```yaml
   approval:
       enabled: true
       pre_merge:
           enabled: true
           approvers:
               - "283716179"      # string, Telegram user ID
           timeout: 24h0m0s
           default_action: rejected
           require_all: false
   ```

## How it behaves
- Autopilot reaches merge stage with CI green → calls `requestApproval`
- `approval.enabled: true` + `pre_merge.enabled: true` → Telegram handler fires
- User receives bot message: "PR #N: approve?" with inline Approve/Reject buttons
- Tap → autopilot merges (or rejects with comment)
- Timeout 24h → defaults to `rejected` (fail-closed safe)

## Failure modes (covered by #2598, v2.117.1)
- `approval.enabled: false` while env requires approval → `StageFailed` with "approval-misconfig" + idempotent PR comment. No retry loop.

## Why both `approval_source: telegram` AND `approval.pre_merge.enabled` are needed
`approval_source` says *where* to send approval requests. `approval.pre_merge.enabled` says *whether* to run the pre_merge stage at all. Both must be set; either alone deadlocks.

## Sister files
- `internal/approval/telegram.go` — handler with inline-button keyboard
- `internal/autopilot/auto_merger.go:178-197` — `requestApproval` caller (returns the typed error from #2598 fix when misconfigured)
- Marker: `before-compact-2026-05-04-cascade-2-resolved-smoke-pending.md`

## Cross-refs
- `feedback_verify_pr_state_not_labels.md`
- `pattern_squash_merge_mergedat_null.md`
- `incident_oauth_cascade_series.md` (why stage env was hardened in the first place)
