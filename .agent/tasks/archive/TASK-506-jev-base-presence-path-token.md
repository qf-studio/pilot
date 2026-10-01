# TASK-506: feat(executor): Jev classifier for backticked path spans in the base-presence gate, shadow mode, regex floor kept

**Status**: ✅ SHIPPED 2026-09-30 23:18Z → PR#5546 (merged by autopilot), post-merge review 10-01 APPROVE-w-notes (5 mutation pins; live smoke 5/5 at 0.99–1.00). Box config block staged 10-01 (shadow); activates with the 10-01 train. Follow-up #5548 (per-item debug lines, shared with gate 1).

## Context

The base-presence gate holds a task when a backticked span in the issue body looks like a repo path and is missing from the default branch. The extractor is `ExtractReferencedPaths` in `internal/executor/dependency_detector.go`; the probe and hold are in `internal/executor/base_presence.go`; the dispatcher call site is in `internal/executor/dispatcher.go`. History: 165 holds across 10 tasks and two today (GH-5509 new-file paths in a task doc, GH-5527 the git range origin/main..HEAD with a dot after the slash). Every one was a span that is not an existing prerequisite. A false skip only removes a hold; a real missing prerequisite still fails loudly in the executor.

Gate 1 (TASK-505) shipped the shared client in `internal/typesafe`, the config block, shadow semantics and the per-item question rule (pitfall jev-identical-questions-return-identical-answers).

## Acceptance Criteria

- [ ] For each extracted span the classifier asks Jev one choice question whose instruction names the span index and quotes the span with one sentence of surrounding body text (redacted and capped): `existing_prerequisite` / `to_be_created` / `not_a_repo_file`.
- [ ] Shadow mode (default): the gate behaves exactly as today; one per-task info line logs `spans`, `agreed`, `would_skip`, `low_confidence`, `errors`, `latency_ms`.
- [ ] Live mode (`shadow: false`): a span Jev classifies `to_be_created` or `not_a_repo_file` at or above `min_confidence` is removed from the probe list; `existing_prerequisite`, low confidence, missing or invalid answers, and any Ask error keep today's behaviour.
- [ ] Only span text plus one sentence of context leaves the machine, through the existing redaction; never the whole body.
- [ ] Default config never constructs a client; provider `jev` without `TYPESAFE_API_KEY` warns once and stays regex.
- [ ] Config lives under the existing block as `executor.base_presence.classifier` with `provider`, `min_confidence`, `shadow`, mirroring the acceptance-evidence block.
- [ ] Mutation pins: delete the Ask-error fallback and a test fails; delete the confidence comparison and a test fails; make the instruction a shared constant and a test fails.
- [ ] Live smoke before merge: five hand-labelled spans (one real file, one new file in prose, one git range, one home-dir path, one Go type) classify correctly at confidence above 0.9.

## Implementation

One PR. Add a `basePresenceClassifier` beside the acceptance classifier, reusing `typesafe.RedactAndCap`, `typesafe.Resolve` and the stats shape from `internal/executor/acceptance_classifier.go`. Wire it at the dispatcher call site between extraction and `Check`. Add the config sub-block and accessors in `internal/executor/backend.go`. Document in the configuration page under the existing TypeSafe section. Tests use a fake asker that records questions.

## Out of Scope

Gate 3 (CI failure class). Replacing the regex extractor. Console-side key delivery.

## Verify

- `go test ./internal/executor/ -run 'BasePresence|Classifier' -count=1` passes.
- `go vet ./internal/executor/` clean.
- Live smoke output pasted in the PR body.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5543

- Roadmap: `.agent/system/jev-gates-roadmap.md` gate 2
- TASK-505 (gate 1), pitfall jev-identical-questions-return-identical-answers
- Rule 3b/3c in `.agent/sops/onboarding/new-project-issue-authoring.md`
