---
name: Always use /nav-task to create Pilot issues
description: When creating GitHub issues for Pilot to execute, route through the nav-task skill first — it produces an implementation plan doc that becomes the issue body. Do not file issues directly via gh issue create.
type: feedback
originSessionId: d0fafba1-9001-4881-8900-e3d17b306b13
---
Always use the `nav-task` skill (Navigator) to create Pilot issues. Do not call `gh issue create` directly with hand-written bodies.

**Why:** nav-task produces a structured implementation plan in `.agent/tasks/` that captures effort estimates, file:line refs, and acceptance criteria in a consistent format. The plan becomes the source of truth and the issue body is derived from it. Hand-written issue bodies drift from the plan and lose the audit trail.

**How to apply:**
- Any time the user asks to file a Pilot issue (or a follow-up after research), invoke `Skill(skill: "navigator:nav-task", args: "<feature description>")` first.
- After nav-task produces the plan, file the GitHub issue using the plan as the body.
- This applies to ALL Pilot issues, not just complex features. Single-file fixes go through nav-task too.
- Established 2026-05-05 after I filed #2637 directly via `gh issue create`. User correction.
