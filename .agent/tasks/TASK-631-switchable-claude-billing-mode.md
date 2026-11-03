# TASK-631: Switchable Claude billing mode (subscription | api_key) without a restart, with per-execution attribution

**Status**: 🚀 **A1 DISPATCHED 2026-11-03 → [pilot#6016](https://github.com/qf-studio/pilot/issues/6016)** (`pilot` + `no-decompose`). Triage of [pilot#5955](https://github.com/qf-studio/pilot/issues/5955) (scrappydog): legitimate; reply + A1 link posted on the issue. D1–D4 accepted as proposed. **A1 run → [PR#6017](https://github.com/qf-studio/pilot/pull/6017) 11:20Z; CI failed on `TestRecordCost_SplitsByKeySource` (the expected `costKey`s lack the new `BillingMode: "unknown"`); the autopilot CI-fix size guard declined to file (759 production additions > 200) → hand-filed [pilot#6019](https://github.com/qf-studio/pilot/issues/6019) in the autopilot-fix shape (footer `branch:pilot/GH-6016 pr:6017 iteration:1 source:6016`), picked up 11:52Z on the PR branch → **PR#6017 MERGED 12:14Z as `f72c25810`** (#6016 closed `pilot-done`; #6019 closed by hand 12:25Z — borrowed-branch fix issues never self-close). Post-merge review APPROVE-WITH-NOTES, nine minors (no sink: `file_minors` off on the box): OpenCode anthropic backend now starts without `ANTHROPIC_API_KEY` and its :144 comment is stale · autopilot post-merge review bills the subscription in api_key mode · worker billing-key failure `continue`s past `EmitProgress(Failed)`/`monitor.Fail` (card stays running) · first outage row is a real claim → repick backoff on a task that never ran · non-dispatcher `Runner.Execute` path gets no admission pause/alert · decomposed subtasks carry no `billingMode` (cost labelled unknown) · stashed key lives until `stopBillingState` on cancelled/stale rows · resolver half of the api_key acceptance test is hand-built. **Carry these into A2.** **Daemon defect seen on this run (new restart re-pick variant):** the box hot-upgraded to 2.309.0 ~11:50Z and boot reconcile re-picked #6016 at 11:54Z — its `pilot-in-progress` marker had been stripped at the 11:34Z CI failure and `pilot-needs-human` does not gate pickup, while PR#6017 was still open; a fresh worktree off `f5488f74a` started re-implementing the merged work at 12:01Z (`/tmp/pilot-worktree-GH-6016-1793707268387046953`, 1 h timeout). TASK-624 D2's open-PR heal did not cover a failed-with-open-PR issue. File as a follow-up to [TASK-625](archive/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md).** A2 → B → C held until A1 is on a release (next cut after v2.309.0).
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

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6016
