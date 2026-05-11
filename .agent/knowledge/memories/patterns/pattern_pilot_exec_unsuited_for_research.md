---
name: Pilot executor unsuited for research-only tasks
description: Pilot's exec wrapper has ~30K tokens of boilerplate + hard-coded "MUST commit" directive + pattern-learning pollution — research tasks overflow context or get forced into hallucinated code
type: project
originSessionId: a8872db5-b4ff-43ee-ae17-f38dcfa4023a
---
Pilot's executor wrapper is designed for implementation tasks. Filing a research-only Pilot issue triggers three failure modes that compound:

**1. Context overflow.** The wrapper prefaces every prompt with ~30K tokens of boilerplate: PILOT EXECUTION MODE block, "Key Files" section, "Project Structure", workflow phases (INIT/RESEARCH/IMPL/VERIFY/COMPLETE), pre-commit verification rules, and the full issue body. After ~200–300 tool calls of codebase research, cumulative context blows past Claude API's window. CC dies with `"Prompt is too long"` (visible in `~/.pilot/recordings/<task>/stream.jsonl` as the final assistant message); execution row shows `unknown: exit status 1`.

**2. Hard-coded commit requirement.** The wrapper appends: *"CRITICAL: You MUST commit all changes before completing. A task is NOT complete until changes are committed."* This OVERRIDES anything in the issue body that says "no code changes, post a comment." A confused executor either fabricates code to satisfy the directive or spirals trying to reconcile contradictory instructions.

**3. Pattern-learning pollution.** Failed execution subtasks are stored as `execution_learning` entries in the memory/graph DB. Pilot's prompt builder re-injects them as "Related Learnings" on every future task that matches keywords. Even after a failed decomposition is closed, its sub-issue stubs (`Subtask 1/2/3 of 3: Implement changes in <file>.go`) keep surfacing as guidance to subsequent runs of related work, suggesting decomposition for tasks that shouldn't decompose.

**TASK-60 / 2026-05-11 cascade:**
- #2987 filed as 4-phase plan → decomposer fanned out 5 sub-issues with thin context
- #2988 crashed `exit 1` (90 min), #2991 OOM-killed SIGKILL exit 137 (95 min), #2989 declined-preflight, #2990 + #2992 stuck queued
- Closed sub-issues, re-scoped #2987 to investigation-only with explicit anti-decompose marker
- Run 2 of #2987: died at `"Prompt is too long"` after 270 tool calls (context overflow)
- Run 3 auto-dispatched by daemon, same trajectory
- Halted by stripping `pilot` label from #2987 + killing CC child PID
- Investigation completed manually in planning session; root cause posted as comment

**How to apply:**
- Do NOT file research-only Pilot issues. Do the research in a planning session (Read/Grep/Explore agent), post findings, then file a fresh focused **implementation** issue with the root cause + callsites + suggested changes inlined.
- If a long task starts failing repeatedly with `unknown: exit status 1`, immediately check `~/.pilot/recordings/<task>/stream.jsonl` — last assistant message often reveals the real failure ("Prompt is too long", auth error, etc.) that the daemon flattens to opaque exit codes.
- When halting a runaway, ALWAYS strip the `pilot` label first to stop daemon re-dispatch; only then kill the CC child. Otherwise the daemon spawns a new CC seconds later.
- Pattern-learning pollution: there's no clean purge mechanism known yet. Until one exists, expect new tasks in the same problem area to inherit failed-decomposition hints. Reading the stream.jsonl prompt is the only way to spot pollution.

**Related:**
- `pattern_decomposer_thin_subissue_oom.md` — sub-issue context-thinning that primes OOM cascades
- `feedback_pilot_issue_section_headers.md` — intake judge regex on H2 headers
