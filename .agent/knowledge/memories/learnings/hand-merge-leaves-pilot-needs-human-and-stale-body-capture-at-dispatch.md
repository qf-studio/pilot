---
name: hand-merge-leaves-pilot-needs-human-and-stale-body-capture-at-dispatch
description: two operator facts from 2026-09-08 — (1) pilot-needs-human is only removed in autopilot's own external-close label block (controller.go ~9855-9885), so a hand merge leaves it next to pilot-done; strip it by hand. (2) the task description is built from the issue body at dispatch/queue time (handlers.go taskDesc from ev.Body, no re-fetch), so editing the body of a QUEUED issue changes nothing — cancel the queued row (or close+refile) and re-label
type: learning
---

# Hand merges leave `pilot-needs-human`; queued rows keep the old body

1. **Label residue after a hand merge:** #5351 ended `pilot-done` +
   `pilot-needs-human` because the removal lives only in the external-close
   block (`controller.go` ~9855-9885) that autopilot runs on its own
   close/merge. After `gh pr merge` by hand: strip the label, close revision
   issues yourself (they are not auto-closed by the PR).
2. **Body is captured at dispatch:** `cmd/pilot/handlers.go` ~807 builds
   `taskDesc` from `ev.Body` when the issue is dispatched into the queue; the
   executor does not re-fetch. For a queued-but-not-started issue whose body
   you must change: edit body → remove `pilot` → cancel the `queued` executions
   row on the box (`UPDATE executions SET status='cancelled', error='…'`) →
   re-add `pilot` → poller re-dispatches as the next generation with the new
   body (done for #5378 12:1xZ). Related: alert-engine pitfall (cancelled rows
   count as consecutive failures — expect a possible false page).
