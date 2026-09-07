---
name: ci-fix-size-guard-holds-own-pr-on-one-lint-annotation
description: autopilot's CI-fix size guard keys on the failing PR's size, not the failure's — a single lint annotation on an 880-line Pilot PR gets pilot-needs-human with no fix issue, no comment, no alert; recovery is a manual revision issue with the autopilot-meta footer
type: pitfall
---

# CI-fix size guard holds Pilot's own PR on one lint annotation

**What happened (2026-09-07, pilot#5351 / PR#5356):** run 1 shipped an
880-line PR; CI `test` green, `lint` failed on ONE `unused` helper in a test
file. Autopilot classified it (`class=code signal=real_annotation`), then:

```
13:32:54Z "CI fix size guard fired — failing PR exceeds size floor, refusing to spawn fix issue" production_additions=387 test_additions=463 limit=200
13:32:55Z "escalateAndHold: PR held for human review, branch intact" labels=[pilot-needs-human]
```

No fix issue, no PR comment, no alert. The board showed the issue as `failed`
and the operator's first read was "autopilot got conflicts".

Compounded by the run itself being recorded `no_op` (merge-base against
`origin/main` unresolvable in the worktree → PR guard said `no_changes`
although the PR existed — pilot#5359), so the ledger showed a failed retry
that never ran Claude at all.

## Why it matters
- The guard fires BEFORE `max_ci_fix_iterations` / platform breaker; the
  cheapest fix (one-line delete) becomes a human hold.
- `pilot-needs-human` on a PR is invisible unless you read labels.

## Recovery
- Do NOT close+refile (discards a green `test` run and a mostly-correct
  diff). File a pilot-labeled revision issue against the existing branch,
  ending with `<!-- autopilot-meta branch:pilot/GH-N pr:M iteration:K -->`
  (decision `pilot-never-reads-gh-comments-by-design`, precedent #5261).
- Fix filed: pilot#5360 (exempt single-annotation lint failures; comment +
  alert on every size-guard hold).
