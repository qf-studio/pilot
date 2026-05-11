---
name: autopilot_pr_state is ephemeral by design
description: Successful PR rows are deleted on lifecycle completion; only failed rows persist. Don't mistake row absence for missing OnPRCreated wiring.
type: project
originSessionId: edeae92e-c7c5-4a3a-9e82-b1c0b1399bb9
---
`autopilot_pr_state` is an **active state tracker, not an audit log**. Every successful PR's row is deleted when its lifecycle completes.

**Why:** the only consumers (CI monitoring, auto-merge, approval tracking, post-merge SHA, restart recovery via `LoadAllPRStates`) need in-flight state only. Persistent history is covered by the `executions` table (`pr_url` column) and metric counters (e.g. `pilot_pr_merge_recorded_total`, shipped via TASK-59 v2.146.3 / PR #2985).

**How to apply:** if you find `autopilot_pr_state` "missing rows" for recently-merged PRs, that is correct behavior. Do **not** debug it as an `OnPRCreated` wiring gap. The cleanup path is `controller.removePR()` → `persistRemovePR()` → `RemovePRState()` → `DELETE FROM autopilot_pr_state` (`state_store.go:272`), called from `controller.go:1381` plus 8 other lifecycle-completion sites. Failed rows persist because only `PurgeTerminalPRStates` (`state_store.go:818`) removes them, on a schedule.

**Verification recipe** (2026-05-11 closed TASK-60 with this):
1. File a small trigger PR.
2. Within a minute of PR creation, query: `sqlite3 ~/.pilot/data/pilot.db "SELECT * FROM autopilot_pr_state WHERE pr_number = N"` — should show a row at `stage='waiting_ci'`.
3. Wait for the full lifecycle (merge + release) to complete.
4. Re-query — row should be gone.

**TASK-60 was a phantom bug.** The instrumentation (`6df99408`, PR #3000) and observability work (#3005/#3008/#3011) it spawned are still real wins, but the premise was wrong. Diagnostic logs in `poller.go` at the `OnPRCreated` gates are kept for future regressions.
