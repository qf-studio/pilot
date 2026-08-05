# TASK-441: Contract hardening tune-up — seams, dead-man signals, tripwires

**Status**: 🏁 **Legs 1–5 + 7 ALL MERGED 2026-08-04** — one dispatch-to-merge day per wave. L1 PR#4711 · L2 PR#4712 (`DeadManTracker`) · L3 PR#4713 (audit: no GH-4692-class bug in any of the 6 — SDK pollers label internally) · L4 PR#4722 (`LivenessPolicy`, zero literals left in detectors) · L5 PR#4724 (Finish tripwire sweep, 4 checks, post-Persist, recover()-guarded) · L7 PR#4704. L3 follow-ups all merged: Linear #4723 · Jira #4725 · GitLab #4728 · ADO #4729 · Asana #4730 (1 lint retry: PR#4726 closed → autopilot fix loop → #4730; dangling fix-issue #4727 closed manually). **L8 MERGED** same evening (#4731→PR#4732 — `.agent/system/ARCHITECTURE.md` dated 08-04; Pilot's own audit corrected the table count to **30 live tables** across 3 stores, not the assessment's "34"; convergence doc superseded with pointer). **L6 dispatched 08-05 #4733** (IssueLabeler narrow interface — the final leg). Remaining: L6 merge · **operator kill-drill** once the first post-08-04 train lands on the box (still v2.253.0) — procedure written: `.agent/sops/operations/task441-kill-drill.md`
**Created**: 2026-08-04
**Assignee**: Pilot (legs dispatched via nav-pilot after review)

---

## Context

**Problem**: A week of production incidents shares one shape — *wiring bugs wearing
a green test suite*: intent judge dead 17 days (fail-open hid it), `pilot-in-progress`
never applied since the 07-16 SDK cutover (SDK `Notifier` = tested dead code, wired to
nothing), `runSelfReview` executing in the repo root (mock discarded `ExecuteOptions`;
3rd recurrence of the class), two uncoordinated stdout-silence detectors with drifted
floors, canary flag intake no-op. Units pass; the seam between them is miswired; nothing
observes the seam.

**Goal**: Not a rewrite. The modularization investment is real (studio-sdk extraction
TASK-318/385, `ExecutionLifecycle` chokepoint TASK-404, `backendExecute` path guard
GH-4707, `repo_guardrail.go`). This task generalizes the patterns that worked and closes
the gaps the point-fixes exposed, so seam breakage becomes **loud within an hour**
instead of silent for weeks — under continued high update velocity.

**Ground truth**: nav-research assessment 2026-08-04 (seam-grade table, external
contract freeze list, module map vs ARCHITECTURE.md drift). Key inputs: `PollerDeps`
(`studio-sdk sdk/core/registry.go:191-227`) has no notify/label hook — GH-4692's fix
bypasses the seam; 4 argument-discarding mocks remain (`runner_test.go` ×3,
`gh4517_test.go`); the alerts engine already has `consecutiveFailures` state
(`internal/alerts/engine.go:26`) to build on; `ExecutionLifecycle.Finish`
(`internal/executor/lifecycle.go:283`) is the confirmed universal terminal transition.

---

## Known Pitfalls & Patterns

- **PITFALL** (85%, runselfreview-runs-in-repo-root-phantom-reimplementation): misrouted
  subprocess + "FIX missing changes" prompt = phantom reimplementation staged in shared
  root → Leg 5's root-clean tripwire is the catch-all for the next variant.
- **LEARNING** (95%, mem-035): the TEST SUITE once live-fired real GitHub issues
  (no dryRun guard, real `gh` in fixtures) → Leg 1's mock rules must also assert the
  exec boundary is stubbed; any Leg 3 handler tests use `httptest`, never live creds.
- **PATTERN** (100%, mem-038): test-at-the-seam — tests must cross the adapter→core
  boundary, not mock it away → applies to Legs 1, 3, 6.
- **PITFALL** (90%, pitfall_epic_decompose_discards_child_work): worktree cleanup can
  discard uncollected child work → Leg 5's sweep includes worktree-pruned +
  commits-without-PR checks.
- **PITFALL** (85%, poller-labels-removed-log-means-never-applied): "removed" log fires
  on never-applied; liveness must count *successes*, not absence-of-errors → Leg 2's
  tracker counts attempts AND successes.

---

## Acceptance Criteria

- [ ] A seam that silently stops working (zero successes while attempts flow, or
      attempts stop while tasks flow) raises an alert within 1 hour — verified by
      killing one subsystem in a test and observing the alert.
- [ ] No `*_test.go` mock discards all arguments of a seam method (CI-enforced).
- [ ] Post-task tripwire sweep runs on every terminal `Finish`; violations alert,
      never block.
- [ ] Zero changes to any frozen external contract (list below).
- [x] ARCHITECTURE.md describes the system that exists (30 live tables per PR#4732's
      audit — the "34" here was the assessment's overcount; studio-sdk boundary,
      all packages), dated 2026-08.

---

## External contract freeze (constraint on every leg — breaking any requires explicit operator sign-off)

