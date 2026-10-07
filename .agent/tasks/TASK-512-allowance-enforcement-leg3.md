# TASK-512: Allowance enforcement at exhaustion — switch to the own key, suspend without one, per-period state (TASK-502 leg 3)

**Status**: ✅ **BOTH LEGS MERGED + REVIEWED, LIVE IN DRY-RUN (prod-0.1.14) — 2026-10-06/07.** PR#363 (3a) merged 10:34Z, PR#364 (3b) merged 10:52Z, prod-0.1.14 = d06ac97 auto-deployed 10:59Z (api/workers/reconciler all green — the console deploy now runs on main automatically). Post-merge reviews APPROVE-w-notes on both PRs (3b closes 3a's note 2: suspend acts on state per tick). Follow-ups filed 10-07: [console#365](https://github.com/qf-studio/pilot-console/issues/365) (README no-own-key paragraph stale + dry-run WARN dedupe) · [ui#203](https://github.com/qf-studio/pilot-console-ui/issues/203) (enforcement states: paused box + resume hint, switched-to-own copy, Connections row). Dry-run review 10-07: reconciler restarted clean 10-06 11:03Z, sweep wired, zero ERROR, zero allowance lines (no org past 80 %). **Nelya SYNCED 10-07 (infra dc10719)**: flag on + 15m poll interval on console-reconciler. console#365 → PR#366 merged 07:35Z (reviewed APPROVE, no notes), prod-0.1.15 auto-deployed 07:41Z. **Redeploy with the new template: run 37590661945 (`image_tag=prod-0.1.15`) dispatched ~09:35Z** — a first dispatch with 0.1.14 (run 37590485173) was cancelled after the mirror job because 0.1.15 was already live (pitfall: always read the newest prod tag before a manual dispatch; the auto-deploy may have moved on). ui#203 in progress. Original dispatch 2026-10-06 — [console#361](https://github.com/qf-studio/pilot-console/issues/361) leg 3a (helper + state table + key-source rule + sweep: warn 80 %, switch to own at 100 %, reset on rollover, poll-interval knob) → [console#362](https://github.com/qf-studio/pilot-console/issues/362) leg 3b (event-typed suspend for no-own-key, resume on own-key save or rollover, `enforcement` block on the wire). Both `pilot` + `no-decompose`. Pilot queue was empty at dispatch. **09:50Z #361 held "prerequisite not on main"**: the body backticked the to-be-created `allowanceenforce.go` path (SOP Rule 3b slip); body edited 10:0xZ to plain text, hold releases on the next tick (GH-5193 live-body re-check). #362 paths all exist on main. **PR#363 (3a) reviewed 2026-10-06 ~10:50Z: APPROVE-w-notes posted pre-merge** (CI all green; fleet tests run locally on the PR head → 29 PASS; both mutation pins kill). Notes: dry-run WARN has no per-tick dedupe · `no_own_key` event lost on a transient Exists error (3b must act on state) · rollover on a stopped box leaves SSM `own` vs wire `platform` until the first running tick · credentials listing still says pool assigned after a switch (UI follow-up) · switch restarts the daemon mid-task. Gate finding: `-run 'A|B'` refused as a shell operator → SOP Rule 5c.
**Created**: 2026-10-06 · **Parent**: TASK-502 (leg 3) · **Plan**: 3 nav-research agents on console @ f4c9ff4 + pilot main, 2026-10-06
**Done =** both merged + post-merge reviewed · `PILOT_CONSOLE_ALLOWANCE_ENFORCE_ENABLED` flipped on in prod (Nelya template default) after one dry-run tick log review · one real exhaustion exercised on a test org (own-key switch observed: SSM selector `own`, `secret_refresh_requested` event, daemon restart marker) · follow-ups filed (UI suspended state, email alert, daemon admission pause).

