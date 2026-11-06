# TASK-640: Credential health — the console re-checks the org's own Anthropic key and tracker tokens, shows a truthful status, and gates chat on a dead key

**Status**: 🚀 **K5 DISPATCHED 11-06 15:3xZ → [ui#389](https://github.com/qf-studio/pilot-console-ui/issues/389)** (`pilot` + `no-decompose`, pre-check 0, dry run clean against ui main `f19040c`). Post-merge fix [ui#387](https://github.com/qf-studio/pilot-console-ui/issues/387) → PR#388 merged 15:13Z (test-only, +19 −1 `ChatView.spec.ts`; post-merge APPROVE-w-notes 15:16Z, one minor: the heal step bypasses the real save path; ui deployed 15:18Z) — none of K5's files touched, so K5 went out as drafted. ✅ when K5 is live and the live check passes (`/connections` reload shows a fresh `checked <time>`; `/chat` and `/` no notice with the healthy key). Earlier: 🟡 **K4 LIVE 14:4xZ — PR#385 merged 14:37Z (`e4c8378`, after the hand-filed CI fix [ui#386](https://github.com/qf-studio/pilot-console-ui/issues/386) → `9a5d89e`); live: Connections `checked 4m ago` = API `health_checked_at 14:40Z` (fresh mount read), Research + Home composers enabled, no notice (key healthy). Post-merge review REQUEST-CHANGES 14:42Z (blocking: no mock-mode view spec drives the dead-key toggle) → autopilot [ui#387](https://github.com/qf-studio/pilot-console-ui/issues/387) in progress. **LEG K5 drafted, dry run clean, dispatch after ui#387 lands** (same files): the console PUT credentials response carries no `health_status`, so a save does not clear the notice on prod — re-read credentials after save and on mount; mock dead-key row `configured: true`. ✅ when K5 is live and the live check passes.** Earlier: 🟡 K4 PR#385 red on a test route 13:54Z · 🚀 K4 dispatched 13:05Z · 🟢 K3 merged 11:07Z.
**Created**: 2026-11-06
**Assignee**: Navigator (plan) → Pilot (legs) · founder (go; the live tenant's dead key and PAT are an operator fix, not this task)
**Parent**: [TASK-583](TASK-583-wave1-design-update-proof-surface.md) proof surface · incident 2026-11-05 21:21Z (marker `2026-11-06-0740_console-session-board-landed-tenant-credentials-dead.md`)

---

## Context

On 2026-11-05 the founder asked the Research page "What was shiped last 30 days?" and got `Chat turn failed: unknown: exit status 1` after two seconds. The tenant daemon (org 425f9f99, `i-0289111000cebcee9`) had two dead credentials: the org's own Anthropic key answered 401 `invalid x-api-key` on `GET /v1/models`, and the GitHub PAT answered 401 `Bad credentials`; the daemon's `/ready` said `{"checks":{"github":false}}`. The console page showed the GitHub connection as **connected** and the Model row as **Connected · •••• aQAA**.

What the console does today (`origin/main` 11-06):

- `PUT /api/v1/credentials/{key}` (`internal/orgs/handlers.go` `handlePutCredential`) validates the secret once, on save, through `CredentialValidators.forKey` → `AnthropicValidator.Validate` (`GET /v1/models`, `internal/orgs/validators.go`). Tracker connections validate on save through `GitHubValidator.Validate` (`/user` + repos). Nothing re-checks afterwards. The key can be revoked a day later and every surface still says Connected.
- `connections.status` (`connected|error`, migration 0006) has two writers that disagree: `internal/fleet/readychecks.go` `recordReadyChecks` writes from the daemon's `/ready` (truthful, but only on a config push or provision), and `internal/syncingest/worker.go` `processConnection` writes `error` when the connector cannot be built and `connected` whenever it can (`recoverConnection`) — a build proves the secret exists, not that it works. On the incident the row flipped `error` 21:25:44Z → `connected` 21:37:59Z with the same dead token.
- Credentials live in `organizations.credentials` (JSONB; `CredentialRecord{Key, Last4}`, `UpsertCredential` / `ListCredentials`). `GET /api/v1/credentials` items carry `configured`, `last4`, `store_present` (live SSM presence, GH-189) — presence, not validity.
- The chat relay (`internal/chatapi/handlers.go` `handleSendMessage`) already turns daemon states into codes the UI understands: `409 chat_not_enabled`, `409 instance_not_running`. The UI (`src/stores/chat.ts` lines ~190, `src/views/ChatView.vue` ~228) disables the composer with a one-line reason for each code.
- Periodic work in the console runs as workers started from `main.go` with `PILOT_CONSOLE_*_INTERVAL` env config (`internal/config/config.go`: `SyncConfig`, floors, invalid-value errors), e.g. `syncingest.NewWorker` (poll interval), `fleet.NewUsagePoller`.
- The daemon side (classify Claude's `is_error` result as a credential failure instead of `unknown: exit status 1`) is the Pilots session's; this task does not wait on it.

## Decisions

1. **The console owns credential health.** A sweeper re-runs the save-time validators against the stored secrets on an interval: the org's Anthropic key (only when the org runs on its own key — `PlatformKeyForOrg` → not found ⇒ own) and every tracker connection. One call per credential per tick; `/v1/models` and `/user` are the lightest authenticated calls. Default interval 15 min, floor 5 min, env `PILOT_CONSOLE_CREDENTIAL_HEALTH_INTERVAL`.
2. **One record per credential**: `status ok|error`, `checked_at`, `error` (the validator's message, which never contains the secret). Connections keep `status` (the UI's signal) and gain `health_checked_at` + `health_error`; credentials get a new table `credential_health (org_id, key, status, checked_at, error)` because `organizations.credentials` is a JSONB blob.
3. **Status precedence**: a live validator result and the daemon ready probe may write `connected`/`error`; `syncingest.recoverConnection` is removed (`flagConnectionError` stays — an error is always true). Nothing else writes `connected`.
4. **Chat gate**: `handleSendMessage` refuses a turn with `409 anthropic_key_invalid` (message: the stored `error`) when the org's Anthropic health is `error` and the org runs on its own key. The UI treats it like `chat_not_enabled`: composer disabled, one-line reason with a link to Connections. A platform-key org is never gated by this.
5. **Save still validates**; a save that passes resets the health to `ok` with `checked_at = now()`, so the row is truthful the second the founder pastes a good key.
6. **No alert channel yet** (brief email/Slack is C6, held on I3). The bell and the rows are the surface.
7. **Operator fix for the live tenant is separate** — 11-06: new Anthropic key saved + daemon restarted 09:46Z (Anthropic 200); new fine-grained PAT saved + daemon restarted 10:57Z → GitHub 200, push true, `/ready` github true, poller started. **Tenant incident closed.**: the founder saves a new key and PAT, the tenant daemon restarts (it reads `/run/pilot/env` at service start). This task prevents the next silent week, not this one.

## Legs (K1 → deploy console-api → K2)

### LEG K1 — repo: qf-studio/pilot-console — title: feat(credentials): credential health sweep — re-validate the org's own Anthropic key and tracker tokens on an interval, keep connection status truthful, gate chat on a dead key

## Context
`PUT /api/v1/credentials/{key}` validates a secret once on save (`handlePutCredential` → `CredentialValidators.forKey` → `AnthropicValidator.Validate`, `GET /v1/models`) and tracker connections validate once on save (`GitHubValidator.Validate`); nothing re-checks afterwards. `connections.status` has two writers that disagree: `recordReadyChecks` in `internal/fleet/readychecks.go` writes from the daemon's `/ready`, and `processConnection` in `internal/syncingest/worker.go` writes `connected` whenever the connector builds (`recoverConnection`), which proves the secret exists, not that it works. On 2026-11-05 a revoked Anthropic key (401 on `/v1/models`) and a revoked GitHub PAT (401 `Bad credentials`, daemon `/ready` `github:false`) showed as Connected on every surface, and chat turns died with `unknown: exit status 1`. The chat relay (`handleSendMessage` in `internal/chatapi/handlers.go`) already maps daemon states to codes the UI disables the composer on (`chat_not_enabled`, `instance_not_running`).

probe: present qf-studio/pilot-console func[[:space:]]NewAnthropicValidator
probe: present qf-studio/pilot-console func[[:space:]]\(w[[:space:]]\*Worker\)[[:space:]]recoverConnection
probe: present qf-studio/pilot-console chat_not_enabled
probe: present qf-studio/pilot-console PILOT_CONSOLE_SYNC_INGEST_INTERVAL

## Scope
- Migration `0042_credential_health`: table `credential_health (org_id uuid REFERENCES organizations(id), key text, status text CHECK (status IN ('ok','error')), checked_at timestamptz NOT NULL, error text NOT NULL DEFAULT '', PRIMARY KEY (org_id, key))`; `ALTER TABLE connections ADD COLUMN health_checked_at timestamptz NULL, ADD COLUMN health_error text NOT NULL DEFAULT ''`. Down migration drops both.
- `internal/orgs/store.go`: `UpsertCredentialHealth(ctx, orgID, key, status, checkedAt, errText)`, `ListCredentialHealth(ctx, orgID)`, `SetConnectionHealth(ctx, connectionID, status, checkedAt, errText)` (writes `status` + the two columns in one statement); `Connection` gains `HealthCheckedAt *time.Time`, `HealthError string`.
- New package `internal/credhealth`: `Sweeper` with `Config{Interval}` (`PILOT_CONSOLE_CREDENTIAL_HEALTH_INTERVAL`, default 15m, floor 5m, invalid value is a startup error — same shape as `SyncConfig` in `internal/config/config.go`). Each tick, for every live org: if `PlatformKeyForOrg` returns `ErrNotFound` (own key) and the Anthropic credential is configured, read the own key through `secrets.CredentialReader` and run `AnthropicValidator.Validate`; for every connection, read its secret and run that tracker's `Validator.Validate(secret, config)`. Write the result per decision 2. A validator network error (not a 4xx) is logged and leaves the previous record untouched. Started from `main.go` next to `syncingest.NewWorker`; it holds the fleet lock the same way the usage poller does if that is how cross-org workers are serialized, otherwise plain.
- `handlePutCredential` and the connection save handlers: after a successful validate, write `ok` + `now()` to the health record (decision 5).
- `internal/syncingest/worker.go`: delete `recoverConnection` and its call; `flagConnectionError` unchanged; update the `processConnection` docblock.
- `GET /api/v1/credentials` items (`credentialListItem`) gain `health_status` (`ok|error|unknown`), `health_checked_at` (RFC 3339 or null), `health_error`; `GET /api/v1/connections` items and the `connections` array of `GET /api/v1/org` (`connectionResponse`) gain `health_checked_at` and `health_error` (status is the existing `status`).
- `handleSendMessage`: before the relay, when the org runs on its own key and its Anthropic health is `error`, answer `409` with code `anthropic_key_invalid` and the stored `error` as the message (`writeJSONErrorCode`). Document the code next to `chat_not_enabled`.
- Tests: store tests for the three new functions against the migration; sweeper tests with an `httptest` Anthropic and GitHub server — a 401 writes `error` with the message and flips the connection to `error`, a 200 writes `ok`, a network error leaves the record; a `handlePutCredential` test asserts a passing save writes `ok`; a `syncingest` test asserts a successful connector build no longer writes `connected` over an `error` row; a `chatapi` test asserts `409 anthropic_key_invalid` on an `error` record for an own-key org and no gate for a platform-key org; a raw-body test pins the literal field names `health_status`, `health_checked_at`, `health_error` on the credentials list.

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] Restore `recoverConnection` and its call -> the syncingest no-overwrite assertion fails
- [ ] Skip the gate in `handleSendMessage` -> the chatapi `anthropic_key_invalid` assertion fails
- [ ] `make lint` passes; paste the output into the PR body.

## Out of scope
Alert channels (brief email / Slack, C6). The daemon's chat-turn classification (Pilots session). The UI (K2). Rotating or fetching secrets onto the tenant (`fetch-secrets.sh` at service start is unchanged).

## Refs
- Navigator TASK-640 K1 · console#189 (store_present) · console PR#192 · TASK-631 (billing mode) · TASK-583

## K1 review (PR#489, 11-06 09:45Z) — APPROVE-with-defects → K3 before K2

Merged 09:33Z (+2010 −35, 20 files; pre-PR review enforce APPROVE-WITH-NOTES, 7 notes). Verified in the diff: migration 0042, `internal/credhealth` sweeper with `PILOT_CONSOLE_CREDENTIAL_HEALTH_INTERVAL`, `recoverConnection` removed, save stamps `ok`, health fields on both list endpoints, `409 anthropic_key_invalid` gate with tests. Two of the reviewer's notes are design defects in my decision 3, not Pilot's:

- [defect] The sweep writes `connections.status` (`SetConnectionHealth` sets `status = $2`). The fleet ready probe writes the same column from the daemon's view. After the founder saves a good key the stored secret validates (`connected`) while the tenant still runs the old one (`/ready` false → `error`) — exactly the 11-05 shape — and the two writers flip the row every tick. **Fix: split the facts.** `status` = the daemon's view (ready probe + syncingest `flagConnectionError`); `health_status/health_checked_at/health_error` = the stored secret's validity (sweep + save). The sweep stops writing `status`. The UI (K2) renders both: health `error` → Fix key/connection; health `ok` + status `error` → "Saved credential is not live on the box yet — restart" (the GH-189 `store_present`/`push_rejected` family).
- [defect] Stale-over-fresh race: a sweep that read the old secret can write `error` after the save wrote `ok`, gating chat for up to one interval. **Fix:** the sweep's write is compare-and-set on `checked_at` (skip when the row's `checked_at` is newer than the sweep's read time).
- [minor] `isGitHubUpstreamStatus` counts 403 as transient → a 403-rejected token is never recorded. Record 401 and 403 as `error`.
- [minor] The transient 429/5xx message reads "rejected api key (status N)" and reaches the PUT 422 body → an outage reads as a rejected key. Separate wording.
- [minor] `startCredentialHealth` opens a second `fleet.Open` pool and never closes it; reuse the main pool or close on shutdown.
- [accepted] The sweeper runs unlocked like syncingest.

### LEG K3 — repo: qf-studio/pilot-console — title: fix(credhealth): the sweep owns health, not status — split the stored-secret validity from the daemon's connection view; connection compare-and-set; 403 is a rejection; one DB pool

## Context
Leg K1 (console#488 → PR#489) added `internal/credhealth` (sweeper); the post-merge minors leg (console#490 → PR#491, merged 10:07Z) then added the compare-and-set (`UpsertCredentialHealthSince`, `checked_at <= $6`), the "unavailable" wording for transient failures, and a guard in `recordReadyChecks` that keeps a ready-probe `connected` from overwriting a row whose `health_error` is set. Three things remain. Leg K1 (console#488 → PR#489) added `internal/credhealth` (sweeper), migration 0042 (`credential_health` table; `connections.health_checked_at`, `connections.health_error`), `Store.SetConnectionHealth` and `Store.UpsertCredentialHealth`, and the `409 anthropic_key_invalid` chat gate. Its pre-PR review found that `SetConnectionHealth` sets `connections.status` as well as the health columns, so the sweep and `recordReadyChecks` in `internal/fleet/readychecks.go` overwrite each other: a saved key that validates in the console while the tenant still runs the old secret (the daemon reads `/run/pilot/env` only at service start) flips the row between `connected` and `error` every tick, and `SetConnectionStatus` never clears `health_error`. A sweep that read the old secret can also write `error` over the `ok` a fresh save just wrote (`recordCredential` / `recordConnection` in `internal/credhealth/sweeper.go`), gating chat for up to one interval. `isGitHubUpstreamStatus` in `internal/orgs/validators.go` treats 403 as transient, so a 403-rejected token is never recorded. The transient 429/5xx wording "rejected api key (status N)" reaches the `PUT` 422 body. `startCredentialHealth` in `main.go` opens a second `fleet.Open` pool that is never closed.

probe: present qf-studio/pilot-console func[[:space:]]\(s[[:space:]]\*Store\)[[:space:]]SetConnectionHealth
probe: present qf-studio/pilot-console anthropic_key_invalid
probe: present qf-studio/pilot-console func[[:space:]]isGitHubUpstreamStatus

## Scope
- Two facts, two owners. `connections.status` is the daemon's view and is written only by `recordReadyChecks` and syncingest `flagConnectionError`. The health columns are the stored secret's validity and are written only by the sweep and the save handlers. `SetConnectionHealth` no longer touches `status`; it takes `healthStatus ok|error`, `checkedAt`, `errText`. Migration 0043 adds `connections.health_status text NOT NULL DEFAULT 'unknown' CHECK (health_status IN ('ok','error','unknown'))`; down drops it. `connectionResponse` gains `health_status`; `status` keeps its meaning.
- Compare-and-set for connections: `SetConnectionHealth` gets the same `Since` form PR#491 gave `UpsertCredentialHealthSince` (apply only when the row's `health_checked_at` is null or older than the sweep's read time).
- `isGitHubUpstreamStatus`: 403 is not upstream; the validator returns a rejection for 401 and 403 and `IsTransient` is false for both. The Anthropic validator: 401 and 403 reject, 429/5xx transient.
- `startCredentialHealth` reuses the process's existing store/pool; if a separate pool is unavoidable, close it on shutdown with the others.
- Tests: sweeper — a ready-probe `error` row survives a passing sweep (`status` untouched, `health_status` ok) and a connection sweep never writes over a newer `health_checked_at`; validators — 403 is a rejection; raw-body test pins `health_status` on the connections list; the PR#491 guard in `recordReadyChecks` is removed (the two columns no longer share a writer) and its test re-pointed.

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] Make `SetConnectionHealth` set `status` again -> the sweeper status-untouched assertion fails
- [ ] Drop the `health_checked_at` predicate on connections -> the newer-row assertion fails
- [ ] `make lint` passes; paste the output into the PR body.

## Out of scope
The UI (K2, which renders both facts). Alert channels. The daemon's `/ready` semantics.

## Refs
- Navigator TASK-640 K3 (review of PR#489) · console#488 · console#189 (store_present)

## K3 review (PR#493, 11-06 11:15Z) — APPROVE-with-notes

Verified in the diff: 0043 adds `health_status` (`ok|error|unknown`, default unknown) on `connections`; `SetConnectionHealth` no longer sets `status` (mutation pin `TestSweep_ReadyProbeErrorSurvivesPassingSweep`); the PR#491 guard in `recordReadyChecks` is gone — the two writers own different columns; connection compare-and-set on `health_checked_at` with a dropped-verdict log line; the credhealth store is closed on shutdown. Reviewer notes, all accepted: the PUT save path still writes `status=connected` (correct: validated on save) and syncoutbound still writes `status=error` (correct: a real failure) — the "single writer" wording in the leg was too strong; no backfill of `health_status` for rows the 0042 sweep judged (the next sweep corrects within 15 min); a config PATCH between the sweep's list and its per-connection `started` can let an old-config verdict land (narrow; a per-connection re-read of config before validate closes it — fold into any later credhealth leg); the store-backed pins skip without `DATABASE_URL` locally (CI runs them).

## K2 review (PR#379, 11-06 11:56Z) — APPROVE-with-notes, then post-merge REQUEST-CHANGES

Navigator (pre-merge comment): wire resync by script to `d833a09`; `Credential`/`Connection` health fields loose-parsed; Model row `Fix key` / tracker `Fix connection` / `Saved, not live on the box yet` / `Connected · checked …` as specified; `anthropic_key_invalid` → `keyInvalid`, cleared by send success and `reset()`; the events poll leaves it alone (the pre-PR enforce round's blocking fix). Notes: Home notice needs a prior failed send; `Fix key` is a span on a non-link row.

Post-merge reviewer (12:04Z) found what the Navigator review missed: **the composer is disabled while `keyInvalid` is set, so the "successful send" clearing path can never run; `reset()` runs only on auth loss; `saveCredential` never touches the chat store → a user who fixes the key stays locked out until reload or logout.** Also: mock adapter without `health_*` / `anthropic_key_invalid` (ui CLAUDE.md lockstep rule); `Fix key` template runs for every `CREDENTIAL_GROUPS` entry, not only the Model row; `session.loadCredentials`/`loadConnections` are cached per session, so `checked <time>` goes stale; the view specs set `keyInvalid` by hand and never test a clearing path. Fix issue: [ui#380](https://github.com/qf-studio/pilot-console-ui/issues/380). Review its PR for: clear on `upsertCredential` success (or on Connections mount / credentials `health_status: ok`), seed `keyInvalid` from `GET /api/v1/credentials` on load, mock parity, Model-row-only `Fix key`, a refetch of credentials on the Connections view.

Fix PR#381 (ui#380, +6 −6): removes the `:disabled` on `keyInvalid` in ChatView and DashboardView — notice shown, composer enabled, a retry after the fix sends and a successful send clears the notice. **Design change accepted (K2 said "composer disabled"): the disabled form had no reachable clearing path; the console still gates the turn with 409.** Pre-PR enforce APPROVE-w-notes (save does not clear the notice; no view-level recovery test). Navigator review APPROVE-w-notes on the PR. Post-merge round on PR#381 → review-minor ui#382 → PR#383 (+54 −1): save clears `keyInvalid` by a direct store write, DOM-level recovery test, catch comment; Navigator APPROVE-w-notes. The remaining items are LEG K4 below (dispatched after PR#383's post-merge round — same files).

## K4 review (PR#385, 11-06 13:5xZ) — one defect (Navigator spec error)

Met: `syncKeyHealth` action; seed on Research/Home mount; `{force}` loaders + Connections mount refetch; `Fix key` anthropic-only; mock health seeds + `mockFlags.deadAnthropicKey` + `upsertCredential` heals; specs. **Defect:** the leg said the save response carries `health_status`; the console `PUT /api/v1/credentials/{key}` returns `{key, last4}` only (`handlers.go` `credentialResponse`), so on prod `syncKeyHealth` after a save sees no health and the notice stays — PR#383's direct clear regressed; the mock returns `ok` from a save, so the spec passed. Also: mount sync applies a cached `ok` over a fresh `keyInvalid`; mock dead-key row has `configured: false`. → LEG K5 (re-read credentials after save and on mount; mock seed fix). **CI on PR#385 red 13:54Z:** 1123 tests green, one unhandled rejection — the new DashboardView test's memory router has only `/` while the view renders `RouterLink {name: 'board'}`; the CI fix size guard declined (259 additions > 200) → `pilot-needs-human` on ui#384 → **hand-filed [ui#386](https://github.com/qf-studio/pilot-console-ui/issues/386)** in the autopilot-fix shape (routes array like the sibling test; test-only) 14:08Z. Close #386 by hand after PR#385 merges (borrowed-branch fix issues never self-close). Lesson: a leg that says "the response carries X" must cite the handler line; verify the wire on the producer before the consumer leg is written.

Lesson (memory `disabled-gate-whose-only-clearing-path-is-the-disabled-action-locks-out`): a gate that disables the only action which can clear it is a lockout — every "disabled until X" needs a clearing path that does not go through the disabled control.

### LEG K2 — repo: qf-studio/pilot-console-ui — title: feat(connections): credential health on the Connections rows and a dead-key notice on the composer — Fix key / Fix connection with the reason and the check time; chat composer disabled on anthropic_key_invalid

## Context
After console leg K1 (TASK-640, filed in qf-studio/pilot-console; its issue link is in the task doc Refs) `GET /api/v1/credentials` items carry `health_status` (`ok|error|unknown`), `health_checked_at`, `health_error`; `GET /api/v1/connections` items and the org's `connections` carry `health_status` + `health_checked_at` + `health_error` (the stored secret's validity, K3) next to the existing `status` (the daemon's view); and `POST /api/v1/chat/messages` answers `409 anthropic_key_invalid` with the reason when the org's own Anthropic key is dead. Today `src/views/ConnectionsView.vue` shows the Model row as `Connected · •••• last4` whenever a credential is configured (the `isStoreMissing` warning is the only other state) and the tracker rows show `Connected` / `Fix connection` from `status` alone; `src/stores/chat.ts` maps `chat_not_enabled` and `instance_not_running` to `chatDisabled` / `boxStopped`, and `src/views/ChatView.vue` (~line 228) renders a one-line reason above the composer. The vendored wire-field list `src/lib/api/__contracts__/console-wire-fields.txt` pins the console ref in `console-ref.txt`.

probe: present qf-studio/pilot-console-ui isStoreMissing
probe: present qf-studio/pilot-console-ui chat_not_enabled
probe: present qf-studio/pilot-console-ui data-header-meta

## Scope
- Resync the wire contract first: `scripts/sync-console-wire-fields.sh` against the console ref that carries K3 (main `d833a09`, PR#493; never edit `console-wire-fields.txt` by hand); `src/lib/api/types.ts` `Credential` gains `healthStatus?: 'ok' | 'error' | 'unknown'`, `healthCheckedAt?: string`, `healthError?: string`; `Connection` gains `healthCheckedAt?`, `healthError?`; `src/lib/api/httpAdapter.ts` maps them (loose-parse: absent fields stay undefined).
- `src/views/ConnectionsView.vue`: the Model row renders `health_status === 'error'` as the needs-you dot + `Fix key` (same affordance as the tracker rows' `Fix connection`) with the `healthError` text and `checked <relative time>`; `ok` renders `Connected · checked <relative time>`; `unknown`/absent renders as today. Tracker rows: `healthStatus === 'error'` → `Fix connection` + `healthError` + `checked <relative time>`; `healthStatus === 'ok'` and `status === 'error'` → warning dot + `Saved, not live on the box yet` (the daemon still runs the old secret; a restart or redeploy picks it up); both ok → `Connected · checked <relative time>`.
- `src/stores/chat.ts`: code `anthropic_key_invalid` → `keyInvalid` (with the message); `reset()` clears it. `src/views/ChatView.vue` and `src/views/DashboardView.vue` (Home composer): when `keyInvalid`, the composer is disabled and a one-line notice reads `Your Anthropic key was rejected: <message>. Fix it on Connections.` with a `RouterLink` to `/connections`, in the same place and type as the `boxStopped` line.
- Tests: `ConnectionsView.spec.ts` — the three Model-row states and the tracker-row reason line; `chat.spec.ts` — the new code sets `keyInvalid` and `reset()` clears it; `ChatView.spec.ts` and `DashboardView.spec.ts` — the notice + disabled composer on `keyInvalid`; `httpAdapter.spec.ts` — the new fields map and absent fields stay undefined; `checkWireFields` stays green on the resynced list.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Render the Model row as Connected when `health_status` is `error` -> the ConnectionsView.spec Fix-key assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
Mobile layout changes (the rows reuse the existing responsive markup). The Instances page Anthropic row. Any daemon message text.

## Refs
- Navigator TASK-640 K2 · ui#189-era `storePresent` rows · ui#311 (Research view) · TASK-637 (composer notices on mobile)

## Verify

- Live tenant after K1 deploy: `GET /api/v1/credentials` shows `health_status` for the anthropic key; with the dead key still on file it reads `error` with `anthropic rejected api key (status 401)` within one interval; the GitHub connection reads `error` and stays there (no flip to `connected` on the next syncingest tick).
- After K2 deploy: Connections shows `Fix key` and `Fix connection` with reasons; Research and Home composers show the notice; after the founder saves a good key the rows read `ok · checked just now` and the composer re-enables on the next load.

### LEG K4 — repo: qf-studio/pilot-console-ui — title: fix(credential-health): the dead-key notice follows the key — clear on save, seed from credentials health on load, fresh health on Connections, Fix key on the Model row only, mock in lockstep

## Context
K2 ([ui#378](https://github.com/qf-studio/pilot-console-ui/issues/378) → PR#379) put `health_status` on the Connections rows and a dead-key notice on the Research and Home composers; the post-merge fix PR#381 (ui#380) re-enabled the composer so a retry can clear the notice; the review-minor PR#383 (ui#382) made `saveCredential('anthropic')` in `src/views/ConnectionsView.vue` write `chat.keyInvalid = null` directly after `session.upsertCredential` and added the view-level recovery test. What is left (post-merge review of PR#379, Navigator reviews of PR#381/#383): `chat.keyInvalid` in `src/stores/chat.ts` is set only by a failed send and cleared by a successful send, `reset()`, or that direct write from the view (no store action; the `chat.ts` contract comment "only a successful send (or reset) clears keyInvalid" is stale); on a fresh load with a dead key no notice shows until the first failed send although `GET /api/v1/credentials` already carries `health_status`/`health_error` (`src/lib/api/types.ts` `Credential.healthStatus`); `src/stores/session.ts` caches `loadCredentials`/`loadConnections` per session (`if (credentialsLoaded.value) return`), so `checked <time>` on Connections never refreshes; the `Fix key` branch in `ConnectionsView.vue` runs for every `CREDENTIAL_GROUPS` entry, not only the Model (anthropic) row; `src/lib/api/mockAdapter.ts` has no `health_*` fields and no `anthropic_key_invalid` path (ui CLAUDE.md: adapter, mock and fixtures move in lockstep).

probe: present qf-studio/pilot-console-ui keyInvalid
probe: present qf-studio/pilot-console-ui healthStatus
probe: present qf-studio/pilot-console-ui upsertCredential

## Scope
- `src/stores/chat.ts`: add `syncKeyHealth(credential: Credential | undefined)`: `healthStatus === 'error'` → `keyInvalid = healthError || 'rejected'`; `healthStatus === 'ok'` → `keyInvalid = null`; `unknown`/absent → unchanged. Export it with the store.
- `src/stores/session.ts`: `loadCredentials(opts?: { force?: boolean })` and `loadConnections(opts?: { force?: boolean })` refetch when `force` is set; `upsertCredential` success replaces the row from the server response (it carries `health_status`; the console validates on save).
- `src/views/ConnectionsView.vue`: `onMounted` calls both loaders with `{ force: true }` so `checked <time>` is current; the direct `chat.keyInvalid = null` after `saveCredential('anthropic')` (PR#383) becomes `chat.syncKeyHealth(credentialFor('anthropic'))` on the row the server returned (a saved key is a validated key → the notice clears; a save that comes back `error` keeps it with the server's reason); update the `chat.ts` contract comment to name the three clearing paths (send success, `reset()`, `syncKeyHealth`). The `Fix key` branch applies only when `group.key === 'anthropic'`; other credential groups keep the pre-K2 render.
- `src/views/ChatView.vue` and `src/views/DashboardView.vue`: `onMounted` → `await session.loadCredentials()` (cached is fine here) → `chat.syncKeyHealth(session.credentialFor('anthropic'))`, so the notice is on screen before the first failed turn when the key is already dead, and absent when it is healthy. A send failure still sets `keyInvalid` as today.
- `src/lib/api/mockAdapter.ts` + fixtures: credentials and connections seeds carry `healthStatus: 'ok'`, `healthCheckedAt`, `healthError: ''`; one seed toggle (e.g. `mockFlags.deadAnthropicKey`) makes `listCredentials` return anthropic `healthStatus: 'error'`, `healthError: 'anthropic rejected api key (status 401)'` and makes `sendChatMessage` throw `ChatError('anthropic_key_invalid', …)`; `upsertCredential` returns `healthStatus: 'ok'` and clears the toggle.
- Tests: `chat.spec.ts` — `syncKeyHealth` sets, clears and ignores `unknown`; `ConnectionsView.spec.ts` — mount refetches (`force`), the existing PR#383 save test still passes through `syncKeyHealth`, a Slack credential with `healthStatus: 'error'` shows no `Fix key`; `ChatView.spec.ts` + `DashboardView.spec.ts` — seeded dead key shows the notice on mount with the composer enabled, seeded `ok` shows none; `session.spec.ts` — `force` refetches, default stays cached; mock-mode spec reaches the dead-key notice through the toggle.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Remove the `syncKeyHealth` call in ChatView `onMounted` -> the ChatView.spec "seeded dead key shows the notice on mount" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
Any change to the composer `disabled` prop (PR#381 settled it: enabled). Tracker-row copy. The console API. Mobile layout.

## Refs
- Navigator TASK-640 § K2 review · ui#378 / PR#379 · ui#380 / PR#381 · ui#382 / PR#383 · ui CLAUDE.md lockstep rule

### K5 review — PR#391 (Navigator, 11-06 ~17:05Z, posted as a comment) — REQUEST-CHANGES
Scope met (re-read after save; `{ force: true }` on Chat/Home mount; docblocks; mock dead-key row configured; GH-389 specs). **CI `test` red, 3 failures, while the PR body's `npm test` block is green — the head is one commit `wip(GH-389): uncommitted session work (auto-preserved)`, so the pasted evidence predates the head (TASK-460 shape).** Causes, bisected locally at `9f86cda`: (1) `ConnectionsView.spec` GH-384 puts the error on the PUT response — the contract this leg rules out; rewrite with the error on the list re-read. (2) `ChatView.spec` GH-389 pollutes GH-387: `seedKeyHealth` re-`spyOn`s an already-spied `listCredentials`; `mockRestore` unwinds one layer, so GH-387's "real adapter" is a leftover spy resolving `ok` → no notice; re-mocking the existing spy makes the file 27/27 (the GH-384 sibling passes GH-387 only by luck today). (3) GH-382 is timing-flaky: two sequential random 150–400 ms mock delays vs one 500 ms fake-timer advance. **Navigator spec error #2 in this task: the leg said "existing GH-382/384 specs still green" — GH-384 encodes the old contract and had to be rewritten.** Autopilot filed [ui#392](https://github.com/qf-studio/pilot-console-ui/issues/392) (`autopilot-fix`) 16:50Z; the pre-flight judge **rejected it as `reject_vague`** (`pilot-needs-clarification`: the daemon's own fix issue carries no root cause) → body rewritten by Navigator with the three causes + the fix recipe, label removed 16:55Z — **redundant: the daemon's owner-death recovery had already re-armed #389 as `pilot-retry-1` at 16:51Z and closed PR#391** (fix issue died at pre-flight → the source issue retries). #392 closed by hand as superseded 17:1xZ; the retry's PR gets the review. Lesson: after an autopilot-fix dies at pre-flight, check the source issue for `pilot-retry-N` before re-filing the fix. Daemon gaps: the autopilot-fix issue shape fails the pre-flight judge and the re-arm restarts from scratch — both are legs of the Pilots session's [TASK-643](TASK-643-ci-fix-chain-loses-finished-work.md) (same day); the executor's evidence block predating an auto-preserved head is TASK-460's class, unfiled.

### LEG K5 — repo: qf-studio/pilot-console-ui — title: fix(credential-health): a saved Anthropic key clears the dead-key notice on prod — re-read credentials after the save and on Research mount; mock seed only reports a dead key on a configured row

## Context
K4 ([ui#384](https://github.com/qf-studio/pilot-console-ui/issues/384) → PR#385) made `saveCredential('anthropic')` in `src/views/ConnectionsView.vue` call `chat.syncKeyHealth(credentialFor('anthropic'))` on the row that `session.upsertCredential` stored from the `PUT /api/v1/credentials/{key}` response. The console answers that PUT with `{key, last4}` only — no `health_status` — so on prod the stored row has `healthStatus` undefined, `syncKeyHealth` in `src/stores/chat.ts` leaves `keyInvalid` untouched, and the "Your Anthropic key was rejected" notice stays up after a good save until the next successful send (PR#383's direct clear worked; K4 regressed it; the mock's `upsertCredential` returns `healthStatus: 'ok'`, which is why the spec passed). The console does write the anthropic health row `ok` on every save, so `GET /api/v1/credentials` returns truth right after. Second gap: `ChatView.vue`/`DashboardView.vue` seed `keyInvalid` from the cached `session.loadCredentials()`, so a list loaded before the key died applies a stale `ok` over a fresh `keyInvalid` on navigation. Third: `src/lib/api/mockAdapter.ts` `listCredentials` with `mockFlags.deadAnthropicKey` reports `error` on the default seed's anthropic row that has `configured: false`, so the composer notice shows while the Connections `Fix key` branch (which requires `configured`) stays hidden.

probe: present qf-studio/pilot-console-ui syncKeyHealth
probe: present qf-studio/pilot-console-ui deadAnthropicKey
probe: present qf-studio/pilot-console-ui force: true

## Scope
- `src/views/ConnectionsView.vue` `saveCredential`: after `session.upsertCredential(key, secret)` resolves and `key === 'anthropic'`, `await session.loadCredentials({ force: true })` then `chat.syncKeyHealth(session.credentialFor('anthropic'))`. The mount's forced loads stay; the save path's own re-read comes after the save, so an in-flight mount read cannot overwrite the saved row with pre-save data for the health call.
- `src/views/ChatView.vue` and `src/views/DashboardView.vue` `onMounted`: `session.loadCredentials({ force: true })` before `chat.syncKeyHealth(...)` (one GET per mount; the stale-`ok`-over-fresh-error case goes away). Keep the `.catch(() => {})`.
- `src/stores/chat.ts`: the docblock on `keyInvalid` and the poll comment name all clearing paths: a successful send, `reset()`, `syncKeyHealth` with `ok`, and any other chat error code (which `applyChatErrorCode` sets to `null`).
- `src/lib/api/mockAdapter.ts`: with `mockFlags.deadAnthropicKey`, `listCredentials` reports `error` only when the anthropic row is `configured: true` (the default seed flips `configured: true, last4: '9f2a'` for that row while the flag is on); jira seed rows carry `healthStatus: 'unknown'`. `upsertCredential` keeps returning `healthStatus: 'ok'` (the real PUT returns no health; the ui must not depend on it — document that in the mock comment).
- Tests: `ConnectionsView.spec.ts` — a save whose adapter response has no `healthStatus` (spy returns `{ key, configured: true, last4 }`) and whose `listCredentials` then returns `ok` clears `keyInvalid` (mutation pin: drop the `force` re-read → fails); `ChatView.spec.ts` — a cached `ok` list plus `listCredentials` now returning `error` seeds the notice on mount (pin: mount reads the list again); `mockAdapter.spec.ts` — the dead-key row is `configured: true`; existing GH-382/384 specs still green.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Remove the `loadCredentials({ force: true })` re-read in `saveCredential` -> the ConnectionsView.spec "save with a health-less PUT response clears keyInvalid" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
The console PUT response shape (a console leg may add `health_status` to it later; the ui must work without it). Composer `disabled` behaviour (PR#381 settled it). Tracker rows.

## Refs
- Navigator TASK-640 § K4 review · ui#384 / PR#385 · console `internal/orgs/handlers.go` `handlePutCredential` (`credentialResponse{Key, Last4}`) · ui#382 / PR#383

## Refs

- K1 → https://github.com/qf-studio/pilot-console/issues/488
- K3 → https://github.com/qf-studio/pilot-console/issues/492
- K2 → https://github.com/qf-studio/pilot-console-ui/issues/378
- K4 → https://github.com/qf-studio/pilot-console-ui/issues/384
- K5 → https://github.com/qf-studio/pilot-console-ui/issues/389
