---
name: absolute-state-paths-bypass-cutover-shim
description: Machine-cutover path shims must cover EVERY absolute path in config — the one you miss becomes a silent shadow state tree (split-brain ledger incident 2026-07-17)
type: pitfall
---

# Absolute state paths bypass cutover shims silently

**What happened (2026-07-17, #4393):** After the S6-lite AWS cutover
([[s6-lite-cutover-recipe]]), the box daemon ran ~3h writing its ledger to
`/Users/aleks.petrov/.pilot/data/pilot.db` — a shadow DB it auto-created —
while logs/recordings/ops tooling used the canonical
`/var/lib/pilot/pilot-home` tree. Cause: `config.yaml` carried the laptop-era
absolute path `path: /Users/aleks.petrov/.pilot/data`; the cutover shimmed
`/Users/aleks.petrov/Projects` but not `/Users/aleks.petrov/.pilot`. Nothing
failed: to the daemon, an empty dir at a valid path is just "first run".

**Why it bites:** a process can split-brain across state trees *within
itself* — some components resolve `$HOME`-relative (landed canonical), some
use config-absolute (landed shadow). Every dashboard/board/metrics/claims
query silently reads the wrong (frozen) ledger; the duplicate-execution
guard and autopilot PR tracking key per-DB and go blind.

**How to avoid:**
1. Cutover checklist: `grep -nE '/Users/|/home/' config.yaml` and shim or
   rewrite EVERY hit, not just repo paths.
2. After any daemon start on a new box: verify the open DB FD via
   `/proc/<pid>/fd` matches the canonical path — don't trust the log path.
3. Hardening tracked in #4393: startup banner logs resolved DB path; refuse
   to silently initialize an empty ledger when one exists elsewhere.

Detection recipe that caught it: ledger `max(rowid)` frozen while daemon log
shows fresh executions, and log-reported claim generations exceeding the
claims table. Related: [[github-user-aggregate-rate-pool]].
