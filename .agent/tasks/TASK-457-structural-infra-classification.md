# fix(autopilot): structural infra-failure classification — stop string-matching outage prose, never close a PR on zero evidence

## Problem

On 2026-08-06 a GitHub Actions outage made every job fail at the synthetic `Set up job` step with *"Failed to resolve action download info. Error: Service Unavailable"* — repo code never ran. Autopilot classified this as a **code** failure and took irreversible action: closed a correct PR (#4770), spawned fix work, burned a GH-4756 retry, escalated GH-4771 to `pilot-needs-human`. The operator had to stop the daemon manually.

The infra-classification machinery already exists (TASK-418, v2.246.0, built after the GH-4526 429/504 incident) — it did not fire because `classifyCheckFailure` (`internal/autopilot/failure_class.go:66-89`) matches **four hardcoded log substrings**:

```go
hasInfraSignature := (strings.Contains(logs, "Failed to download action") && strings.Contains(logs, "429")) ||
	(strings.Contains(logs, "##[error]Failed to run:") && strings.Contains(logs, "Unexpected HTTP response: 5")) ||
	strings.Contains(logs, "##[error]The runner has received a shutdown signal") ||
	strings.Contains(logs, "lost communication with the server")
```

This outage's prose matches none of them ("resolve action download info" ≠ "download action"; no `429`; "Service Unavailable" has no digits for the `5xx` matcher). **Adding a fifth string is not the fix** — every future outage has new prose. The classification must rest on structural facts, and the destructive path must require positive evidence.

## Context (verified 2026-08-06, origin/main)

- `mapCheckStatus` (`internal/autopilot/ci_monitor.go:580-599`) recognizes only 6 conclusions (`success`/`failure`/`cancelled`/`timed_out`/`skipped`/`neutral`). **`startup_failure`, `stale`, `action_required` fall to `default: CIPending`** — a completed check with those conclusions reads as forever-pending until the CI wait timeout. They are also absent from the studio-sdk constant list (use local literals; do not block on an SDK release).
- **Live bug that amplifies outages**: `GetFailedChecks` (`ci_monitor.go:666-683`), `GetFailedCheckLogsByCheck` (`:792-833`) and `GetFailedCheckExcerpts` (`:848-871`) all filter `run.Conclusion != ConclusionFailure` — literal `"failure"` only — while `mapCheckStatus` maps `cancelled`/`timed_out` to the same `CIFailure`. Such a PR yields **zero evidence**, and `classifyPRFailure(nil)` returns `FailureClassCode` (`failure_class.go:156-158`) → destructive path with an empty fix issue. Outages produce `cancelled`/`timed_out` in bulk.
- **Unused structural data already fetched**: `JobStep{Name, Status, Conclusion, StartedAt, CompletedAt}` (`internal/adapters/github/jobs.go:17-25`) via `GetWorkflowJob`. Nothing inspects step *names*. GitHub's synthetic setup steps (`Set up job`, `Set up runner`, `Complete job`) bracket repo-defined steps — a job whose failing step is synthetic never ran repo code.
- `isJobsNeverStartedInfra` (`failure_class.go:100-142`, GH-4591) already has the right *shape* (structural: `StepsKnown && StepsCount == 0`) but is scoped to billing-refusal.
- Existing infra path when classification succeeds: `maybeRetryInfraFailure` → `rerunInfraFailures` → `RerunFailedJobs` (`controller.go:2437-2512`), budget `maxInfraRerunBudget = 2` per head SHA, persisted on `PRState`. No delay before the rerun fires.
- Irreversible sites: `ClosePullRequest` at `controller.go:2273` (iteration limit) and `:2399` (after fix-issue creation). Non-destructive rung: `escalateAndHold` (`:4938`).

## Acceptance

1. **Structural signals** added to the classifier, evaluated BEFORE prose matching (prose stays as a supplementary signal, not the basis):
   - Failing step is a GitHub synthetic step (`Set up job` / `Set up runner` / `Complete job`, case-insensitive) → `FailureClassInfra`.
   - Job completed with zero repo-defined steps executed (extend the existing `StepsKnown`/`StepsCount` logic beyond billing).
   - Check-run conclusion is `startup_failure` or `stale` → `FailureClassInfra` (these are never code failures).
2. **Conclusion coverage**: `mapCheckStatus` handles `startup_failure`, `stale`, `action_required` explicitly (no silent `CIPending` fallthrough); document the mapping for each in a comment.
3. **Evidence-filter fix**: the three evidence-gathering functions include every conclusion that `mapCheckStatus` treats as `CIFailure` (`failure`, `cancelled`, `timed_out`, plus new infra ones) so classification never runs blind. Regression test asserting parity between "what counts as CIFailure" and "what evidence gathering collects" — a table both sides read, so they cannot drift again.
4. **THE INVARIANT — never close on zero evidence**: when the aggregate says `CIFailure` but evidence gathering returns nothing (logs unfetchable, no matching check-runs), classify `FailureClassUnknown` (new) and take the NON-destructive path: `escalateAndHold` (or the infra rerun path if budget remains) — never `ClosePullRequest`, never a fix issue. `FailureClassCode` must require positive evidence of a repo-code failure.
5. Metrics/logging: record the classification and which signal produced it (structural-step / conclusion / prose / zero-evidence) so the next incident is diagnosable from logs alone.
6. Tests: table-driven over captured fixtures incl. **this incident's shape** (failing step `Set up job`, "Service Unavailable" text, conclusion `failure`) → must classify infra; `cancelled`-conclusion PR → evidence non-empty; zero-evidence → `Unknown` + no close; genuine test failure → still `FailureClassCode` and destructive path intact (no regression in the common case).
7. `make build` / `make test` / `make lint` green.

## Scope fence

No changes to the rerun budget or its semantics · no daemon-level breaker (that is the sibling task, TASK-458 — this task provides the classification signal it consumes) · no studio-sdk changes (use local conclusion literals) · no changes to the dedup guards in `feedback_loop.go`.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Incident 2026-08-06 (GitHub Actions outage): PR#4770 closed wrongly, GH-4771 escalated, daemon stopped manually ~50 min in
- Prior art: `.agent/tasks/archive/TASK-418-ci-infra-failure-classification.md` (the mechanism this task makes structural), pitfall `ci-infra-failure-misclassified-as-code.md` (signature set now stale), `.agent/tasks/archive/TASK-421-repick-counter-counts-non-failures.md` (dispatcher-level sibling classifier — same `failure_class` vocabulary, keep them consistent)

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4779
