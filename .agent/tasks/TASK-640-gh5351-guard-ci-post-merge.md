# TASK-640: GH-5351 delivery-mismatch guard skips post-merge CI fixes and already-merged originals

**Status**: ✅ **A1 MERGED 11-05 21:35Z → [PR#6128](https://github.com/qf-studio/pilot/pull/6128) (12 min; pre-PR APPROVE-WITH-NOTES). Unreleased → 11-06 16:00 Berlin tick.** Was: 🚀 A1 DISPATCHED 2026-11-05 → [pilot#6123](https://github.com/qf-studio/pilot/issues/6123) (`pilot` + `no-decompose`). Nothing held.
**Created**: 2026-11-05
**Assignee**: Pilot

---

## Triage

On 2026-11-05 17:24Z the GH-5351 guard posted "Delivery mismatch detected (GH-5351)" on issue #6100. The original PR #6104 was already merged and the issue was already closed `pilot-done`. The fix issue #6107 was a `ci_post_merge` fix for a transient `sync-docs.yml` push failure. Its PR #6114 changed only the workflow file. A post-merge CI fix shares no files with its original by design, so the zero-overlap test cannot tell a dropped delivery from a normal post-merge fix. The comment also said "Leaving this issue open", which was false for a closed issue, and it fired the `mismatch_no_supersede` owner-death alert.

Mechanics (verified on main):

- `verifyFixPRDeliversSourceScope` in `internal/autopilot/owner_death.go` runs when a fix PR merges. It reads the fix issue body, and `fixIssueFailureTypeRe` already extracts the "Failure Type" line. `isReviewRevisionFixIssue` uses it for `review_requested`. No helper exists for `ci_post_merge` (`FailureCIPostMerge` in `internal/autopilot/feedback_loop.go`).
- The original PR number comes from `originScope`. The original PR state is never read. `GetPullRequest` exists on the GitHub client and returns `Merged`.
- The CI-fix mismatch branch (the code after the `isReviewRevision` branch) posts the comment and fires the alert. The review-revision branch (GH-5362) is separate and stays as it is.

Real GH-5351 case to keep: the original PR is open or closed-unmerged, the fix PR merges, and the file sets are disjoint. The fix then replaced the original's work instead of continuing it.

## Legs

### LEG A1 — repo: qf-studio/pilot — title: fix(autopilot): GH-5351 delivery-mismatch guard skips post-merge CI fixes and already-merged originals

## Context

`verifyFixPRDeliversSourceScope` in `internal/autopilot/owner_death.go` posts the GH-5351 "Delivery mismatch detected" comment and fires the `mismatch_no_supersede` alert whenever a merged CI-fix PR shares no changed files with the original PR. It has two false-alarm inputs. (1) The fix issue is a `ci_post_merge` fix: the original already merged, and the fix PR touches CI config or the workflow, never the original's files. (2) The original PR itself is already merged: its delivery is proven, so a disjoint fix PR cannot mean dropped changes. Seen on 11-05: original PR merged, fix issue of failure type `ci_post_merge`, fix PR touched only `.github/workflows/sync-docs.yml`; the guard commented on an issue that was already closed `pilot-done`.

The failure type reaches the guard through the fix issue body: the line `- **Failure Type**: <type>` parsed by `fixIssueFailureTypeRe` (used today by `isReviewRevisionFixIssue`). The constant is `FailureCIPostMerge` ("ci_post_merge") in `internal/autopilot/feedback_loop.go`. The original PR's merged state is not read anywhere in the guard; `GetPullRequest` on the GitHub client returns a `Merged` field. The existing test helper is `newScopeMismatchGHServer` in `internal/autopilot/gh5348_scope_mismatch_test.go`; its default reply for `GET /pulls/<n>` is `{}`, which decodes as not merged, so existing cases keep their meaning.

probe: present qf-studio/pilot func[[:space:]]+\(c \*Controller\) verifyFixPRDeliversSourceScope
probe: present qf-studio/pilot ^var fixIssueFailureTypeRe
probe: absent qf-studio/pilot ^func isCIPostMergeFixIssue

## Scope

- `internal/autopilot/owner_death.go`, CI-fix mismatch branch only (the code after the `isReviewRevision` branch, where the GH-5351 comment is built). Before the comment and the alert, skip when either condition holds:
  - the fix issue failure type is `ci_post_merge`, via a new helper next to `isReviewRevisionFixIssue` that reuses `fixIssueFailureTypeRe` (new helper name: `isCIPostMergeFixIssue`, takes the body, returns bool); this check is cheap and runs first, with no API call;
  - the original PR is merged: call `GetPullRequest` for the origin PR number from `originScope` and read `Merged`. If the call fails, log a warning and keep the old behaviour (guard fires). Never fail the merge path.
- On skip: log at info level with the reason (`ci_post_merge` or `origin_merged`), do not post the comment, do not fire the owner-death alert, do not touch labels, call `cleanupOriginScope`, return. The source issue is not marked superseded: overlap was not proven, and the source is already delivered or not this PR's concern.
- Do not change the review-revision (GH-5362) branch, the overlap branch, the fail-open early returns, or the idempotence check for `pilot-superseded`.
- Tests, new file, plain text: internal/autopilot/gh5351_post_merge_skip_test.go. Add a `prMerged map[int]bool` field to `scopeMismatchGHServer` in `internal/autopilot/gh5348_scope_mismatch_test.go` and serve `GET /repos/owner/repo/pulls/<n>` from it (merged flag only); keep the default reply unchanged for numbers not in the map. Table test `TestVerifyFixPRDeliversSourceScope_PostMergeSkip` with cases (all with zero file overlap):
  1. fix issue body has `- **Failure Type**: ci_post_merge`, origin open → no comment, no alert, no labels (the 11-05 shape);
  2. failure type `ci_failure`, origin PR merged → no comment, no alert;
  3. failure type `ci_failure`, origin PR open (not merged) → GH-5351 comment still posted, source stays open (the real case);
  4. failure type `ci_failure`, origin PR closed unmerged → comment still posted;
  5. failure type `ci_failure`, `GET /pulls/<origin>` returns 500 → comment still posted (fail toward the guard);
  6. failure type `review_requested`, origin PR merged, zero overlap → the GH-5362 needs-human escalation still happens (guard unchanged on that path).
  Plus a unit test `TestIsCIPostMergeFixIssue` for the helper: `ci_post_merge`, `ci_failure`, `review_requested`, empty body, and a body where an attacker-controlled later line says `ci_post_merge` (the first match is the genuine one, so a `ci_failure` first line stays false).
- Run the existing mismatch tests unchanged: `TestVerifyFixPRDeliversSourceScope_ClosedSource` and the GH-5348 table must pass without edits to their expectations.

## Acceptance

- [ ] `go test -race ./internal/autopilot/ -run TestVerifyFixPRDeliversSourceScope` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/autopilot/ -run TestIsCIPostMergeFixIssue` passes; paste the output into the PR body.
- [ ] remove the `ci_post_merge` skip -> TestVerifyFixPRDeliversSourceScope_PostMergeSkip fails
- [ ] remove the origin-merged skip -> TestVerifyFixPRDeliversSourceScope_PostMergeSkip fails
- [ ] make the skip unconditional (always skip) -> TestVerifyFixPRDeliversSourceScope_PostMergeSkip fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The review-revision GH-5362 branch, the label mutations, the `originScope` recording at spawn time, the wording of the comment, other post-merge fix behaviour (see TASK-508), and the fix-issue spawner itself. No change to which issues get a `ci_post_merge` fix.

## Refs

- Origin: 11-05 17:24Z false alarm on #6100 (original PR #6104, fix issue #6107, fix PR #6114) · GH-5351 · GH-5362 · Navigator TASK-640 in this repo

## Refs

- A1 → (pending)

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6123
