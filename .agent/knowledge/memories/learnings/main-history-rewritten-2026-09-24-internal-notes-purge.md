# Learning: main history and tag v2.276.3 were rewritten on 2026-09-24 to purge internal notes; PR #5458 deleted by GitHub Support

## Summary
On 2026-09-24 the history of `main` and the tag `v2.276.3` in the public Pilot repo were rewritten: 7 commits re-hashed, trees unchanged, to remove internal notes that had been committed by mistake. Merged PR #5458 was deleted by GitHub Support at our request for the same reason. Any fork or clone fetched before 2026-09-24 21:45 CEST carries the old hashes and must rebase onto the current `main` (do not merge, do not force-push the old history back).

## Context
This repo is public; the private denylist gate (`scripts/check-secret-patterns.sh`, list outside the repo) exists because of the 2026-09-24 incident and the earlier client-material incident. Recorded 2026-09-28 from the founder's note so future sessions do not misread the re-hashed range as a corrupted history, a missing PR, or a botched release (v2.276.3 was the 09-24 train and shipped normally).

## Details
- Rewritten range: 7 commits on `main` up to and including the `v2.276.3` tag; commit contents (trees) identical, only hashes and the tag object changed.
- PR #5458 no longer exists on GitHub; references to it in older docs, issue bodies, or execution rows point at nothing. Do not file it as a missing-PR defect.
- Pilot execution rows and knowledge-graph entries from before 09-24 may carry the pre-rewrite SHAs; they are historical, not wrong.
- Pilot worktrees (`pilot-worktree-GH-*`) and interactive worktrees created before the cutoff base on stale hashes; rebase them onto `origin/main` before pushing.

## Recommended Approach
- If a branch, fork, or worktree predates 2026-09-24 21:45 CEST: `git fetch origin && git rebase origin/main`; never merge old `main` into new.
- If an old SHA from that range shows up in a log, a receipt, or a doc, map it by commit message, not hash.
- Internal notes never enter this repo (see the client-engagement rule in CLAUDE.md); the denylist gate runs on commit and push.

## Related
- CLAUDE.md § Forbidden Actions (public repo, client material rule)
- TASK-460 (delivery evidence), memory `pitfall_noop_terminal_state_invisible_to_dispatch_guards` (stale SHAs on execution rows)

---
**Captured**: 2026-09-28
**Confidence**: 95%
**Concepts**: git-history, public-repo, incident, secrets-hygiene, worktrees
