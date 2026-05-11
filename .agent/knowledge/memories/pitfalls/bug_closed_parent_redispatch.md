---
name: Closed-parent re-dispatch loop (GH-201 / cascade-3)
description: Autopilot reconciliation re-invokes CreateSubIssues on closed/merged parents. LLM hallucinates a plausible subtask from sparse parent input + project domain context. Fix shipped TASK-50 / v2.139.0+. Recurrence risk if any new dispatch/decompose entry point omits the parent-state gate.
type: feedback
originSessionId: 4f6a94e6-d5cd-4b19-a4a0-c93b6769ad05
---
When an issue keeps spawning identical sub-issues every poll cycle (e.g., GH-201 spawning OAuth-titled children daily 5/8), this is **not** a prompt leak even though the symptom looks identical to OAuth cascades #1/#2. It's the autopilot reconciliation path calling `CreateSubIssues(P)` on a parent `P` that's already closed/merged.

**Why:** All prior guards key on the child or on execution state, not parent state. `ProcessedStore` is child-keyed; the GH-2242 `executions.status='completed'` lockout gates execution dispatch only; `queryOpenSubIssues` only counts OPEN siblings (closing dupes resets the guard).

**Why the OAuth title:** LLM hallucination, not a leak. `internal/executor/epic.go:317` `buildPlanningPrompt` is clean (PR #2562, ALL_CAPS placeholders, invariant test #2592 passes). When fed a sparse closed parent + project's heavy OAuth domain memory (34+ rows in SQLite `memories` from real OAuth work), the LLM consistently emits `feat(auth): add OAuth provider integration` as the most plausible subtask. Same input → same output (low temp).

**Why:** This was a real recurrence-risk pattern — it took 64 dupes across 19 hours before the fix landed. Prior debugging instinct ("must be another prompt leak") wasted time scanning prompts; the actual issue was upstream of any prompt.

**How to apply:**
- Symptoms: identical-titled sub-issues spawning every 10–15 min from a closed/done parent. Body is the bare `<!--autopilot-meta\nparent: GH-N\ninherited-spec: true\n-->` marker.
- First check: is the parent already `pilot-done` / `MERGED`? If yes, the bug is the dispatcher / decomposer / reconciler entry point not gating on parent state — not a prompt leak.
- Bridge: open a sentinel issue with NO labels and body `Parent: GH-N` to keep `queryOpenSubIssues` tripped while a real fix ships.
- Real fix: gate every "should we run X on parent P?" entry point on parent state. `isParentDone` helper in `internal/executor/epic.go` is the canonical check after TASK-50 / PR #2872 (v2.139.0+).

**Don't:** scan prompts again unless the invariant test #2592 actually fails — if it passes, the leak is upstream.