studio-sdk `sdk/core` public API (`api.golden`; console C3/C4 consume `SyncCapable`) ·
`pilot-*` label vocabulary (`internal/adapters/github/types.go:99-122` — cross-repo
protocol, console board sync depends on it) · Prometheus metric names
(`internal/gateway/prometheus.go` ↔ grafterm dashboards) · release artifact naming
`pilot-{os}-{arch}.tar.gz` (`internal/upgrade/upgrade.go:369` — self-upgrade fetches by
name) · `~/.pilot/config.yaml` schema (tenant configrender) · `executions`/
`execution_claims`/`execution_events` DB schema (pilot-board-remote, TUI, trace) ·
gateway REST + `/ws/dashboard` + webhook paths (`internal/gateway/server.go:219-253`) ·
Telegram command surface.

---

## Implementation

### Leg 1: Ban argument-discarding mocks
**Goal**: a test double that ignores what crosses the seam can never certify the seam again.
- [ ] CI check (grep-based, `make`-target style like `check-secrets`) failing on
      all-`_` mock method params in `*_test.go`; start with the
      `Execute(_ context.Context, _ ExecuteOptions)` pattern.
- [ ] Fix the 4 live offenders (`mockSelfReviewBackend`, `mockFixedBackend`,
      `mockSequentialBackend`, `mockDirtyBackend`) to record + assert, modeled on
      `guardRecordingBackend` (`backend_execute_guard_test.go`).
- [ ] Rule added to `.agent/system/PR-CHECKLIST.md` (+ mem-035's corollary: exec
      boundary must be stubbed in tests).

**Files**: `Makefile`, `scripts/` (new check), `internal/executor/runner_test.go`,
`internal/executor/gh4517_test.go`, `.agent/system/PR-CHECKLIST.md`

### Leg 2: Reusable dead-man primitive in the alerts engine
**Goal**: generalize GH-4685's bespoke judge streak-counter so any seam gets
zero-success/failure-streak paging by registration, not by hand-rolled wrapper.
- [ ] `alerts.ConsecutiveFailureTracker` (or extension of `engine.go:26`'s
      `consecutiveFailures`): registers (name, threshold, window); counts attempts and
      successes separately (poller-labels lesson: absence of errors ≠ liveness).
- [ ] Migrate the judge streak counter onto it; register: label lifecycle
      (post-GH-4692), self-review completion, gh-guard verdicts (once GH-4671 lands).
- [ ] One new `AlertType`, reused across seams — metric names additive only (freeze).

**Files**: `internal/alerts/{types,engine}.go`, `cmd/pilot/poller_github.go`

