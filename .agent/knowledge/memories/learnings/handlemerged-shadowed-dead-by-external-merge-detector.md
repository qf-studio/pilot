---
name: handlemerged-shadowed-dead-by-external-merge-detector
description: handleMerged (deploy + review-learning + eval extraction) was dead code in production for the box's entire life — checkExternalMergeOrClose consumed every own-merge as "external" before stage dispatch; GH-4872 item-3 guard (PR#4908) resurrected the whole tail on 2026-08-17
type: learning
---

# handleMerged was shadowed dead for months; PR#4908 resurrected it (2026-08-17)

**What happened:** The TUI eval panel ("pass@1 100.0% · 1 tasks") appeared out of
nowhere on 2026-08-17. Investigation: `eval_tasks` had ONE row ever, written 16:09Z
that day for PR#4920. Box log covering the entire box lifetime (since 07-16 cutover)
shows `handleMerged: PR merged` logged exactly ONCE — same PR. A month of daily
autopilot merges never entered `handleMerged` at all.

**Root cause:** `checkExternalMergeOrClose` runs BEFORE ProcessPR's stage dispatch
every tick. After autopilot's own merge, `handleMerging` finalized and left
`Stage=StageMerged`; on the next tick the detector saw `ghPR.Merged==true` with no
stage guard, classified the daemon's OWN merge as external, re-ran the external
finalize (second issue close, second completion comment, second event write) and
called `removePR` — deleting the row before ProcessPR could ever dispatch
`handleMerged`. PR#4908 (GH-4872 item 3) added `if Stage == StageMerged return
false` at `controller.go:~7732`, un-shadowing the path. First post-rebuild
autopilot merge (PR#4920, 16:08Z) → first `handleMerged` run ever.

**Why it matters beyond the panel:** everything in handleMerged's body was silently
dead in production the whole time and is now LIVE for the first time:
- `deployer.Deploy` post-merge (if configured)
- GH-1823 review-learning (`LearnFromReview` — "Learned from PR reviews" had zero
  log hits ever)
- GH-2059 eval extraction (`SaveEvalTask` → `eval_tasks` → TUI eval panel /
  pass@1 metrics)
Expect newly-active behaviors after v2.259.4-2; treat "feature suddenly doing
something" post-GH-4872 as possible resurrection, not regression.

**How to apply:**
- A surface appearing "out of nowhere" after an upgrade may be an old feature whose
  feeding path just got un-deadened — check whether the WRITE path ever fired
  (grep the full-lifetime daemon log for the site's log line) before assuming the
  feature itself is new. Rendering code that returns empty on no-data hides dead
  producers for months.
- Early-tick interceptors (`checkExternalMergeOrClose` runs before stage dispatch)
  can permanently shadow whole stage handlers; when adding one, prove each stage
  still reaches its handler.
- Panel decision 2026-08-17: eval pass@1 moves to Prometheus (#4922), TUI panel
  removed. Related: [[hot-restart-preserves-pid-uptime-false-mismatch]].
