# TASK-503: fix(executor): acceptance-evidence classifier treats "`<allowlisted command>` passes" as a paste-output item

**Status**: ✅ Shipped 2026-09-30 — #5514 → PR#5515 merged 15:05Z, in v2.276.10 (review APPROVE-w-notes)
**Created**: 2026-09-30
**Assignee**: Pilot

---

## Context

**Problem**:
The acceptance-evidence gate only renders a `## Evidence` block for checklist items it can classify. Today the classifier in `internal/executor/acceptance_evidence.go` recognises two shapes: paste-output, triggered by one of the phrases "pasted into the PR body", "paste the output", "terminal output", "evidence in the PR body", "into the PR body", "output line"; and mutation pins, `<change> -> <TestName> fails`. Everything else is classified other and renders nothing.

The most common acceptance phrasing in this repo's own issues is a backticked command followed by an outcome word: "`go test -race ./internal/alerts/` passes", "`make build` succeeds", "`npm test` is green". None of these match. Proof from production on 2026-09-30: issue #5500 was the first task built on v2.276.8 (the first release where criteria reach the gate, #5493). The gate ran, all five acceptance bullets classified other, and PR #5512 carried no `## Evidence` at all. Running ClassifyAcceptanceItem over the bullets confirmed the classification. Third and last cause of "the evidence gate never fires": #5488 (heading), #5493 (criteria dropped), and now authoring dialect.

**Goal**:
An item that names a runnable command in backticks, whose first token is one of the gate's default allowlisted tools, and that asserts a passing outcome, is classified paste-output with that command as the thing to run. The existing phrasings keep working unchanged. Items whose backticked span is not a command (a type name, a file path) stay other even when the sentence says "passes".

---

## Known Pitfalls & Patterns

- **LEARNING** (95%, mem-203): Pilot implements the Acceptance list literally and copies issue-body claims verbatim. Every required file change is listed under Acceptance Criteria below; nothing is asserted that was not verified.
- **PATTERN** (#5486): mutation shape is tested before paste-output so a permissive paste pattern cannot hijack a pin. The new trigger is added inside the paste-output branch, after the mutation check, so ordering is preserved.
- **PITFALL** (#5510, open): negation handling in the mutation regex has a clause-boundary defect. This task does not touch the mutation path.

---

## Acceptance Criteria

- [ ] `go test ./internal/executor/ -run 'AcceptanceItem|AcceptanceEvidence' -count=1` passes; paste the output into the PR body.
- [ ] New table-driven test TestClassifyAcceptanceItem_CommandPassesIsPasteOutput in `internal/executor/acceptance_evidence_classify_test.go`: each of "`go test -race ./internal/alerts/` passes", "`make build` succeeds", "`npm test` is green", "`go test ./internal/alerts/` exits 0", "`cargo test` passes and `internal/alerts/types.go` gets two rows" classifies paste-output, and in the last case the extracted commands contain only the cargo span, not the file path.
- [ ] Same test, negative rows: "`Store` passes the org id through", "`internal/alerts/types.go` default-rule table covers the two new types", "`go vet ./...`" (no outcome word), "CI green", "tests pass" all classify other.
- [ ] Remove the new command-passes trigger from ClassifyAcceptanceItem -> TestClassifyAcceptanceItem_CommandPassesIsPasteOutput fails.
- [ ] Existing tests TestClassifyAcceptanceItem_MutationPhrasings, TestClassifyAcceptanceItem_PasteOutputEvidencePhrasings and TestClassifyAcceptanceItem_MutationBeatsPasteOutput pass with no edits to their tables.
- [ ] The doc comment on pasteOutputPatternRe in `internal/executor/acceptance_evidence.go` names the new trigger and the reason (PR #5512, 2026-09-30).
- [ ] `configs/pilot.example.yaml`, in the acceptance_evidence comment block, gains one comment line stating that a backticked allowlisted command followed by passes / succeeds / is green / exits 0 is run and pasted.

---

## Implementation

### Phase 1: Classifier trigger
**Goal**: recognise the command-passes shape without widening the existing phrase list.

**Tasks**:
- [ ] Add a regexp for the outcome words, whole-word, case-insensitive: passes, pass, succeeds, is green, green, exits 0, exit code 0, returns 0.
- [ ] Add a helper that returns the inline code spans whose first whitespace-delimited token is in DefaultAcceptanceEvidenceAllowedCommands() (defined in `internal/executor/backend.go`). The classifier stays pure; it uses the default list only for shape detection. The runtime allowlist is still enforced later by the runner, which already reports "command not in allowlist".
- [ ] In ClassifyAcceptanceItem, after the mutation check and after the existing pasteOutputPatternRe branch: if the outcome regexp matches and the helper returns at least one span, set Kind to paste-output and Commands to exactly those spans.
- [ ] Keep extractInlineCommands as the command source for the existing phrase-triggered branch; do not change its behaviour.

**Files**:
- `internal/executor/acceptance_evidence.go` - trigger, helper, doc comment
- `internal/executor/acceptance_evidence_classify_test.go` - new table test

### Phase 2: Docs
**Tasks**:
- [ ] One comment line in `configs/pilot.example.yaml` under the acceptance_evidence block.
- [ ] One sentence in the acceptance-evidence section of the configuration page under docs/content (search for "acceptance_evidence" to find it) listing the recognised shapes: phrase-triggered paste, command-passes, mutation arrow.

---

## Out of Scope

- The negation clause-boundary defect in the mutation regex (#5510, in review).
- The optional Jev classifier (#5509). This task is the regex floor that #5509 falls back to.
- Widening pasteOutputPatternRe with more free-text phrases; the command-passes trigger is anchored on a backticked allowlisted command, which is what keeps it from misfiring.
- Any change to the runner, the allowlist, redaction, or the PR-body renderer.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Anchor for the new trigger | any inline code span + outcome word; allowlisted first token + outcome word; new phrase list | allowlisted first token + outcome word | "`Store` passes the org id" must stay other; a phrase list is the trap #5479 already fell into |
| Which list to check against | runtime allowlist (signature change); default allowlist constant | default constant | keeps ClassifyAcceptanceItem pure and every existing test valid; runner still enforces the configured list |
| Commands recorded | all inline spans in the item; only qualifying spans | only qualifying spans | a file path in the same sentence must not be executed or reported as not-in-allowlist noise |
| Placement | before paste phrase branch; after it | after it, before other | phrase-triggered items keep their current Commands extraction byte-for-byte |

---

## Verify

```bash
go test ./internal/executor/ -run 'AcceptanceItem|AcceptanceEvidence' -count=1
go vet ./internal/executor/
```

---

## Done

- [ ] "`go test -race ./internal/alerts/` passes" classifies paste-output with one command.
- [ ] "`Store` passes the org id through" classifies other.
- [ ] Removing the trigger fails exactly the new test; nothing else changes.
- [ ] The first Pilot PR built after this merges shows a `## Evidence` block for a plain "`go test …` passes" bullet.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5514
- #5512 / #5500: the production run that proved the gap (gate ran, five bullets classified other).
- #5435 gate · #5438 description half relaxed · #5479 phrase recall · #5486 mutation precedence · #5488 heading · #5493 criteria carried · #5506 / PR #5510 negation · #5509 optional Jev classifier.
- SOP: `.agent/sops/onboarding/new-project-issue-authoring.md` Rule 5b (evidence dialect for authors).

---

**Last Updated**: 2026-09-30
