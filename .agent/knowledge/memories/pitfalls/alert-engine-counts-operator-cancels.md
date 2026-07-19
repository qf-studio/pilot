---
name: alert-engine-counts-operator-cancels
description: consecutive_failures alert counts operator-cancelled executions as failures — ledger surgery days (de-label + cancel rows) fire false CRITICAL pages naming an innocent task
type: pitfall
---

# Alert engine: operator cancels count as consecutive failures → false CRITICAL

**What happened (2026-07-18 09:30Z):** Founder got a CRITICAL
`consecutive_failures` page ("3 consecutive task failures … Source:
task:GH-4393") minutes after routine de-label surgery. GH-4393 had NOT
executed at all that day. The three "failures" were the surgery itself:
GH-4415 cancelled 09:03, GH-4393 cancelled 09:20, GH-4415 cancelled 09:29 —
all operator `UPDATE … status='cancelled'` rows with audit notes.

## Why it matters
- The alert names whichever task happens to sit in the window → founder
  burns time re-investigating an already-parked task ("jumping around it
  2 days in a row").
- Every future surgery day (an established recipe: cancel row so re-pick
  captures a fresh issue body) will re-fire this page until fixed.

## Fix direction
Alert engine should exclude `cancelled` (and arguably `declined-preflight`)
from the consecutive-failure counter — only `failed`/`infra`/`stalled`
outcomes represent pipeline health. Issue not yet filed as of 2026-07-18
(founder aware).

Related: [[hard-cap-rearm-in-memory-gate]].
