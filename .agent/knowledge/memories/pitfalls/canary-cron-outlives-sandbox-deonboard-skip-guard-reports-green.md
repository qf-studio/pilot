---
name: canary-cron-outlives-sandbox-deonboard-skip-guard-reports-green
description: De-onboarding the canary sandbox from the box (2026-08-14) left the pilot-canary.yml cron enabled; its open-issue guard skipped every run as "prior run in flight" behind two stale sandbox issues and reported SUCCESS for 7 weeks, until the CANARY_GH_TOKEN PAT expired (2026-10-04) and the guard itself started failing, reopening tracker issues #4265/#4366 with bare "Reopened" emails
type: pitfall
created: 2026-10-04
---

# Canary cron outlives the sandbox de-onboard; the skip guard reports green

## What happened

- 2026-08-14: the canary scenario suite was "disarmed" by de-onboarding
  `pilot-canary-sandbox` from the box config and restarting the daemon. The GitHub
  Actions cron (`.github/workflows/pilot-canary.yml`, every 6 h) was not touched.
- The same day two sandbox issues (#318 version-bump, #319 epic-lifecycle) died at the
  `git_clean` preflight (the box checkout had a stale staged index: both packages
  deleted, `version.go` downgraded) and hit the repick cap. The label-cleanup job stripped
  `pilot-blocked` a day later, leaving them open with `pilot`.
- Every later run saw an open `pilot`-labelled issue per scenario, logged "a prior run is
  still in flight. Skipping.", set `skip=true`, and the job concluded **success**. The
  report step is gated on `skip != true`, so the tracker never heard about it. Seven
  weeks of green no-ops.
- 2026-10-04: `CANARY_GH_TOKEN` (classic PAT minted 08-05, 60 days) expired. The guard's
  `gh issue list` returned 401, the step failed, the report step ran with
  `result=failure`, and `scripts/canary-report.py` reopened the closed trackers #4265 and
  #4366. Reopen events carry no body; the status comment is edited in place, so the
  founder saw bare "Reopened #4265" emails and suspected an attack.

## Rules

- **Disarming a canary means disabling its cron too**: `gh workflow disable
  pilot-canary.yml`. A workflow whose target the daemon no longer watches can only
  skip or fail.
- **A skip is not a pass.** The open-issue guard must report `skipped` to the tracker (or
  fail after N consecutive skips); `success` on a skipped run hides a dead canary.
- **Stale sandbox issues block the suite forever.** Any sandbox issue older than the
  scenario timeout should be closed by the guard, not treated as in flight.
- **Token expiry calendar**: classic PATs `pilot-saas` and `qf-studio-homebrew-tap` expire
  2026-10-28; fine-grained `qf-studio-homebrew-tap` / `grot-homebrew-ta` 2026-10-05.
  `pilot-local` (laptop daemon) expired 2026-10-04 and is dead by design.

## Resolution 2026-10-04

Workflow `pilot-canary.yml` disabled; #4265, #4366 (pilot) and #318, #319 (sandbox)
closed. `CANARY_GH_TOKEN` was regenerated but the pasted secret carried a stray byte
(`invalid header field value for "Authorization"`); irrelevant while disabled. Re-arm =
re-onboard the sandbox on the box, `git reset --hard origin/main` its checkout, fix the
sandbox `required_checks` name mismatch from 08-14, re-set the secret with
`pbpaste | tr -d '\n\r ' | gh secret set ...`, then `gh workflow enable`.

Related: [[linear-invoices-tickets-founder-owned-hands-off]] (same session),
TASK-403 (canary design), TASK-441 (drill that caused the 08-14 de-onboard).
