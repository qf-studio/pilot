> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-307: `Rework` lane — clean reset on review-changes-requested

**Status**: queued
**Created**: 2026-05-26
**Severity**: P0
**Effort**: M (~4h)
**Job (JTD)**: J4 Course-correct
**Source**: Symphony research, Wave 5 / `~/.claude/plans/let-s-plan-that-use-staged-seal.md`

---

## Context

**Problem**: When a human reviewer requests changes on a Pilot PR, today's flow falls into `review_requested` (PRStage enum, `internal/autopilot/types.go:366-388`) and the agent incrementally patches the existing PR. Patching a rotten plan is the #1 source of bad autonomous PRs — the agent compounds confusion instead of replanning.

**Goal**: A `Rework` lane that, on review-changes-requested, executes a **clean reset**:
1. Close the PR (no merge).
2. Delete or archive the Workpad (TASK-306 dependency where shipped).
3. Create a fresh branch from `main`.
4. Re-run the planning phase — the agent sees the reviewer's feedback as new input, not as a patch instruction.

Borrowed from Symphony's `Rework` lane (`/tmp/symphony/elixir/WORKFLOW.md` lines 251–260).

**Why now**: Trust gap. Adjacent to Wave 4 ghost-SHA / orphan-PR fixes (TASK-300, TASK-302). Without `Rework`, the existing `review_requested` stage drives agents into compounding-mistake loops.

---

## Acceptance Criteria

- [ ] When reviewer comments "needs rework" (configurable trigger phrase) **or** transitions PR via explicit Pilot label `pilot-rework`, the autopilot closes the PR.
- [ ] Branch is deleted/archived; new branch is created from latest `origin/main`.
- [ ] Reviewer's most recent comments are passed to the agent as planning input, not as a patch instruction.
- [ ] `autopilot_pr_state.stage` records the rework cycle (new enum value: `reworking`).
- [ ] Limited to N rework cycles per issue (configurable, default 2) — beyond that, escalate to human-required.
- [ ] No data loss: prior PR description + Workpad archived for audit.

---

## Implementation

### Phase 1: PRStage extension
**Tasks**:
- [ ] Add `reworking` to `PRStage` enum in `internal/autopilot/types.go`.
- [ ] Update `AllPRStages()` and Prometheus zero-value gauges.
- [ ] Add transition: `review_requested → reworking → pr_created` (new cycle).
- [ ] Add rework-cycle counter column to `autopilot_pr_state` (migration).

**Files**:
- `internal/autopilot/types.go`
- `internal/autopilot/state_store.go` (migration + counter column)

### Phase 2: Trigger detection
**Tasks**:
- [ ] In autopilot reviewer-feedback handler, detect rework-trigger (configurable phrase + label).
- [ ] Add config field `autopilot.rework_trigger_phrase` (default `"rework"`) and `autopilot.rework_label` (default `"pilot-rework"`).

**Files**:
- `internal/autopilot/feedback.go` (or equivalent reviewer-comment scanner — verify exact path during impl)
- `internal/config/` (YAML schema)

### Phase 3: Reset sequence
**Tasks**:
- [ ] Implement `Rework()` in autopilot:
  1. Close PR via adapter (GitHub/GitLab/etc.) — non-merge close.
  2. Archive Workpad comment (rename marker to `<!-- pilot-workpad:archived-{cycle} -->`).
  3. Delete or rename old branch to `pilot/archive/{branch}-{cycle}`.
  4. Create new branch from `origin/main`.
  5. Re-enqueue issue with `from_pr: <old_pr_number>` + `rework_feedback: <reviewer_comments>` so executor prompt includes feedback as planning input.
- [ ] Cap at `autopilot.max_rework_cycles` (default 2). Beyond that, label issue `pilot-needs-human`.

**Files**:
- `internal/autopilot/rework.go` (new)
- `internal/autopilot/state_machine.go` (wire transitions)

### Phase 4: Executor prompt awareness
**Tasks**:
- [ ] Executor prompt template recognizes `rework_feedback` field and frames it as "Reviewer feedback on prior attempt" rather than "patch this PR".

**Files**:
- `internal/executor/prompt_builder.go` (or current prompt-construction site)

---

## Out of Scope

- Auto-detection of "this rework is going nowhere, escalate" beyond a hard cycle cap.
- ML-based reviewer-intent classification (use configurable phrase + label for v1).
- Cross-issue rework correlation (no learning across reworks in v1; deferred to J7 / future).

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Trigger mechanism | Phrase-only, label-only, both | Both (OR) | Label = explicit; phrase = lower-friction |
| Branch handling | Delete, archive-rename, keep | Archive-rename (`pilot/archive/...`) | Preserves audit trail; restorable if cycle cap hit |
| Cycle cap | None, 1, 2, 3, configurable | Default 2, configurable | Empirically: 2 rework attempts before human is right |
| Feedback to executor | Patch list, free-text, structured | Free-text from reviewer comments, framed as planning input | Avoids the "patch this" anchor |

---

## Files Affected (estimate)

- `internal/autopilot/types.go`
- `internal/autopilot/state_store.go`
- `internal/autopilot/rework.go` (new)
- `internal/autopilot/feedback.go`
- `internal/autopilot/state_machine.go`
- `internal/executor/prompt_builder.go`
- `internal/config/` (YAML schema)

---

## Verify

```bash
go test ./internal/autopilot/...
go test ./internal/executor/...

# E2E: file a Pilot ticket on a test repo, open PR, comment "needs rework" on the PR,
# verify branch archived + new branch from main + new PR opened + cycle counter incremented.
make test
```

---

## Done

- [ ] `reworking` PRStage exists + persisted + observable in dashboard
- [ ] Rework triggers on configured phrase or label
- [ ] Old branch archived, new branch from main, new PR opened, Workpad reset
- [ ] Cycle cap enforced; `pilot-needs-human` label applied at cap
- [ ] E2E run on test repo demonstrates full reset
- [ ] No regression on incremental-fix path (when rework NOT triggered, behavior unchanged)

---

## Refs

- Master plan: `~/.claude/plans/let-s-plan-that-use-staged-seal.md`
- Symphony evidence: `/tmp/symphony/elixir/WORKFLOW.md` lines 251–260
- Wave 4 adjacent: `TASK-300`, `TASK-301`, `TASK-302`
- Related: `TASK-306` (Workpad archive on reset)

---

**Last Updated**: 2026-05-26
