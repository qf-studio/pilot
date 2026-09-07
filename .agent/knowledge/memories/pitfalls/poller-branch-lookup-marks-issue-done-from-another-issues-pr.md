---
name: poller-branch-lookup-marks-issue-done-from-another-issues-pr
description: studio-sdk poller hasMergedWork branch fallback stamps pilot-done on issue N whenever ANY merged PR has head branch pilot/GH-N — a CI-fix PR for a different issue that reused the branch makes N permanently unpickable; recovery = close + refile under a new number
type: pitfall
---

# Poller branch lookup marks issue done from another issue's merged PR

**What happened (2026-09-07, pilot-console #275):** #275 → PR#276 failed on a
CI flake → CI-fix continuation filed #277 and PR#278 **on the same head branch
`pilot/GH-275`** → PR#278 merged (flake only; none of #275's code). Every
re-arm of #275 (reopen + `pilot` label, `pilot-done` stripped at 10:08 and
10:31Z) was reverted within one poll cycle (10:08:42, 10:35:42Z):

```
msg="Merged PR found via branch lookup" component=github-sdk-poller issue=275 branch=pilot/GH-275
msg="Issue already has merged PRs, marking as done" issue=275
```

Source: studio-sdk `sdk/integrations/github/poller.go` `hasMergedWork` —
direct search (`SearchMergedPRsForIssueOnBase`) correctly finds nothing, then
the fallback `FindMergedPRByBranchOnBase(pilot/GH-<n>)` accepts any merged PR
with that head branch without checking the PR references issue n.

## Why it matters
- The issue number is **permanently unpickable**: no label surgery can revive
  it while the merged PR exists. Operators burn cycles stripping `pilot-done`
  and watching it come back.
- Compounds the #5348 class (CI-fix continuation rebuilding the branch from
  main): the false-success issue cannot even be re-armed afterwards.

## Recovery / rule
- **Close + refile under a new number** (fresh `pilot/GH-<new>` branch). Done:
  #275 → #280. Do not fight the label.
- Fix filed upstream: studio-sdk#138 (require `GH-<n>:` title prefix or a
  `#<n>` reference in the found PR before marking done). Pilot bumps the sdk
  pin after release.
- Related: `handlemerged-shadowed-dead-by-external-merge-detector`,
  pilot#5348 / #5351 (CI-fix continuation branch handling).
