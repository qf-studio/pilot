---
name: golangci-action-cache-self-poisoning-sa5011
description: golangci-lint-action@v7 (golangci v2.8.0, go 1.25) analysis cache self-poisons — every GREEN run saves a cache whose NEXT restore yields exactly 6 phantom SA5011s in untouched adapter test files (staticcheck loses t.Fatal's noreturn fact). Deleting caches greens exactly one run, then the freshly-saved cache poisons the next: whack-a-mole. Fixed by skip-cache:true on main (049456d5). Signature = SA5011 in asana/webhook_test.go + azuredevops/poller_test.go + github/client_test.go on a diff that touches none of them.
type: pitfall
created: 2026-08-18
---

# golangci-lint action cache self-poisons: green run saves it, next restore goes red

**What happened (2026-08-18 evening).** Lint went red on a docs-only main
commit with 6 SA5011 "possible nil pointer dereference" errors in three
untouched test files (`internal/adapters/{asana/webhook_test,azuredevops/poller_test,github/client_test}.go`
— all the `x := New…; if x == nil { t.Fatal }; x.field` pattern, which is fine:
staticcheck normally knows `t.Fatal` doesn't return). Autopilot then **closed
four consecutive green PRs** for the phantom failure (#4956/#4958/#4960 on
GH-4953, then #4969/#4971 on GH-4961 and #4973), each spawning a garbage
`autopilot-fix` issue (#4962/#4970/#4972/#4974).

**The loop.** The action's cache key follows go.mod; after the sdk-pin bump
(dd87db34) created a fresh key:

1. cache-miss run → green → **saves** cache at job end
2. next run **restores** it → 6 phantom SA5011s → red
3. delete the cache → next run green (miss) → saves again → goto 2

Deleting caches is therefore a one-run fix only. Verified twice: both
post-delete runs green, both post-save restores red, byte-identical trees.

**Fix.** `skip-cache: true` on the golangci-lint-action step (main `049456d5`).
Costs ~1 min per lint run. The 6 flagged sites need no code change — they are
false positives.

**Recovery for killed PRs** (extends the banked flake recipe): close the
spawned `autopilot-fix` issues → recreate the branch ref from the PR's
`headRefOid` via `gh api repos/…/git/refs` (autopilot deletes branches on
close) → `gh pr reopen` (reopening triggers fresh CI under the merge-ref
workflow, which already carries the fix — no manual rerun needed) → merge on
green. Note two PRs can share one branch/SHA (retry filed a second PR);
recover the canonical GH-prefixed one.

**Diagnosis shortcut next time:** red lint where the failing files are absent
from the diff + green↔red flapping across identical trees ⇒ check
`gh cache list` before reading a single line of Go.

Related: [[poller-labels-in-progress-before-dispatcher-claim-wedge]] (same
evening; the wedge + this flake compounded into the closed-PR cascade).
