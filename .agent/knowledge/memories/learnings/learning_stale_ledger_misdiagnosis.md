---
name: stale-ledger-misdiagnosis
description: Sessions reading the laptop's frozen pre-cutover pilot.db confidently misdiagnose healthy tasks — every status claim must name its ledger and verify freshness first
type: learning
---

# Stale-ledger misdiagnosis: name your ledger before diagnosing

**Incident (2026-07-27)**: a session in another repo read the laptop's
`~/.pilot/data/pilot.db` — a plausible-looking but FROZEN archive (nothing
newer than 2026-07-13; the daemon moved to the AWS founder box in the
S6-lite cutover, TASK-409, 2026-07-16) — and produced a confident, internally
consistent, wrong diagnosis: healthy auth-service tasks (GH-468/469/470, all
PRs merged) reported as "failed" with an invented mechanism ("reaper post-PR
state loss") and wrong remediation ("redeploy so heals flip the rows").
The live box ledger showed the rows `completed`.

**Why it happens**: two ledgers exist and the dead one sits at the canonical
path. Nothing at any surface (file path, CLI, dashboard) marks it stale.
Any competent reader pointed at it reasons correctly from wrong data.

**Rules**:
1. Every Pilot status/diagnosis claim must NAME its data source: box DB
   (via SSM, `/var/lib/pilot/pilot-home/data/pilot.db`), GitHub, or the
   laptop archive.
2. Before reasoning about any pilot DB, check freshness:
   `select max(datetime(created_at)) from executions` — if it is days old,
   you are reading an archive.
3. This is the mem-160 family generalized ("run `.tables` BEFORE reasoning
   about empty rows"): verify the substrate before interpreting its content.
4. When two observers disagree about system state, first ask whether they
   are reading the same ledger.

**Related**: [[top-level-autopilot-yaml-binds-to-nothing]] (mem-160),
TASK-409 (cutover), `.claude/skills/pilot-aws/SKILL.md` hard rule 6 (added
same day). Open hardening ideas: archive-sentinel + staleness banner in CLI
and dashboard; rename the laptop DB; global CLAUDE.md pointer.
