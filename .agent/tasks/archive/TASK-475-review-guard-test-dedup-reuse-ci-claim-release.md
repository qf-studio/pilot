# fix(autopilot): seeded-claim test for the review guard; dedup reuse-don't-replace for clarification owners; CI-path claim release

**Status**: ✅ Merged 2026-08-12 12:55Z → [pilot#4860](https://github.com/qf-studio/pilot/issues/4860) → **PR#4862** — **POST-MERGE REVIEW PENDING.** In tag v2.259.1 (on the box's disk); NOT in the running process until next restart (process started 08-12 11:02Z on v2.259.0-14).
**Created**: 2026-08-12
**Assignee**: Pilot

## Context

PR#4858 (GH-4856) post-merge review (verdict on the PR) — the ship stands, three gaps remain:

- **D1 (major, test gap)**: the `(0, nil)` half of the review guard (`if err != nil || issueNum <= 0`, internal/autopilot/controller.go:3145) is unreachable by the suite — neutering it to `if err != nil` survives every test. Real producers of `(0, nil)` from `CreateReviewIssue` (feedback_loop.go:584-587): crash between `CreatePilotIssue` success and `RecordSpawnedFixIssue`; a concurrent claim in flight; the `lookupErr` fail-open returning `existing == 0`. The untested branch's failure mode is the original GH-4856 bug: PR closed, branch deleted, review round discarded.
- **D2 (minor, new false-positive window)**: owner_death.go:51 now classifies open + `pilot-needs-clarification` as dead, and BOTH dedup re-checks consume that as a REPLACE signal (`CreateFailureIssue` feedback_loop.go:329, `CreateReviewIssue` feedback_loop.go:611). But the label is documented resumable (notifier.go:151, epic.go:777): a transient `ClosePullRequest` failure or crash-before-close re-enters the create path, dedup classifies the declined-but-open owner dead, mints a replacement, moves the claim, fires a "replaced" alert — while a human is mid-clarification. Human removes the label → both issues dispatch → duplicate executors; review issues share `branch:` meta → branch clobbering. The replaced owner is left open (zombie), and every re-entry fires another alert. NOTE: the `notifyExternalClose` fallback's use of the widened classification is a strict improvement — do NOT revert it there.
- **N1 (parity)**: `CreateFailureIssue`'s create-error path (feedback_loop.go:~385) still does not release its claim — a transient create error on the CI path poisons `fix:pr<N>:ci_failure_pre_merge` forever (every re-drive re-escalates per the GH-4459 comment). The review path self-heals since PR#4858; mirror it.

## Implementation

1. **Seeded-claim regression test**: seed `ClaimSpawnedFix` WITHOUT `RecordSpawnedFixIssue`, drive `handleReviewRequested`, assert PR open + branch intact + escalate-and-hold (`pilot-needs-human`, alert). Must kill the `if err != nil`-only mutation of controller.go:3145.
2. **Reuse-don't-replace in the dedup re-checks**: in `CreateFailureIssue` (feedback_loop.go:329) and `CreateReviewIssue` (:611), treat an open + `pilot-needs-clarification` owner as REUSE (return the existing issue), not replace — or, if replacement is kept, comment-and-close the zombie and dedup the "replaced" alert. Leave `classifyOwnerHealth` itself and the `notifyExternalClose` fallback behavior unchanged (GH-4852 acceptance scenarios must keep passing).
3. **Release the CI-path claim on create failure**: add the `ReleaseSpawnedFix` treatment to `CreateFailureIssue`'s create-error path, mirroring the review path (feedback_loop.go:679-680), gated on claim ownership the same way.

Optional nits if in the area: `fireOwnerDeathAlert("rearmed")` fires before the `issueAlreadyClosed` gate (controller.go:7614 vs :7627) — false alert for closed sources; decline reaction + external-close fallback can double-fire the owner-death alert for the same source (no dedup).

## Acceptance

- Mutation `if err != nil || issueNum <= 0` → `if err != nil` at controller.go:3145 fails at least one test.
- Test: dedup-hit where the existing owner is open + `pilot-needs-clarification` → reused (no new issue, claim row unchanged, no "replaced" alert) — or, if replace-with-cleanup chosen, the zombie is closed with a comment and only one alert fires.
- Test: transient `CreatePilotIssue` error on the CI path → claim released; next tick creates cleanly (no permanent escalate loop).
- GH-4852 / GH-4856 / GH-4841 / GH-4826 / owner-death suites pass unchanged.

## Refs

- PR#4858 post-merge review verdict (comment on the PR) — D1 major, D2 minor, N1 parity.
- GH-4459 guard precedent · GH-4842 · PR#4854/PR#4858 lineage (TASK-468/TASK-473).
