# TASK-507: feat(autopilot): Jev classifier for CI failure class behind the regex floor, shadow mode, override only toward retry

**Status**: 🚀 Dispatched 2026-10-01 as [#5555](https://github.com/qf-studio/pilot/issues/5555) (`pilot`, `no-decompose`), queued behind #5553 and #5554

## Context

`classifyPRFailure` in `internal/autopilot/failure_class.go` reads each failed check's logs and annotation text (`FailedCheckLog` in `internal/autopilot/ci_monitor.go`) and returns `code`, `infra` or `infra_billing`; `code` is the fail-safe default for anything ambiguous. The controller consumes it at two sites in `internal/autopilot/controller.go` (pre-merge CI failure and post-merge CI failure) through `newCIFailureVerdict`. Incident history: infra outages read as `code` closed correct PRs and spawned fix issues (#4526, #4533, #4591, #4779, #4791). Gates 1 and 2 shipped the shared client in `internal/typesafe`, the per-item question rule, shadow stats and the config shape; this is the first consumer outside `internal/executor`.

## Acceptance Criteria

- [ ] One Ask per classification with one choice question per failed check; each instruction names the check index and check name and quotes a redacted, capped excerpt: the annotation text plus the last 1500 characters of the job log (failures sit at the tail), never the full log. Choices: `code` / `infra` / `infra_billing` / `unknown` with one-sentence rubrics taken from the comments on the `FailureClass` constants.
- [ ] Shadow mode (default): behaviour unchanged; one info line per classification logs `checks`, `agreed`, `would_override`, `low_confidence`, `errors`, `latency_ms`, plus one debug line per check with the redacted excerpt head, `jev_choice`, `confidence`, `reason`.
- [ ] Live mode: Jev may only move a regex `code` verdict to `infra` or `infra_billing` at or above `min_confidence` (the retry direction). `infra` → `code`, any `unknown`, low confidence, invalid answers and Ask errors keep the regex verdict. The verdict's `source` names the classifier so downstream evidence stays honest.
- [ ] Config under `orchestrator.autopilot.ci_failure.classifier` with `provider`, `min_confidence`, `shadow`, same accessors and defaults as the executor blocks; the top-level `typesafe:` block must reach the autopilot config in `internal/config/config.go` the same way it reaches the executor, with a test pinning it.
- [ ] Default config never constructs a client; provider `jev` without `TYPESAFE_API_KEY` warns once and stays regex.
- [ ] Mutation pins: delete the Ask-error fallback; delete the confidence comparison; allow the `infra` → `code` direction; make the instruction a shared constant. Each must fail a named test.
- [ ] Live smoke before merge with four labelled excerpts: a Go compile error, a runner-lost outage, a billing refusal message, an empty log. Paste the per-check output in the PR body.

## Implementation

One PR. Add `ci_failure_classifier.go` beside `failure_class.go`, reusing `typesafe.RedactAndCap`, `typesafe.Resolve` and the stats shape from `internal/executor/acceptance_classifier_jev.go`. Wrap both `classifyPRFailure` call sites in the controller through one method that applies the classifier when configured. Document in the configuration page under the existing TypeSafe section.

## Out of Scope

Replacing the regex classifier. Retry policy changes. Gate 4 and later.

## Verify

- `go test ./internal/autopilot/ ./internal/config/ -run 'CIFailure|Classifier|TypeSafe' -count=1` passes.
- `go vet ./internal/autopilot/ ./internal/config/` clean.

## Refs

- Roadmap: `.agent/system/jev-gates-roadmap.md` gate 3
- TASK-505, TASK-506 (archived), pitfall jev-identical-questions-return-identical-answers