### Leg 3: Audit 6 non-GitHub SDK handlers for the notify-started gap
**Goal**: research-first — GH-4692 fixed GitHub only; Linear/Jira/Asana/Plane/GitLab/
AzureDevOps handlers (`cmd/pilot/handlers.go:124-624`) untouched. Tracker-native status
semantics may make the label gap inapplicable per adapter — audit, then dispatch narrow
per-adapter fixes only where real (each mirroring GH-4692's httptest + wiring-order test).
- [ ] Audit doc: per adapter — status mechanism, gap yes/no, evidence.
- [ ] Issues filed only for confirmed gaps (label-additive per saas-architecture
      correction #5).

**Files**: `cmd/pilot/handlers.go` (audit), per-adapter follow-ups TBD

### Leg 4: One source of truth for stdout-silence
**Goal**: GH-4695 synced the floors but left two mechanisms (`heartbeat_monitor.go`
hard-kill, `watchdog.go` soft-stall) that can re-drift on the next tuning PR — GH-4691's
own out-of-scope note.
- [ ] Single `LivenessPolicy` resolved per-task, threaded through `ExecuteOptions`
      (which `backendExecute` already centralizes); both detectors read it, neither
      owns constants.
- [ ] Drift becomes impossible by construction; table-driven tests over
      effort × complexity lanes.

**Files**: `internal/executor/{heartbeat_monitor,watchdog,backend,runner}.go`

### Leg 5: Post-task invariant tripwire sweep on `Finish`
**Goal**: the catch-all — a *new* call site reintroducing any past class gets caught on
first fire, not months later.
- [x] Optional post-`Persist` hook in `ExecutionLifecycle.Finish` (terminal paths):
      (a) root-clean — no staged/unstaged diff in `task.ProjectPath`; (b) label
      lifecycle completed; (c) decomposed children all terminal; (d) worktree pruned +
      no commits-without-PR (epic-discard pitfall). PR#4716 (2026-08-04).
- [x] Log-and-alert only (never block); each check feeds a Leg 2 tracker
      (`finish_tripwire_{root_clean,label_lifecycle,children_terminal,worktree}`,
      new `AlertTypeFinishTripwireFailureStreak`). Sweep panics/errors recovered,
      never propagate past `Persist`.
- Byproduct fix: `runPollingMode` (`cmd/pilot/main.go`, the actual
  `pilot start --telegram --github` entrypoint) never called
  `runner.SetAlertProcessor` — every executor-side alert relay (Leg 2's trackers
  included) was silently dead in production polling mode. Wired there and in
  `internal/pilot/pilot.go`'s `initAlerts` (webhook-only mode); all 4 new
  trackers registered at both sites.

**Files**: `internal/executor/lifecycle.go` (+ small check helpers)

### Leg 6: Narrow interface for autopilot's GitHub client
**Goal**: make "which of the 61 methods does autopilot depend on" a type-system fact.
Lower urgency — tests already assert real HTTP bodies.
- [ ] Start with the label-lifecycle subset (`IssueLabeler`: AddLabels/RemoveLabel);
      grow only as needed.

**Files**: `internal/autopilot/controller.go`, `internal/adapters/github/`

### Leg 7: gh-guard shim — already filed as GH-4671 (re-armed, in queue)
The one seam where the *executed agent* is the untrusted party. Dispatch independently;
do NOT bundle with any branch-protection decision (founder item).

### Leg 8: ARCHITECTURE.md refresh (last — after Legs 1–5 land)
- [ ] Real schema (34 tables or pointer to `store.go`), studio-sdk boundary section,
      9 undocumented packages, autopilot stage vocabulary.
- [ ] Supersede `.agent/system/processed-store-executions-convergence.md` (pre-cutover,
      actively misleading) with a pointer note.
- [ ] Note the intent-naming trap: `IntentJudge` (executor pre-flight) ≠
      `internal/intent` (comms classifier).

---

## Out of Scope

- Any rewrite/restructuring of module boundaries — tune-up only.
- `memory.Store` god-object decomposition (113 methods) — separate future task; Leg 5's
  sweep mitigates the read-side scoping risk cheaply for now.
- Branch protection on main (founder decision, interacts with auto-merge).
- LocalMode q-doc relocation (documented pitfall, cosmetic).
- Payments/S3-exit anything (explicitly parked).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Mock rule enforcement | golangci custom plugin, grep CI check, review-only | grep CI check + checklist | Ships today, zero tooling investment; plugin later if evasion appears |
| Dead-man home | new service, per-seam wrappers, alerts engine extension | alerts engine extension | Engine already has streak state + dispatch channels; smallest diff |
| Tripwire location | dispatcher, runner defer, `Finish` | `ExecutionLifecycle.Finish` | TASK-404 made it the universal terminal transition; catches every path incl. future ones |
| Silence policy | keep 2 detectors + sync constants, merge into one | one `LivenessPolicy`, two enforcers | Merging enforcement changes kill semantics (risk); merging *policy* kills drift |
| Leg order | by module, by effort | 1→2→3 first, 8 last | Class-coverage per token; doc refresh before code lands = rework |

---

## Verify

```bash
make build && make test && make lint
# Leg 1: the new check
make check-mocks   # (name TBD) — must fail on a planted all-underscore mock
# Leg 2/5: kill-a-subsystem drill on the box (operator-run):
#   disable label notify → alert within 1h; dirty the root in a sandbox task → tripwire alert
```

---

## Done

- [ ] Legs 1–5 merged and live on the box; kill-drill alert observed end-to-end
- [x] Leg 3 audit doc committed; per-adapter issues filed or ruled out with evidence
      (2026-08-04: no GH-4692-class bug ruled for all 6; comment-gap fixes #4717–#4721)
- [x] GH-4671 merged (Leg 7) — PR#4704, 2026-08-04
- [x] ARCHITECTURE.md refreshed and dated (Leg 8) — PR#4732, 2026-08-04
- [ ] Zero regressions on the frozen contract list (grep-diff on metric names, label
      vocabulary, artifact names, REST paths against this doc)

---

## Refs

- Pilot issues (dispatched 2026-08-04): Leg 1 #4708→PR#4711 · Leg 2 #4709→PR#4712
  (`alerts.DeadManTracker`) · Leg 3 #4710→PR#4713 (audit doc
  `.agent/system/notify-started-adapter-audit.md`) — all merged same day
- Legs 4–5 dispatched 2026-08-04: Leg 4 https://github.com/qf-studio/pilot/issues/4715 ·
  Leg 5 https://github.com/qf-studio/pilot/issues/4716
- L3 follow-up fixes (filed 2026-08-04, one PR per adapter, independent): Linear #4717 ·
  Jira #4718 (M; + dead `Transitions` config) · Asana #4719 · GitLab #4720 · ADO #4721
- Adjacent, unscoped (flagged by audit, Navigator to scope separately if desired):
  `NotifyTaskCompleted`/`NotifyTaskFailed` unwired for ALL adapters incl. GitHub
- nav-research seam assessment 2026-08-04 (this doc's ground truth; agent report in
  session transcript)
- Incident cluster: TASK-437 · GH-4669/4687/4691/4702/4703/4685/4692/4695/4706/4707
- Chokepoint precedents: `internal/executor/repo_guardrail.go`,
  `internal/executor/backend_execute_guard.go`, `internal/executor/lifecycle.go`
- Pitfall memories: `runselfreview-runs-in-repo-root-phantom-reimplementation`,
  `poller-labels-removed-log-means-never-applied`,
  `config-env-expansion-eats-dollar-vars-in-commands`

---

**Last Updated**: 2026-08-04
