fix(fleet): B7 idle-sleep policy — idle window, real readers, fleet-wide execution scope, and an off-by-default gate (supersedes #215 / PR#222)

## Context

Supersedes #215 and its draft PR#222, which is closed unmerged: the branch drifted eleven files behind main after the ECS and consolectl legs (#259–#280), and the 2026-08-26 review left two blocking items unimplemented. Start from main. The closed branch pilot/GH-215 is prior art you may read for the window logic and the reader shapes, but do not base the branch on it, do not cherry-pick it wholesale, and do not add an autopilot-meta footer.

The idle-sleep policy in `internal/fleet/reconciler.go` can stop a running customer machine. A false positive is a severe production defect. Today the policy is inert because both activity readers are wired nil; that escape hatch goes away when the readers land, so this issue must also add the explicit gate.

Root defects carried over from #215 (all confirmed by the 08-26 review of the previous attempt):

- The window measured uptime, not idleness. Predicates were sampled once, the window gate returned before activity was checked, and the clock never reset.
- Both readers were unimplemented; the interface collapsed queued and running into one bool.
- The desired-state write was unconditional against a value read at the top of the tick (clobbers a concurrent suspend or terminate).
- A missing observed-running-since key read as infinite idle (fail-open).
- The journal recorded constants, not measurements, and committed after the state change.
- The execution read was unbounded and inline in the tick.

New blockers found on the previous attempt, which the fresh implementation must include:

- **Execution reader was blind to every repo except the tenant's first.** It polled the daemon's `/api/v1/queue`, which is scoped to the single dashboard project; the tenant systemd unit rendered by `internal/fleet/bootstrap.sh.tmpl` starts pilot without a dashboard-scope flag, so the queue is pinned to the first project while `internal/fleet/configrender.go` renders a multi-repo projects list. A tenant with running work on repo two and no open pilot-labelled board card read as idle on both predicates and would be stopped mid-execution.
- **No kill switch.** The idle window was not a config field (4 h default only) and nothing gated the policy once readers existed.

## Design

**Window.** Keep an idleSince timestamp per instance from a monotonic clock. Evaluate both activity predicates on every sampled tick. Reset idleSince whenever either predicate is true, either reader errors, the row is in flight, or the row leaves running/running. Sleep only when elapsed since idleSince exceeds the configured window. On the in-flight skip branch delete the idleSince entry (unobserved time never counts as idle).

**Readers.**
- Execution reader: distinguish running from queued in the interface (two values or a small struct, not one bool). Classify by an allowlist of TERMINAL statuses (completed, failed, no_op, canceled, superseded, declined, needs_human, stalled, skipped and any other terminal status the daemon documents); everything not on that list is active. Never an allowlist of active statuses. Fail closed: error, empty body, non-200, or unparsable response means not idle.
- Trigger-work reader: one org-scoped SQL EXISTS against the local board tables. No tracker API calls. Add the supporting index for the label-containment predicate in the same migration series (latest is 0017; next free is 0018 unless the usage-rollup issue lands first, then 0019).
- Fleet-wide scope: render the pilot dashboard-scope flag with value all into the tenant unit in `internal/fleet/bootstrap.sh.tmpl` and pin it with a template test, so the queue endpoint reports every project. The flag is CLI-only on the pilot side (no config-file equivalent), so the unit is the only place to set it. Note in the PR that already-bootstrapped tenants need a re-bootstrap or the next AMI roll to pick the flag up; the gate below keeps the policy off until then.

**Bounding and cadence.** All three network legs per instance (gateway token, EC2 describe, HTTP GET) run under one per-instance deadline derived from the tick context, 10 s total. Sample activity at most once per five minutes per instance, the same throttle the readiness probe uses; a four-hour window does not need 60-second sampling. One unreachable daemon must not delay provisioning, readiness, or config pushes for other tenants.

**State write.** Conditional update guarded on desired_state still being running, with a rows-affected check, in `internal/fleet/store.go`. Journal entry in the same transaction, written with the measured values: both predicate results as observed, idle_since, idle_elapsed, and the sampled-at time. If the conditional write affects zero rows, skip the journal, the slice mutation and the idleSince update.

**Gate.** New fleet config field idle_sleep_enabled, default false, plus idle_window as a duration with the 4 h default, both read wherever the reconciler config is built (`main.go` and `cmd/consolectl/run.go`). When disabled the reconciler must not call either reader and must not write desired state. Log once at startup which mode is active.

**Housekeeping.** Do not open a second board store pool in `main.go` for the trigger reader; reuse the existing one or close what you open. Widen the mid-window test margins so the test cannot flaky-fail.

## Acceptance

- [ ] An instance whose activity resumes at any point during the window is not slept; the window restarts. Test ticks repeatedly with activity mid-window.
- [ ] Sleeping requires both predicates to hold continuously for the full window; one tick short does not sleep.
- [ ] Queued and running are distinguished by the interface; an unknown execution status counts as active.
- [ ] Every error path fails closed, covered by tests that set errors on both readers and on the token and describe legs.
- [ ] The tenant unit template renders the dashboard-scope flag with value all, pinned by a test.
- [ ] Conditional desired-state write cannot overwrite a concurrent suspend or terminate; journal is transactional and records measured values.
- [ ] Missing observed-running key and the in-flight branch cannot produce an immediate sleep.
- [ ] With idle_sleep_enabled false (default) neither reader is called and no desired-state write happens.
- [ ] check-fleet-org-scoping passes; migration up/down round-trip green.

## Implementation

- `internal/fleet/reconciler.go`, `internal/fleet/reconciler_test.go`
- `internal/fleet/store.go` (conditional write + journal)
- new reader files under internal/fleet (names at implementer's discretion)
- `internal/fleet/bootstrap.sh.tmpl` + its test
- `internal/fleet/configrender.go` only if the flag is better carried through config render
- `main.go`, `cmd/consolectl/run.go` (gate + wiring)
- one migration for the board index

Do not decompose. Single PR.
