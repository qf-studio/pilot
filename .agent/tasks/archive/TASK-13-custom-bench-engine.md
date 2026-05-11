# TASK-13: Custom Anthropic API Engine for Terminal Bench 2.0

**Status**: ✅ Submitted to Leaderboard
**Created**: 2026-03-25
**Branch**: `feat/pilot-bench-real`

---

## Context

**Problem**: Pilot scores 49-55% on Terminal Bench 2.0 using Claude Code CLI as a black box. ForgeCode scores 81.8% with the same model (Opus 4.6). The gap is runtime control — can't manage thinking budgets, tool dispatch, or retry behavior through CC.

**Goal**: Replace Claude Code with a custom Python engine calling the Anthropic Messages API directly. Full control over progressive thinking, loop detection, context management.

**Success Criteria**:
- [x] Engine builds and passes syntax check
- [ ] 3-task validation passes (break-filter, chess, gcode)
- [ ] Full 89-task k=1 run beats 58% (stock CC baseline)
- [ ] Full 89-task k=5 run beats 55% (v32 with CC)
- [ ] Submit to Terminal Bench leaderboard

---

## Architecture

```
Harbor Orchestrator
  → PilotAgent (agent.py) — setup + command dispatch
    → engine.py (in container) — direct Anthropic API
      → Tools: bash, read_file, write_file, edit_file
      → Progressive thinking: 16K budget (turns 1-8), 4K (turns 9+)
      → Loop detection: repeated commands, excessive edits
      → Auto-test: pytest every 8 turns
      → Context pruning: prune at 150K tokens, keep last 12 turns
      → Prompt caching: cache_control on system prompt
```

**Replaces**: `pilot task --local` → Go binary → Claude Code CLI → Anthropic API

**Now**: `engine.py` → Anthropic API (direct, no intermediaries)

---

## Implementation Progress

### Phase 1: Engine Scaffolding ✅
- [x] Created `engine.py` with direct Anthropic API calls
- [x] Tool definitions: bash, read_file, write_file
- [x] Progressive thinking budget
- [x] Loop detection (LoopDetector class)
- [x] Auto-test verification
- [x] System prompt with task-specific patterns from learning DB
- **Commits**: `d7c4fec4`, `f7edc832`

### Phase 2: Bug Fixes + Features ✅
- [x] Fixed `MAIN_TIMEOUT` undefined (NameError crash)
- [x] Fixed `global RESULT_JSON` declaration order
- [x] Added `edit_file` tool (precise string replacement)
- [x] Added context window pruning (150K threshold)
- [x] Added prompt caching (cache_control on system)
- [x] Added cost tracking (Opus 4.6 pricing)
- [x] Added robust API retry (exponential backoff on 429/529)
- [x] Fixed test detection (regex-based)
- **Commit**: `8f993884`

### Phase 3: Agent Wiring ✅
- [x] `agent.py` updated: runs `python3 /opt/pilot-agent/engine.py` instead of `pilot task --local`
- [x] `install-pilot-agent.sh.j2` updated: skips Claude Code install
- [x] API key passed via `ANTHROPIC_API_KEY` env var
- [x] Learning DB still uploaded to `/root/.pilot/data/pilot.db`
- [x] Env bootstrap still runs in `setup()`

### Phase 4: Custom Engine Validation ✅
- [x] Python engine: 3/3 validation passed (engine-v7)
- [x] Python engine: 83.3% at 12 trials (engine-v9) — credits exhausted
- [x] Go backend: built, compiles, tests pass
- [x] Go backend: OAuth rejected by raw API — blocked on credits

### Phase 5: CC Backend Fallback ✅
- [x] Reverted to CC backend with optimized config
- [x] Applied engine learnings: model routing, session resume, 15m heartbeat
- [x] v33 validation running — 1/3 passed (chess-best-move) so far

### Phase 6: Full Run ✅
- [x] v34 validation: 3/3 passed (CC optimized)
- [x] v35-k5 first pass: **83.1% (74/89)** — above ForgeCode #1 (81.8%)
- [x] v35-k5 final: **82.0% (365 pass, 74 fail, 6 errors, 439/445 trials)**
- [x] dna-assembly passed (17% historical → now passing)
- [x] Leaderboard submission: PR #108, bot validated, ready to merge
- [x] HF link: https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard/discussions/108
- [ ] When credits available: switch to anthropic-api backend → target 90%+

### Phase 7: Self-Improvement Wiring (Hyperagents-Inspired)
- [x] ROAD-01: Anti-pattern injection — confirmed fixed (tests pass)
- [x] ROAD-02: Patterns injected into self-review prompt
- [x] Pattern DB persistence — containers learn and merge back to seed
- [ ] ROAD-10: Activate knowledge graph queries
- [ ] Quality gate feedback → retry prompt
- [ ] Outcome → complexity re-classification
- [ ] Bench DB → main DB sync
- [ ] Prompt variant archive (Hyperagents-style)
- [ ] Meta-improvement: auto-prompt optimization

---

## Key Files

| File | Role |
|------|------|
| `pilot-bench/pilot_agent/engine.py` | Custom execution engine (direct API) |
| `pilot-bench/pilot_agent/agent.py` | Harbor agent shim (setup + dispatch) |
| `pilot-bench/pilot_agent/templates/install-pilot-agent.sh.j2` | Container bootstrap |
| `pilot-bench/pilot_agent/data/pilot.db` | Learning DB (17 patterns) |
| `pilot-bench/pilot_agent/scripts/seed-learning-db.py` | Pattern seeder |

---

## Previous Runs (Claude Code era)

| Run | Config | Score | Notes |
|-----|--------|-------|-------|
| v24 (k=1) | CC 2.1.74, Haiku classifier | 65.9% | Best k=1 ever |
| v31 (k=5) | CC 2.1.74, phased prompt | 50.0% @22 | Killed |
| v32 (k=5) | CC 2.1.74, all improvements | 49.2% @58 | Killed |
| v30 (k=5) | CC 2.1.74, old prompt | 55.0% @35 | Killed |

## Leaderboard Context

| Agent | Model | Score |
|-------|-------|-------|
| ForgeCode | Opus 4.6 | 81.8% |
| TongAgents | Gemini 3.1 Pro | 80.2% |
| Capy | Opus 4.6 | 75.3% |
| Stock Claude Code | Opus 4.6 | 58.0% |
| **Pilot (target)** | **Opus 4.6** | **>65%** |

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| API approach | Claude Code CLI vs direct API | Direct API | Full control over thinking, tools, context |
| Thinking budget | Fixed vs progressive | Progressive (16K→4K) | ForgeCode's #1 technique |
| Tools | bash only vs bash+file tools | bash + read + write + edit | edit_file enables precise patches |
| Context mgmt | None vs pruning | Prune at 150K, keep 12 turns | Prevents API errors on long tasks |
| Caching | None vs cache_control | cache_control on system | ~90% input token savings |

---

**Last Updated**: 2026-03-25
