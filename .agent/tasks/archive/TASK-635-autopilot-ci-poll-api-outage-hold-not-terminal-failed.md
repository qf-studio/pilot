# TASK-635: An API outage during the CI poll is a hold with a redrive, not a terminal `failed` stage

**Status**: 📦 **ARCHIVED 11-08 — H1 PR#6059 + fix PR#6062 released in v2.312.0.** ✅ **H1 SHIPPED in v2.312.0 — [pilot#6056](https://github.com/qf-studio/pilot/issues/6056) → [PR#6059](https://github.com/qf-studio/pilot/pull/6059) merged 11-04 13:07Z; post-merge REQUEST-CHANGES ([blocking] the outage-budget `StageFailed` was set inside `redriveAPIOutageHeldPR` before `ProcessPR`, which exits on a terminal stage and never persisted it; fixture had no state store) → [#6060](https://github.com/qf-studio/pilot/issues/6060) → [PR#6062](https://github.com/qf-studio/pilot/pull/6062) merged 13:54Z (APPROVE-WITH-NOTES). Box on it since 15:11Z. Candidate for archive after one real outage proves the redrive.** Was: 🚀 **H1 DISPATCHED 2026-11-04 11:4xZ → [pilot#6056](https://github.com/qf-studio/pilot/issues/6056)** (`pilot` + `no-decompose`). Incident: PR#6055 (GH-6051), hand-merged 11:35Z.
**Created**: 2026-11-04
**Assignee**: Pilot

---

## Context

**Incident (box log 2026-11-04):** PR#6055 (the review-minor fix for PR#6049) registered with autopilot at 11:13:50Z and moved to `waiting_ci` at 11:14Z. From 11:17:13Z to 11:17:58Z the CI status poll got `API error (status 500)` five times in a row from GitHub (the same outage rejected a docs push from the laptop at that minute). At the fifth error the controller set `StageFailed` with `TerminalLabel = pilot-failed` and logged "PR transitioned to failed due to consecutive API failures". The PR's only CI run finished green at 11:28Z. Nothing brought the PR back: `ProcessPR` never leaves `failed`, and the two redrives in `processAllPRs` (rebase hold, base retarget) do not apply. The founder saw "6055 failed on CI" on the board; CI had never failed. Resolution by hand: squash merge, the external-merge path closed #6051 with `pilot-done`.

**Verified on main (`internal/autopilot/controller.go`, 2026-11-04):** the branch near line 3552 inside the waiting-CI check increments `ConsecutiveAPIFailures` on every CheckCI error, and at 5 sets `Stage = StageFailed`, a free-text `Error`, and `TerminalLabel = pilot-failed` (GH-4855 made it terminal so an external close could not re-dispatch the issue). Below 5 it returns and retries on the next poll with no backoff. The poll interval is about 10 s, so a 45 s outage is enough. `PRState` has the hold booleans `RebaseHoldActive` and `BreakerHoldActive` (`internal/autopilot/types.go` near lines 1449 and 1474) and `ConsecutiveAPIFailures` (near line 1224). Revival of a failed PR is a flag-gated redrive in `processAllPRs` (near line 10256) that runs before `ProcessPR`: `reAdoptHeldRebasePR` (near line 8483) and `redriveFailedPRForBaseRetarget` (near line 8573).

**Why it matters:** every GitHub incident longer than a minute turns every PR in `waiting_ci` into a terminal failure with a label that blocks re-dispatch, and each one needs a human merge. The project rule for this (memory `autopilot-failed-stage-revival-is-a-flag-gated-redrive-before-processpr`): a new recoverable failure class gets a purpose-built hold boolean set only at its failure site, and its own redrive in `processAllPRs`; never a string match on `Error`.

---

## Known Pitfalls & Patterns

