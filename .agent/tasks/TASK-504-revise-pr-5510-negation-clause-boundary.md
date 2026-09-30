# TASK-504: fix(executor): revise PR #5510 — negation regex must not reject positive pins with an unrelated earlier "not"

**Status**: 🚧 In Progress
**Created**: 2026-09-30
**Assignee**: Pilot

---

## Context

**Problem**:
PR #5510 (issue #5506, branch pilot/GH-5506, head 431b6908c) adds mutationNegatedFailsRe to `internal/executor/acceptance_evidence.go` so "-> TestFoo must not fail" is no longer classified as a mutation pin. The review on the PR (verdict REQUEST-CHANGES) found the clause boundary too wide: the regex at line 114 bounds a clause only by "." or ";", so any negation cue earlier in the outcome half rejects a POSITIVE pin. Verified on the branch with ClassifyAcceptanceItem:

- "delete the guard -> TestFoo, not TestBar, fails" classifies other (must be mutation)
- "delete the guard -> does not panic and TestFoo fails" classifies other (must be mutation)
- "delete the guard -> when no config is set, TestFoo fails" classifies other (must be mutation)
- "delete the guard -> TestFoo must not fail" classifies other (correct)
- "delete the guard -> TestFoo no longer fails" classifies other (correct, no test case yet)

A real pin silently downgraded to other weakens the gate in the opposite direction of #5506. Two smaller notes from the same review: widening mutationFailsRe to "failing" makes "-> TestFoo is failing" a mutation (acceptable if intended, needs the doc comment to say so), and "no longer fails" has no test case.

**Goal**:
Tighten the negation check so a cue only counts when it governs the fail word, add positive controls for the three inputs above, and push the fix onto the existing PR branch. No new PR.

**Branch instruction**: check out pilot/GH-5506 at its head (431b6908c), commit on top, push to the same branch. Do not open a new PR; PR #5510 is the PR. It is a draft on purpose and will be marked ready after this revision lands.

---

## Acceptance Criteria

- [ ] `go test ./internal/executor/ -run 'AcceptanceItem|AcceptanceEvidence' -count=1` passes; paste the output into the PR body.
- [ ] TestClassifyAcceptanceItem_MutationPhrasings in `internal/executor/acceptance_evidence_classify_test.go` gains positive controls: "delete the guard -> TestFoo, not TestBar, fails", "delete the guard -> does not panic and TestFoo fails", "delete the guard -> when no config is set, TestFoo fails" each classify mutation with target TestFoo.
- [ ] TestClassifyAcceptanceItem_NegatedOutcomeIsNotMutation gains "-> TestFoo no longer fails" as a negated (not mutation) case; all existing rows in that test keep passing.
- [ ] Restore the negation clause boundary to the current "[^.;]*" form -> TestClassifyAcceptanceItem_MutationPhrasings fails.
- [ ] The doc comment on mutationFailsRe in `internal/executor/acceptance_evidence.go` states that "failing" is matched on purpose and gives one example.
- [ ] No change to ClassifyAcceptanceItem ordering, no new AcceptanceItemKind, no change outside `internal/executor/acceptance_evidence.go` and `internal/executor/acceptance_evidence_classify_test.go`.

---

## Implementation

### Phase 1: Tighten the negation check
**Tasks**:
- [ ] Replace the wide clause with one of: (a) clause boundaries that include "," and the conjunctions "and", "but", "when", "that"; or (b) require the cue adjacent to the fail word: "not"/"never"/"n't" followed by an optional auxiliary ("fail", "must not fail", "does not fail", "doesn't fail", "never fails", "should not fail"), plus the two-word forms "no test fails", "no longer fails", "without failing". Option (b) is preferred: it cannot be broken by a longer sentence.
- [ ] Keep the positive-side regex mutationFailsRe as is (including "failing"); document it.

### Phase 2: Tests and push
**Tasks**:
- [ ] Add the table rows listed under Acceptance Criteria.
- [ ] Commit on pilot/GH-5506 and push; do not create a PR.

**Files**:
- `internal/executor/acceptance_evidence.go` - regex + doc comment
- `internal/executor/acceptance_evidence_classify_test.go` - table rows

---

## Out of Scope

- The command-passes paste-output trigger (TASK-503, separate issue).
- Any change to the runner, renderer, or allowlist.

---

## Refs

- PR #5510 review (REQUEST-CHANGES) and issue #5506 (original spec).
- #5486 mutation-before-paste ordering · #5438 · #5479.

**Last Updated**: 2026-09-30

<!-- autopilot-meta branch:pilot/GH-5506 pr:5510 iteration:1 sha:431b6908ce1af53acbb9c9efd4e688df2112defe -->
