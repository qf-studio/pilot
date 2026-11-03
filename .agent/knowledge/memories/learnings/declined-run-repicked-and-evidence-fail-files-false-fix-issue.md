# Learning: A DECLINED: run is re-picked as a retry and its reason is lost; a FAIL in pasted evidence files a fix issue that green CI already refutes (GH-6031, 2026-11-03)

## Summary
GH-6031: the runner's evidence paste of go test -race ./internal/executor/ ended FAIL 585s from a loaded run (a second -race pass ran in parallel); the head-20/tail-80 trim dropped the --- FAIL line so no artifact names the test; CI was green; the post-merge reviewer blocked by the TASK-614 contract and filed an autopilot-fix issue; Pilot reproduced ok 552s on main and returned DECLINED: with a full reason; the dispatcher counted declined as no-progress, re-picked it, run 2 died no_op mid-test, the cap of 2 stalled it with a generic comment. The decline reason lives only in daemon.log.

## Context
Founder asked why GH-6031 was pilot-blocked. Box daemon.log (grep GH-6031, GH-6028), recording TG-1793717982550 stream.jsonl, PR#6030 body, navigator-research on main 12c753c6a. Laptop confirmation: ok github.com/qf-studio/pilot/internal/executor 511.456s.

## Details
Chain: acceptance_evidence_run.go runs the paste command itself and pastes a non-zero exit as a legitimate result; trimOutputForEvidence (acceptance_evidence.go near 329) keeps head 20 + tail 80, so --- FAIL:/panic:/DATA RACE lines in the middle vanish while evidenceHeading flips to FAILS (GH-5568). post_merge_reviewer.go near 393: a failing test in a changed package is blocking; the reviewer is read-only and only sees 'CI status at merge time' as a context line; the downgrade guards near 560-590 have no evidence-vs-CI case. feedback_loop CreatePostMergeReviewIssue files blocking defects as autopilot-fix. dispatcher.go isNoProgressOutcome near 1787 returns true for declined, so the next poll re-picks; stallTaskAfterNoProgressCap fires at 2; surfaceStalledIssue posts only the generic cap text; finishDeclined (runner.go near 2314) only logs and reportProgress; only escalateRefusal posts a reason and only for refusals. Reading a decline reason requires grep 'Task declined by executor' on the box log.

## Recommended Approach
When a review-minor or autopilot-fix issue goes pilot-blocked after a declined run, grep the box log for 'Task declined by executor' before reading the issue comment: the comment is generic, the log has the judgement. Verify with a full go test -race on main before re-arming. Fix tracked in TASK-634: A1 declined stalls on the first run and the comment quotes the reason; B1 evidence FAIL with green CI downgrades to minor; C1 the trim keeps failure lines and the runner logs failing test names.

## Related
- TASK-634
- TASK-633
- TASK-620
- TASK-614
- `internal/executor/dispatcher.go`
- `internal/executor/runner.go`
- `internal/executor/post_merge_reviewer.go`
- `internal/executor/acceptance_evidence.go`
- `internal/executor/acceptance_evidence_run.go`
- `internal/autopilot/feedback_loop.go`

---
**Captured**: 2026-11-03
**Confidence**: 95%
**Concepts**: autopilot, review, autopilot-fix, dispatch