## Decisions (2026-10-06, autonomous under TASK-502's founder decisions)
| Question | Answer | Why |
|---|---|---|
| Where exhaustion is detected | reconciler tick sweep `enforceAllowance`, after `enforceBilling` | only periodic loop that already touches every running instance; flag-off dry-run idiom exists |
| Shared computation | new `internal/allowance.Status` used by API, consolectl, sweep | exhaustion was inline in two places; a third copy would drift |
| Per-period state | table `org_allowance_state` keyed by `period_start` | usage is on-demand with no period-scoped state; warned/exhausted/switched must dedupe per period and reset on rollover |
| Switch mechanics | write `PILOT_ANTHROPIC_KEY_SOURCE=own` + `RefreshConfig` | the only wire the box reads; bump needed or nothing re-pushes; restart happens only when env sha changes |
| `key_source` truth | state row wins over the `platform_keys` row; `Assign` honors it | derived-from-row would keep saying `platform` after a switch and `Assign` would rewrite the selector |
| No own key | suspend via a new `allowance_suspended` event type; resume on own-key save or rollover | only pause primitive the console can drive; reusing `billing_suspended` is undone next tick for active orgs; daemon has no pause knob |
| Alerts | instance events `allowance.warning` / `allowance.exhausted` + WARN logs | no customer channel exists (email is HTTP-only, off); founder declined an 80 % UI tone |
| Staleness | `PILOT_CONSOLE_USAGE_POLL_INTERVAL` knob, default 1h, floor 5m | hard cap overshoots by one poll interval; prod lowers it later |

## Research findings (file:line at console f4c9ff4 / pilot main)
- Selector: `configpush.go:681-695` resolves `ANTHROPIC_API_KEY` from `PILOT_ANTHROPIC_KEY_SOURCE` + `_PLATFORM`/`_OWN`; apply script `configpush.go:594-624` restarts `pilot` only when `/run/pilot/env` sha changes. Rendered YAML carries no Anthropic params.
- `key_source` derived at `allowanceapi/routes.go:172-180` from `PlatformKeyForOrg`; only write site of the selector is `fleet/platformkey.go:83` (`platform`). Nothing writes `own`.
- Bump path: `orgconfigstatus.go:29` → `store.go:1292-1322` (`BumpConfigGenerationForOrg`, byte-identical spec, `reconcile.secret_refresh_requested` event) → `reconciler.go:2283-2330` `syncConfig` on drift; tick 60s.
- Sweep idiom: `billingenforce.go:85` on `reconciler.go:897`; flag `PILOT_CONSOLE_BILLING_ENFORCE_ENABLED` default off = WARN "would"; `SuspendIfRunning` `store.go:696-740`, `ResumeIfBillingSuspended` `store.go:750` resumes only when latest suspend event is `billing_suspended`.
- Exhaustion: `billing_period.go:60` dollars only; inline in `routes.go:151-200` and `consolectl/billing.go:414-467`; `MeteredUsage` `usage_meter.go:60` delta vs baseline at period start.
- Poller: `usagepoller.go:19` 1h ticker, first poll 1h after start, only in `consolectl run`; polls `:9090/api/v1/metrics`.
- Daemon (pilot): no pause knob; `Dispatcher.PauseAdmissionFor` in-process only (`dispatcher.go:2947`); `max_concurrent >= 1`; key read from process env → restart required; `sk-ant-svc-` keys go `x-api-key` like `sk-ant-api` (`anthropic_auth.go:44-72`).
- Latest migration 0024 → leg 3a uses 0025.

## Follow-ups (not filed)
- console-ui: suspended-for-allowance state on Plan & Billing + Instances (reads `enforcement.state`).
- Email alert at 80 %/100 % once a customer recipient and an in-process sender exist.
- pilot repo: console-driven admission pause (gateway endpoint or config flag calling `PauseAdmissionFor("console-allowance")`) as the lighter alternative to suspend.
- Nelya: `PILOT_CONSOLE_ALLOWANCE_ENFORCE_ENABLED=true` + `PILOT_CONSOLE_USAGE_POLL_INTERVAL=15m` in the console-reconciler template once 3a/3b are on the image.

## Refs
- Parent: `.agent/tasks/TASK-502-included-tokens-platform-key.md`
- Issue bodies: console#361, console#362 (authored per SOP new-project-issue-authoring Rule 5b — paste-output bullets carry only command spans; pilot#5618 not yet on the box).
