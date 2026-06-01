---
name: Self-heal silently heals nothing when scoped by owner/repo instead of the executions.project_path filesystem path
description: The dashboard QUEUE shows shipped work as "failed" when SelfHealExecutionAfterMerge is scoped by the wrong discriminator. executions.project_path stores an ABSOLUTE FS PATH, not owner/repo. Also: the merged-PR scan and parent epics weren't healed at all. Fixed in v2.166.4 (#3363).
type: learning
metadata:
  type: learning
---

The dashboard QUEUE renders the `executions` table `status` column verbatim
(`internal/dashboard/tui.go` `GetRecentExecutions` → `if exec.Status == "failed"`) with **no
reconciliation against GitHub issue-closed / PR-merged state**. A row only flips `failed → completed`
via `SelfHealExecutionAfterMerge` (`internal/memory/store.go`), called from the autopilot on merge.

**The bug (3 compounding, found 2026-06-01 on TASK-322 Wave 3):**
1. **Wrong discriminator (regression from D3/#3354).** D3 added `AND project_path = ?` to prevent
   cross-repo clobber, and the controller passed `projectPath := c.owner + "/" + c.repo`
   (`qf-studio/pilot`). But `executions.project_path` stores the **absolute filesystem path**
   (`/Users/.../pilot`; set at `runner.go` `ProjectPath: executionPath`). The two never match →
   self-heal matched **0 rows on every merge path** since #3354. Confirmed against the live DB
   `~/.pilot/data/pilot.db`.
2. **`ScanRecentlyMergedPRs` never self-healed.** It is the only catch-all for PRs merged outside the
   controller (`gh pr merge` / GitHub UI), but only triggered release + metrics.
3. **Parent epic never healed.** Self-heal keys on the merged PR's issue number = the sub-issue
   (`GH-3353`); the parent (`GH-3344`) no-op row was never touched.

**Fix (#3363, shipped v2.166.4):** `WithProjectPath(path)` controller option threaded the real FS path
at all 3 `NewController` sites in `main.go`; `SelfHealExecutionAfterMerge` tolerates empty projectPath
(`(? = '' OR project_path = ?)` → task_id-only fallback, so a wrong/missing discriminator can't
silently heal nothing); `selfHealForPR()` heals the issue AND its parent (regex `Parent:\s*GH-(\d+)`,
same as `epic.go`), called from both the merge path and the scan.

**Why:** distinct from the TASK-321/341 phantom-`pilot-blocked` bug (that's the GitHub-label/dispatch
layer; it worked here — no redispatch loop). This is the executions/self-heal layer. A "failed" row on
the dashboard for shipped work is usually self-heal not firing, not a real failure.

**How to apply:** (1) any store method scoped by `project_path` MUST use the absolute FS path the
executor stored — never owner/repo. (2) When you merge a Pilot PR **manually** (`gh pr merge`) on an
old binary, the row won't heal and won't backfill (the merged-PR scan window is 30 min); clear stale
rows with `UPDATE executions SET status='completed' WHERE task_id IN (...shipped...) AND status='failed'`,
scoped only to genuinely-shipped work. (3) Don't trust the dashboard "failed" for an issue with a merged
PR — check the PR, not the row. Related: [[learning_pilot_issue_spec_guard_headers]].
