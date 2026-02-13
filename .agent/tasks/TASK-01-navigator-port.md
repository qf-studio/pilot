# TASK-01: Complete Navigator Port into Pilot

**Status**: Scaffolding Done, Wiring Pending (GH-1026)
**Created**: 2026-02-13
**Updated**: 2026-02-13
**Priority**: P0

---

## Context

**Problem**:
Pilot depends on Navigator plugin for execution. This creates external dependency, version coupling, and wiring complexity. Users must have Navigator installed for Pilot to work properly.

**Goal**:
Make Pilot fully self-contained by porting ALL Navigator concepts into Pilot's Go codebase. Users won't need Navigator plugin installed.

**What happened**: 13 sequential issues (GH-986-1000) created. Serial merge conflict cascade killed the critical wiring PR (GH-994). All scaffolding files landed on `main` but most are dead code. Consolidated into single wiring issue GH-1026.

**Success Criteria**:
- [x] All scaffolding files created (8/8)
- [ ] All scaffolding wired into execution pipeline (GH-1026)
- [ ] Pilot works without Navigator plugin installed
- [ ] No regression in execution quality

---

## Implementation Plan

### Phase 1: Execution Workflow (Foundation) ✅ DONE
**Goal**: Replace `/nav-loop` with embedded execution instructions

**Issues**:
- [x] #986 - Create workflow.go with embedded execution instructions (DONE)
- [ ] #987 - Update runner.go to use embedded workflow

**Files**:
- `internal/executor/workflow.go` - Embedded workflow prompts
- `internal/executor/runner.go` - BuildPrompt() changes

### Phase 2: Documentation Management ✅ DONE
**Goal**: Native task doc and SOP management

**Issues**:
- [ ] #988 - Create docs.go for documentation management
- [ ] #989 - Add task and SOP templates

**Files**:
- `internal/executor/docs.go` - Task doc creation, archival
- `internal/executor/templates/task.md` - Task template
- `internal/executor/templates/sop.md` - SOP template

### Phase 3: Context Markers ✅ DONE (scaffolding)
**Goal**: Session checkpoints for work continuity

**Issues**:
- [ ] #990 - Create markers.go for context markers

**Files**:
- `internal/executor/markers.go` - Marker CRUD operations

### Phase 4: Knowledge Management ✅ DONE (scaffolding)
**Goal**: Persistent experiential memory

**Issues**:
- [ ] #991 - Create knowledge.go for experiential memory

**Files**:
- `internal/memory/knowledge.go` - Knowledge graph operations
- SQLite schema updates

### Phase 5: User Profiles ✅ DONE (scaffolding)
**Goal**: Learn user preferences across sessions

**Issues**:
- [ ] #993 - Create profile.go for user preferences
- [ ] #994 - Wire all features into runner.go

**Files**:
- `internal/memory/profile.go` - Profile storage
- `internal/executor/runner.go` - Integration

### Phase 6: Code Simplification ✅ DONE (scaffolding, deferred wiring)
**Goal**: Auto-simplify code before commit (nav-simplify)

**Issues**:
- [ ] #995 - Create simplify.go for code simplification

**Files**:
- `internal/executor/simplify.go` - Simplification engine

### Phase 7: Workflow Enforcement ✅ DONE
**Goal**: WORKFLOW CHECK block before every task

**Issues**:
- [ ] #996 - Add workflow enforcement to prompts

**Files**:
- `internal/executor/workflow.go` - Add enforcement section

### Phase 8: Diagnose/Re-anchor ✅ DONE (scaffolding)
**Goal**: Detect collaboration drift, prompt re-anchoring

**Issues**:
- [ ] #997 - Create diagnose.go for collaboration drift detection

**Files**:
- `internal/executor/diagnose.go` - Drift detection

### Phase 9: Knowledge Graph Query — DEFERRED (GH-998 never landed)
**Goal**: Unified search across all docs (nav-graph)

**Issues**:
- [ ] #998 - Enhance knowledge.go with full-text search and concept indexing

**Files**:
- `internal/memory/knowledge.go` - Enhanced query

### Phase 10: Template Rendering ✅ DONE
**Goal**: Go template rendering for task/SOP docs

