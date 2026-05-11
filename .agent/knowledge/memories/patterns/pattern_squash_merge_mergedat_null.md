---
name: Squash-merge can leave PR with mergedAt=null while commit is on main
description: GitHub's squash-merge returns the squash commit's SHA on main, but the PR record's mergedAt field can stay null — gh pr view will lie to you. Verify via git log origin/main, not gh API alone.
type: feedback
originSessionId: a45a0b36-53c9-4751-93ff-3cd0d8b24386
---
Autopilot squash-merges via stage env can show `mergedAt: null` on `gh pr view --json mergedAt` while the squash commit is on `main`. Don't trust the gh API for merge state.

**Why:** GitHub returns the squash commit's SHA on `main`, not the PR's HEAD SHA, so the PR record never gets `merged_at` populated. Surfaced during cascade #2 (2026-05-04) — PR #2572 was squash-merged into `main` (512 LoC OAuth contamination landed) but the PR card still appeared open. Wasted ~30 minutes hunting "where did this commit come from" before checking `git log origin/main`.

**How to apply:**
- When verifying merge state, check `git log origin/main --grep "<PR-number>" --oneline` or look for the commit's first-line subject in `git log origin/main`.
- Do NOT rely on `gh pr view <N> --json mergedAt` alone. Cross-check with state, mergeCommit, and `git log`.
- For automated checks: `gh pr view N --json state,mergedAt,mergeCommit` — `mergeCommit.oid` is more reliable than `mergedAt` for "is this on main now".
- Cross-references existing `feedback_verify_pr_state_not_labels.md` (label-vs-state) and `incident_oauth_cascade_series.md` (where this bit us).
