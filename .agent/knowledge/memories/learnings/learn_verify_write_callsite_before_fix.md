---
name: Verify the alleged write call site exists before drafting a code fix
description: When a task file alleges "Pilot's X path forgets to do Y", grep for the X call site FIRST. If Pilot does not even own that path, the fix is configuration/external, not code.
type: learning
originTask: TASK-360
---

When a task file frames a board/state anomaly as a Pilot bug ("path Z in
`internal/adapters/...` calls A but doesn't follow up with B"), the FIRST
diagnostic step is to grep for A's call sites in `internal/`. If there are
zero call sites, Pilot does not own that write path — the root cause is
external (a different bot, a human, a GitHub-side workflow, etc.) and the
fix is configuration, not code.

**Why:** TASK-360 alleged a `Pilot dispatch race` where some code path
called `addProjectV2ItemById` and then forgot to set Status. Reality
(2026-06-09 audit):

```
$ grep -rn 'addProjectV2ItemById\|addProjectV2DraftIssue' internal/
(empty)
```

Pilot has zero `addProjectV2Item*` callers — it only READS the project
(`internal/adapters/github/project_source.go`) and UPDATES Status on
existing items (`project_board.go:48` — `updateProjectV2ItemFieldValue`).
The cards in `(none)` status were added by `github-project-automation[bot]`
via the project's own workflow #4 ("Auto-add sub-issues") and #7 ("Auto-add
to project"), with workflow #6 ("Item added to project" — which would set
default Status) **disabled**. The fix is enabling workflow #6 on the board,
not patching Go code.

**How to apply:**

1. Read the task file's hypothesis. Identify the exact API/function it
   accuses Pilot of mis-calling.
2. `grep -rn '<that-function-name>' internal/ --include="*.go"` BEFORE
   drafting a fix, opening a worktree, or writing a spec.
3. **Zero hits** → fix is external. Check (a) the GitHub Project workflow
   configuration via `gh api graphql` query on `projectV2.workflows`, (b)
   the issue timeline (`gh api repos/.../issues/N/timeline`) to identify
   the actual actor — look for `added_to_project_v2` events and their
   `actor.login`.
4. **Hits exist** → audit each call site for the missing follow-up.

**Related lessons:**
- [[feedback_check_state_before_designing]] — verify live state before
  drafting any fix; same theme one layer up.
- Carried-forward marker note from `2026-06-09_1715`: "task files can
  drift; verifying live state before drafting the fix prevented shipping
  a defense-in-depth for already-resolved bugs" (TASK-354 reconciliation).
