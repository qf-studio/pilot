# GH-1387 + GH-1388: Pilot-Navigator Context Bridge

**Status**: Pending
**Created**: 2026-02-16

## Problem

Pilot executes tasks without loading Navigator's accumulated project knowledge (.agent/ docs), and never writes back meaningful documentation after execution. A human with `/nav-start` gets key files, feature matrix, SOPs, architecture — Pilot gets none of it.

## Goal

Close the context gap so Pilot has the same project awareness as a Navigator-equipped developer, and writes back useful documentation after shipping features.

## Implementation Plan

### Issue 1: Pre-Execution Context Loading (GH-1387)

| Phase | What | Files |
|-------|------|-------|
| 1 | `loadProjectContext()` — extract key sections from DEVELOPMENT-README.md | `prompt_builder.go` |
| 2 | Update INIT phase to instruct reading `.agent/` docs | `workflow.go` |
| 3 | `findRelevantSOPs()` — keyword-match task against SOP filenames | `prompt_builder.go` |
| 4 | Tests | `prompt_builder_test.go` |

### Issue 2: Post-Execution Docs Update (GH-1388)

| Phase | What | Files |
|-------|------|-------|
| 1 | Add DOCUMENT phase (4.5) between VERIFY and COMPLETE | `workflow.go` |
| 2 | `UpdateFeatureMatrix()` — append to FEATURE-MATRIX.md for feat tasks | `docs.go` |
| 3 | Enrich knowledge store + richer context markers | `runner.go` |
| 4 | Tests | `docs_test.go` |

## Technical Decisions

| Decision | Chosen | Reasoning |
|----------|--------|-----------|
| Context in prompt vs lazy-load | Hybrid | Key sections in prompt (~2k tokens), instruct INIT to read more on-demand |
| SOP inclusion | Paths only | Full SOPs would bloat prompt; paths let Claude read during RESEARCH |
| Feature matrix update trigger | `feat(*)` commit prefix | Skip for fix/refactor/docs — only features matter |
| Token budget | +2,000 tokens | Current 5k → 7k total, 121k remaining for execution |

## Dependencies

- GH-1388 depends on GH-1387 (workflow.go changes must land first)

## Done

- [ ] GH-1387 merged (pre-execution context)
- [ ] GH-1388 merged (post-execution docs)
- [ ] Manual test: Pilot's prompt includes project context
- [ ] Manual test: FEATURE-MATRIX.md updated after feature task
