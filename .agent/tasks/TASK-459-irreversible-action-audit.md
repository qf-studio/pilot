# TASK-459: Irreversible-action audit — destructive sites require typed verdicts with positive evidence

**Status**: 🚧 Phase 2 dispatched 2026-08-08 → [#4811](https://github.com/qf-studio/pilot/issues/4811) (`no-decompose`). Phase 1 ✅ (#4796 → PR#4802, post-merge reviewed 08-08: 4 findings, all folded into the Phase 2 spec — zero-value `Verdict{}` hazard is the load-bearing one). Phase 3 next after 2; Phase 5 still needs a scope call
**Created**: 2026-08-07
**Assignee**: Pilot (multi-leg; dispatch one leg at a time)

---

## Context

**Problem**:

Pilot repeatedly takes irreversible actions on false signals. Over 2026-08-06/07 alone: a correct PR closed three times during a GitHub Actions outage (#4765/#4768/#4770, ~$10.65 of executor work discarded), a resurrected PR re-closed within 90 seconds because superseded check-runs were counted (#4781), junk fix-issues spawned (#4766/#4769/#4775), and a false "task failed" alert raised for an execution that was correctly skipped (#4794).

Individually these were four bugs. Structurally they are four instances of the same three root patterns:

1. **Absence of evidence read as evidence of failure.** `classifyPRFailure(nil)` returns `FailureClassCode` — when the system cannot tell what happened, it chooses the most destructive interpretation. The fail-safe default was picked to guarantee forward progress, so uncertainty converts directly into closed PRs.
2. **Failure inferred from side-effects rather than read from recorded state.** The poller concluded failure because *no PR appeared*, while the ledger plainly said `superseded` (#4794). Same shape as a conflicted PR that never gets CI being recorded as `failed` (PR#4785).
3. **One fact, two implementations, silent drift.** `mapCheckStatus` counted `cancelled`/`timed_out` as failure while the three evidence-gathering functions filtered on `"failure"` only — so a real failure arrived with zero evidence and fell into pattern 1.

Underneath all three: **the code applies the same confidence threshold to closing a PR as to writing a log line.** No decision path asks "how irreversible is this action, and how strong is my evidence?" Combined with the daemon's autonomy pressure (never get stuck), every ambiguous case resolves toward *act* rather than *wait*.

**Goal**:

Make the false-signal class structurally unable to recur: every irreversible action must consume an explicit typed verdict carrying positive evidence. Uncertainty must route to a non-destructive path by construction, not by the diligence of whoever writes the next call site.

Point fixes already shipped (#4787 structural classification + `FailureClassUnknown`, #4790 check-run dedupe) and in flight (#4791 correlation, #4794 superseded-vs-failed) are instances. This task makes the invariant global and enforced.

---

## Known Pitfalls & Patterns

- **PITFALL** (95%, `ci-infra-failure-misclassified-as-code`): GitHub reports `conclusion=failure` identically for real lint findings and runner-infra death; `handleCIFailed` trusted it blindly — an infra flake closed a correct PR (GH-4526/PR#4529), spawned garbage fix issue #4530, burned the repick budget. → *Reflected in Phase 2*: the CI-failure path is the first gated caller, and the gate rejects verdicts lacking positive evidence.
- **PITFALL** (95%, `required-checks-allowlist-makes-other-gates-decorative`): with an allowlist set, `isScopedCheck` reports only those checks — every other gate can fail red while auto-merge proceeds. UNFIXED (founder config decision). → *Reflected in Phase 1*: the inventory must record which signals are *authoritative* vs *decorative* per config, because a verdict built from a decorative signal is not positive evidence. Config policy itself stays out of scope.
- **PITFALL** (90%, `mem-151`): epic collapse → scaffold-only under-delivery that passed CI green; helpers built but never wired, parent auto-closed as false completion. → *Reflected in Phase 5*: the audit covers false **success** as well as false failure — "green CI" is not positive evidence that the requested change shipped.
- **PITFALL** (0%, `global-required-checks-leak-across-projects`): a global allowlist shared by every project controller left repos polling `waiting_ci` forever with all checks green — fixed by per-project overlay. → *Reflected in Phase 1*: the inventory is per-controller/per-project aware; a verdict's authority is project-scoped.

---

## Acceptance Criteria

- [x] A written inventory exists of every irreversible action site in the daemon, each classified by reversibility and by the evidence it currently consumes.
- [x] A typed `Verdict` contract exists: class + evidence + source + scope. Constructing one without evidence is impossible or explicitly marked `Unknown`.
- [ ] `Unknown` (or evidence-free) verdicts cannot authorize a destructive action anywhere — enforced by the type system and/or a CI check, not by convention.
- [ ] Every site that derives the same external fact twice (status vocabularies) reads from one shared table, with a parity test that fails on drift.
- [ ] No subsystem infers failure from a missing side-effect when a recorded status exists.
- [ ] A CI check bans direct calls to destructive APIs outside the gate (following the `check-mocks.sh` grep-gate precedent).
- [ ] An SOP documents the invariant for future call sites.

---

## Implementation

Legs are sized per the #4780 lesson (one subsystem + its tests per `no-decompose` issue; that spec timed out twice at 1h as a single unit).

### Phase 1: Inventory + verdict contract
**Goal**: Know every destructive site and define the contract before changing behaviour.

**Tasks**:
- [x] Enumerate irreversible/costly sites: `ClosePullRequest`, branch deletion, `CreateFailureIssue`, retry/repick counter increments, terminal label writes, merge, cancel/supersede writes, `escalateAndHold` (semi — non-destructive but operator-costly).
- [x] For each: current evidence consumed, reversibility tier, blast radius, and whether its signal is authoritative or decorative under the active `required_checks` config.
- [x] Define the `Verdict` type and its construction rules; decide its home package. (Extends `internal/autopilot/failure_class.go`, per the biased default — no non-autopilot consumer forced a new package.)
- [x] Write the inventory to `.agent/system/irreversible-actions.md` as the reference table.

**Files**: `.agent/system/irreversible-actions.md` (new) · `internal/autopilot/failure_class.go` (contract likely extends this) · new package if the contract outgrows it.

### Phase 2: Gate the CI-failure path
**Goal**: The biggest offender consumes verdicts only.

**Tasks**:
- [ ] `handleCIFailed` and its close/fix-issue/escalate rungs take a `Verdict`, never raw strings or nil-checks.
- [ ] Evidence-free path routes to hold; positive-evidence path retains today's behaviour.
- [ ] Regression tests for the outage shapes already captured in #4787's fixtures.

**Files**: `internal/autopilot/controller.go` · `internal/autopilot/failure_class.go` · `internal/autopilot/ci_monitor.go`

### Phase 3: Status vocabulary is authoritative (executor/dispatcher/poller)
**Goal**: Kill inference-from-side-effects.

**Tasks**:
- [ ] Terminal statuses (`superseded`, `canceled`, `stalled`, `failed`) drive reporting/labelling decisions; no subsystem infers failure from "no PR produced" (#4794 generalized).
- [ ] Label writes respect issue state (no retry labels on closed issues).

**Files**: `cmd/pilot/poller_github.go` · `internal/executor/dispatcher.go` · `internal/executor/lifecycle.go`

### Phase 4: Parity + enforcement
**Goal**: Make drift and bypass impossible rather than unlikely.

**Tasks**:
- [ ] Shared table for "what counts as a failed check" consumed by both status mapping and evidence gathering; parity test fails on divergence (the #4790 fix generalized).
- [ ] Second parity target (PR#4795 post-merge review, 2026-08-07): approval-channel vocabulary has **three** implementations — the unexported alias table in `internal/approval/channel.go`, `validApprovalSourceValues` in `internal/config/config.go`, and the `sourceRegistered` switch in `internal/health/health.go`. Export one table, consume it from all three, parity test. Fold in the one-line fix for explicit `approval_source: ""` project overlays: validation documents empty as "inherits env/global" but `NewController` copies the pointer verbatim → `PreferredChannel: ""` → `defaultChannelName` routes to telegram instead of the resolved source (add `!= ""` guard in the resolution block + test).
- [ ] `scripts/check-destructive-calls.sh` + gate step: destructive APIs may only be called from gated helpers.
- [ ] SOP `.agent/sops/quality/irreversible-actions.md`.

**Files**: `scripts/` · `Makefile` · `scripts/pre-push-gate.sh` · `.agent/sops/quality/`

### Phase 5 (optional, scope decision required): false-success side
**Goal**: Green CI is not proof the requested change shipped (`mem-151`).

**Tasks**:
- [ ] Decide whether delivery-evidence checks (diff touches the target surface; ACs observable) belong in this audit or a separate track.

---

## Out of Scope

- The `required_checks` allowlist policy itself (founder config decision; recorded in the inventory, not changed here).
- Classification internals — #4787 owns those; this task consumes their output.
- Breaker legs #4791/#4792 — they are callers of the contract, not part of it.
- Any change to what the daemon does when evidence IS positive (today's destructive behaviour stays for genuine failures).
- Retro-fixing historical mislabelled rows.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|---|---|---|---|
| Contract location | New `internal/verdict` package · extend `internal/autopilot/failure_class.go` | **Decide in Phase 1** — bias to extending `failure_class.go` | The vocabulary already lives there and both the autopilot and dispatcher classifiers reference it (TASK-421 sibling); a new package earns its keep only if non-autopilot callers need it |
| Enforcement mechanism | Type system only · grep CI check only · both | **Both** | Types stop honest mistakes; the grep gate stops new direct call sites — precedent: `check-mocks.sh` (TASK-441 L1) chose grep for exactly this reason |
| Uncertainty representation | Boolean `hasEvidence` · `FailureClassUnknown` · confidence score | **`Unknown` class** (already shipped in #4787) | Extending a shipped vocabulary beats inventing a parallel one; scores invite threshold-tuning bikesheds |
| Rollout | One big PR · phased legs | **Phased legs** | #4780 timed out twice at 1h as a single unit; one subsystem + tests per leg |

---

## Verify

```bash
make build
make test
make lint
./scripts/check-destructive-calls.sh   # Phase 4
make gate
```

---

## Done

- [x] `.agent/system/irreversible-actions.md` lists every destructive site with its evidence contract.
- [ ] Destructive call sites take a `Verdict`; evidence-free verdicts cannot reach them (compile-time or gate-enforced).
- [ ] Parity test fails when status mapping and evidence gathering disagree.
- [ ] `check-destructive-calls.sh` green in the pre-push gate and CI.
- [ ] SOP published; no behaviour change for genuine failures (regression suite green).

---

## Refs

- Phase 1 issue: https://github.com/qf-studio/pilot/issues/4796 (dispatched 2026-08-07) → PR#4802 (merged; post-merge review in PR comments, 2026-08-08)
- Phase 2 issue: https://github.com/qf-studio/pilot/issues/4811 (dispatched 2026-08-08)
- Incident: 2026-08-06 GitHub Actions outage (~9.5h) and 08-07 recovery — marker `2026-08-06_outage-pause-approval-wave-dispatched.md`
- Instances already fixed/in flight: #4787 (structural classification + `Unknown`), #4790 (check-run dedupe), #4791/#4792 (breaker), #4794 (superseded ≠ failed)
- Prior art: `.agent/tasks/archive/TASK-418-ci-infra-failure-classification.md`, `.agent/tasks/archive/TASK-421-repick-counter-counts-non-failures.md`, `.agent/tasks/archive/TASK-441-contract-hardening-tune-up.md` (grep-gate precedent)
- Root-cause analysis: `.agent/system/approval-architecture-roadmap.md` § outage recovery + hardening

---

## Notes

Dispatch order: Phase 1 first (its inventory is the input to everything else), then Phase 2, then 3, then 4. Phase 5 needs a scope call before it is written up.

**Phase 2 dispatched** (2026-08-08, #4811): scope per the inventory's "How Phase 2+ consumes this" — pre-merge `handleCIFailed` ladder + post-merge CI-failure rung. Folds in the PR#4802 post-merge review findings: (1) zero-value `Verdict{}` hazard — `Class()` hardening + positive-evidence gate via one shared helper, never `!= Unknown`; (2) composite-literal construction ban deferred to Phase 4's grep; (3) SHA binding left as an in-PR decision point, biased to same-tick constraint over contract change; (4) migrated inventory rows get refreshed line refs. `SetApprovalDecision`/`handleReleasing` tracing stays Phase 3.

**Phase 1 complete** (2026-08-07, #4796): `.agent/system/irreversible-actions.md` inventories 9 site families (`ClosePullRequest`, branch delete, `CreateFailureIssue`, retry/repick counters, terminal labels, merge, ledger cancel/supersede writes, `escalateAndHold`, plus cross-cutting findings) with file:line, reversibility tier, blast radius, evidence shape, and `required_checks` authoritative/decorative status per site. `Verdict` (`internal/autopilot/failure_class.go`) extends the existing `FailureClass` vocabulary rather than a new package — unexported fields, `NewVerdict`/`NewUnknownVerdict` constructors, evidence-free construction always downgrades to `FailureClassUnknown` regardless of requested class. Table-driven tests cover the downgrade rule for every destructive class, evidence retention, and scope/source round-tripping. No call site migrated; no behaviour changed — `make build`/`make test`/`make lint` all green. Two inventory items flagged as not fully traced in this pass for Phase 2/3 pickup: `SetApprovalDecision`'s CAS arbitration mechanics, and the `handleReleasing` release-pipeline retry/escalation ladder (controller.go ~4196-4615).

**Last Updated**: 2026-08-08
