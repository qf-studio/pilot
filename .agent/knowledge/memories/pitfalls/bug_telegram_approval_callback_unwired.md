---
name: Telegram approval button taps were never dispatched
description: Day-1 wiring bug — internal/adapters/telegram/handler.go:handleCallback has no case for "approve:" / "reject:" prefixes, so all pre-merge approval button taps fall through to default and are silently dropped. tgApprovalHandler.HandleCallback is a separate object that was never wired into the poller's dispatch.
type: project
originSessionId: d0fafba1-9001-4881-8900-e3d17b306b13
---
**Status as of 2026-05-06:** ✅ FULLY RESOLVED + VERIFIED LIVE + LEGACY PATH REMOVED.

End-to-end smoke-tests confirmed across PRs #2720, #2723, #2725 (GH-2716/2717/2718):
- TG button tap → `RecordDecision` → in-memory PRState ✅
- `executions.approval_request_id` + `approval_decision` + `approval_decision_by` populated ✅ (TASK-33 / v2.128.2)
- Auto-merge fires ✅
- Auto-release fired for the first PR (v2.128.3) ✅

Releases shipped during the cleanup arc:
- v2.128.2 (TASK-33) — executions persistence
- v2.128.3 (TASK-34) — releaser-from-resolvedRelease (audit Gap 1)
- v2.128.4 (TASK-35) — non-blocking handlePostMergeCI (audit Gap 2 — 3-PR burst starvation)
- v2.129.0 (TASK-36 + TASK-37 / GH-2727 + GH-2731) — legacy `RequestApproval` removed; observability metrics added (audit Gap 5)

v2.129.0 was the first clean **multi-PR auto-release sequence** since the trifecta started — proves Gap 2 fix is working in production.

**Original v2.121.0 → v2.126.0 fix series:**
- v2.121.0 (commit 8c4ab2b2, PR #2653) — tactical dispatch wiring: `handleCallback` routes `approve:`/`reject:` to injected `*approval.TelegramHandler.HandleCallback`
- v2.122.0 (commit 24f54ea0) — `approval_pending` SQLite table + store
- v2.123.0 (commit 69d98489) — store wiring + rehydrate at startup
- v2.124.0 (commit 533de86b) — PR-state decision fields + writer
- v2.125.0 (commit b258da90, PR #2682) — Manager async API: `SubmitApprovalRequest` + `RecordDecision` + `Config.AsyncDispatch`
- v2.126.0 (commit 76117a2, PR #2688) — controller refactor: `handleAwaitApproval` non-blocking, two-path tick handler

Re-enabled in `~/.pilot/config.yaml` lines 185, 481 on 2026-05-05.

**Smoke-test (PR #2688)**: live-tested 2026-05-05 with daemon-restart edge case. Tap was lost across restart because the controller was still synchronous at the time (TASK-31 hadn't shipped yet); manual `gh pr merge 2688` was the workaround. With v2.126.0 controller now landed, the next restart-during-approval scenario will be resilient (tap → `RecordDecision` → state writer → next tick advances stage).

**Known related bug:** `bug_approval_pending_zero_timestamps.md` (GH-2690) — orphan rows with blank timestamps. Less load-bearing post-v2.126.0 (state writer is the source of truth) but still worth fixing.

**Original report:** Critical bug. Telegram pre-merge approval button taps had never functionally worked. Discovered via nav-research after PR #2640 froze the autopilot loop for 1h12min. Manual `gh pr merge` was the only path to advance.

**Mechanism:**
- Approval message is sent via `tgApprovalHandler` (`internal/approval/telegram.go`) with `callback_data: "approve:<request_id>"` / `"reject:<request_id>"`.
- Telegram poller in `internal/adapters/telegram/handler.go:372-421` (`handleCallback` switch) only handles: `execute:*`, `cancel:*`, `switch_*`, `voice_check_status`.
- Approval prefixes hit the default branch and are silently dropped after `AnswerCallback` (which gives the user a fake-success loading spinner).
- The blocking `<-responseCh` in `internal/approval/manager.go:205-213` never receives → 24h timeout → `DefaultAction: rejected`.

**Combined with two other bugs:**
- Pending approval state is in-memory only (`approval/telegram.go:42`) — lost on restart. Old messages return "Request expired" even after the dispatch fix.
- `processAllPRs` (`internal/autopilot/controller.go:2059-2114`) is a sequential for-loop — `handleAwaitApproval`'s 24h block starves every other PR until timeout. PRs in earlier stages cannot advance.

**Fix path (Option C from research):**
- Add `case strings.HasPrefix(data, "approve:") || strings.HasPrefix(data, "reject:")` in `handler.go:handleCallback` and route to `tgApprovalHandler.HandleCallback`. Requires passing `*approval.TelegramHandler` into `tgHandler` at construction.
- Persist pending approval requests in SQLite (request_id → pr_number, stage, message_id) and rehydrate the `pending` map on restart so old messages still work.
- Move `handleAwaitApproval` to per-PR goroutine OR make approval truly async (callback drives stage transition, not blocking receive).

**How this evaded notice:**
- Approval flow had been treated as "wired but broken because chat_id" through 2026-05-03/04 cascades. Manual `gh pr merge` was the established workaround. No one tested click-tap-merge end-to-end.
- The TG message is sent OK (`AnswerCallback` works), so partial functionality masks the dispatch gap.

**Cross-refs:**
- `pattern_approval_chat_id_bootstrap.md` — earlier theory was that chat_id misalignment was the root cause; that was a real bug too but not the only one.
- `reference_approval_telegram_wiring.md` — promised "tap → merge" UX; actually never worked.
- TASK-26 (#2638): deterministic handler selection — independent of this.
- PRs that exhibited the symptom: #2631 (smoke), #2635 (chat_id fix), #2640 (deterministic-selection fix), #2644, #2646.

**Outstanding follow-ups (to file separately):**
- **M** — Persist pending approvals in SQLite (`approval_pending(request_id, pr_number, stage, message_id, chat_id, expires_at, created_at)`); rehydrate `tgApprovalHandler.pending` on restart so taps on pre-restart messages don't return "Request expired or already processed".
- **L** — Replace blocking `<-responseCh` in `approval/manager.go:205-213` with callback-driven stage advance — handler writes decision to PR state on tap, next controller tick picks up; eliminates 24h queue starvation when a single PR stalls (current `processAllPRs` is sequential).
