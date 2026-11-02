# TASK-603: approval — RecordDecision cleans the waiter on sql.ErrNoRows, GH-4777 comment restated

**Status**: ✅ Merged 2026-10-27 12:08Z — #5888 → PR#5894. **Post-merge review not posted.**
**Spec refs**: the review comment on the PR (APPROVE-WITH-NOTES), issue body as filed (Context / Scope / Acceptance / Out of scope; one targeted `-run` evidence bullet, SOP Rule 5d).
**Origin**: 10-27 review batch of the four unreviewed 10-26 merges (PR#5870/#5871/#5872/#5875) — own read + independent second read + `go test -race` + mutation pins.

## Why
- `Manager.RecordDecision` early-returns on every writer error except AlreadyDecided, before the pending-map delete + CancelFn; the gateway treats `sql.ErrNoRows` (unlinked request) as resolve-anyway → waiter lives to the stage timeout, card stays armed. Regression vs GH-4777.
- Doc comment above `RecordDecision` contradicts the code.

## Legs — pilot (daemon)

### LEG 1 — repo: qf-studio/pilot — issue pilot#5888
Body as filed. Path pre-flight passed on every backticked path.

## Refs
- https://github.com/qf-studio/pilot/issues/5888
