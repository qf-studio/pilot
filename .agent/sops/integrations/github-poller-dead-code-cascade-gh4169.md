# GH-4169: in-tree GitHub poller deletion — verified scope reduction

## Problem

Parent GH-4155 (task 2) asked to delete 8 files from `internal/adapters/github/`:
`poller.go`, `cleanup.go`, `merger.go`, `retry.go`, `issue_creator.go`,
`issue_create.go`, `project_source.go`, `spec_validator.go` — on the premise
that a prior sub-issue (main.go flag-off pruning, #4170) had already made them
dead code with no live callers.

## Root cause

#4170's actual scope (`createPollerForRepo` + the `use_sdk_poller` flag-off
branches) only touched the *poller registration* path. It did not touch three
independent, still-live features wired unconditionally in `cmd/pilot/main.go`:

- Stale-label cleanup (`github.NewCleaner`, `main.go:2397`, calls `Start`/`StartupRecover`)
- Sub-issue merge-wait (`github.NewMergeWaiter`, `main.go:2354`, GH-2179)
- Bot `/draft-issue` comms issue creation (`github.NewIssueCreator`, `main.go:1849`)

`retry.go` additionally turned out to be a hard in-package dependency of
`client.go` (`RetryOptions`/`WithRetryVoid` wrap every HTTP call) — invisible
to an external-caller-only grep.

Only `project_source.go` (the in-tree `ProjectBoardSource` read path,
superseded by studio-sdk's equivalent in #4168) and `spec_validator.go`
(`ValidateSpec`, whose sole caller was the dead `handleGitHubIssueWithResult`
function) were genuinely dead.

## What shipped instead

- Deleted `project_source.go` + test, `spec_validator.go` + test (confirmed
  dead via `ToolSearch`-driven external-reference audit).
- Deleted the legacy in-tree issue handler `handleGitHubIssueWithResult`
  (`cmd/pilot/handlers.go`, zero live callers — the SDK path
  `handleGithubIssueEventSDK` replaced it) and its exclusive-use helpers
  (`syncBoardStatus`, `issueAlreadyMerged`, `issueHasOpenPR`,
  `issueHasOpenChildren`, `requestReviewersFromConfig`, `resolveGitHubMemberID`,
  `buildFailureComment`, `noOpErrorMarker`) plus the now-orphaned
  `applySpecGuard`/`spec_guard.go` (moved its still-shared
  `buildSpecIncompleteComment` helper into `spec_guard_sdk.go`).
- Left `poller.go`, `cleanup.go`, `merger.go`, `retry.go`, `issue_creator.go`,
  `issue_create.go` in place — each has a live, unrelated caller in
  `cmd/pilot/main.go` or `client.go` that no sibling GH-4155 sub-issue
  addresses. Deleting them would have required removing working production
  features (stale-label cleanup, sub-issue merge-wait, bot issue creation)
  out of this subtask's scope fence.

## Prevention

Before deleting files flagged "dead" by a parent/decomposed spec, grep for
external callers across the whole repo (not just the files the spec lists as
KEEP) — a prior "pruning" sub-issue closing does not guarantee every file it
implies is dead actually lost 100% of its callers. Check in-package
dependents too (e.g. `client.go` on `retry.go`); an external-callers-only
grep misses those.
