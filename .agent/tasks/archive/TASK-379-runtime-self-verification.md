# TASK-379: Runtime Self-Verification — Verifiable Preflight, Fail-Loud Policy, Execution Ledger, Synthetic Canary

**Status**: ✅ **ALL 8 WAVES SHIPPED** (V1–V8), 2026-07-04. V1–V3+V5 ✅ · V4/V6/V7 ✅ (#3839/#3840/#3841; V6 children #3844–#3849 all merged) · V8 ✅ (#3862→PR #3864; sandbox `qf-studio/pilot-canary-sandbox` + `projects:` config + `CANARY_GH_TOKEN` secret).

**V8 E2E proof (workflow_dispatch run 28713784142 — SUCCESS):** canary filed sandbox issue #1 → daemon executed → opened PR #2 with the correct `0.0.1`→`0.0.2` diff → merge detected → issue closed. Full poll→spec→execute→PR chain proven unattended; canary plumbing (idempotency/detect/close/alert-skip) all green. The **merge hop was manual** this run (see open gap).

**Two bugs the first run surfaced (canary earned its keep) — both fixed on main:**
- Poll step `gh pr list --jq --arg` invalid → false-alarms every run → ✅ **#3866 merged** (proven working this run).
- Thin issue body → daemon spec-completeness gate `pilot-spec-incomplete`+`pilot-blocked` (needs an H2 header) → ✅ **#3869 merged** (workflow body now carries `## Context`/`## Acceptance`/`## Implementation`).

**Cron STOPPED:** workflow `pilot-canary.yml` is `disabled_manually` (`gh workflow disable`) to prevent false-alarms while the auto-merge question below is unresolved. Re-enable with `gh workflow enable` once decided, then a manual `workflow_dispatch` to re-validate.

**Open — design decision (do NOT build naively):**
- **Auto-merge not proven unattended**: sandbox has no CI, so autopilot `handleCIPassed` never fires; PR #2 was merged manually this run. The obvious fix (add minimal `pull_request` CI to the sandbox so autopilot auto-merges) is one option, **but merge automation is project-specific** — real users have many different pipeline/gate/approval shapes, so a generic "canary auto-merges" path risks encoding assumptions that don't generalize. Decision deferred: think through whether the canary should (a) rely on each project's own auto-merge config, (b) get a dedicated minimal-CI sandbox, or (c) assert only up to PR-opened rather than merged. Owner: interactive (design), not a Pilot dispatch.

**On the user:** 🔑 rotate exposed `CANARY_GH_TOKEN` (pasted in plaintext during setup).
**Created**: 2026-07-03
**Assignee**: Pilot (phased dispatch)

---

## Context

**Problem**:
Recurring production bugs are almost exclusively *wiring* bugs, not logic bugs: config defined but never consumed (`subprocess_limits`, `execution.mode`), silent auth death (dead PAT → Warn-log every 30s forever), silent model-routing fallback (`--model` omitted, worker runs on personal default), API contract drift (400s on `max_tokens`/`output_config`), and a ledger that can't answer "which stage failed" (task-IDs stored in UUID columns, UTC/local timestamp split, destructive status UPDATEs with no history). Unit tests mock exactly the seams that break; `pilot doctor` checks field presence, never authentication; the alert engine itself fails silent when unwired.

**Goal**:
Make the deployed system self-verifying: every wire probeable on demand (`doctor`/`/ready`), every degraded path loud (log + alert), every execution reconstructable as a stage timeline, and one scheduled end-to-end canary that exercises the real pipeline against a sandbox repo.

**Research basis** (4 navigator-research agents, 2026-07-03): all file:line references below verified against current `main`.

---

## Acceptance Criteria

- [ ] `pilot doctor` performs live auth probes for every enabled adapter (GitHub, Telegram, Slack at minimum) — a dead token is a red check naming the token source, not a green "token present"
- [ ] Gateway `/ready` returns real per-subsystem checks (currently always `allReady=true`, empty map — `RegisterReadinessChecker` has zero call sites)
- [ ] A dead GitHub PAT triggers an alert within N consecutive poll failures, instead of indistinguishable Warn logs every 30s
- [ ] No silent alert-engine death: nil processor / failed `Engine.Start()` is visible in doctor and startup summary
- [ ] Anthropic non-200 responses include the response body in the returned error (both `internal/llm` and `internal/intent`)
- [ ] `execution_logs.execution_id` holds the execution UUID; new rows joinable to `executions.id`
- [ ] All ledger timestamps UTC
- [ ] New `execution_events` table records stage transitions; `pilot trace <task-id>` (or dashboard) renders poll → spec → execute → PR → merged per execution
- [ ] Scheduled canary files a synthetic issue on a sandbox repo and asserts terminal state, alerting on stall with the failing stage named
- [ ] Anthropic request construction has a single shared builder (or per-site contract tests) preventing #3700/#3703-class regressions

---

## Implementation

### Phase A: Fail-loud foundations (independent, cheapest, do first)

**Goal**: Every currently-silent degraded path logs at WARN or alerts. No new architecture.

| # | Fix | Evidence |
|---|---|---|
| A1 | Read + include Anthropic error response body on non-200 | `internal/llm/client.go:84-86`, `internal/intent/classifier.go:138-140` |
| A2 | Alert-nil cluster: `emitAlertEvent` warn-once when processor nil; `MetricsAlerter.Run` Debug→Warn; `Engine.Start()` failure surfaced as persistent state (not one boot log) | `internal/executor/runner.go:4804-4810`, `internal/autopilot/metrics_alerter.go:111-114`, `cmd/pilot/main.go:624-628,2076-2079` |
| A3 | Model-routing silence: log INFO when `--model` omitted on CC backend; log WARN at zero-telemetry `fallbackModelName()` call site | `internal/executor/backend_claudecode.go:357-361`, `internal/executor/runner.go:2611-2620`, `internal/executor/model_routing.go:102-105` |
| A4 | `os.ExpandEnv` silent-empty: fail loud at config load when `${VAR}` resolves empty for `*_token`/`*_key`/`*_secret` fields | `internal/config/config.go:503` |
| A5 | `getBotLogin` failure Debug→Warn (dead PAT silently disables GH-3417 human-recovery guard) | `internal/autopilot/controller.go:3146-3163,3192` |
| A6 | `handleReleasing` nil-releaser skip Debug→Warn + log resolved release policy at controller construction | `internal/autopilot/controller.go:1976-1981,240-244` |

### Phase B: Verifiable preflight (doctor + /ready + startup)

**Goal**: One interface, three consumers (doctor, `/ready`, startup preflight).

- B1: Define `Verifiable { Name() string; Verify(ctx context.Context) error }`. Satisfy existing dormant `gateway.ReadinessChecker` (`internal/gateway/server.go:26,470`) via `Ready() = Verify(ctx)==nil` adapter.
- B2: Implement probes:
  - GitHub: reuse `GetAuthenticatedUser` (`internal/adapters/github/client.go:1096` — exists, currently only lazily called with errors swallowed)
  - Telegram: reuse onboarding `GetUpdates` pattern (`cmd/pilot/onboard_notify.go:190-203` — proven)
  - Slack: new `auth.test` call (current validator is an intentional stub, `onboard_notify.go:181`)
  - Linear/Jira/GitLab/Azure/Asana: new probes (all validators are stubs, `cmd/pilot/onboard_ticket.go:706-751`)
- B3: Wire into `pilot doctor` (replace presence-only checks at `internal/health/health.go:474-656`), daemon startup preflight (after config `Validate()`, before poller construction ~`cmd/pilot/main.go:2168`; non-blocking — log ERROR + alert, don't crash), and `RegisterReadinessChecker`.
- B4: GitHub 401 escalation in poller: consecutive-failure counter distinguishing auth errors from transient errors → alert after threshold (`internal/adapters/github/poller.go:1160-1165,449-455`). Requires giving adapters an alert path — see Technical Decisions.
- B5: Doctor "silently disabled subsystems" panel: alert engine wired Y/N, releaser Y/N, model routing Y/N, subprocess_limits defined-but-disabled, intent classifier on/off.
- B6: Redact secrets in `pilot config show` (`cmd/pilot/config_cmd.go:34` — currently dumps `github.token`, `slack.bot_token` plaintext).

**Coordination**: #3718 (TASK-378 B5: gh-token fallback + doctor 401 check, blocked by #3717) overlaps with B2/B3-GitHub and the 6× token-resolution consolidation (`cmd/pilot/main.go:497,736,1493,1761,1858,2176` + unused `ghAuthToken()` helper at `cmd/pilot/project_wizard.go:49`). Let #3718 land first (token resolution + basic doctor check), then B-phase generalizes it into the interface. Do not double-dispatch.

### Phase C: Execution ledger consistency (independent of A/B)

**Goal**: One canonical ID, one timezone, durable stage events.

- C1: Thread execution UUID (created `internal/executor/dispatcher.go:338,396`) through to `saveLogEntry`/`persistBackendDiagnostics` (`runner.go:1102,1158` — all ~14 call sites currently pass `task.ID`). Also fixes `pattern_feedback.execution_id` (`runner.go:3797`, `internal/memory/feedback.go:80` — FK to `executions(id)` that can never resolve today).
- C2: UTC everywhere: `time.Now().UTC()` in `saveLogEntry` (`runner.go:1104`); audit other `time.Now()` DB writes. (`executions` already UTC via SQLite `CURRENT_TIMESTAMP`.)
- C3: New `execution_events` table: `execution_id` (UUID, FK), `stage` (enum: queued|spec|running|pr_created|ci_passed|ci_failed|merged|released|failed|no_op|…), `occurred_at` (explicit UTC), `detail`. Insert at existing chokepoints: `dispatcher.go:648,685,701,717` + runner milestone sites + autopilot `PRStage` transitions (`internal/autopilot/types.go:377-401`) — autopilot currently *deletes* successful PR rows (`pattern_autopilot_pr_state_ephemeral`), so events are the only durable history.
- C4: Surface: `pilot trace <task-id>` CLI rendering the stage timeline; dashboard stage strip on execution cards (replaces binary success/failed collapse at `internal/dashboard/tui.go:624-627`).
- C5: Drop the `'claude-sonnet-4-5'` column default for `model_name` (`internal/memory/store.go:151`) — NULL means "unknown", stop synthesizing ground truth.

**Not in scope here**: #3724 (`pilot logs` sort) is already filed — one-line fix at `internal/replay/recorder.go:585-590`.

### Phase D: Synthetic canary + Anthropic contract checks (D2 depends on C3)

- D1: Extract shared Anthropic request builder — six independent hand-rolled sites today: `internal/llm/client.go:43-62`, `internal/intent/classifier.go:110-115`, `internal/executor/effort_classifier.go:187-224`, `internal/executor/haiku_parser.go:15-46`, `internal/autopilot/release_summary.go:220`, `internal/executor/backend_anthropic.go:96-109`. Builder enforces `max_tokens` present, rejects unknown top-level keys. Table-driven contract tests per site during migration.
- D2: Scheduled canary — GitHub Actions cron modeled on the existing `.github/workflows/brew-tap-token-canary.yml` (production precedent: schedule → check → idempotent `gh issue create` on failure). Flow: file synthetic issue on sandbox repo (`pilot` label) → poll for stage progression (GitHub-visible: labels/PR/merge; or `execution_events` once C3 lands) → assert terminal state within timeout → alert naming the stalled stage. Sandbox repo = one `projects:` entry (`internal/config/config.go:216-229`); per-project poller loop (`cmd/pilot/main.go:2376-2400`) picks it up with zero new Go code. Needs: sandbox repo created + scratch clone path.
- D3 (optional): live 1-token Anthropic probe in doctor (catches server-side contract drift Go-struct tests can't).

---

## Out of Scope

- Big-bang restructure of gateway/adapters/executor/autopilot — structure is sound; only seams change
- More unit tests / mutation testing — failure class is integration-shaped
- #3718 and #3724 — already filed under TASK-378; this task coordinates, doesn't duplicate
- Plaintext Anthropic key rotation in `~/.pilot/config.yaml` — operational task, carried separately
- `internal/webhooks`, `internal/transcription`, `internal/gateway/auth.go` silent-failure passes — flagged by AUDIT-2026-05-25, deferred to a follow-up sweep
- Dead `RecordAPIError` metric (`internal/autopilot/metrics.go:167`, zero call sites) — fold into B4 only if trivial

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Probe interface | Extend `ReadinessChecker.Ready() bool` / new `Verify(ctx) error` | `Verify(ctx) error` | Doctor needs failure reason + token source; `Ready()` adapter derived for free |
| Adapter → alert path | Global engine / inject `AlertEventProcessor` into adapters / callback | Inject narrow interface | Engine is deliberately DI-only (constructed 2× in main.go); adapters get the small interface, not the engine |
| Stage history | Retrofit `execution_logs` / new `execution_events` table | New table | `execution_logs` keys on wrong ID, local time, free-text, no stage column — retrofit costs more than a clean table |
| Canary scheduling | In-daemon cron (`briefs.Scheduler`) / GitHub Actions cron | GitHub Actions | Existing precedent (`brew-tap-token-canary.yml`), exercises the real deployed daemon from outside, survives daemon death — which is exactly what it must detect |
| Anthropic contract safety | Per-site tests only / shared request builder | Shared builder + tests | Six hand-rolled sites is why #3700/#3703 recur per-site; builder makes the next site correct by construction |

---

## Dispatch Plan (pilot issues, dependency order)

| Issue | Content | Depends on |
|---|---|---|
| V1 | Phase A1+A2+A5+A6 (fail-loud log/alert fixes) → [#3725](https://github.com/qf-studio/pilot/issues/3725) ✅ **SHIPPED** — decomposed into #3733–#3736, merged as v2.203.1–v2.203.4 (epic parent closed manually after epic-PR-creation failure left it dangling) | — |
| V2 | Phase A3+A4 (model-routing logging, config env-var check) → [#3726](https://github.com/qf-studio/pilot/issues/3726) ✅ **SHIPPED** — children #3754→PR #3757 (v2.204.2), #3755→PR #3758 (v2.204.3); parent auto-closed | — |
| V3 | B1+B2+B3 (Verifiable interface, probes, doctor/ready/startup wiring) → [#3760](https://github.com/qf-studio/pilot/issues/3760) ✅ **SHIPPED** — #3767 PR #3771, #3768 PR #3776, #3769 PR #3778 (merged manually 19:3x — autopilot lost post-restart PR tracking, see notes) | #3718 merged ✅ (v2.204.0) |
| V4 | B4+B5+B6 (401 escalation, disabled-subsystems panel, config-show redaction) → [#3839](https://github.com/qf-studio/pilot/issues/3839) 🚀 2026-07-04 | V3 ✅ |
| V5 | C1+C2+C5 + no_op reclassification → [#3759](https://github.com/qf-studio/pilot/issues/3759) ✅ **SHIPPED** — child #3764's work salvaged manually (worker completed but push/PR failed), PR #3773 merged 17:52 UTC. Note: C5 partially deferred — worker filed TASK-381 (store.go model_name migration gap) | — |
| V6 | C3+C4 (execution_events + trace/dashboard) → [#3840](https://github.com/qf-studio/pilot/issues/3840) 🚀 2026-07-04 | V5 ✅ |
| V7 | D1 (shared Anthropic builder + contract tests) → [#3841](https://github.com/qf-studio/pilot/issues/3841) 🚀 2026-07-04 | — |
| V8 | D2 (canary workflow) → [#3862](https://github.com/qf-studio/pilot/issues/3862) 🚀 2026-07-04. Sandbox `qf-studio/pilot-canary-sandbox` created + scaffolded (`version.go` target, `pilot` label); `projects:` entry added to `~/.pilot/config.yaml` (daemon restart pending). Prereq: add repo secret `CANARY_GH_TOKEN` (PAT w/ repo scope on sandbox). | sandbox repo ✅ exists |

Serialize V1/V2 vs V3/V4 if they touch the same files (per SOP `new-project-issue-authoring.md` rule: serialize root-file/overlapping tasks).

---

## Verify

```bash
make build && make test && make lint
# Doctor probes live auth (unplug: set a bogus GITHUB_TOKEN → red check naming source)
./bin/pilot doctor
# /ready has real checks
curl -s localhost:<gateway-port>/ready | jq .checks
# Ledger: stage timeline for a recent task
./bin/pilot trace GH-<n>
# Canary: trigger workflow_dispatch manually, watch sandbox issue → PR → assert
```

---

## Done

- [ ] `pilot doctor` red-checks a revoked token with the token source named
- [ ] `/ready` lists per-adapter checks with real status
- [ ] Alert fires within threshold on sustained GitHub 401s
- [ ] `execution_events` rows exist for a fresh execution; `pilot trace` renders the timeline
- [ ] New `execution_logs` rows join to `executions.id`
- [ ] Shared Anthropic builder used by all six call sites; contract tests green
- [ ] Canary run (manual dispatch) completes issue→PR→terminal-state assertion on sandbox repo

---

## Refs

- Pilot issue (V1): https://github.com/qf-studio/pilot/issues/3725
- Pilot issue (V2): https://github.com/qf-studio/pilot/issues/3726 (`Blocked by: #3725`)
- Research: 4 navigator-research agent reports, 2026-07-03 session (doctor/startup, silent fallbacks, execution ledger, canary infra)
- Prior art: `.agent/audits/AUDIT-2026-05-25.md` ("Silent skip is the default operational mode")
- Related memories: `bug_poller_silent_stop`, `bug_default_model_clobbers_routing`, `bug_llm_missing_max_tokens`, `bug_smoke_test_wrong_cli_contract`, `pattern_autopilot_pr_state_ephemeral`, `feedback_subprocess_not_api`
- Coordinates with: TASK-378 chain (#3715–#3718), #3724, #3732 (B3-sibling: queue re-adoption on restart + serialization visibility, filed 2026-07-03)
- SOP: `.agent/sops/onboarding/new-project-issue-authoring.md`

---

## Notes

- Canary cost note: canary runs bill real model tokens — schedule daily, not hourly; use the cheapest task shape (docs-only change) and Sonnet routing.
- Bench (`pilot-bench`) is NOT the canary vehicle — it benchmarks coding quality via `pilot task --local`, bypassing the issue→PR→autopilot pipeline entirely.
- Unknowns to resolve during implementation: whether `usage_events.execution_id` is ever populated; whether `pattern_feedback` FK violations throw under `PRAGMA foreign_keys=ON` (silently swallowed in `LearningLoop.RecordExecution`?); sandbox repo existence.

---

**Last Updated**: 2026-07-03
