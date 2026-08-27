# TASK-486: Mode-gate the iteration-limit PR closes + document both limit fields (GH-5227)

**Status**: ✅ SHIPPED + REVIEWED 2026-08-27 — #5241 → PR#5244 (mode threading + gated closes) + PR#5245 (docs), both merged ~11:30–11:57Z, both **APPROVE-w-notes** (verdicts on the PRs). Mode genuinely threaded (all 3 `NewController` sites assign from loaded config; `--sequential` override reaches the gate); both limit branches gated; `escalateAndHold` terminal/loop-free (`pilot-needs-human` + GH-5072 admission skip); normal revision close untouched. Notes: board fail-column sync skips held PRs · main.go wiring untested (`wiring/harness.go` not updated). Docs review surfaced a systemic pre-existing bug → **[#5246](https://github.com/qf-studio/pilot/issues/5246) filed**: every documented top-level `autopilot:` block is silently ignored (loader reads only `orchestrator.autopilot`). Fix rides the next train (v2.270.0 still latest at review time). Leg 2 (StageFailed-on-healthy-handoff, TASK-460 feed) still deliberately deferred.
**Created**: 2026-08-27
**Assignee**: Pilot

---

## Context

**Problem**:
External report [#5227](https://github.com/qf-studio/pilot/issues/5227) (contributor `stevensommer`, claims verified by nav-research 2026-08-27): both iteration-limit branches in `internal/autopilot/controller.go` — `handleCIFailed` (`MaxCIFixIterations`, ~3592) and `handleReviewRequested` (`ReviewFeedback.MaxIterations`, ~4046) — call `ClosePullRequest` unconditionally. The in-code rationale ("Close the failed PR so the sequential poller can unblock") only applies under `orchestrator.execution.mode: sequential`; the default is `auto`, where nothing blocks and closing discards work. The Controller cannot even see the mode: `internal/autopilot.Config` (`types.go:82`) has no execution-mode field. Even in genuine sequential mode the close is an optimization — the SDK `MergeWaiter` self-unblocks on a bounded timeout (default 1h).

Second half: `max_ci_fix_iterations` has zero documentation anywhere (`docs/`, `configs/pilot.example.yaml`); `review_feedback.max_iterations` is documented in `docs/content/features/autopilot.mdx` but with a wrong iteration-marker format and the same mode-blind close rationale.

**Goal**:
Under non-sequential modes, reaching an iteration limit holds the PR for a human instead of closing it. Both limit fields, their closure semantics, and the `max_iterations: N` mental-model trap are documented. Stale comments corrected.

---

## Known Pitfalls & Patterns

- **PATTERN** (GH-3806): Close/hold sites in `controller.go` never post comments inline — they set `prState.Error` / `prState.TerminalLabel` and let `notifyExternalClose` (~9118) write the audit trail. The hold path must follow the same discipline.
- **PATTERN** (GH-4826/GH-4841): `TerminalLabel` is designated through the `spawnFailureIssue`/`spawnReviewIssue` seams the moment a continuation issue exists. The limit branches spawn nothing, so they set `TerminalLabel` directly — keep that, but only on the (sequential) close path.
- **PATTERN** (`escalateAndHold`, ~7097): existing "hold, don't close" idiom used by the CI-fix size guard and GH-4459/GH-4856 declined-continuation guards — sets `StageFailed` + `pilot-needs-human`, leaves PR and branch intact. Reuse it; do not invent a parallel hold mechanism.
- **PITFALL** (#5237, merged 08-26): the platform-outage breaker gates `handleCIFailed`'s destructive branch (incl. the CI iteration-limit close) but there is NO breaker check on the review-feedback path. Do not accidentally reorder the breaker check (~3520) relative to the iteration check (~3573).
- **PITFALL** (PR #5207, 08-24): `execution.mode` was silently dropped before the SDK poller until three days ago; `cmd/pilot/main.go:67-73` still carries a stale GH-4191-era comment claiming the SDK adapter runs auto unconditionally. Fix the comment in this task — it is exactly the trap that would mislead the next reader of this wiring.
- **PITFALL** (repick/claim memories): closing a PR arms clean retry semantics elsewhere; holding must not re-arm anything — verify the held PR is not re-picked as a fresh candidate by admission/sweep loops.

---

## Acceptance Criteria

- [ ] `autopilot.Config` (or a `ControllerOption`) carries the execution mode; all three `NewController` call sites in `cmd/pilot/main.go` pass `cfg.Orchestrator.Execution.Mode` through.
- [ ] `handleCIFailed` iteration-limit branch (~3583): under `sequential`, behavior unchanged (close + `StageFailed` + `TerminalLabel=LabelFailed` + board sync + metrics). Under any other mode (incl. empty/unset → treat as `auto`), the PR is NOT closed: hold via the `escalateAndHold` idiom with a reason naming the limit (`CI fix iteration limit reached (N/M)`), `pilot-needs-human` labeled, branch left intact.
- [ ] `handleReviewRequested` iteration-limit branch (~4039): same gating, reason `review feedback iteration limit reached (N/M)`.
- [ ] The normal continuation closes (~3749, ~4128) and all other `ClosePullRequest` sites are UNTOUCHED.
- [ ] A held-at-limit PR is terminal for the controller (no further automated cycles: no revision spawn, no re-close attempt on subsequent polls) but remains open on GitHub.
- [ ] Table-driven tests cover both branches × {sequential, auto, parallel, unset}: assert ClosePullRequest called/not-called, stage, label, and that no continuation issue is spawned at the limit.
- [ ] Docs — `docs/content/features/autopilot.mdx`: add `ci_monitor.max_ci_fix_iterations` (default 3, `types.go:144-147`); state explicitly for BOTH fields what happens at the limit per mode (sequential: PR closed; otherwise: held open + `pilot-needs-human`); fix the iteration-marker callout from `Autopilot-Iteration: N` to the real `<!-- autopilot-meta branch:... pr:... iteration:N -->` format; note that `max_iterations: N` spawns exactly N continuation issues — the Nth revision PR gets no further revision.
- [ ] Docs — `configs/pilot.example.yaml`: both keys present with defaults and one-line closure-semantics comments.
- [ ] `cmd/pilot/main.go:67-73` stale comment corrected to reflect PR #5207 (mode is now wired to the SDK poller).

---

## Implementation

### Phase 1: Mode plumbing
**Goal**: Controller knows the execution mode.

**Tasks**:
- [ ] Add `ExecutionMode string` to `internal/autopilot/types.go` `Config` (document valid values; empty = auto) — or `WithExecutionMode` option; pick whichever matches surrounding style, `Config` field preferred (it is config, not a dependency).
- [ ] Thread `cfg.Orchestrator.Execution.Mode` at the 3 `NewController` sites in `cmd/pilot/main.go` (~934, ~2372, ~2438).

### Phase 2: Gate the two limit branches
**Goal**: Non-sequential → hold, not close.

**Tasks**:
- [ ] `controller.go` ~3583 and ~4039: on limit, branch on mode. Sequential: existing behavior verbatim. Otherwise: `escalateAndHold`-style hold (reuse/extract as needed), no `ClosePullRequest`, no branch deletion, `pilot-needs-human`, `prState.Error` set with the limit reason so `notifyExternalClose` conventions hold.
- [ ] Confirm terminality: held PR must not be re-entered by `ProcessPR` into another spawn/close cycle (check the `StageFailed` early-return at ~2805 covers it; if the hold uses a flag pattern like `BreakerHoldActive`, mirror that idiom only if required).

### Phase 3: Tests
- [ ] Table-driven tests in `internal/autopilot/` for both branches × 4 modes (fake GH client asserting `ClosePullRequest` invocations; existing test scaffolding for `handleCIFailed`/`handleReviewRequested` should be extended, not duplicated).

### Phase 4: Docs + comment hygiene
- [ ] `autopilot.mdx`, `pilot.example.yaml`, `main.go:67-73` per acceptance criteria.

---

## Out of Scope

- **Leg 2 (separate future task)**: `StageFailed`/`TerminalLabel=LabelFailed` conflation on HEALTHY continuation closes (~3408, ~4004, ~3770, ~4143) — successful revision hand-offs are recorded as pipeline failures in the ledger/dashboard (`c.monitor.Fail` via `notifyExternalClose:9190`). Touches `ProcessPR` switch, `RestoreState`, `release_backfill.go:198`, `isStackBaseCandidateStage`, three re-drive guards. Feeds TASK-460. Do NOT change stage vocabulary in this task.
- The normal revision-cycle close + branch delete (~4128/~4133) — documented, intentional behavior.
- Iteration-counter arithmetic — verified as-designed (N continuation issues), only the docs clarify it.
- #5228 (configurable review triggers) — related contributor request, separate.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Mode visibility | Config field vs ControllerOption vs global | `Config` field (option acceptable) | It's configuration, not a dependency; 3 call sites |
| Non-sequential limit behavior | Close anyway / hold+label / leave silently | Hold + `pilot-needs-human` | Nothing is blocked in auto mode; closing discards healthy work; `escalateAndHold` idiom exists |
| Sequential behavior | Also hold (rely on MergeWaiter timeout) / keep close | Keep close | Preserves queue-unblocking optimization; zero behavior change for sequential operators |

---

## Verify

```bash
make build
go test ./internal/autopilot/ -run 'TestHandleCIFailed|TestHandleReviewRequested|IterationLimit' -v
make lint
```

---

## Done

- [ ] Both limit branches mode-gated; tests prove close-vs-hold per mode
- [ ] `make build`, `make test`, `make lint` green
- [ ] `max_ci_fix_iterations` documented for the first time; marker format corrected
- [ ] PR references #5227

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5241
- [#5227](https://github.com/qf-studio/pilot/issues/5227) — source report (claims verified; two corrections: PR#615 was closed by the NORMAL revision path, and "branch survives" is backwards — the limit close preserves the branch, the normal close deletes it)
- nav-research deep-dive 2026-08-27 (session) — 6-question architecture report
- PR #5207 (`16ccb228`) — mode wiring to SDK poller
- #5237 (`a6d4bfd7`) — outage breaker, orthogonal gate on the same branch
- GH-3806 / GH-4826 / GH-4841 / GH-4856 / GH-4459 — guard lineage on these close paths

---

**Last Updated**: 2026-08-27