- **PATTERN** (`autopilot-failed-stage-revival-is-a-flag-gated-redrive-before-processpr`): hold boolean + redrive before ProcessPR; preserve attempt counters; persist.
- **PITFALL** (GH-4855 / GH-4851): a stranded `failed` PR without a TerminalLabel re-dispatches the issue on an external close → the budget-exhausted path keeps `TerminalLabel = pilot-failed`.
- **PITFALL** (TASK-418 / #4531 class): GitHub reports infra death and real failures alike; the poll error here is not even a CI result, so it must never reach a CI verdict path.
- **PITFALL** (GH-4851): the CI-timeout clock `CIWaitStartedAt` must not count an API outage, or a long outage ends in the confirmed-timeout terminal branch instead.

---

## Legs

### LEG H1 — repo: qf-studio/pilot — title: fix(autopilot): consecutive CI-poll API errors hold the PR with backoff and a redrive on the first good poll; only a 30-minute outage budget ends in failed

## Context

In `internal/autopilot/controller.go`, the waiting-CI check (near line 3552) counts CheckCI errors in `ConsecutiveAPIFailures` and at 5 moves the PR to `StageFailed` with `TerminalLabel = pilot-failed`; below 5 it retries on the next poll at the normal interval. On 2026-11-04 GitHub returned HTTP 500 five times in 45 s and PR#6055 was failed while its CI run was green; no redrive applies to that stage (`processAllPRs` near line 10256 runs only `reAdoptHeldRebasePR` near line 8483 and `redriveFailedPRForBaseRetarget` near line 8573 before `ProcessPR`). `PRState` in `internal/autopilot/types.go` carries `ConsecutiveAPIFailures` (near line 1224), `TerminalLabel` (near line 1370) and the hold booleans `RebaseHoldActive` (near line 1449) and `BreakerHoldActive` (near line 1474); `persistPRState` (controller near line 1543) saves it.

probe: present qf-studio/pilot consecutive API failures
probe: present qf-studio/pilot ConsecutiveAPIFailures[[:space:]]+int
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]redriveFailedPRForBaseRetarget\(
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]processAllPRs\(
probe: absent qf-studio/pilot APIOutageHoldActive

## Scope

- `PRState` gains `APIOutageHoldActive bool`, `APIOutageSince time.Time` and `APIOutageNextPoll time.Time`, persisted like the other hold fields.
- At the fifth consecutive CheckCI error the PR stays in `waiting_ci`: set `APIOutageHoldActive = true`, `APIOutageSince = now` (first error time), log one WARN "CI poll API outage: holding PR", emit one `ci_poll_api_outage` alert (SeverityWarning; new executor const, alerts EventType, AlertType and default rule, modelled on the usage-limit pause plumbing) and do not set `StageFailed` or `TerminalLabel`. No label is added to the PR or the issue.
- Backoff: while the hold is active the poll for that PR runs only when `now >= APIOutageNextPoll`, doubling from 30 s to a cap of 5 min; a successful CheckCI clears the hold, resets `ConsecutiveAPIFailures` and the backoff, and logs "CI poll API outage over" with the outage length. The CI-timeout clock is corrected by the outage length (`CIWaitStartedAt` moves forward by it) so an outage never ends in the confirmed-timeout branch.
- Budget: when `now - APIOutageSince` exceeds `ci_checks.api_outage_budget` (new config key, default 30m) the PR moves to `StageFailed` exactly as today (same `Error` text with the count and the elapsed time, `TerminalLabel = pilot-failed`), and the one alert is sent again at SeverityError.
- Redrive: a third redrive in `processAllPRs` before `ProcessPR`, `redriveAPIOutageHeldPR`, keyed on `APIOutageHoldActive` and a successful CheckCI (not on a head-SHA change), which clears the hold and lets the normal waiting-CI check continue; attempt counters are untouched.
- The GH-4855 behaviour for an external close of a budget-failed PR is unchanged.
- `configs/pilot.example.yaml`: document `api_outage_budget` under `ci_checks` with the default and the hold semantics.

Tests to add in a new file, plain text: internal/autopilot/ci_poll_outage_test.go — named exactly: TestWaitingCI_APIOutageHoldsInsteadOfFailing (five errors → stage still waiting_ci, hold set, no TerminalLabel, one alert), TestWaitingCI_APIOutageBackoffDoublesAndCaps (fake clock, poll times 30 s, 60 s, …, 5 min), TestWaitingCI_APIOutageRedriveOnFirstGoodPoll (held PR, CheckCI succeeds with green checks → hold cleared, PR proceeds to merge), TestWaitingCI_APIOutageDoesNotCountTowardCITimeout (an outage longer than the CI timeout does not trip the confirmed-timeout branch), TestWaitingCI_APIOutageBudgetFails (fake clock past 30 min → failed with TerminalLabel pilot-failed and the error alert), TestPRState_APIOutageFieldsPersist (round trip through the state store).

## Acceptance

- [ ] `go test -race ./internal/autopilot/ -run TestWaitingCI_APIOutage` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/autopilot/ -run TestPRState_APIOutageFieldsPersist` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/alerts/ -run TestCIPollAPIOutage` passes; paste the output into the PR body.
- [ ] set StageFailed at the fifth error as before -> TestWaitingCI_APIOutageHoldsInsteadOfFailing fails
- [ ] poll at the normal interval during the hold -> TestWaitingCI_APIOutageBackoffDoublesAndCaps fails
- [ ] drop the redrive from processAllPRs -> TestWaitingCI_APIOutageRedriveOnFirstGoodPoll fails
- [ ] leave CIWaitStartedAt untouched during the outage -> TestWaitingCI_APIOutageDoesNotCountTowardCITimeout fails
- [ ] never fail on the budget -> TestWaitingCI_APIOutageBudgetFails fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The confirmed CI-timeout branch (GH-4851/#4853), the rebase and base-retarget redrives, the size-guard hold, the rate-limit budget client (#4391), and any change to how CheckCI classifies a check conclusion (TASK-418/#4531).

## Refs

- PR#6055 / GH-6051 (incident) · GH-4855 (terminal label on API failures) · GH-4851 (CI-timeout terminal) · memory `autopilot-failed-stage-revival-is-a-flag-gated-redrive-before-processpr` · Navigator TASK-635 in this repo

---

## Refs

- PR#6055 hand-merged 2026-11-04 11:35Z (`6033c3ffd`); #6051 closed `pilot-done` by the external-merge path
- TASK-634 (the 11-03 false fix issue), TASK-632 (restart re-pick with an open PR)

## Refs

- H1 → https://github.com/qf-studio/pilot/issues/6056
