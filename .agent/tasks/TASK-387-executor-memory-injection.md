# feat(executor): inject relevant knowledge-graph memories into task prompts (TASK-387)

**Status**: 🚀 Dispatched to Pilot
**Created**: 2026-07-06
**Assignee**: Pilot

---

## Context

**Problem**:
Pilot's executor repeats mistakes the knowledge graph already documents.
`bug_pilot_ghost_closes` alone catalogs a failure class that recurred across
TASK-320/321/334/355/369 — each time rediscovered, never recalled. The graph
now holds 85 curated memories (reconciled 2026-07-06) with pitfalls tied to
concepts like `epic-decomposition`, `worktree`, `release` — but nothing
injects them into the prompts autonomous executions actually run with.

Navigator v6.17.0 closed this loop for *interactive* sessions (session-start
surfacing + nav-task recall). The executor path — where mistakes are most
expensive because nobody is watching — is still blind.

**Goal**:
`BuildPrompt` appends a compact "Known pitfalls (project memory)" block —
the top-N graph memories whose concepts match the task — implemented
natively in Go, fail-open, capped, and config-gated.

---

## Known Pitfalls & Patterns

<!-- From knowledge graph (nav-task Step 2.5) — these MUST shape the implementation -->

- **PITFALL** (100%, mem-004): **CRITICAL: Navigator integration in the executor prompt is THE CORE VALUE of Pilot.** When touching `BuildPrompt()`, ALWAYS preserve the Navigator session prefix / `/nav-loop` invocation. It was once removed during a "simplification" refactor and broke the entire system. The memory block must be APPENDED — zero changes to existing prompt structure.
- **PATTERN** (95%, mem-005): BuildPrompt checks for `.agent/` to conditionally add the Navigator session start — the same `hasNavigator` detection gates memory injection (no `.agent/` → no graph → no injection).
- **LEARNING** (90%, mem-077): LLM primitives must spawn the claude subprocess, never call api.anthropic.com directly. Corollary here: recall must be pure local computation (read graph.json, rank in Go) — no LLM call, no Python subprocess.
- **PATTERN** (95%, mem-006): Navigator prompt uses 'Run until done' to trigger Loop Mode — the injected block must sit outside/after that scaffolding so WORKFLOW CHECK and EXIT_SIGNAL behavior are untouched.

---

## Acceptance Criteria

- [ ] New Go recall implementation (suggested: `internal/executor/memory_recall.go` or `internal/memory/graphrecall`): loads `.agent/knowledge/graph.json` from the project path, ranks memories by concept overlap, returns top-N summaries
- [ ] Concept matching: derive target concepts by case-insensitive matching of graph concept ids AND their aliases as substrings/words against the task title + body; rank memories by `|mem.concepts ∩ targets|` desc, then `confidence` desc, then id asc (port of Navigator v6.17.0 `memory_recall.py` — the reference algorithm)
- [ ] Excludes memories with `resolved: true` or a `superseded_by` field
- [ ] Schema-tolerant: node path key may be `path`, `file`, `memory_file`, or absent; graph may lack `concept_index` (never use it); malformed/missing graph.json → empty result, never an error that blocks execution (**fail-open**)
- [ ] `BuildPrompt` appends (never prepends/modifies existing sections) a block formatted:
  ```
  ## Known pitfalls from project memory
  - PITFALL: "<summary>" (90%)
  ...
  Heed these before implementing.
  ```
  capped at 5 memories AND ~1500 chars total, whichever is smaller
- [ ] Injection happens on the Navigator path and the standard path; **skipped for `task.LocalMode`** (GH-2103 LocalMode priority untouched)
- [ ] The `"Start my Navigator session"` / `/nav-loop` prefix and all existing prompt content remain byte-identical when the graph is absent or empty (regression-guarded by test)
- [ ] Config: `executor.memory_injection.enabled` (default **true**) + `executor.memory_injection.max_memories` (default 5) in config schema + `configs/pilot.example.yaml`
- [ ] Table-driven tests: ranking, resolved-exclusion, schema variants (path/file/no-key), missing graph, LocalMode skip, char cap, prompt-unchanged-when-disabled

---

