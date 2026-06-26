---
name: autopilot orphans the last epic child PRs when the epic execution ends — green CLEAN child PRs sit unmerged, epic stays pilot-in-progress, dependents gated forever
description: When Pilot decomposes a pilot-labeled issue into an epic, the epic's own execution run owns the child-PR merge sequence. Children whose PRs are created late (just before the epic run ends) are left created-but-unmerged when the run finishes — even though stage env has require_approval:false + auto_merge:true and CI (test/lint) is green. The standalone autopilot auto-merge loop does NOT reclaim them because their tracking issues were already CLOSED at PR-creation time, so it treats the work as done. Result: green/CLEAN child PRs with ZERO autopilot review/comment, the epic stuck OPEN+pilot-in-progress, and any issue with "Blocked by: #<epic>" gated indefinitely. Observed 2026-06-26 on the bot-module epic #3665: it merged children #3670/#3674 during its run, then ended ("completed but no changes made, 44m") leaving #3678/#3679 orphaned. TASK-359 finalization shape.
type: pitfall
---
When a `pilot`-labeled issue is large enough, Pilot's executor **decomposes it into an
epic** with child issues, and the **epic's execution run** drives creating + merging the
child PRs. If a child PR is created **late in the run** (e.g. the run hits its wall-clock
limit right after opening the PR), the epic finishes **before merging that child** and the
PR is left **created-but-unmerged**.

**Why autopilot's standalone auto-merge loop does NOT rescue it:** the child issues are
**already CLOSED** at the moment their PR is created (Pilot closes the child issue up
front). The auto-merge loop keys on an **open tracking issue**; with the issue closed it
considers the work finished and never claims the orphaned green PR. So even with the
stage-env policy that should merge automatically —
```yaml
autopilot: {environment: stage, auto_merge: true,
  environments: {stage: {require_approval: false}}, required_checks: [test, lint]}
```
— the PR just sits there: `state=OPEN mergeable=CLEAN`, **no autopilot review, no comment**,
for as long as you wait.

**The downstream trap (why it's worse than one stuck PR):** the epic issue itself stays
`OPEN` + `pilot-in-progress`. Any follow-up issue that gates on it via the poller's
dependency parse — `Blocked by: #<epic>` / `Depends on: #<epic>` (see
`internal/adapters/github/poller.go` `ParseDependencies` / `hasPendingDependencies`) —
is skipped **forever** with `ReasonPendingDependency`. A whole phased pipeline can wedge
behind one orphaned child PR.

**How to recognize it (the diagnostic that proved it, 2026-06-26, epic #3665):**
- Children merged DURING the run by the daemon identity, then it stops:
  `gh pr view <child> --json mergedBy,mergedAt` → first ones merged by the bot account at
  03:04/03:09; later ones (`#3678` created 03:27, `#3679` 03:34) never merged.
- Epic comment: **"Pilot execution completed but no changes were made. Duration: 44m.
  Branch pilot/GH-<epic>. No commits or PR."** ← this is the **parent epic's own** run
  (expected: real work is in children) — NOT itself the failure; the failure is the
  unmerged children it left behind.
- Orphaned PRs: `gh pr view <n> --json state,mergeable,mergeStateStatus,reviewDecision`
  → `OPEN / MERGEABLE / CLEAN / review:""` with **no autopilot review or comment**, long
  after `gh pr checks <n>` shows test+lint **pass**.
- Their issues already `CLOSED`; epic still `OPEN`+`pilot-in-progress`.

**How to apply / recover:**
- **Don't wait on autopilot for orphaned children** — it will not act. Merge them yourself,
  in dependency order, then close the epic to release dependents:
  ```bash
  gh pr merge <child-A> --squash --delete-branch    # earliest-needed first
  gh pr merge <child-B> --squash --delete-branch
  gh issue close <epic> -c "Epic complete — children merged manually; autopilot orphaned them (TASK-359 shape)."
  ```
  Closing the epic satisfies `Blocked by: #<epic>` and the gated dependents dispatch on the
  next poll.
- **Late children commonly CONFLICT once an earlier sibling merges.** #3678 and #3679 both
  added the *same* Telegram/Slack `cfg.Bot` wiring; merging #3678 turned #3679 `DIRTY`.
  In an interactive session **do not hand-resolve in the repo root** (worktree discipline) —
  instead close the conflicted PR and **re-dispatch only its UNIQUE remainder** as a fresh
  `pilot` issue against the now-updated `main` (a clean agent build, no conflict). Check
  uniqueness first: `gh pr diff <n> --name-only` + `git grep <symbol> origin/main` to see
  what already landed via the sibling.
- **Prefer narrow dispatch to reduce orphaning.** Bundling tightly-coupled phases into one
  issue invites decomposition into many late children. If a unit is small enough to be one
  PR, dispatch it as its own issue with explicit `Blocked by:` ordering rather than letting
  the epic decomposer split it.
- The cosmetic `pilot-in-progress` label left on the just-closed epic is stripped by the
  cleanup loop (`internal/adapters/github/cleanup.go` removes it from CLOSED issues) — ignore.

Related: [[learn_restart_vs_rebuild_stale_binary]] (verify the running daemon binary before
blaming config), TASK-359 daemon-finalization hardening (the structural fix surface:
epic vs direct path divergent error contracts, orphaned-child reclamation).
