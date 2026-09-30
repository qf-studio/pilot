---
name: file-pilot-issues-via-nav-pilot-skill-only
description: Founder instruction 2026-09-30 — every Pilot issue is filed through the nav-pilot skill from a Navigator task doc (nav-task authors .agent/tasks/TASK-N-slug.md with H2 Context/Implementation/Acceptance; nav-pilot dispatches it with the H1 as title and the doc as --body-file, records the URL under Refs). Never raw gh issue create with an inline or /tmp body.
type: learning
---

# Learning: File every Pilot issue through the nav-pilot skill (task doc first), never raw gh issue create

## Summary
Founder instruction 2026-09-30: all Pilot issues are filed with the nav-pilot skill. Author the spec as a Navigator task doc (nav-task) in .agent/tasks/, then dispatch it with nav-pilot, which uses the H1 as the title and the doc as --body-file, and records the issue URL back under ## Refs. Raw gh issue create with an inline or /tmp body is not the workflow, even for small issues.

## Context
2026-09-30 session filed #5503, #5504, #5505, #5506, #5509 with raw gh issue create and /tmp body files, and rewrote #5500 the same way. Founder then instructed: use the nav-pilot skill to file issues on Pilot, all the time. The project CLAUDE.md already names nav-pilot as the preferred handoff; this makes it the only one.

## Details
Why it matters: a task doc under .agent/tasks/ is the durable spec. It survives the issue, feeds the knowledge graph via the PostToolUse hook, gets archived to .agent/tasks/archive/ on merge, and lets later sessions see WHY an issue was filed. A /tmp body file is gone after the session; the issue body alone loses the planning context. nav-pilot also adds the pre-flight confirmation (repo, title, label, body path) before the outward-facing gh call, and writes the URL and Dispatched status back into the doc. Pilot's spec_validator still needs the H2 set (## Context, ## Implementation, ## Acceptance) inside the doc; nav-pilot does not validate, so author the doc with those headers.

## Recommended Approach
For each issue: (1) nav-task creates .agent/tasks/TASK-N-slug.md with an H1 title in conventional-commit form, a Status line, and the H2 sections Pilot's validator needs (Context, Implementation, Acceptance, Refs); (2) invoke the nav-pilot skill ("dispatch TASK-N to Pilot"), confirm the pre-flight, let it run gh issue create --title <H1> --label pilot --body-file <doc>; (3) it records the URL under ## Refs and sets Status to Dispatched. When an issue must NOT be picked up yet (serialize on a shared file, wait for a box upgrade), ask nav-pilot for a dry run or dispatch without the pilot label and add the label later with gh issue edit. Editing an existing issue's body (as done for #5500) goes through the task doc too: update the doc, then gh issue edit --body-file <doc>. Related: use the navigator-research agent for the research that precedes the doc (see use-nav-research-agent-for-all-project-research).

## Related
- SOP: onboarding/new-project-issue-authoring

---
**Captured**: 2026-09-30
**Confidence**: 95%
**Concepts**: workflow-discipline, dispatch, navigator-integration
