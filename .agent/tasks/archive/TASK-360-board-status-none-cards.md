# TASK-360: Board cards added to project without a Status field

**Status:** ✅ RESOLVED 2026-06-09 — NOT a Pilot bug; GitHub Project workflow misconfiguration
**Priority:** P3 (was)
**Severity:** low (cosmetic / lifecycle visibility)
**Related:** [[TASK-354]] (board orphan reconciliation found these); [[learn_verify_write_callsite_before_fix]]

## Original observation (2026-06-09)

`gh project item-list 1 --owner qf-studio` returned 5 cards with `(none)`
status. By the time the task was picked up later 2026-06-09, only **2**
remained:

- `qf-studio/studio-sdk#26` — refactor(sdk): neutralize connector surface (MERGED PR, closed 2026-06-02)
- `qf-studio/studio-sdk#27` — M7 cutover: absorb IssueEvent behavior changes from #26 (OPEN issue)

The other 3 (#52, #64, #65) self-resolved between 2026-06-09 morning and afternoon.

## Investigation

**Three hypotheses in the original task file — all wrong or refuted:**

1. ❌ Manual API insertion — refuted by timeline: actor was `github-project-automation[bot]`.
2. ❌ Pilot dispatch race — refuted by grep:
   ```
   $ grep -rn 'addProjectV2ItemById\|addProjectV2DraftIssue\|addProjectV2Item ' internal/
   (empty)
   ```
   Pilot has ZERO call sites that add items to the project board. It only
   READS items (`internal/adapters/github/project_source.go`) and UPDATES
   Status on existing items (`internal/adapters/github/project_board.go:48`
   — `updateProjectV2ItemFieldValue`).
3. 🟡 External tool — confirmed in a specific form: GitHub's own native
   project automation, not a third-party bot.

## Root cause

The `qf-studio/projects/1` board has these GitHub-native workflows:

| # | Workflow | Status |
|---|---|---|
| 4 | Auto-add sub-issues to project | ✅ enabled — adds cards |
| 7 | Auto-add to project | ✅ enabled — adds cards |
| 6 | Item added to project | ❌ **DISABLED** — would set default Status |
| 1 | Item closed | ❌ disabled — would set Done on close |
| 3 | Auto-close issue | ❌ disabled |
| 5 | PR linked to issue | ❌ disabled |
| 2 | PR merged | ❌ disabled |

Workflows #4 and #7 add items but don't set a Status field. Workflow #6
— the one that WOULD set a default Status when items are added — is
disabled. This is a classic GitHub Project (v2) gotcha: the auto-add
workflows must be paired with the "Item added to project" workflow to
default new items into a column.

Timeline evidence for both stuck cards:
```
#26  2026-06-02T15:52:57Z  github-project-automation[bot]  added_to_project_v2
#27  2026-06-02T17:17:03Z  github-project-automation[bot]  added_to_project_v2
```

## Resolution

**Code:** No changes required. Pilot is not involved.

**Configuration (manual, on GitHub):**
- [ ] Enable workflow **#6 "Item added to project"** on `qf-studio/projects/1`, set `Status = Todo` on add. *(Blocked: requires `project` scope on the local `gh` token — run `gh auth refresh -s project` to enable scripted fixes; or change in the web UI.)*
- [ ] (Optional) Enable workflow **#1 "Item closed"** to set `Status = Done` on close.

**Backfill:**
- [ ] studio-sdk#26 → `Done` (it's a merged PR; closed 2026-06-02)
- [ ] studio-sdk#27 → `Todo` (open issue)

Both backfills require the `project` gh scope as above.

## Lesson captured

See [[learn_verify_write_callsite_before_fix]] — grep for the alleged write
call site in `internal/` BEFORE drafting any Pilot-side fix. Saved a
worktree + spec + PR round for a non-bug.
