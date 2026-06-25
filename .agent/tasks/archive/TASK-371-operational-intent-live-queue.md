# TASK-371: Answer operational queries ("what's in the queue?") from live daemon state

**Status**: ✅ Shipped — v2.194.0 (issue #3648 → GH-3649 PR #3651 + GH-3650 commit `62abeac7`)
**Created**: 2026-06-24
**Assignee**: Pilot
**Labels**: `pilot`, adapter-layer, additive

---

## Context

**Problem**:
Natural-language operational questions like `@Pilot what's in the queue?`,
`what are you working on?`, `anything running?` are classified as `IntentQuestion`
and routed to `handleQuestion`, which **spawns a 90s Claude Code execution** that
reads 5–10 codebase files to *guess* an answer. The result is slow, costs tokens,
and is **wrong by construction** — it inspects the code, not the daemon's actual
task queue.

This is the operational-intent follow-up surfaced live on 2026-06-24 alongside
TASK-369 (the same `what is in the queue?` message that exposed the ghost-SHA
guard bug). TASK-369 stopped the read-only path from *failing*; this task makes
it *answer correctly and cheaply*.

**Current (wrong) path**:
`internal/comms/handler.go:196-210` — the regex fast-path returns `IntentQuestion`
for any clear question (`IsClearQuestion()`), so `what's in the queue?` (starts with
"what", ends with "?") short-circuits to `handleQuestion` **before** the LLM
classifier is ever consulted. `handleQuestion` (`handler.go:244-286`) then builds a
`Q-<ts>` `executor.Task` (LocalMode, 90s) and runs Claude Code over the repo.

**Why it matters**: operational queries are the single most common conversational
ask, they should be instant (no executor, no tokens), and the answer must reflect
the *real* runtime queue, not a code scan.

---

## Existing infrastructure (reuse — do NOT rebuild)

The daemon already answers these queries from live state for the **slash-command**
path; only the **natural-language** path is missing.

| Capability | Location | Notes |
|---|---|---|
| `/queue` command (queued+pending) | `internal/comms/commands.go:255-282` | reads `store.GetQueuedTasks(limit)` |
| `/status` command (running + queue summary) | `internal/comms/commands.go:185-242` | reads store + `statusQueryFunc` |
| Queued/pending tasks | `memory/store.go:1068` `GetQueuedTasks(limit) []*Execution` | all projects |
| Running tasks | `memory/store.go:981` `GetActiveExecutions() []*Execution` | status='running', durable |
| Recent history | `memory/store.go:727` `GetRecentExecutions(limit, path)` | optional, for context |
| Handler already holds the store | `internal/comms/handler.go:28-41` (`HandlerConfig.Store`) + struct field | **no new wiring needed** — every adapter got it via TASK-370's shared factory |

Intent definitions:
- Constants + regex: `internal/intent/intent.go:11-19` (enum), `:20-69` (patterns, `DetectIntent`, `IsClearQuestion`, `StartsWithGreeting`)
- LLM classifier prompt: `internal/intent/classifier.go:62-78`
- Dispatch switch: `internal/comms/handler.go:173-189`; detect entry `:194-236`

---

## Proposed solution

Add an **`IntentOperational`** category that is detected *before* the question
fast-path and answered inline from the store — zero Claude Code, zero tokens.

### 1. New intent (`internal/intent/intent.go`)
- Add `IntentOperational = "operational"` to the enum (`:11-19`).
- Add `operationalPatterns` regex + an `IsOperationalQuery(text) bool` helper
  (`:20-69`). Cover: `queue`, `what.*working on`, `what.*running`, `anything (running|pending|queued|in progress)`, `what.*pending`, `status of (the )?(queue|pilot)`, `how many tasks`, `is anything (running|queued)`.
- In `DetectIntent`, check `IsOperationalQuery` **before** the question branch so
  it wins over the generic "ends with ?" rule.

### 2. Beat the regex fast-path (`internal/comms/handler.go:196-210`)
- In `detectIntent`, add the operational check **at the top**, ahead of the
  greeting/`IsClearQuestion` fast-path. This is the critical ordering fix —
  otherwise `what's in the queue?` never reaches the new branch.

### 3. LLM classifier awareness (`internal/intent/classifier.go:62-78`)
- Add the `operational` category to the system prompt definitions so the LLM
  path (used for phrasings regex misses) also routes correctly. Keep regex
  authoritative for the common phrasings (cheap, no 2s LLM round-trip).

### 4. New handler `handleOperational` (`internal/comms/handler.go`)
- Add `case intent.IntentOperational: return h.handleOperational(...)` to the
  dispatch switch (`:173-189`).
- Implementation: query `h.store.GetActiveExecutions()` + `h.store.GetQueuedTasks(N)`,
  format a compact inline reply (running first, then queued; counts + titles +
  task IDs; "✅ Queue is empty" when both are zero), send via `SendText()`.
- **No `executor.Task`, no runner call.** Respond in < 1s.
- Scope to the active project context where one is set (use
  `GetQueuedTasksForProject` / filter by `activeProject[contextID]`); fall back to
  all-projects when no project context.

### 5. DRY the formatter (recommended, in the spirit of TASK-370)
- The `/queue` and `/status` command handlers (`commands.go:185-282`) and the new
  `handleOperational` should share one rendering helper (e.g.
  `formatQueueSummary(running, queued []*memory.Execution) string`) rather than
  duplicating layout. Extract from `commands.go`; call from both. Prevents the
  command and NL paths from drifting (the exact failure mode TASK-370 closed for
  classifier wiring).

---

## Edge cases / decisions

- **Ordering** (most important): operational detection MUST precede the question
  fast-path in both `intent.DetectIntent` and `comms.detectIntent`. Add a
  table-driven test asserting `what's in the queue?` → `IntentOperational`, not
  `IntentQuestion`.
- **False positives**: a genuine code question like "where is the queue
  *implemented*?" should stay `IntentQuestion`. Keep operational patterns tight —
  match queue/status *state* phrasing, exclude "implement/where is/how does … work".
  Add negative test cases.
- **Running-task source**: use `store.GetActiveExecutions()` (durable, correct
  across the comms handler which has no Monitor reference) rather than the TUI
  `Monitor`. The handler does NOT have `statusQueryFunc` — that's on
  `CommandHandler`; do not add a cross-dependency, just use the store.
- **Empty/disabled store**: guard `h.store == nil` → fall back to existing
  `handleQuestion` (don't panic; some minimal configs may not wire a store).

---

## Implementation steps

1. `internal/intent/intent.go` — add `IntentOperational`, patterns, `IsOperationalQuery`, wire into `DetectIntent` before question branch.
2. `internal/intent/classifier.go` — add `operational` to the LLM system prompt.
3. `internal/comms/handler.go` — operational check at top of `detectIntent`; add dispatch case; implement `handleOperational`; `h.store == nil` fallback.
4. `internal/comms/commands.go` — extract `formatQueueSummary`; call from `/queue`, `/status`, and `handleOperational`.
5. Tests (table-driven):
   - `intent` pkg: operational phrasings → `IntentOperational`; negative cases (code questions) → `IntentQuestion`/`IntentResearch`.
   - `comms` pkg: `detectIntent` ordering (operational beats question fast-path); `handleOperational` with a fake store returning N running + M queued → asserts inline text, asserts **runner never invoked**; empty queue → "queue is empty"; nil store → falls back.

## Acceptance criteria

- [ ] `@Pilot what's in the queue?` (and the phrasings above) answer **inline in < 1s** from store state — no `Q-<ts>` execution appears in logs/db.
- [ ] Answer lists running + queued tasks (IDs/titles) or "queue is empty".
- [ ] Code-implementation questions still route to `handleQuestion`.
- [ ] One shared `formatQueueSummary` used by `/queue`, `/status`, and the NL path.
- [ ] All new tests pass; `make lint` + `make test` green.

## Estimated size
~120–180 LOC + tests. Single package cluster (`intent` + `comms`). Additive, no schema/migration. Pilot-safe.

## Refs
- Follows TASK-369 (PR #3643, ghost-SHA guard fix) and TASK-370 (PR #3646, unified comms.Handler factory).
- Live diagnosis 2026-06-24 (Slack): `what is in the queue?` → `Q-1782293200`.
