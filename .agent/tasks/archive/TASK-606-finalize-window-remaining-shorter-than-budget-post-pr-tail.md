# TASK-606: executor — finalizeCtx grants the window on a live-but-short deadline, and the window covers the post-PR tail

**Status**: ✅ Merged 2026-10-27 13:25Z — #5891 → PR#5896. **Post-merge review not posted.**
**Spec refs**: PR#5875 review comment (APPROVE-WITH-NOTES), TASK-597 (finalization overrun), GH-5346 (finalizeCtx origin), GH-5342 (deadline-exceeded classification).
**Origin**: the 10-27 review batch; the mechanism is proven from code (`finalizeCtx` at `internal/executor/runner.go` ~L348 extends only when `ctx.Err() == DeadlineExceeded`), no incident investigation needed.

## Why
- **Seconds-left tail (rare):** deadline falls inside the next few seconds after evidence → window is a child of the task ctx → three `CreatePR` attempts 300 ms apart die; recovery ref survives, run ends without a PR. Same signature as GH-5861.
- **Post-PR tail (common for complex runs):** the re-derived window is block-scoped to each creator branch; `dispatchWebhook` (~L7372), `syncMainBranch` (~L7449), `recordLearning` (~L7494) run on the outer ctx, dead whenever the deadline blew during the detached 15-min evidence step → notice, sync and learning row lost silently.

## Legs — pilot (daemon)

### LEG 1 — repo: qf-studio/pilot — issue #5891
Scope: remaining < budget grants the window (cancel still never extended; AfterFunc rule) · one window before the creator chain kept in scope for the three tail calls · one Info line when granted on a live-but-short ctx. Three tests named in the body; one targeted `-run` evidence bullet (SOP Rule 5d).

## Out of scope (kept on the PR#5875 notes)
- Upper bound on `acceptance_evidence_budget`.
- Sticky `ctx.Err()` after a blown deadline ignoring a later cancel (heartbeat class).

## Refs
- https://github.com/qf-studio/pilot/issues/5891
- https://github.com/qf-studio/pilot/pull/5875#issuecomment-6440256743