## Implementation

### Phase 1: Go recall
**Goal**: Pure-local ranking, mirroring the reference algorithm

**Tasks**:
- [ ] Graph structs (only the fields recall needs; tolerate unknown fields)
- [ ] `RecallRelevant(projectPath, taskText string, limit int) []MemorySummary`
- [ ] Table-driven tests incl. a fixture mirroring this repo's real graph shape (`file:` keys, no concept_index, summary-only nodes)

**Files**:
- `internal/executor/memory_recall.go` (or `internal/memory/graphrecall/recall.go`)
- matching `_test.go`

### Phase 2: Prompt wiring
**Goal**: Append-only injection in BuildPrompt

**Tasks**:
- [ ] `internal/executor/prompt_builder.go` — after existing prompt assembly (`BuildPrompt`, :67; Navigator gate at :134-151), append the block when enabled && !LocalMode && recall non-empty
- [ ] Config plumbing: `internal/config` struct + defaults + `configs/pilot.example.yaml` docs
- [ ] Prompt tests: golden/structure assertions that existing sections are unchanged

**Files**:
- `internal/executor/prompt_builder.go`
- `internal/config/*.go`
- `configs/pilot.example.yaml`

---

## Out of Scope

- Writing memories from executions (feedback loop — future task)
- Python/`memory_recall.py` subprocess invocation (Go-native only)
- LLM-based concept extraction (substring/alias matching only)
- Semantic/embedding relevance (concept overlap only)
- Dashboard surfacing of injected memories
- Changing what memories exist (graph content is Navigator's domain)

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Recall runtime | shell out to Navigator's memory_recall.py, Go-native port | Go-native | Daemon can't depend on plugin install paths or python3; algorithm is ~80 lines; fail-open is trivial in-process |
| Concept derivation | LLM classify, keyword map, match graph vocabulary against task text | graph vocabulary match | Zero-cost, deterministic, uses the project's own concept ids + aliases as the keyword set |
| Default state | opt-in (dormant), default-on with kill switch | default-on + `enabled: false` opt-out | Additive appended text, fail-open, capped — low risk; value requires it actually running |
| LocalMode | inject, skip | skip | LocalMode is the sandbox/bench problem-solving path (GH-2103); keep it hermetic |
| Placement | prepend, inline, append | append only | mem-004: BuildPrompt structure is load-bearing; append cannot break the Navigator prefix |

---

## Verify

```bash
# Build + unit tests
make build
go test ./internal/executor/... ./internal/config/... ./internal/memory/... 

# Live: prompt for a fake epic-decomposition task against THIS repo's graph
# (expect the ghost-close / orphan-PR pitfalls in the block)
go test ./internal/executor/ -run TestBuildPrompt -v | grep -A8 "Known pitfalls" || true

# Lint
make lint
```

---

## Done

- [ ] Recall implementation + tests on main; `go test ./...` green
- [ ] `BuildPrompt` output for a task mentioning "epic decomposition" against this repo's graph contains a `## Known pitfalls from project memory` block listing epic-decomposition pitfalls
- [ ] With `executor.memory_injection.enabled: false` or no `.agent/knowledge/graph.json`, prompt output is byte-identical to pre-change output (test-asserted)
- [ ] LocalMode prompts contain no injection (test-asserted)
- [ ] `configs/pilot.example.yaml` documents both keys

---

## Refs

- Dispatched: https://github.com/qf-studio/pilot/issues/3899
- Reference algorithm: Navigator v6.17.0 `skills/nav-graph/functions/memory_recall.py` (alekspetrov/navigator)
- Blocked by: none. Related: TASK-386 (CI drift gate keeps the injected data trustworthy)
- Motivating incident class: `.agent/knowledge/memories/pitfalls/bug_pilot_ghost_closes.md` (recurred 5×)
- CLAUDE.md § "CRITICAL: Core Architecture Constraints" (BuildPrompt Navigator integration)

---

## Notes

The graph this reads was reconciled and gate-protected on 2026-07-05/06
(85 memories, 0 drift). Injection quality depends on that hygiene — which
is why TASK-386 ships alongside.

---

**Last Updated**: 2026-07-06
