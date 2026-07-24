# Hosted-tenant self-deadlock: scaffold vs git_clean preflight, missing pilot-* labels

## Problem

On the first hosted tenant instance (S2 canary, `PILOT_HOSTED=1`), a freshly
cloned project repo deadlocked itself on its very first dispatch:

```
preflight check "git_clean" failed: working directory has 1 uncommitted change(s)
```

...followed by 5 re-picks, the repick hard cap, and `pilot-blocked`. The
stalled-issue surfacing side channel then *also* failed:

```
gh issue edit: 'pilot-failed' not found
```

## Root Cause

1. **Scaffold vs preflight.** `NavigatorInitializer.Initialize`
   (`internal/executor/navigator.go`) writes an untracked `.agent/`
   directory into any repo that doesn't already have one, a couple minutes
   after the daemon starts against a freshly cloned repo. `checkGitClean`
   (`internal/executor/preflight.go`) treated any `git status --porcelain`
   output as dirty — including that untracked scaffold. Box repos never hit
   this because `.agent/` has been committed there for years; every
   hosted tenant repo starts from a clean clone with no `.agent/` at all, so
   the scaffold write is the *first* thing that ever touches the tree.
2. **Missing label set.** `gh issue edit --add-label X` / `--remove-label X`
   hard-fails if label `X` doesn't exist on the repo yet. Box repos have
   accreted the full `pilot-*` label set through years of manual use; a
   freshly onboarded hosted repo has none of them, so the very first
   `pilot-blocked`/`pilot-failed` label edit (stalled-issue surfacing, title
   rejection escalation, etc.) fails outright.

## Solution

- `checkGitClean` now ignores untracked (`??`) `git status --porcelain`
  lines under `.agent/` — see `isScaffoldNoise` in
  `internal/executor/preflight.go`. Tracked/modified files under `.agent/`
  (the box-repo case) still count as dirty; only the *scaffold's own*
  untracked write is excluded.
- `ghEditLabels` (`internal/executor/title_rejection.go`) now calls
  `ensureGitHubLabels` (`internal/executor/gh_labels.go`) before every edit,
  which runs `gh label create <name> --force` for each label the edit
  touches. `--force` makes this idempotent (creates if missing, updates
  color/description if present) so no separate existence check is needed,
  and it's best-effort — a labels-API hiccup logs a warning but never blocks
  the caller's actual edit/comment attempt.

## Prevention

Any future "thing the daemon writes into the repo on its own" (not just
`.agent/`) needs the same two-sided check when onboarding a brand-new
hosted tenant: (1) does it trip a preflight/dirty-tree check that only
box repos are exempt from by virtue of having already committed the
artifact, and (2) does it depend on repo state (labels, project boards,
webhooks, ...) that box repos accreted manually but a fresh `gh repo
clone` starts with zero of.
