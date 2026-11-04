# TASK-631: Switchable Claude billing mode (subscription | api_key) without a restart, with per-execution attribution

**Status**: ✅ **A2 MERGED 11-04 12:19Z → [PR#6057](https://github.com/qf-studio/pilot/pull/6057) (`9728a8324`, unreleased; #6052 `pilot-done`). Post-merge review REQUEST-CHANGES (own review agrees, repro test confirmed) → fix issue [pilot#6058](https://github.com/qf-studio/pilot/issues/6058) (`autopilot-fix`): [blocking] a row admitted under subscription/unset has no stashed credential (`stashBillingCredential` drops an empty env), so `billingCredentialForRun` re-resolves through a later api_key override — the queued task spends the key while its row says subscription; fix = stash every non-nil credential. [blocking] `TestBillingOverride_SetAppliesToNextAdmissionOnly` covers only api_key→subscription. Minors (unfiled): chat / `pilot task` / post-merge reviewer resolvers read config only, so an override does not reach them (fold into B or D4) · a Set that only extends `until` alerts "api_key → api_key" · store write under the mutex every admission reads · pre-PR shadow reviewer had found the blocking defect (TASK-620 agreement data point). A3 ✅ MERGED 13:38Z → [PR#6061](https://github.com/qf-studio/pilot/pull/6061) (APPROVE-WITH-NOTES; minors: gateway drops the posted `set_by` so a CLI switch reports source gateway · `pilot status` fetches /api/v1/status with no token · TUI observer `program.Send` before Run). #6058 closed by hand 12:26Z before any run → reopened 13:49Z → **[PR#6065](https://github.com/qf-studio/pilot/pull/6065) merged 14:21Z** (every non-nil credential stashed; APPROVE-WITH-NOTES). A4 (set_by passthrough + authed status fetch) → [pilot#6064](https://github.com/qf-studio/pilot/issues/6064) → **[PR#6066](https://github.com/qf-studio/pilot/pull/6066) merged 14:47Z** (APPROVE-WITH-NOTES). **A2 + A3 + both fixes shipped in v2.312.0 (auto-cut 14:47Z at `9a3c7379b`; box on it 15:11Z).** ✅ **B1 usage-limit fallback MERGED 11-04 17:45Z → [PR#6070](https://github.com/qf-studio/pilot/pull/6070) (`e41ca4097`, unreleased → next tag). Own review APPROVE-WITH-NOTES: scope met in full (12 files, +515); minors = a pause armed by an earlier failed fallback is not released when a later fallback succeeds (override set, admission still held to the old resume) · two workers ending rate_limited in the same instant both install the override (two alerts) · key command runs up to 10 s on the worker goroutine before the terminal persist · `reset_at` omitted (not empty) when no reset parses. Enable on the box after the tag: `billing.mode: subscription` + a key source + `usage_limit_pause.fallback: api_key` (explicit subscription only; the unset passthrough never falls back). Filed as [pilot#6069](https://github.com/qf-studio/pilot/issues/6069) (§ LEG B1: `usage_limit_pause.fallback: pause|api_key`, override with source `fallback` and until = the pause's resumeAt, revert by the A2 expiry, `billing_fallback_unavailable` alert on a key miss). Held: C budget scope, D4 console write path.** Was: 🚀 **A2 → [pilot#6052](https://github.com/qf-studio/pilot/issues/6052) + A3 → [pilot#6053](https://github.com/qf-studio/pilot/issues/6053) DISPATCHED 11-04 10:0xZ (`pilot` + `no-decompose`; A3 depends on A2; endpoint and command named billing-mode because the console's /api/v1/billing/... is Paddle) (§ Dispatched legs: A2 store + resolver override + expiry + GET/POST /api/v1/billing + billing_mode_changed alert; A3 pilot billing-mode status/set + pilot status line + TUI chip, depends on A2). A1 ✅ shipped in v2.310.0; F1–F6 in TASK-633 (F4 v2.311.0 fixed the unset-mode regression, F5 process-wide mode, F6 pilot#6050 open).** Was: 🚀 **A1 DISPATCHED 2026-11-03 → [pilot#6016](https://github.com/qf-studio/pilot/issues/6016)** (`pilot` + `no-decompose`). Triage of [pilot#5955](https://github.com/qf-studio/pilot/issues/5955) (scrappydog): legitimate; reply + A1 link posted on the issue. D1–D4 accepted as proposed. **A1 run → [PR#6017](https://github.com/qf-studio/pilot/pull/6017) 11:20Z; CI failed on `TestRecordCost_SplitsByKeySource` (the expected `costKey`s lack the new `BillingMode: "unknown"`); the autopilot CI-fix size guard declined to file (759 production additions > 200) → hand-filed [pilot#6019](https://github.com/qf-studio/pilot/issues/6019) in the autopilot-fix shape (footer `branch:pilot/GH-6016 pr:6017 iteration:1 source:6016`), picked up 11:52Z on the PR branch → **PR#6017 MERGED 12:14Z as `f72c25810`** (#6016 closed `pilot-done`; #6019 closed by hand 12:25Z — borrowed-branch fix issues never self-close). Post-merge review APPROVE-WITH-NOTES, nine minors (no sink: `file_minors` off on the box): OpenCode anthropic backend now starts without `ANTHROPIC_API_KEY` and its :144 comment is stale · autopilot post-merge review bills the subscription in api_key mode · worker billing-key failure `continue`s past `EmitProgress(Failed)`/`monitor.Fail` (card stays running) · first outage row is a real claim → repick backoff on a task that never ran · non-dispatcher `Runner.Execute` path gets no admission pause/alert · decomposed subtasks carry no `billingMode` (cost labelled unknown) · stashed key lives until `stopBillingState` on cancelled/stale rows · resolver half of the api_key acceptance test is hand-built. **Carried into [TASK-633](TASK-633-billing-mode-post-merge-fast-follow.md) same day (nav-research on main confirmed 8 of 9 + found 2: retry ctx from `context.Background()` drops the credential; Navigator bootstrap has none) → F1 [pilot#6021](https://github.com/qf-studio/pilot/issues/6021) · F2 [pilot#6022](https://github.com/qf-studio/pilot/issues/6022) dispatched 13:3xZ.** **Daemon defect seen on this run → [TASK-632](TASK-632-restart-repick-failed-with-open-pr.md) → [pilot#6020](https://github.com/qf-studio/pilot/issues/6020):** when #6019's fix commit landed on the PR branch, autopilot's `redriveHeldPR` re-adopted PR#6017 and `releaseHoldLabels` stripped `pilot-needs-human` from #6016 (11:53:50Z); a `failed` row is not terminal and admission has no PR-awareness, so the poller re-picked #6016 19 s later and a fresh worktree off `f5488f74a` re-implemented the merged work (12:01Z; the 2.309.0 hot-upgrade restart was incidental). Killed by hand at 12:37Z after its gates failed and a Claude retry started (`status=infra`); stray `pilot/GH-6016` at `b5b3564` left on origin. A2 → B → C held until A1 + TASK-633 are on a release (next cut after v2.309.0).
**Created**: 2026-11-03
**Assignee**: Pilot (after D1–D4)

---

## Triage: is the request real?

- **Author**: Eric Bowen (`scrappydog`), GitHub since 2009, fork under `Scrappydog-Software`. Ten prior issues on this repo (Oct 14–27), all closed as shipped, including #5862 usage-limit pause, #5898, #5897, #5865, #5849, #5829, #5803. This issue is the natural next leg of #5862 (they asked there for "continue on an API key" and it was scoped out).
- **Content**: every internal reference checks out (#5275 env scrub, `executor.usage_limit_pause`, `reported_cost_usd`, `pilot budget`). The ask contains nothing destructive and asks for no secret or access. Not a scam.
- **Stale or wrong claims** (correct them in the reply):
  1. "Pilot has no record of which tasks were billed which way" — partly stale. `executions.key_source` (`platform | own | unknown`) exists since #5676, but it is a process-level **env label** (`PILOT_ANTHROPIC_KEY_SOURCE`, `internal/executor/key_source.go:17`) for the hosted console, not a credential-derived fact. It does not express subscription vs API key.
  2. "`pilot budget` limits apply to every execution's cost" — true in effect, but the enforcer reads `usage_events.total_cost` (`internal/budget/enforcer.go:120-171` → `internal/memory/metering.go:216`), not `executions`. `usage_events` has no key-source column at all. Budget scoping needs a column there, not on `executions`.
  3. "The key sits in the daemon's environment; every process inherits it" — **understated**. Quality gates (`internal/quality/runner.go:509`), git (`internal/executor/git.go:1073`), `gh` (`gh_credentials.go:87`) all use `append(os.Environ(), …)` with no scrub. Only acceptance-evidence commands use an allowlist (`acceptance_evidence_run.go:30-47`). The model-subprocess scrub keep-lists the `ANTHROPIC_` / `CLAUDE_` prefixes (`model_env.go:51-65`).

## Why this matters beyond one user

The hosted product has the **same switch with the same restart**: the console flips a tenant between the platform key and the customer's own key by rewriting `/run/pilot/env` and running `systemctl restart pilot` (`pilot-console/internal/fleet/configpush.go:603, :874`). The allowance-exhausted → own-key fallback (TASK-546, `allowanceenforce.go`) therefore also costs a restart and a drain. A daemon-side credential selector with a runtime switch is the primitive both need. This task builds it for self-hosted; the hosted migration is a follow-up (D4).

---

## Context: how the credential reaches `claude` today

- The `claude` CLI picks its own credential. If `ANTHROPIC_API_KEY` is in its env it bills the key, else OAuth / `~/.claude` (`effort_classifier.go:98-99`). No CLI change is needed; the mode is "which vars the subprocess env carries".
- Env build: `backend_claudecode.go:657` `env := append(modelSubprocessEnv(os.Environ()), "PILOT_EXECUTOR=1")`, then `:662-669` appends `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` (from `api_auth_token`, `backend.go:569`), `ANTHROPIC_MODEL`; `cmd.Env = env` at `:710`. There is **no** config field for an Anthropic API key for the CLI subprocess, and no `*_command` secret source anywhere in `internal/config` (secrets come from `${ENV}` expansion only).
- Twelve model spawn sites share `modelSubprocessEnv`: `backend_claudecode.go:657`, `intent_judge.go:208`, `effort_classifier.go:116`, `complexity_classifier.go:75`, `subtask_parser.go:71`, `epic.go:284`, `parallel.go:271`, `runner.go:9501/:9555`, `backend_qwencode.go:203/:257`, `backend_opencode.go:122`. Judges and classifiers bill too; the mode must apply to all of them, not only the main run.
- Post-merge review sandbox: `postMergeReviewEnviron` (`post_merge_confine.go:139-174`) keeps `postMergeAuthEnv` (`:50-56`) and, when none is set, copies `~/.claude/.credentials.json` into the sandbox (`:158-172`). This is the subscription path and must stay reachable in `subscription` mode.
- Usage-limit hook: `dispatcher.go:4095` → `w.onRateLimited` (single func field, `:3444`) → `d.armUsageLimitPause` (wired `:3109`). State: `usageLimitResumeAt`, `usageLimitGen`, `usageLimitTimer` (`dispatcher.go:252-255`, `usage_limit_pause.go:76-148`); owner `"usage-limit"` on `PauseAdmissionFor` (`dispatcher.go:3260`). `ParseRateLimitError` (`ratelimit.go:48`) yields the reset time.
- Attribution: `key_source` stamped once at `lifecycle.go:236` via `KeySourceFromEnv()`; also `navigator_bootstrap.go:104`, `comms/chat_turn_handler.go:276`. Consumers: Prometheus label on `pilot_execution_cost_usd_total` (`gateway/prometheus.go:367`), `GET /api/v1/executions/usage` (`dashboard.go:584`), briefs (`briefs/receipts.go:345`). **No CLI groups by it**: `cmd/pilot/usage.go` reads `usage_events`, `cmd/pilot/metrics.go` reads `executions`, neither filters on key source.
- Runtime config: none. `Config.Reload` (`config/config.go:1183`) has no callers and no SIGHUP handler. Every `pilot X set` (`budget.go:426`, `backend.go:270`) edits `config.yaml` offline and needs a restart. The gateway has no config-mutating endpoint (`server.go:271-293`; only approvals and `POST /api/v1/learnings` mutate). No `pilot pause` command exists (the P1 leg held from #5862).
- Status surfaces: gateway `handleStatus` `server.go:697-728` (already carries `admission_*`); `pilot status` `commands.go:196` + `fetchAdmissionPauseLine` `:309`; TUI header `dashboard/tui.go:1650-1676`.
- Alerts: executor emits via `Runner.emitAlertEvent` (`runner.go:9143`) / `AlertProcessor().ProcessEvent`; a new type needs `executor/alerts.go` const + `alerts/types.go` AlertType + default rule + `alerts/engine.go` EventType + `ProcessEvent` case (model: `handleUsageLimitPause`, `engine.go:1214`). `SeverityInfo` exists.
- Env-class failure signatures (`runner.go:167-180`, GH-5211) treat a missing `ANTHROPIC_API_KEY` as infra noise that does not consume retries. A fail-closed key error must surface as an **admission** failure with its own reason, not as a run that loops through this path.

---

## Design

### Core primitive: a credential resolver, frozen per execution

```
type BillingMode string  // "subscription" | "api_key"

type CredentialResolver interface {
    // Resolve is called once at task admission. It returns the mode in force
    // and the env entries to add to every model subprocess of this execution.
    Resolve(ctx) (mode BillingMode, env []string, err error)
}
```

- **Mode source**, in precedence: runtime override (set by `pilot billing set` / gateway, persisted) → `executor.claude_code.billing.mode` in config → `subscription` (today's behaviour).
- **`subscription`**: `modelSubprocessEnv` drops `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` from the inherited env (new default; `api_auth_token` from config still applies because it is appended after the scrub, so non-Anthropic providers are unaffected). The CLI then uses OAuth / `~/.claude`.
- **`api_key`**: the key comes from `billing.api_key_command` (stdout, trimmed, run with a 10 s timeout, stderr to the log, value never logged) or `billing.api_key_env` (name of a daemon env var). The value is appended as `ANTHROPIC_API_KEY=` to the model-subprocess env only. It never enters `os.Environ()`, so quality gates, git and `gh` never see it. The resolved value lives on the execution context for the run's lifetime and is zeroed after.
- **Frozen per execution**: `Resolve` runs once at admission (next to `lifecycle.Begin`). All twelve spawn sites take the env from the execution context, so judges, classifiers, parallel children and the post-merge review of one run bill the same way. A switch during a run applies from the next admission.
- **Fail closed**: in `api_key` mode a command that fails, times out or prints empty output → the task is **not admitted**; it finishes `failed` with reason `billing_key_unavailable`, admission pauses for owner `billing-key` (new `PauseAdmissionFor` owner, auto-resume on the next successful `Resolve` or on `pilot billing set`), and one alert fires. Never silently fall back to the subscription. Reverse direction: in `subscription` mode an inherited key is scrubbed, so a stray key cannot bill the API.

### Attribution: new column, not an overloaded enum (D1)

- `executions.billing_mode TEXT` (`subscription | api_key | unknown`), set from the **resolver's result**, not from an env label. Same ALTER-with-ignore idiom as #5676 (`memory/store.go:202-203`). Also on `chat_turns` and on `usage_events` (the budget reads that table).
- `key_source` stays untouched; hosted semantics (`platform | own`) are orthogonal (an own key is still `api_key` mode).
- Surfaces: `GET /api/v1/executions/usage` row field; `pilot usage summary --by billing-mode` and `pilot metrics summary` split; Prometheus label `billing_mode` on `pilot_execution_cost_usd_total` beside `key_source`; dashboard metrics card.

### Runtime switch: the first runtime-override store (D2)

- `pilot billing show | set subscription|api_key [--until <RFC3339|duration>]` → `GET/POST /api/v1/billing` on the gateway (same auth as metrics). The daemon persists the override in a small `runtime_overrides` table in the existing SQLite store (`key TEXT PRIMARY KEY, value TEXT, until TIMESTAMP NULL, set_by TEXT, set_at`). It survives restarts, which the author's workaround could not.
- Config stays the default; `pilot billing set` does **not** edit `config.yaml` (the first `set` command that does not). This store is the precedent for the held `pilot pause` P1 leg from #5862.
- Each switch: INFO log line + `billing_mode_changed` alert (SeverityInfo), payload `{from, to, until, source: cli|gateway|fallback}`.

### Automatic fallback on usage limit

- `executor.usage_limit_pause.fallback: pause | api_key` (default `pause`, today's behaviour).
- In `armUsageLimitPause` (`usage_limit_pause.go`): when `fallback == api_key` and the current mode is `subscription`, call `Resolve` for `api_key`. On success: write the runtime override `mode=api_key, until=resetTime+RetryBuffer` (capped by `max_pause`, same arithmetic as today), do **not** pause admission, alert `billing_mode_changed{source: fallback}`. A timer reverts the override at `until` (reuse the `usageLimitTimer` / generation pattern so a second limit extends, never stacks). On `Resolve` failure: pause as today and alert that fallback was unavailable. A usage-limit result while already in `api_key` mode (an API-side 429) pauses as today.
- The single rate-limited task's re-queue at reset (`poller_github.go`) is unchanged.
- Out of scope, as in #5862: a percentage-of-window threshold (no data source).

### Budget scope

- `budget.scope: all | api_key` (default `all`). `UsageQuery` gains `BillingMode string`; `GetUsageSummary` filters on the new `usage_events.billing_mode` column. When an `api_key`-scoped budget is exhausted and the subscription is not under a usage-limit override, `Enforcer.Pause` is replaced by a runtime override back to `subscription` with an alert; otherwise admission pauses as today.

### Visibility

- `handleStatus`: `billing_mode`, `billing_mode_source` (`config|cli|gateway|fallback`), `billing_mode_until`. `pilot status`: one line `billing: api_key (fallback, back to subscription at <time>)`. TUI header: one chip beside the admission state.

---

## Legs (dispatch order)

| Leg | Scope | Depends on |
|---|---|---|
| **A1 — resolver + attribution** | `CredentialResolver`, config `billing.mode/api_key_command/api_key_env`, subscription-mode scrub of inherited keys, api_key injection at the twelve spawn sites via execution context, fail-closed admission (`billing_key_unavailable`, owner `billing-key`, alert), `billing_mode` column on `executions`/`chat_turns`/`usage_events`, usage endpoint field, Prometheus label, `pilot usage`/`pilot metrics` split. Mode comes from config only. | — |
| **A2 — runtime switch + visibility** | `runtime_overrides` table, `GET/POST /api/v1/billing`, `pilot billing show/set`, status fields, `pilot status` line, TUI chip, `billing_mode_changed` alert. | A1 |
| **B — usage-limit fallback** | `usage_limit_pause.fallback`, override-with-expiry in `armUsageLimitPause`, revert timer, fallback-unavailable alert. | A1, A2 |
| **C — budget scope** | `budget.scope`, `UsageQuery.BillingMode`, scoped exhaustion behaviour. | A1 |

A1 is the largest; it is still one PR (one schema change, one env seam, one admission check). Each leg is `pilot` + `no-decompose`.

## Acceptance (per leg, pinned by tests)

- A1: with `billing.mode: subscription` and `ANTHROPIC_API_KEY` in the daemon env, the recorded `cmd.Env` of the claude backend and of the effort classifier contain no `ANTHROPIC_API_KEY`; mutation: remove the scrub → test fails. With `api_key` + a fake `api_key_command`, `cmd.Env` carries the command's stdout, `os.Environ()` and the quality-gate env do not; mutation: inject into `os.Setenv` instead → test fails. A command exiting 1 → execution `failed`, reason `billing_key_unavailable`, admission paused for `billing-key`, one alert; mutation: fall back to subscription → test fails. `executions.billing_mode` equals the resolver's mode; `GET /api/v1/executions/usage` row carries it.
- A2: `POST /api/v1/billing {mode: api_key}` → the next admitted task resolves `api_key`, a task running since before the call finishes with `subscription`; restart → override still in force; `GET /api/v1/status` carries the three fields.
- B: a `rate_limited` outcome with a parsable reset and `fallback: api_key` → no admission pause, override `api_key` with `until = reset + RetryBuffer`, next task resolves `api_key`; fake clock past `until` → mode `subscription` again; resolver failure → pause as today plus the unavailable alert. Mutation: drop the revert timer → test fails.
- C: two `usage_events` rows, one per mode, `budget.scope: api_key`, daily limit between the two → status exhausted on the api_key row only.
- Per #5862 lesson: do not re-run `make build/test/lint` as evidence in the PR; the daemon's gates run them.

## Known pitfalls

- `postMergeReviewEnviron` copies `~/.claude/.credentials.json` only when no auth env is set. In `api_key` mode the injected key must be present in that env before the check, or the review falls to the subscription.
- The `ANTHROPIC_` keep-list prefix in `model_env.go:61` also protects `ANTHROPIC_BASE_URL`/`ANTHROPIC_MODEL`. Scrub the two credential names explicitly; do not drop the prefix from the keep-list.
- `backend_openai.go:56` reuses `APIAuthToken` as its key. The billing resolver is Anthropic-only; it must not touch non-Anthropic backends.
- `KeySourceFromEnv` is evaluated at execution creation; `billing_mode` must be stamped at the same point (`lifecycle.go:236`) so one row never mixes a pre-switch label with a post-switch run.
- Env-class signatures (`runner.go:167-180`) match "no API key configured"; the fail-closed path must short-circuit at admission and never reach the retry classifier.
- `handleUsageLimitPause` drops the alert silently without a matching default rule; add the rule for `billing_mode_changed` in the same PR as the event.

## Founder decisions

| # | Question | Proposed |
|---|---|---|
| D1 | New `billing_mode` column vs extending `key_source` | **New column.** `key_source` is the hosted owner label; mixing enums breaks the console meter's `FILTER key_source='platform'`. |
| D2 | Where the runtime override lives | **SQLite `runtime_overrides` table** in the existing store. Survives restarts; sets the precedent for `pilot pause`. Alternative: `~/.pilot/data/runtime.json`. |
| D3 | Accept the author's offer to test a build | Yes. Ask them to run A1 on their box before A2 is filed. |
| D4 | Hosted follow-up: replace `systemctl restart pilot` in `configpush.go` with `POST /api/v1/billing` + key rotation | **Out of scope here**; file as a TASK-546/TASK-502 follow-up once A2 is on a release. |

## Reply to post on #5955 (draft, after D1–D4)

> Thanks — accepted, and the split you offer is the one we'll use (A1 resolver + attribution → A2 runtime switch + status → B fallback → C budget scope). Two corrections to the "today" section: `executions` already has `key_source` (platform/own/unknown, #5676), but it is an env label for the hosted console and does not express subscription vs API key, so this adds a separate `billing_mode` derived from the credential actually injected. And `pilot budget` reads `usage_events`, not `executions`, so the scope filter lands there. One thing you understated: quality gates and git currently inherit the full daemon env, so the key reaches project test commands too; in `api_key` mode with `api_key_command` the key never enters the daemon env at all. We'd take you up on testing A1 on your box.

## Dispatched legs

### LEG A1 — repo: qf-studio/pilot — title: feat(executor): billing mode subscription or api_key — per-execution credential resolver, fail-closed api_key_command, billing_mode attribution

## Context

Requested by @scrappydog in #5955 (same author as #5862). They run Pilot on a Claude Max subscription and move to an Anthropic API key for a few days when the weekly window is nearly used up. Today that means exporting `ANTHROPIC_API_KEY` into the daemon's environment and restarting. The key then reaches every subprocess, and no row records how a task was billed.

Verified state on main:

- The `claude` CLI picks its own credential: with `ANTHROPIC_API_KEY` in its env it bills the key, otherwise the OAuth login (comment in `internal/executor/effort_classifier.go` near line 98). The mode is therefore "which variables the subprocess env carries"; no CLI change is needed.
- The subprocess env is built in `internal/executor/backend_claudecode.go` near line 657: modelSubprocessEnv over the daemon env, then `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` (config api_auth_token) and `ANTHROPIC_MODEL` are appended. modelSubprocessEnv in `internal/executor/model_env.go` keep-lists the prefixes ANTHROPIC_, CLAUDE_ and CLAUDE_CODE_, so an inherited key always passes.
- Twelve model spawn sites share modelSubprocessEnv: the claude backend, `internal/executor/intent_judge.go`, `internal/executor/effort_classifier.go`, `internal/executor/complexity_classifier.go`, `internal/executor/subtask_parser.go`, `internal/executor/epic.go`, `internal/executor/parallel.go`, two sites in `internal/executor/runner.go`, two in `internal/executor/backend_qwencode.go`, one in `internal/executor/backend_opencode.go`. Judges and classifiers bill too.
- The post-merge review sandbox (postMergeReviewEnviron in `internal/executor/post_merge_confine.go`) copies the credentials file from the home directory only when none of the auth variables is set.
- Quality gates (`internal/quality/runner.go` near line 509), git and gh helpers build their env from the full daemon environment with no scrub.
- Attribution: executions.key_source is stamped once in ExecutionLifecycle.Begin (`internal/executor/lifecycle.go` near line 236) from the env label PILOT_ANTHROPIC_KEY_SOURCE (`internal/executor/key_source.go`). It means platform-vs-own for the hosted console and must not change. chat_turns carries the same label (`internal/memory/chat_turns.go`). usage_events (`internal/memory/metering.go`, the table the budget enforcer reads) has no key or mode column.
- Admission pause by owner exists: PauseAdmissionFor and ResumeAdmissionFor in `internal/executor/dispatcher.go`, owners usage-limit, self-upgrade, platform breaker. The usage-limit owner's timer idiom lives in `internal/executor/usage_limit_pause.go`.
- Env-class failure signatures in `internal/executor/runner.go` near line 167 (GH-5211) match a missing API key as infra noise that does not consume retries. The fail-closed path below must stop at admission and never reach that classifier.

probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnv\(
probe: present qf-studio/pilot func[[:space:]]KeySourceFromEnv\(
probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]PauseAdmissionFor\(
probe: absent qf-studio/pilot BillingModeAPIKey[[:space:]]+BillingMode
probe: absent qf-studio/pilot AlertEventTypeBillingKeyUnavailable[[:space:]]+AlertEventType

## Scope

Config, in `internal/executor/backend.go` on ClaudeCodeConfig, validated in `internal/config/config.go`, documented in `configs/pilot.example.yaml`:

- billing.mode: subscription or api_key. Default subscription. In this leg the mode comes from config only; a runtime switch is a separate leg.
- billing.api_key_command: a command string run through the shell at each admission in api_key mode; trimmed stdout is the key. Timeout 10 s. stderr goes to the log at warn level; the stdout value is never logged.
- billing.api_key_env: the name of a daemon environment variable that holds the key. Exactly one of api_key_command and api_key_env is required when mode is api_key; both set, or neither, is a config validation error.

Credential resolver (new file, plain text: internal/executor/billing_mode.go):

- A BillingMode string type with constants for subscription and api_key, and a resolver with one method that returns the mode in force and the env entries to add to every model subprocess of one execution, or an error.
- subscription mode returns no entries. api_key mode returns one entry, ANTHROPIC_API_KEY equals the resolved value.
- The resolver runs once per execution, in the dispatcher right before beginWithGenerationRetry calls ExecutionLifecycle.Begin, and once per chat turn before the turn's claude spawn in `internal/comms/chat_turn_handler.go`. Begin gets the mode as a new parameter or via the Task so the row carries it from creation.
- The result travels on the context: a WithBillingCredential(ctx, cred) helper and a BillingCredentialFromContext(ctx) reader in the executor package. The value is zeroed when the execution finishes.

Subprocess env, in `internal/executor/model_env.go`:

- A context-aware variant modelSubprocessEnvFor(ctx, base) that first runs the existing scrub, then removes ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN inherited from the daemon env, then appends the credential entries from the context. Keep the ANTHROPIC_ prefix on the keep-list for ANTHROPIC_BASE_URL and ANTHROPIC_MODEL; remove only the two credential names.
- All twelve spawn sites listed above switch to the context-aware variant. Config api_auth_token in the claude backend is still appended after the scrub, so non-Anthropic providers behave as today.
- postMergeReviewEnviron must build on the same variant so the injected key is present before its credentials-file check.
- Quality gates, git and gh are not changed in this leg. With api_key_command the key never enters the daemon env, so they never see it; api_key_env remains the operator's choice.

Fail closed, in the dispatcher admission path:

- A resolver error in api_key mode (command exit non-zero, timeout, empty stdout, env var unset) finishes the execution row as failed with the error text prefixed billing_key_unavailable and the command's exit code, without starting the backend. It never falls back to the subscription.
- The same event calls PauseAdmissionFor with a new exported owner constant for billing-key, and emits one alert through the existing alert processor: a new executor alert event type for billing key unavailable, mirrored in `internal/alerts/types.go` (AlertType, default rule, severity warning) and `internal/alerts/engine.go` (EventType, ProcessEvent case, handler modelled on handleUsageLimitPause). Without the default rule the engine drops the alert silently; add both in this PR.
- While paused for that owner the dispatcher re-runs the resolver every 5 minutes (timer and generation idiom from `internal/executor/usage_limit_pause.go`); the first success resumes admission for the owner and logs one info line.

Attribution:

- New column billing_mode TEXT (values subscription, api_key, unknown) on executions, chat_turns and usage_events, added with the ALTER-with-ignore idiom used for key_source in `internal/memory/store.go`. A NormalizeBillingMode helper next to NormalizeKeySource. Rows created before this change read back unknown.
- executions.billing_mode is set from the resolver result in Begin, at the same point as key_source. chat_turns.billing_mode likewise in the chat turn handler. usage_events.billing_mode is copied from the execution when the per-task events are recorded in `internal/memory/metering.go`.
- The usage endpoint row in `internal/gateway/dashboard.go` (field keySource, near line 562) gains billingMode, read by GetExecutionUsageSince.
- The Prometheus counter pilot_execution_cost_usd_total in `internal/gateway/prometheus.go` gains the label billing_mode beside key_source; the metrics recorder call near line 5754 of `internal/executor/runner.go` passes the mode, and the restart baseline query in the store groups by both.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBillingMode` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestModelSubprocessEnvFor` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/memory/ -run TestBillingMode` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run TestExecutionUsage` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/alerts/ -run TestBillingKeyUnavailable` passes; paste the output into the PR body.
- [ ] A test sets ANTHROPIC_API_KEY in the base env with mode subscription and asserts the claude backend's recorded command env has no ANTHROPIC_API_KEY and no ANTHROPIC_AUTH_TOKEN, while ANTHROPIC_BASE_URL from config survives.
- [ ] A test with mode api_key and a fake command that prints a value asserts the recorded command env of the claude backend and of the effort classifier both carry that value as ANTHROPIC_API_KEY, and that the process environment does not.
- [ ] A test with mode api_key and a command that exits 1 asserts the execution row is failed with an error starting billing_key_unavailable, no backend run started, admission is paused for the billing-key owner, and one alert event was processed; a following fake-clock tick with a command that succeeds resumes admission.
- [ ] A test asserts an executions row created in api_key mode reads back billing_mode api_key and key_source unchanged from the env label.
- [ ] A test calls the usage endpoint and asserts the row JSON contains the literal key "billingMode".
- [ ] remove the credential-name strip from modelSubprocessEnvFor -> TestModelSubprocessEnvFor_SubscriptionStripsInheritedKey fails
- [ ] make the resolver return subscription on a command error -> TestBillingMode_FailClosedNeverFallsBack fails
- [ ] remove the billing_mode write in Begin -> TestBillingMode_StampedOnExecution fails
- [ ] remove the PauseAdmissionFor call from the fail-closed path -> TestBillingMode_FailClosedPausesAdmission fails
- [ ] Config validation test: mode api_key with neither api_key_command nor api_key_env fails validation; with both set fails validation.
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Runtime switch and persistence (pilot billing set, gateway endpoint, status fields, TUI), the pilot usage and pilot metrics split by mode, the usage-limit fallback (usage_limit_pause.fallback), budget scope, scrubbing quality-gate and git env, any change to key_source semantics, any hosted console change.

## Refs

- #5955 (request), #5862 (usage-limit pause), #5275 (env scrub), #5676 (key_source), #5211 (env-class failures)
- Navigator TASK-631 in this repo

## Refs

- [pilot#5955](https://github.com/qf-studio/pilot/issues/5955) — the request
- #5862 usage-limit pause (same author), #5275 env scrub, #5676 `key_source` + reported cost, #5211 env-class failures
- TASK-546 (hosted allowance → own-key fallback), TASK-502 (platform key pool), memory `feedback_subprocess_not_api`
- `pilot-console/internal/fleet/configpush.go:603` — the hosted restart this primitive could remove

### LEG A2 — repo: qf-studio/pilot — title: feat(executor): runtime billing-mode override — runtime_overrides store, resolver precedence override > config, expiry timer, GET/POST /api/v1/billing-mode, billing_mode_changed alert

## Context

A1 (PR#6017) and the fast-follows F1–F5 (TASK-633) shipped the per-execution credential resolver: BillingResolver in `internal/executor/billing_mode.go` (NewBillingResolver near line 203, Mode near line 214, Resolve near line 225) reads only the config block; the mode in force is cfg.EffectiveMode(), which is BillingModeUnset when the block or the field is empty (GH-6032, passthrough of the daemon environment), subscription or api_key. NewBillingResolver and the Runner constructor (`internal/executor/runner.go` near line 1491) also publish that mode process-wide through SetProcessBillingMode (GH-6038), which modelSubprocessEnvFor in `internal/executor/model_env.go` consults for spawn sites with no credential on the ctx. The credential is resolved once per execution by admitBilling in `internal/executor/billing_admission.go` (near line 53) and frozen on the execution ctx; a usage-limit pause already uses a clock-driven timer with a generation counter in `internal/executor/usage_limit_pause.go` (near lines 127–133 and 162–166). The gateway exposes admission state through the AdmissionStatusSource interface in `internal/gateway/server.go` (near line 86) that *executor.Dispatcher implements, wired by SetAdmissionStatusSource (near line 483); handleStatus (near line 697) already writes admission_paused and admission_pause_owners; protected routes hang off apiMux (near line 271). Alerts: the executor emits AlertEventTypeBillingKeyUnavailable (`internal/executor/alerts.go` near line 125), mirrored by EventTypeBillingKeyUnavailable in `internal/alerts/engine.go` (near line 237) with a ProcessEvent case (near line 755), an AlertType in `internal/alerts/types.go` (near line 151) and a default rule (near line 736). The store adds columns with the ALTER-with-ignore idiom in `internal/memory/store.go` (near line 207) and tables with CREATE TABLE IF NOT EXISTS (near line 431).

Today a mode change is a config edit plus a restart. The requester's workaround (#5955) and the hosted console's key switch (configpush.go, TASK-631 D4) both need a restart. Naming: the hosted console already serves /api/v1/billing/... for Paddle (customer billing), so the daemon's endpoint and command are billing-mode, matching the billing_mode column; they never touch customer billing. This leg adds the first runtime override store (founder decision D2: SQLite table in the existing store, survives restarts) and the daemon-side switch. The CLI and the status surfaces are leg A3.

probe: present qf-studio/pilot func[[:space:]]NewBillingResolver\(
probe: present qf-studio/pilot func[[:space:]]SetProcessBillingMode\(
probe: present qf-studio/pilot type[[:space:]]AdmissionStatusSource[[:space:]]interface
probe: present qf-studio/pilot ALTER TABLE executions ADD COLUMN billing_mode TEXT
probe: present qf-studio/pilot AlertEventTypeBillingKeyUnavailable[[:space:]]+AlertEventType
probe: absent qf-studio/pilot CREATE TABLE IF NOT EXISTS runtime_overrides
probe: absent qf-studio/pilot AlertEventTypeBillingModeChanged
probe: absent qf-studio/pilot \"/api/v1/billing-mode\"

## Scope

- Store (`internal/memory/store.go`): table runtime_overrides (key TEXT PRIMARY KEY, value TEXT NOT NULL, until DATETIME NULL, set_by TEXT NOT NULL DEFAULT '', set_at DATETIME NOT NULL) created with the CREATE TABLE IF NOT EXISTS idiom; methods GetRuntimeOverride(key) (value, until, setBy, setAt, found, err), SetRuntimeOverride(key, value, until, setBy), DeleteRuntimeOverride(key). Key for this leg: billing.mode. Values: subscription or api_key only; an absent row means "config decides".
- Resolver precedence (`internal/executor/billing_mode.go`): BillingResolver gains SetOverrideSource(func() (mode BillingMode, source string, until time.Time, ok bool)). Mode() returns the override when ok, else cfg.EffectiveMode(). Resolve() is unchanged otherwise, so a running execution keeps the credential it froze at admission; the switch applies from the next admitBilling.
- Dispatcher (`internal/executor/dispatcher.go` plus a new file, plain text: internal/executor/billing_override.go): BillingOverride() (mode, source, until) and SetBillingOverride(mode BillingMode, until time.Time, setBy string) and ClearBillingOverride(setBy string). Set persists the row, calls SetProcessBillingMode(mode), installs the resolver override source, logs one INFO line, emits the alert, arms an expiry timer with the usage_limit_pause.go clock + generation pattern when until is non-zero (a later Set replaces the timer, never stacks). Clear deletes the row, restores SetProcessBillingMode(cfg.EffectiveMode()), clears the source, logs and alerts. A switch away from api_key while admission is paused for owner billing-key resumes that owner. At boot the dispatcher loads the row, drops it when until is in the past, otherwise applies it and arms the remaining timer.
- Source values: cli, gateway, fallback (reserved for leg B), config (reported when no override is in force).
- Gateway (`internal/gateway/server.go`): interface BillingOverrideSource { BillingOverride() executor.BillingOverrideStatus; SetBillingOverride(mode string, until time.Time, setBy string) error; ClearBillingOverride(setBy string) error } wired by SetBillingOverrideSource next to SetAdmissionStatusSource; route /api/v1/billing-mode on apiMux: GET returns {mode, source, until, config_mode}; POST with JSON {"mode": "subscription" | "api_key" | "config", "until": RFC3339 optional} sets or, for "config", clears; invalid mode → 400 with the accepted values; no source wired → 503. handleStatus gains billing_mode, billing_mode_source, billing_mode_until (empty string when none) and billing_config_mode. AdmissionStatus and the existing fields are unchanged.
- Alert: AlertEventTypeBillingModeChanged = "billing_mode_changed" in `internal/executor/alerts.go`; EventTypeBillingModeChanged, a ProcessEvent case and handleBillingModeChanged in `internal/alerts/engine.go` modelled on handleUsageLimitPause; AlertTypeBillingModeChanged in `internal/alerts/types.go` with a default rule, SeverityInfo; payload fields from, to, until, source, set_by. Message: "Billing mode changed <from> → <to> by <set_by> (<source>)" plus "until <time>" when set.
- Runner-side: SetProcessBillingMode stays the single publication point for bare-ctx spawn sites; no spawn site changes.
- Config: no new key. `configs/pilot.example.yaml` billing block gains two comment lines that say a runtime override set through the gateway or the CLI wins over mode until cleared or expired.

Tests to add, named exactly: TestBillingOverride_SetAppliesToNextAdmissionOnly (new file, plain text: internal/executor/billing_override_test.go — a task admitted before the switch keeps its credential, the next one resolves the new mode), TestBillingOverride_SurvivesRestart (set, build a second dispatcher on the same store, mode and until restored), TestBillingOverride_ExpiryRevertsToConfig (fake clock past until → config mode, row gone, alert emitted), TestBillingOverride_SwitchPublishesProcessMode (after Set the effort classifier's env under a bare ctx follows the override), TestBillingOverride_ClearResumesBillingKeyPause, TestRuntimeOverrides_StoreRoundTrip (`internal/memory/store_test.go` or a new file, plain text: internal/memory/runtime_overrides_test.go), TestGateway_BillingEndpoint (`internal/gateway/server_test.go`: GET, POST set, POST config clears, 400 on a bad mode, 503 without a source, auth as the other apiMux routes), TestStatus_CarriesBillingFields, TestBillingModeChangedAlert_RuleAndPayload (`internal/alerts/engine_test.go`).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBillingOverride` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/memory/ -run TestRuntimeOverrides` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run TestGateway_BillingEndpoint` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run TestStatus_CarriesBillingFields` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/alerts/ -run TestBillingModeChangedAlert` passes; paste the output into the PR body.
- [ ] apply the override to a credential already frozen on a running execution -> TestBillingOverride_SetAppliesToNextAdmissionOnly fails
- [ ] skip the boot-time load of the row -> TestBillingOverride_SurvivesRestart fails
- [ ] drop the expiry timer -> TestBillingOverride_ExpiryRevertsToConfig fails
- [ ] skip SetProcessBillingMode on Set -> TestBillingOverride_SwitchPublishesProcessMode fails
- [ ] accept an unknown mode on POST -> TestGateway_BillingEndpoint fails
- [ ] emit the alert without a default rule -> TestBillingModeChangedAlert_RuleAndPayload fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The CLI command, the pilot status line and the TUI chip (leg A3); the usage-limit fallback that writes an override with source fallback (leg B); budget scope (leg C); any config.yaml write; the hosted console's configpush.go (TASK-631 D4); key_source; the unset-mode passthrough and the process-wide fallback semantics (GH-6032, GH-6038) beyond calling SetProcessBillingMode on a switch.

## Refs

- #5955 (request) · PR#6017 (A1) · TASK-633 F1–F6 · GH-5862 (usage-limit pause timer, admission owners) · Navigator TASK-631 in this repo

### LEG A3 — repo: qf-studio/pilot — title: feat(cli): pilot billing-mode status and set through the gateway, a billing line in pilot status, a billing chip in the TUI header

## Context

Leg A2 adds GET/POST /api/v1/billing-mode on the gateway and billing_mode, billing_mode_source, billing_mode_until and billing_config_mode on /api/v1/status. Operators still have no command for it. Existing set commands (`cmd/pilot/budget.go` near line 426, `cmd/pilot/backend.go` near line 270) edit config.yaml offline and need a restart; this command must not. pilot status in `cmd/pilot/commands.go` (near line 196) already fetches /api/v1/status through fetchAdmissionPauseLine (near line 309) with a 2 s client and renders admissionPauseLine; the TUI is the bubbletea Model in `internal/dashboard/tui.go` whose View (near line 1395) renders the header.

Depends on: #{{A2}}

probe: present qf-studio/pilot func[[:space:]]fetchAdmissionPauseLine\(
probe: present qf-studio/pilot func[[:space:]]admissionPauseLine\(
probe: absent qf-studio/pilot Use:[[:space:]]+"billing-mode"

## Scope

- New command file, plain text: cmd/pilot/billing_mode.go — `pilot billing-mode status` prints mode, source, until and the config mode from GET /api/v1/billing-mode; `pilot billing-mode set <subscription|api_key|config> [--until <RFC3339|duration>]` posts to /api/v1/billing-mode with set_by cli and prints the resulting status; exit 1 with the gateway's message on 400, exit 2 with "daemon not reachable at <url>" when the gateway does not answer. The base URL and auth come from the same helpers pilot status uses. No config.yaml write.
- `pilot status`: one line after the admission line, "billing: <mode> (<source>)" plus ", back to <config mode> at <until>" when an override has an expiry; omitted when the status body has no billing_mode field (older daemon).
- TUI: the header in `internal/dashboard/tui.go` gains one chip "billing <mode>" fed by a new dashboard message BillingModeMsg{Mode, Source, Until} that the daemon sends at boot and on every switch (from the dispatcher's Set and Clear via the existing program.Send path in `cmd/pilot/main.go`); source config renders the mode only, an override renders "<mode>·<source>".
- Docs: one page section in `docs/content/` for the billing block if a billing or configuration page exists there, with the three commands; otherwise no docs change.

Tests to add, named exactly: TestBillingCmd_StatusAndSet (`cmd/pilot/`, new file plain text: cmd/pilot/billing_mode_test.go — httptest gateway, both commands, 400 and unreachable exit codes), TestStatusCmd_BillingLine (the new file: line present with an override, absent on an older body), TestTUI_HeaderBillingChip (`internal/dashboard/tui_test.go`: chip text for config and override sources).

## Acceptance

- [ ] `go test -race ./cmd/pilot/ -run TestBillingCmd` passes; paste the output into the PR body.
- [ ] `go test -race ./cmd/pilot/ -run TestStatusCmd_BillingLine` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/dashboard/ -run TestTUI_HeaderBillingChip` passes; paste the output into the PR body.
- [ ] write the mode into config.yaml instead of posting -> TestBillingCmd_StatusAndSet fails
- [ ] print the billing line for a status body without billing_mode -> TestStatusCmd_BillingLine fails
- [ ] render the chip without the source for an override -> TestTUI_HeaderBillingChip fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The gateway endpoint and the store (leg A2), the usage-limit fallback (leg B), budget scope (leg C), any change to admission or spawn sites, the hosted console.

## Refs

- Leg A2 (endpoint) · #5955 · Navigator TASK-631 in this repo

### LEG A4 — repo: qf-studio/pilot — title: fix(gateway): /api/v1/billing-mode honours the posted set_by (cli | gateway) so a CLI switch reports source cli; pilot status sends the API token

## Context

Leg A2 (PR#6057) added POST /api/v1/billing-mode in `internal/gateway/billing_mode.go`: billingModeRequest (near line 32) has only Mode and Until, and both calls into the source hard-code the setBy as executor.BillingModeSourceGateway (near lines 101 and 115). Leg A3 (PR#6061) made `pilot billing-mode set` post set_by "cli" (`cmd/pilot/billing_mode.go` near line 133, billingModeSetRequest.SetBy near line 64), and the docs in `docs/content/cli/commands.mdx` (near line 1357) and the `pilot status` line (near line 331) promise the source cli. The gateway drops the field, so every CLI switch is stored and reported as source gateway; the A3 tests pass only because their fake gateway replies Source "cli" by itself (the doc-vs-wire pitfall). The dispatcher side already derives the source from setBy (billingOverrideSourceOf in `internal/executor/billing_override.go`: the part before the first colon), so a setBy of cli becomes source cli with no executor change. Second gap from the same review: fetchStatusBody in `cmd/pilot/commands.go` (near line 331) calls the protected /api/v1/status with no Authorization header, so with auth.type api-token the billing line never prints, while `pilot billing-mode` (near line 183) does send the token.

Depends on: #6058

probe: present qf-studio/pilot json:"set_by,omitempty"
probe: present qf-studio/pilot BillingModeSourceCLI[[:space:]]+=
probe: present qf-studio/pilot func[[:space:]]fetchStatusBody\(
probe: absent qf-studio/pilot req\.SetBy

## Scope

- `internal/gateway/billing_mode.go`: billingModeRequest gains SetBy string with the json tag set_by,omitempty. Accepted values are cli and gateway; an empty value means gateway; any other value (fallback, config, free text) is a 400 whose body lists the accepted values. The accepted value is passed as setBy to SetBillingOverride and to ClearBillingOverride in place of the hard-coded executor.BillingModeSourceGateway. No other behaviour of the handler changes.
- `cmd/pilot/commands.go`: fetchStatusBody sends the same Authorization header as the billing-mode command, from the same config helpers, so `pilot status` prints the billing line on an authed daemon. No output format change.
- `cmd/pilot/billing_mode_test.go`: the set path of TestBillingCmd_StatusAndSet runs against the real gateway handler (gateway.NewServerWithAuth or the handler on an httptest server) backed by a stub BillingOverrideSource that records setBy and reports it as Source, so the source cli on the printed status comes from the producer, not from a fake reply. The fake-gateway status cases may stay for formatting.
- Docs: no change; `docs/content/cli/commands.mdx` already documents the source cli.

Tests to add, named exactly: TestGateway_BillingEndpoint_SetBy (`internal/gateway/billing_mode_test.go`: set_by cli reaches the source and the response says source cli; omitted set_by means gateway; set_by fallback, config and an unknown word are 400 with the accepted values; clear with set_by cli reports cli), TestBillingCmd_SetReportsCLISource (`cmd/pilot/billing_mode_test.go`: real handler as above, `pilot billing-mode set api_key` prints source cli), TestStatusCmd_BillingLineAuthed (`cmd/pilot/billing_mode_test.go`: an httptest gateway that rejects a missing bearer token with 401; the billing line prints with the token configured and is absent without the daemon).

## Acceptance

- [ ] `go test -race ./internal/gateway/ -run TestGateway_BillingEndpoint` passes; paste the output into the PR body.
- [ ] `go test -race ./cmd/pilot/ -run 'TestBillingCmd|TestStatusCmd'` passes; paste the output into the PR body.
- [ ] keep the hard-coded gateway source on POST -> TestGateway_BillingEndpoint_SetBy and TestBillingCmd_SetReportsCLISource fail
- [ ] accept set_by fallback on POST -> TestGateway_BillingEndpoint_SetBy fails
- [ ] drop the Authorization header from fetchStatusBody -> TestStatusCmd_BillingLineAuthed fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The override semantics, the store and the admission path (leg A2 and its fix #6058); the usage-limit fallback that writes source fallback (leg B); the TUI chip; any new CLI flag; the hosted console.

## Refs

- PR#6057 (A2) · PR#6061 (A3, post-merge review minors 1–4) · #6058 · Navigator TASK-631 in this repo

### LEG B1 — repo: qf-studio/pilot — title: feat(executor): usage-limit fallback — a subscription usage limit switches the daemon to api_key through the runtime override until the reset instead of pausing admission

## Context

Since v2.312.0 the daemon has a runtime billing-mode override (leg A2, PR#6057 + PR#6065): Dispatcher.SetBillingOverride(mode string, until time.Time, setBy string) in `internal/executor/billing_override.go` (near line 108) persists the row, publishes the process mode, arms an expiry timer with the usage-limit clock + generation pattern and emits billing_mode_changed; billingOverrideSourceOf derives the source from setBy, and BillingModeSourceFallback = "fallback" (near line 17) is reserved for this leg. A later Set replaces the override and its timer; ClearBillingOverride and the expiry revert to the config mode. Set refuses api_key when the config has neither api_key_command nor api_key_env (ErrBillingOverrideInvalid).

A usage-limit result still pauses: the worker calls onRateLimited after a run ends ExecStatusRateLimited (`internal/executor/dispatcher.go` near line 4261), which is armUsageLimitPause in `internal/executor/usage_limit_pause.go` (near line 89): it parses the reset with parseRateLimitErrorAt, computes resumeAt = reset + the scheduler RetryBuffer (EffectiveFallbackPause when no reset parses, now + RetryBuffer when the reset already passed), caps it at now + EffectiveMaxPause, then pauses admission for AdmissionPauseOwnerUsageLimit, arms the release timer and emits usage_limit_pause once per episode. UsageLimitPauseConfig in `internal/executor/backend.go` (near line 1556) has Enabled, MaxPause, FallbackPause; validateUsageLimitPause in `internal/config/config.go` (near line 1264) checks the durations; the example block is in `configs/pilot.example.yaml` (near line 726). The mode in force is d.billingResolverOrConfig().Mode() (`internal/executor/billing_admission.go`). Alerts follow the A2 pattern: AlertEventType const in `internal/executor/alerts.go`, EventType + handleEvent case + handler in `internal/alerts/engine.go` (handleBillingModeChanged near line 1376 is the model), AlertType + default rule + HandlerEmittedAlertTypes entry in `internal/alerts/types.go`.

This leg is the automatic switch the requester asked for in #5955: on a subscription usage limit, run the queue on the API key until the subscription resets, then come back, with no operator action and no restart.

Depends on: #6064

probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]armUsageLimitPause\(
probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]SetBillingOverride\(
probe: present qf-studio/pilot BillingModeSourceFallback[[:space:]]*=
probe: present qf-studio/pilot type[[:space:]]UsageLimitPauseConfig[[:space:]]struct
probe: present qf-studio/pilot func[[:space:]]validateUsageLimitPause\(
probe: absent qf-studio/pilot AlertEventTypeBillingFallbackUnavailable
probe: absent qf-studio/pilot yaml:"fallback,omitempty"

## Scope

- Config (`internal/executor/backend.go`): UsageLimitPauseConfig gains Fallback string with the yaml tag fallback,omitempty; accepted values pause (default, today's behaviour) and api_key; EffectiveFallback() returns pause for empty. validateUsageLimitPause (`internal/config/config.go`) rejects any other value with the accepted values in the message, and rejects api_key when executor.claude_code.billing has neither api_key_command nor api_key_env (the fallback would have no key to switch to). `configs/pilot.example.yaml`: one commented line `fallback: pause` under usage_limit_pause with a comment that api_key switches the daemon to the API key until the reset (needs the billing key source) and that `pilot billing-mode status` shows source fallback while it is in force.
- Fallback path (`internal/executor/usage_limit_pause.go`, armUsageLimitPause): after the resumeAt arithmetic and before the pause, when cfg.EffectiveFallback() == api_key AND the mode in force is subscription (d.billingResolverOrConfig().Mode()), resolve the API key through the resolver's key path without changing the mode (same package; the resolver's api_key resolution for the configured source). On success: zero the resolved key (the override makes admission resolve its own), call d.SetBillingOverride("api_key", resumeAt, "fallback") (source fallback, until = the same resumeAt the pause would have used, so the cap and the already-reset rule apply unchanged), do NOT pause admission, do NOT arm the usage-limit release timer, log one INFO line (task_id, reset_at, until) and return. billing_mode_changed is emitted by SetBillingOverride with source fallback; no usage_limit_pause alert for this episode. A second usage-limit result while the fallback override is in force (mode in force is api_key) is an API-side limit: pause as today, leave the override alone. Any mode other than subscription (unset passthrough, api_key by config or override) pauses as today. On key resolution failure: pause exactly as today AND emit AlertEventTypeBillingFallbackUnavailable once per episode with the resolver error (never the key), alongside the existing usage_limit_pause alert.
- Revert: the A2 expiry timer reverts the override at until and emits billing_mode_changed from api_key to subscription with set_by expiry. No second timer. A usage-limit result while a fallback override is in force never extends it (see above); a cli or gateway Set replaces it as today.
- Alert: AlertEventTypeBillingFallbackUnavailable = "billing_fallback_unavailable" in `internal/executor/alerts.go`; EventTypeBillingFallbackUnavailable, a handleEvent case and handleBillingFallbackUnavailable in `internal/alerts/engine.go` modelled on handleBillingModeChanged; AlertTypeBillingFallbackUnavailable in `internal/alerts/types.go` with a default rule, SeverityWarning, listed in HandlerEmittedAlertTypes; payload fields reset_at, until, error. Message: "Usage limit hit and the api_key fallback is unavailable (<error>); admission paused until <until>".
- Status surfaces: none. /api/v1/billing-mode, /api/v1/status, `pilot status` and the TUI chip already render source fallback (A2/A3).
- Docs: in `docs/content/cli/commands.mdx`, the billing-mode section gets one paragraph on `usage_limit_pause.fallback: api_key` (what happens, how status shows it, how `pilot billing-mode set config` ends it early).

Tests to add, named exactly, in a new file, plain text: internal/executor/usage_limit_fallback_test.go (use the fixtures of billing_override_test.go and usage_limit_pause_test.go: fake clock, env key source, alert sink): TestUsageLimitFallback_SwitchesToAPIKeyWithoutPause (config subscription + api_key_env, fallback api_key; a rate_limited outcome with a parsable reset → admission not paused, BillingOverride() is api_key / fallback / until = reset + RetryBuffer, the next admitBilling resolves api_key with the key, one billing_mode_changed alert with source fallback, no usage_limit_pause alert), TestUsageLimitFallback_RevertsAtUntil (fake clock past until → mode subscription, source config, row gone, billing_mode_changed with set_by expiry; next admission resolves subscription), TestUsageLimitFallback_CapAndAlreadyReset (a reset beyond max_pause → until = now + max_pause; a reset in the past → until = now + RetryBuffer), TestUsageLimitFallback_SecondLimitWhileOnFallbackPausesAsToday (second rate_limited result while the override is in force → admission paused by usage-limit, override untouched), TestUsageLimitFallback_KeyUnavailablePausesAndAlerts (api_key_command that exits 1 → paused as today, usage_limit_pause alert plus one billing_fallback_unavailable alert with reset_at / until / error, no override row), TestUsageLimitFallback_OffByDefault (no fallback key in the config → paused as today, no override). Plus TestValidateUsageLimitPause_Fallback (`internal/config/config_test.go`: pause and api_key accepted; "subscription" rejected with the accepted values; api_key without a billing key source rejected) and TestBillingFallbackUnavailableAlert_RuleAndPayload (`internal/alerts/engine_test.go`: default warning rule exists, event type strings match, message and payload).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run 'TestUsageLimitFallback'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/config/ -run TestValidateUsageLimitPause` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/alerts/ -run TestBillingFallbackUnavailableAlert` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run 'TestUsageLimitPause|TestBillingOverride'` still passes; paste the output into the PR body.
- [ ] pause admission as well as setting the override -> TestUsageLimitFallback_SwitchesToAPIKeyWithoutPause fails
- [ ] drop the revert (skip until on the override) -> TestUsageLimitFallback_RevertsAtUntil fails
- [ ] extend the override on a second limit -> TestUsageLimitFallback_SecondLimitWhileOnFallbackPausesAsToday fails
- [ ] fall back silently when the key does not resolve -> TestUsageLimitFallback_KeyUnavailablePausesAndAlerts fails
- [ ] accept fallback: subscription in validation -> TestValidateUsageLimitPause_Fallback fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The rate-limited task's own re-queue at reset time (stays with the poller's Scheduler; under the fallback the other queued tasks proceed on the key, that one task waits as today); budget scope by billing mode (leg C); the hosted console write path (TASK-631 D4); any change to the override store, the gateway endpoint, the CLI or the TUI; chat-turn and post-merge-reviewer resolvers (config only today, see TASK-631 minors); key_source.

## Refs

- #5955 (request: "continue on an API key") · PR#6057 / PR#6065 (A2) · PR#6061 (A3) · PR#6066 (A4) · GH-5862 / GH-5879 (usage-limit pause) · Navigator TASK-631 in this repo

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6016
- A2 → https://github.com/qf-studio/pilot/issues/6052
- A3 → https://github.com/qf-studio/pilot/issues/6053
- A4 → https://github.com/qf-studio/pilot/issues/6064
- B1 → https://github.com/qf-studio/pilot/issues/6069
