# Hosted repo scaffold vs. git_clean preflight deadlock (GH-4526)

## Problem

On a freshly onboarded hosted-tenant repo, the daemon's first dispatch
repeatedly failed preflight: `preflight check "git_clean" failed: working
directory has 1 uncommitted change(s)`, re-picked 5 times, hit the repick
hard cap, and landed on `pilot-blocked`. Box repos (where `.agent/` is
committed) never hit this — every new hosted tenant repo did, on its very
first issue.

Same incident also surfaced: `gh issue edit: 'pilot-failed' not found` when
the dispatcher tried to label a stalled issue on a repo with no pilot-*
labels pre-created.

## Root Cause

1. The daemon scaffolds Navigator's `.agent/` directory into a project
   shortly after clone (`cmd/pilot/init_project.go`'s
   `createNavigatorStructure`), and this write is untracked.
2. The `git_clean` preflight check (`internal/executor/preflight.go`'s
   `checkGitClean`) used to count *any* `git status --porcelain` output as
   dirty — including the daemon's own untracked scaffold.
3. Net effect: the daemon made every fresh clone "dirty" by its own
   scaffolding, then its own preflight refused to dispatch against a dirty
   tree. Self-inflicted, permanent deadlock on first dispatch.
4. Separately, hosted repos are onboarded with zero pilot-* labels (labels
   are a per-repo GitHub resource — cloning doesn't bring them along), so
   any `gh issue edit --add-label pilot-*` call failed outright.

## Solution

- `checkGitClean` now filters `git status --porcelain -z` output through
  `isExcluded` (`internal/executor/git.go`'s `defaultExcludeDirs` /
  `defaultExcludeGlobs` — the same allowlist `GitOperations.Commit` already
  uses to avoid auto-staging `.agent/`, `.claude/`, lockfiles, etc.) before
  counting changes. A tree that's dirty *only* in excluded paths now passes;
  a single real user file still fails the check.
- `internal/executor/labels.go` adds `EnsureRepoLabels`, which runs
  `gh label create <name> --color <c> --force` (idempotent — updates color
  in place if the label exists) for every `pilot-*` label the daemon can
  ever write. Wired into `cmd/pilot/poller_github.go`'s
  `startGithubSDKPollerForRepo`, once per repo, before the poller starts.

## Prevention

- Any new "scaffold-on-first-touch" write the daemon makes to a tenant repo
  must be added to `defaultExcludeDirs`/`defaultExcludeGlobs` in
  `internal/executor/git.go` — that list is now load-bearing for both the
  commit-staging filter *and* the git_clean preflight, not just the former.
- Any new `pilot-*` label introduced anywhere in the codebase must be added
  to `PilotLabels` in `internal/executor/labels.go`, or it will fail with
  "not found" the first time it's written to a repo that has never had it
  created before.
