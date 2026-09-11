---
name: issue-body-literal-migration-version-collides-with-sibling-leg
description: Two console legs dispatched the same morning each told to add "migration 0018" — #284 merged first, #285 collided (duplicate migration file, every DB test red at migrate-load), CI-fix size guard refused a fix issue, task parked needs-human. Never hard-code a migration version in an issue body; say "next free version at merge time".
type: pitfall
---

# Issue bodies must not hard-code a migration version

**What happened (2026-09-10):** console #282 (B7) and #283 (usage rollup) were dispatched within an hour and both needed a migration. #283's body said "new migration 0018_usage_rollup"; #282's body said "0018 unless the usage-rollup issue lands first, then 0019" (a conditional Pilot could not evaluate at write time). #284 (from #282) merged first with 0018; PR#285 (from #283) then failed every DB test with `duplicate migration file: 0018_usage_rollup.down.sql`. The CI-fix size guard refused to spawn a fix issue (707 production lines > 200), so #283 was parked `pilot-needs-human`; recovery = revision issue #286 with the autopilot-meta footer (renumber to 0019).

## Rule
- In issue bodies write **"the next free migration version at merge time (currently 00NN)"**, never a literal filename. Pilot picks the number when it writes the file.
- When two legs in one repo both add migrations, dispatch them serially or say explicitly which one owns the lower number.
- Reviewer check: on any PR adding a migration, compare the version against `origin/main` at review time, not against the branch base.

Related: [[ci-fix-size-guard-holds-own-pr-on-one-lint-annotation]], [[autopilot-failed-stage-revival-is-a-flag-gated-redrive-before-processpr]] (the re-adopt path that later merged PR#285).