**Issues**:
- [ ] #999 - Create templates.go for Go template rendering

**Files**:
- `internal/executor/templates.go` - Template renderer

### Phase 11: Enhanced Workflow Prompts ✅ DONE (already equivalent on main)
**Goal**: Match Navigator's detailed prompt quality

**Issues**:
- [ ] #1000 - Enhance workflow prompts with examples and anti-patterns

**Files**:
- `internal/executor/workflow.go` - Enhanced prompts

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Storage backend | SQLite only, Files only, Both | Both | SQLite for queries + files for git tracking |
| Profile scope | Global, Per-project, Both | Both | Global defaults + project overrides |
| Marker format | JSON, Markdown | Markdown | Human-readable, editable |
| Template engine | text/template, custom | text/template | Standard library, well-known |

---

## Dependencies

**Already Exists (Reuse)**:
- `signal.go` - Signal parsing
- `stagnation.go` - Stagnation monitor
- `complexity.go` - Task complexity detection
- `quality/` - Quality gates
- `intent_judge.go` - Intent verification

**New Files (Create)**:
- `workflow.go` - Embedded prompts
- `docs.go` - Documentation management
- `markers.go` - Context markers
- `knowledge.go` - Knowledge persistence
- `profile.go` - User profiles
- `simplify.go` - Code simplification
- `diagnose.go` - Drift detection
- `templates.go` - Template rendering

---

## GitHub Issues Queue

| # | Title | Phase | Status |
|---|-------|-------|--------|
| 986 | Create workflow.go | 1 | CLOSED — merged PR #1001 |
| 987 | Update runner.go for embedded workflow | 1 | CLOSED — merged PR #1001 |
| 988 | Create docs.go | 2 | CLOSED — merged PR #1004 |
| 989 | Add templates | 2 | CLOSED — merged PR #1007 |
| 990 | Create markers.go | 3 | CLOSED — direct commit 7de71d9e |
| 991 | Create knowledge.go | 4 | CLOSED — direct commit 9c8a2e7b |
| 993 | Create profile.go | 5 | CLOSED — merged PR #1019 |
| 994 | Wire into runner.go | 5 | CLOSED — **NEVER LANDED** (merge conflicts) |
| 995 | Create simplify.go | 6 | CLOSED — direct commit 2a36bdc1 |
| 996 | Workflow enforcement | 7 | CLOSED — direct commit b4d2ad61 |
| 997 | Create diagnose.go | 8 | CLOSED — direct commit 1519b545 |
| 998 | Enhanced knowledge query | 9 | CLOSED — never landed (merge conflicts) |
| 999 | Template rendering | 10 | CLOSED — merged PR #1009 |
| 1000 | Enhanced workflow prompts | 11 | CLOSED — already equivalent on main |
| **1026** | **Wire all scaffolding into pipeline** | **ALL** | **OPEN — consolidated wiring issue** |

---

## Verify

Run these commands to validate:

```bash
# Build
go build ./...

# Tests
go test ./internal/executor/...
go test ./internal/memory/...

# Manual test (without Navigator plugin)
# 1. Uninstall Navigator or use fresh machine
# 2. Create issue with pilot label
# 3. Run: pilot start --github
# 4. Verify phases display correctly
# 5. Verify task completes with EXIT_SIGNAL
```

---

## Done

Observable outcomes that prove completion:

- [ ] Pilot executes tasks without Navigator plugin
- [ ] Phase transitions (INIT→RESEARCH→IMPL→VERIFY→COMPLETE) display
- [ ] pilot-signal JSON parsed correctly
- [ ] Task docs created in .agent/tasks/
- [ ] Context markers saved/restored
- [ ] Knowledge persisted to SQLite + .agent/memories/
- [ ] User preferences applied to prompts
- [ ] Code simplified before commit (if enabled)
- [ ] Workflow check shown before execution
- [ ] All tests pass

---

## Notes

**Key Insight**: Navigator's value is in its PROMPTS and PATTERNS, not its code. Claude Code plugins are just prompt injection. We embed those prompts directly into Pilot.

**Defense in Depth**: Navigator's VERIFY phase (inside Claude) + Pilot's Quality Gates (outside) = redundant verification is intentional.

---

**Last Updated**: 2026-02-13
