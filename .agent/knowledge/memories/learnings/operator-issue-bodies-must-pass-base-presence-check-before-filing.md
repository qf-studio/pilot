# Learning: Run the backtick-path gate check on every operator-authored issue body before gh issue create — twice bitten on 2026-09-06

## Summary
Two operator-written issues were parked pilot-needs-human the same day for the SOP Rule 3b class: pilot-console#263 backticked a to-be-created file (release.yml under the workflows dir) and pilot#5336 backticked an SDK path (poller.go under sdk/integrations/github, which lives in the studio-sdk module, not the target repo). The dispatcher's ExtractReferencedPaths treats any backticked span with a slash + extension as a prerequisite on the target's default branch; new files, other-repo files and module-cache paths all hold the task until max cycles, then escalate.

## Context
TASK-494/495 day: 14 issues filed by the operator across 4 repos; the two that used backticks for non-target paths were the two that stalled. Fix = reword to prose, remove pilot-needs-human.

## Details
Rule of thumb: backtick a path ONLY if 'git cat-file -e origin/main:<path>' succeeds in the TARGET repo. Describe new files, other repos, vendored modules and home-dir paths in prose. Line refs (file.go:123) are fine when the file exists.

## Recommended Approach
Before every create/edit: extract backticked spans containing a slash and an extension from the body, strip any :line suffix, and run git cat-file -e origin/main:<path> in the TARGET checkout for each; reword every MISSING span to prose.

## Related
- TASK-494
- TASK-495
- SOP: onboarding/new-project-issue-authoring
- `internal/executor/dependency_detector.go`
- `internal/executor/base_presence.go`

---
**Captured**: 2026-09-06
**Confidence**: 95%
**Concepts**: dispatch, issue-authoring, base-presence, workflow-discipline
