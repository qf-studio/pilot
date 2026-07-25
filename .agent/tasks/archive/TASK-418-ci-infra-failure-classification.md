# TASK-418: CI infra-failure classification — retry transient GitHub outages instead of closing PRs

**Status**: ✅ SHIPPED — merged and released in v2.246.0 (2026-07-25)
**Created**: 2026-07-24
**Assignee**: Pilot

---

## Context

**Problem**:
`handleCIFailed` (internal/autopilot/controller.go:1977) trusts
`ConclusionFailure` blindly. GitHub reports `conclusion: failure` identically
whether golangci-lint found real violations or the runner infrastructure died
before the linter ever ran. On 2026-07-24, PR #4529 (GH-4526 attempt 2) had
every real check green — test, drift gate, secrets, shellcheck — but the lint
job's `actions/checkout` download got a 429 and the golangci-lint action
download got an HTTP 504. Autopilot classified this as a code failure: closed
the PR, discarded a correct branch, spawned garbage fix issue #4530,
incremented the repick counter toward the hard cap that put GH-4526 into
`pilot-blocked`, and polluted failure metrics (run 30108571707, job
89532043976).

**Goal**:
Before treating a failed check as a code failure, classify it. Infra-level
failures get a bounded automatic re-run of the failed jobs while the PR stays
in `waiting_ci`. Only real code failures (or exhausted retries) fall through
to the existing close-PR / spawn-fix-issue path. All CI verdicts and PR
failures carry a failure class so metrics can exclude infra noise.

---

## Acceptance Criteria

- [ ] A failed check whose job logs match a conservative infra-signature set
      is classified `infra`, the failed jobs are re-run via the GitHub API,
      and the PR returns to `waiting_ci` — no PR close, no fix issue, no
      repick increment.
- [ ] Infra re-runs are bounded (2 per head SHA); a new push resets the
      budget. Exhausted budget falls through to the existing code-failure
      path with the reason recorded.
- [ ] Classification is fail-safe: log fetch error, empty logs, or no
      signature match → status quo (code failure). Never blocks on
      classification.
- [ ] `pilot_ci_runs_total` distinguishes `fail` from `infra_fail` (and
      counts `infra_retry` attempts); PR-failure recording carries a
      `failure_class` (`code` | `infra`).
- [ ] Table-driven tests cover: 429 action-download log, 504 golangci-lint
      log, real errcheck lint log (must classify `code`), empty logs, log
      fetch error, retry-budget exhaustion, budget reset on new SHA.

---

## Implementation

### Phase 1: Classifier
**Goal**: Given a failed check run, decide `code` vs `infra` from job logs.

**Tasks**:
- [ ] New `classifyCheckFailure(logs string) FailureClass` in
      `internal/autopilot/` — pure function, table-driven tests.
- [ ] Conservative signature set (match any ⇒ `infra`):
      - `Failed to download action` + `429`
      - `##[error]Failed to run:` + `Unexpected HTTP response: 5`
      - `##[error]The runner has received a shutdown signal`
      - `lost communication with the server`
- [ ] A job that also contains real annotations on repo files (e.g.
      `\.go:\d+:\d+:` lint lines) is `code` even if an infra line appears —
      real findings win.
- [ ] Reuse the log-fetch path already used by `GetFailedCheckLogs`
      (internal/autopilot/ci_monitor.go:581 → `ghClient.GetJobLogs`,
      internal/adapters/github/client.go:770). Add a `CIMonitor` method
      returning per-failed-check logs scoped by `isScopedCheck` (same
      scoping as `GetFailedChecks`, GH-4307).

### Phase 2: Bounded re-run in handleCIFailed
**Goal**: Infra classification re-runs failed jobs instead of closing the PR.

**Tasks**:
- [ ] gh client: `GetWorkflowRunIDForJob(ctx, owner, repo, jobID)` —
      `GET /repos/{o}/{r}/actions/jobs/{job_id}` → `.run_id` (check-run ID ==
      Actions job ID; see internal/adapters/github/jobs.go:16).
- [ ] gh client: `RerunFailedJobs(ctx, owner, repo, runID)` —
      `POST /repos/{o}/{r}/actions/runs/{run_id}/rerun-failed-jobs`.
