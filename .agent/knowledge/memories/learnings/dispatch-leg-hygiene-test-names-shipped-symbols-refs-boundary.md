# Learning: Four dispatch-leg rules learned on TASK-624/625/626 (2026-10-31): (1) name every

## Summary
Four dispatch-leg rules learned on TASK-624/625/626 (2026-10-31): (1) name every test the Acceptance -run patterns rely on inside Implementation, or Pilot picks its own names and the evidence command runs zero tests; (2) Pilot may export or rename a symbol the leg named in prose (prDeliversIssue shipped as PRDeliversIssue) - re-grep origin/main before writing a follow-up leg's probes; (3) nav-dispatch-legs ends a leg body at the leg's own '## Refs' (DOC_SECTION_RE), so nothing after it is filed, while 'Depends on: #{{ID}}' placed between the probes and '## Context' is kept and resolved; (4) an unanchored absent probe (a bare identifier) self-matches the task doc once it is on main - anchor with ^func, ^const or 'name *= *[0-9]'.

## Context
Discovered during development.

## Details
Four dispatch-leg rules learned on TASK-624/625/626 (2026-10-31): (1) name every test the Acceptance -run patterns rely on inside Implementation, or Pilot picks its own names and the evidence command runs zero tests; (2) Pilot may export or rename a symbol the leg named in prose (prDeliversIssue shipped as PRDeliversIssue) - re-grep origin/main before writing a follow-up leg's probes; (3) nav-dispatch-legs ends a leg body at the leg's own '## Refs' (DOC_SECTION_RE), so nothing after it is filed, while 'Depends on: #{{ID}}' placed between the probes and '## Context' is kept and resolved; (4) an unanchored absent probe (a bare identifier) self-matches the task doc once it is on main - anchor with ^func, ^const or 'name *= *[0-9]'.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-01
**Confidence**: 100%
**Concepts**: issue-authoring, pilot-dispatch, navigator, workflow-discipline
