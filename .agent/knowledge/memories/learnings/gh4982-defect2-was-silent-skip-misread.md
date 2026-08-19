---
name: gh4982-defect2-was-silent-skip-misread
description: The #4982 "second defect" (15:12Z restart never recovered the missed 14:00Z tick) was a misdiagnosis caused by logging shape — the tick was never missed (scope row train:2026-08-18T14:00:00Z state=done created 14:00:05Z, adopted v2.261.0) and recovery DID fire at 15:11:27Z, suppressed correctly by the zero-logging row-exists return. Verification recipe: autopilot_scope_release row for the slot + grep "recovering missed train" (that WARN fires at EVERY boot BEFORE any gate — announces an action that mostly doesn't happen). Fix issue: #4989.
type: learning
created: 2026-08-19
---

# #4982 defect #2 never existed — a silent skip plus a pre-decision WARN produced a false incident

**The misread (2026-08-18 evening).** Founder + session concluded the 15:12Z
restart "started the scheduler with next_run=TOMORROW and no recovery for the
genuinely-missed tick", and #4982 was filed with that as defect #2. PR#4984's
review agent then found the pre-fix source had no gate matching the issue's
hypothesis — so the "fix" for defect #2 targeted a gate that didn't exist.

**Box verification (2026-08-19).** Ledger: `autopilot_scope_release` row
`qf-studio/pilot · train:2026-08-18T14:00:00Z` — state=done, tag=v2.261.0,
created 14:00:05, updated 14:08:36. The scheduled tick RAN on time, found the
13:45Z early cut (real defect #1) already covering its commits, and adopted
v2.261.0. daemon.log: `recovering missed train scheduled_at=2026-08-18T16:00+02`
fired at 14:12, **15:11:27**, and 19:45 — recovery ran at the "missing" boot
and was suppressed by the row-exists return, which logs nothing.

**Why the misread happened (the durable lesson).**
1. `recovering missed train` WARN logs BEFORE any gate is evaluated — it fired
   at every boot on 08-17/08-18 without cutting anything. A log line that
   announces intent pre-decision is worse than no line: it trains readers to
   ignore it AND implies action that didn't happen.
2. The one skip that mattered (row-exists, scope_schedule.go:246-250) is
   silent, and the other skip verdicts are Debug — invisible at Info.
3. When a scheduled action seems to have not happened, check the *outcome
   ledger row for the slot* (`autopilot_scope_release WHERE scope_key LIKE
   'train:%'`) before trusting log absence. Log absence + silent skips ≠
   the action didn't run.

**Status.** Defect #1 (early re-fire of the previous day's covered tick,
sweeping fresh commits) was real and is closed by PR#4984's last-release gate
with proven root cause. Log-hygiene fix filed as #4989. Related: [[golangci-action-cache-self-poisoning-sa5011]]
(same week's other false-signal incident), [[hot-restart-preserves-pid-uptime-false-mismatch]]
(same lesson family: a misleading observability surface repeatedly produces
the same misdiagnosis until the surface itself is fixed).