- [ ] `handleCIFailed`: classify FIRST, before `NotifyCIFailed` and the
      iteration/size guards. If every scoped failed check classifies `infra`
      and retry budget remains: rerun failed jobs (dedupe run IDs), increment
      the counter, set `prState.Stage = StageWaitingCI`, log at Warn, return.
      Mixed infra+code ⇒ treat as code (the code failure is real).
- [ ] Retry budget on `PRState`: `InfraRerunCount int` +
      `InfraRerunSHA string`; reset count when `HeadSHA != InfraRerunSHA`.
      Cap = 2. Persist with the rest of PRState (autopilot_pr_state) so a
      daemon restart can't grant a fresh budget mid-loop.
- [ ] Exhausted budget: fall through to existing path with
      `prState.Error` noting `infra retries exhausted (2/2)`.

### Phase 3: Failure-class metrics
**Goal**: Metrics separate infra noise from code failures.

**Tasks**:
- [ ] `RecordCIRun` result vocabulary gains `infra_retry` (recorded on each
      re-run) and `infra_fail` (recorded when budget exhausts). Existing
      `fail` remains code failures only.
- [ ] PR-failure recording (internal/autopilot/metrics.go:242
      `RecordPRFailed`) gains a class: either `RecordPRFailedClass(class)`
      keyed map or equivalent — dashboard/hydration callers updated
      (see `HydrateCIRun`, metrics.go:259, for the restart-hydration
      pattern that must stay consistent).

**Files**:
- `internal/autopilot/controller.go` — handleCIFailed classification gate
- `internal/autopilot/ci_monitor.go` — scoped per-check log fetch
- `internal/autopilot/failure_class.go` (new) — classifier + signatures
- `internal/autopilot/types.go` — PRState fields
- `internal/autopilot/metrics.go` — class-tagged counters
- `internal/adapters/github/client.go` — run-ID lookup + rerun-failed-jobs

---

## Out of Scope

- Repick-counter separation for environment/preflight failures (the
  self-blocking deadlock class seen on GH-4526) — separate issue.
- Auto-closing superseded `autopilot-fix` issues (#4528/#4530 class) —
  separate issue.
- Retrying `timed_out` / `cancelled` conclusions — only `failure` with
  infra-signature logs is in scope.
- Any change to fix-issue body format or the iteration counter (GH-1566).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Classification signal | GitHub annotations API; step-name heuristics; log signature match | Log signatures | Logs are already fetched by autopilot (`GetJobLogs`); 429/504 signatures are exact strings from the observed incident; annotations are empty for infra deaths so they can't distinguish alone |
| Retry mechanism | Re-run failed jobs; re-run whole workflow; push empty commit | `rerun-failed-jobs` API | Cheapest, preserves green check results, no history pollution |
| Retry budget scope | Per PR lifetime; per head SHA | Per head SHA (cap 2) | New push = new code = fresh budget; per-lifetime budget starves long-lived PRs |
| Mixed infra+code failures | Retry anyway; treat as code | Treat as code | The code failure is real and actionable now; retrying only delays the fix issue |
| Fail-safe default | Unknown ⇒ infra; unknown ⇒ code | Unknown ⇒ code (status quo) | Misclassifying code as infra wastes 2 reruns then proceeds; but defaulting-to-infra on fetch errors could loop every flake through retries and mask real failures |

---

## Verify

```bash
make build
go test ./internal/autopilot/... ./internal/adapters/github/...
make lint
```

---

## Done

- [ ] `classifyCheckFailure` exists with table-driven tests covering the
      acceptance-criteria matrix.
- [ ] Replaying the GH-4526 scenario in a test (green real checks + lint job
      log containing the 429/504 signatures) yields: rerun called, stage
      `waiting_ci`, no fix issue, no PR close.
- [ ] Real errcheck failure log still produces the existing fix-issue path.
- [ ] `pilot_ci_runs_total` exposes `infra_retry`/`infra_fail`; PR failures
      carry `failure_class`.
- [ ] `make build`, `make lint`, full test suite green.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4531
- Incident: GH-4526 / PR #4527 (real lint fail) / PR #4529 (infra false
  negative) — run 30108571707 job 89532043976 (`429` + `504`), run
  30107079816 (real `errcheck` at preflight_test.go:165).
- GH-4307 — check scoping (`isScopedCheck`) that classification must respect.
- GH-1567 — `GetFailedCheckLogs` log-fetch precedent.
- GH-3806 — externally-visible close reasons; exhausted-retry reason follows
  the same convention.

---

**Last Updated**: 2026-07-25
