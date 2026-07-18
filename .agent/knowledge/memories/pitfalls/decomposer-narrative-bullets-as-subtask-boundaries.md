---
name: Decomposer treated narrative/timeline bullets as subtask boundaries (GH-4395)
description: TaskDecomposer.analyzeAndSplit() matched ANY "- " bullet or "1. " numbered line as a decomposition boundary, including narrative prose (incident timelines, RCA bullets) — not just explicit work-item structure. Fixed by restricting analyzeAndSplit to checklists + "## Task N" headers only.
type: pitfall
---

**Symptom:** #4390 (a metrics-truthfulness incident issue) has a body shaped
like: `## Symptoms` section with two `### N. <narrative title>` subsections,
each containing plain `- ` bullets describing a timestamped timeline
("2026-07-16 21:29:06Z: PRs #4379... merged"), followed by a real
`## Acceptance criteria` checklist. The decomposer split this into 5 junk
subtasks whose titles were raw timeline bullets, not work items. Subtask 1
then failed (`unknown: exit status 1`, empty stderr) and took the parent
down with it (GH-4395).

**Root cause:** `analyzeAndSplit()` (`internal/executor/decompose.go`) tried
generic strategies in order — numbered steps, then bullet points, then
acceptance-criteria checkboxes, then file groups — and returned as soon as
any strategy matched ≥2 items. `extractBulletPoints`'s regex
(`^\s*[-*•]\s+(.+)$`) matches ANY dash bullet with no distinction between a
work item ("- Create tenant table...") and a narrative timeline entry
("- 2026-07-16 21:29:06Z: PRs #4379 merged"). Since the narrative bullets
appeared before the real checklist in the body, bullet-point extraction found
its ≥2 items first and never reached the acceptance-criteria strategy.

**Why:** Markdown structure (headers, bullets, numbered lists) is used for
both narrative prose (RCA writeups, incident timelines, evidence logs) and
genuine work-item lists in issue bodies. A syntax-only match can't tell them
apart — semantic intent (checklist = actionable, timestamp bullet =
evidence) requires narrower matching than "any list-like line".

**Fix (GH-4395):** `analyzeAndSplit()` now only accepts two structural
boundaries: checklist items (`extractAcceptanceCriteria`, `- [ ] ...` /
`[ ] ...`) and explicit `## Task N` / `### Task N` headers
(`extractTaskHeaders`, new). Generic numbered-step/bullet-point/file-group
extraction is no longer wired into the split decision — those functions
remain as standalone helpers (`extractNumberedSteps` still guards LLM
epic-plan output in `epic_test.go`; `extractAcceptanceCriteria` is reused by
`epic_verify_fold.go`) but a task with prose-only structure now falls back to
no-decompose (`SkipReasonNoSplitPoints`) instead of guessing at boundaries.

**How to apply:**
- If you're adding a new decomposition strategy to `analyzeAndSplit`, it
  MUST require an explicit, low-false-positive-rate marker (checkbox syntax,
  a literal keyword like "Task N") — not a generic markdown list/bullet
  pattern that narrative prose also produces.
- Regression test: `TestTaskDecomposer_GH4390IncidentTimelineDoesNotExplode`
  in `internal/executor/decompose_test.go` reproduces the exact #4390 body
  shape and asserts decomposition only fires on the checklist.
- Related: #4220 (single-child epic short-circuit) is a different guard on a
  different path (LLM epic planner, not the regex `TaskDecomposer`) — don't
  conflate the two when debugging over-decomposition.
