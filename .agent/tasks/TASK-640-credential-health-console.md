# TASK-640: Credential health — the console re-checks the org's own Anthropic key and tracker tokens, shows a truthful status, and gates chat on a dead key

**Status**: 🟢 **K1 LIVE ON PROD 09:50Z — the first sweep already tells the truth: `GET /api/v1/credentials` → anthropic `health_status: ok` (checked 09:50:05Z, the new key); `GET /api/v1/connections` → github `status: error`, `health_error: "orgs: github rejected token (status 401)"`. Holding on console#490 (post-merge minors, running) → K3 residue (the status split) → redeploy → K2.** Earlier 10:05Z: console#490 filed with nine minors covering every K3 item.
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
7. **Operator fix for the live tenant is separate** — 11-06: new Anthropic key saved + daemon restarted 09:46Z (Anthropic 200); the GitHub PAT is still dead (401, SDK poller disabled) until the founder saves a new one and the daemon restarts.: the founder saves a new key and PAT, the tenant daemon restarts (it reads `/run/pilot/env` at service start). This task prevents the next silent week, not this one.

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

### LEG K3 — repo: qf-studio/pilot-console — title: fix(credhealth): the sweep owns health, not status — split the stored-secret validity from the daemon's connection view; compare-and-set on checked_at; 403 is a rejection; outage wording; one DB pool

## Context
Leg K1 (console#488 → PR#489) added `internal/credhealth` (sweeper), migration 0042 (`credential_health` table; `connections.health_checked_at`, `connections.health_error`), `Store.SetConnectionHealth` and `Store.UpsertCredentialHealth`, and the `409 anthropic_key_invalid` chat gate. Its pre-PR review found that `SetConnectionHealth` sets `connections.status` as well as the health columns, so the sweep and `recordReadyChecks` in `internal/fleet/readychecks.go` overwrite each other: a saved key that validates in the console while the tenant still runs the old secret (the daemon reads `/run/pilot/env` only at service start) flips the row between `connected` and `error` every tick, and `SetConnectionStatus` never clears `health_error`. A sweep that read the old secret can also write `error` over the `ok` a fresh save just wrote (`recordCredential` / `recordConnection` in `internal/credhealth/sweeper.go`), gating chat for up to one interval. `isGitHubUpstreamStatus` in `internal/orgs/validators.go` treats 403 as transient, so a 403-rejected token is never recorded. The transient 429/5xx wording "rejected api key (status N)" reaches the `PUT` 422 body. `startCredentialHealth` in `main.go` opens a second `fleet.Open` pool that is never closed.

probe: present qf-studio/pilot-console func[[:space:]]\(s[[:space:]]\*Store\)[[:space:]]SetConnectionHealth
probe: present qf-studio/pilot-console anthropic_key_invalid
probe: present qf-studio/pilot-console func[[:space:]]isGitHubUpstreamStatus

## Scope
- Two facts, two owners. `connections.status` is the daemon's view and is written only by `recordReadyChecks` and syncingest `flagConnectionError`. The health columns are the stored secret's validity and are written only by the sweep and the save handlers. `SetConnectionHealth` no longer touches `status`; it takes `healthStatus ok|error`, `checkedAt`, `errText`. Migration 0043 adds `connections.health_status text NOT NULL DEFAULT 'unknown' CHECK (health_status IN ('ok','error','unknown'))`; down drops it. `connectionResponse` gains `health_status`; `status` keeps its meaning.
- Compare-and-set: `UpsertCredentialHealth` and `SetConnectionHealth` take the `readAt` the sweep captured before it read the secret and apply only when the row's `checked_at` is null or older (`WHERE checked_at IS NULL OR checked_at < $readAt`); the save handlers write unconditionally with `now()`.
- `isGitHubUpstreamStatus`: 403 is not upstream; the validator returns a rejection for 401 and 403 and `IsTransient` is false for both. The Anthropic validator: 401 and 403 reject, 429/5xx transient.
- Wording: transient errors read `<provider> unavailable (status N)`; rejections keep `rejected … (status N)`. The 422 body of a save during an outage says unavailable, not rejected.
- `startCredentialHealth` reuses the process's existing store/pool; if a separate pool is unavoidable, close it on shutdown with the others.
- Tests: sweeper — a ready-probe `error` row survives a passing sweep (`status` untouched, `health_status` ok) and a sweep never writes over a newer `checked_at`; store — the compare-and-set predicate; validators — 403 is a rejection, 503 is transient with the unavailable wording; raw-body test pins `health_status` on the connections list.

## Acceptance
- [ ] `make test` passes; paste the output into the PR body.
- [ ] Make `SetConnectionHealth` set `status` again -> the sweeper status-untouched assertion fails
- [ ] Drop the `checked_at` predicate -> the newer-row assertion fails
- [ ] `make lint` passes; paste the output into the PR body.

## Out of scope
The UI (K2, which renders both facts). Alert channels. The daemon's `/ready` semantics.

## Refs
- Navigator TASK-640 K3 (review of PR#489) · console#488 · console#189 (store_present)

### LEG K2 — repo: qf-studio/pilot-console-ui — title: feat(connections): credential health on the Connections rows and a dead-key notice on the composer — Fix key / Fix connection with the reason and the check time; chat composer disabled on anthropic_key_invalid

## Context
After console leg K1 (TASK-640, filed in qf-studio/pilot-console; its issue link is in the task doc Refs) `GET /api/v1/credentials` items carry `health_status` (`ok|error|unknown`), `health_checked_at`, `health_error`; `GET /api/v1/connections` items and the org's `connections` carry `health_status` + `health_checked_at` + `health_error` (the stored secret's validity, K3) next to the existing `status` (the daemon's view); and `POST /api/v1/chat/messages` answers `409 anthropic_key_invalid` with the reason when the org's own Anthropic key is dead. Today `src/views/ConnectionsView.vue` shows the Model row as `Connected · •••• last4` whenever a credential is configured (the `isStoreMissing` warning is the only other state) and the tracker rows show `Connected` / `Fix connection` from `status` alone; `src/stores/chat.ts` maps `chat_not_enabled` and `instance_not_running` to `chatDisabled` / `boxStopped`, and `src/views/ChatView.vue` (~line 228) renders a one-line reason above the composer. The vendored wire-field list `src/lib/api/__contracts__/console-wire-fields.txt` pins the console ref in `console-ref.txt`.

probe: present qf-studio/pilot-console-ui isStoreMissing
probe: present qf-studio/pilot-console-ui chat_not_enabled
probe: present qf-studio/pilot-console-ui data-header-meta

## Scope
- Resync the wire contract first: `scripts/sync-console-wire-fields.sh` against the console ref that carries K1 (never edit `console-wire-fields.txt` by hand); `src/lib/api/types.ts` `Credential` gains `healthStatus?: 'ok' | 'error' | 'unknown'`, `healthCheckedAt?: string`, `healthError?: string`; `Connection` gains `healthCheckedAt?`, `healthError?`; `src/lib/api/httpAdapter.ts` maps them (loose-parse: absent fields stay undefined).
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

## Refs

- K1 → https://github.com/qf-studio/pilot-console/issues/488
- K3 → (filled by the dispatch script)
- K2 → (filled by the dispatch script)
