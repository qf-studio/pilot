---
name: Decomposer thin-subissue OOM cascade
description: Pilot decomposer creates sub-issues with ~1 paragraph of inlined context; thin-spec executions can run 90+min and OOM-kill instead of failing fast
type: project
originSessionId: a8872db5-b4ff-43ee-ae17-f38dcfa4023a
---
When Pilot's decomposer fires (complexity=complex), each sub-issue body is `<!--autopilot-meta inherited-spec: true -->` + `Parent: GH-NNN` + ~1 paragraph excerpted from the parent. That context is **insufficient for autonomous execution** — the executor spelunks the codebase looking for the missing context, runs 90+ minutes, ballons memory, and gets SIGKILL'd (exit 137).

**Why:** The decomposer chooses a sentence or two per phase but doesn't carry the parent's structural sections (`## Context`, `## Approach`, `## Acceptance`). The pre-flight `reject_vague` judge catches the worst offenders (saved #2989 in TASK-60) but lets through any sub-issue that has concrete file/identifier references — even if those identifiers exist in a codebase context the executor doesn't have.

**How to apply:**
- For research-only or single-phase tasks (investigation, small fix, single test), file with a **single-shot intent** marker: add a comment block like `<!-- pilot-execution-mode: single-shot, no-decompose -->` and an explicit paragraph in the body stating the task is intentionally small and not to be decomposed.
- For genuinely multi-phase work that DOES need decomposition: prefer to file Phase 1 alone (investigation), let it produce the root cause as a comment, THEN file Phase 2+ as separate focused issues with full context inlined — don't rely on the decomposer to fan out.
- After filing, monitor for decomposition within ~1 minute. If sub-issues appear with `inherited-spec: true` markers and minimal bodies, close them immediately and re-scope the parent before they start consuming memory.
- Watch for `oom_killed: Process killed by SIGKILL (exit code 137)` in `executions.error` — that's the cascade fingerprint. Run from SQLite: `SELECT task_id, error FROM executions WHERE error LIKE '%SIGKILL%' OR error LIKE '%oom%'`.

**Incident TASK-60 / 2026-05-11:** #2987 filed as 4-phase plan → decomposed into #2988–#2992 → #2988 crashed (exit 1, 90min), #2991 OOM-killed (95min), #2990 + #2992 stuck queued (dispatcher saturated), parent re-queue never executed. Daemon stopped by operator. Recovered by closing all sub-issues, cleaning orphan queue rows, and re-scoping #2987 to investigation-only with explicit anti-decompose comment block.

**Open question:** does Pilot actually honor `<!-- pilot-execution-mode: single-shot, no-decompose -->`? Unverified — added as a defense-in-depth signal alongside body re-scoping. The primary safeguard is making the parent small enough that complexity classifier scores below the decomposer threshold.
