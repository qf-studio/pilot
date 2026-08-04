# feat(syncoutbound): C4 — outbound op worker (FIFO per card, compare-before-write, shadow echo-suppression, park/retry)

## Context

Blocked by: #90

S4 board track, leg C4 — **the leg that makes board edits reach the tracker**. C7 enqueues `card_ops` rows; today nothing drains them. C3 brings issues in; this pushes changes back out. C1/C2 are merged on main (`755899d`).

Do NOT read or clone sibling repos — everything needed is embedded.

### The write contract (verified at `studio-sdk` HEAD `acee519`, tag `v0.31.2`; C3 adds the dependency)

```go
// sdk/core/sync.go
type FieldPatch map[string]any
type IssueDraft struct { Title, Body string; Labels []string; Priority string }
type SyncWriter interface {
    UpdateFields(ctx context.Context, nativeID string, fields FieldPatch) (IssueSnapshot, error) // returns POST-WRITE snapshot
    TransitionState(ctx context.Context, nativeID, providerState string) error
    AddComment(ctx context.Context, nativeID, body, idemKey string) error
    CreateIssue(ctx context.Context, projectID string, draft IssueDraft) (IssueSnapshot, error)
}
```

**Provider asymmetries that will silently drop writes — every one must be encoded:**

- **`FieldPatch` keys are NOT uniform.** GitHub accepts `title`, `body`, `labels`; Jira accepts `title`→summary, `body`→description, `labels`; **Linear accepts `title`, `description` (NOT `body`), `priority`, `labels`**. GitHub and Jira **silently ignore** unknown keys; **Linear hard-errors** on an unrecognized key. So a naive `{"body": …}` is a silent no-op on Linear, and `{"description": …}` is a silent no-op on GitHub/Jira. Build the patch **per provider**, and treat "field not writable on this provider" as an explicit, journaled outcome — never a silent success.
- **Priority is writable on Linear only.** A `priority` op against GitHub/Jira must resolve to a documented terminal state (applied-as-noop with a journaled reason), not a fake success and not an infinite retry.
- **`TransitionState` input differs**: GitHub takes literally `"open"`/`"closed"`; Linear takes a workflow-state UUID *or* a state name (resolved via a cached per-team lookup); Jira takes a status/transition *name* matched case-insensitively against available transitions. Feed it the mapped `provider_state` from `status_maps` — never the canonical status.
- **Comment idempotency**: all three embed the marker `<!-- pilot-op:{idemKey} -->` and scan before posting. ⚠ **Jira's scan is NOT paginated** (`GET /issue/{key}/comment` with no `startAt`) — on a busy issue a marker beyond page 1 is invisible and `AddComment` will double-post. Known upstream gap; do not work around it inside the op executor, but **journal a warning when posting a Jira comment** so a duplicate is explainable, and note it in the PR body as an SDK follow-up.
- **Retry adoption is 2 of 3**: GitHub and Jira wire `sdk/util/retry` (typed `RateLimitError`/`AuthError`); **Linear has neither** — wrap Linear writes yourself. Auth failures come as three distinct types (`github.AuthError`, `jira.AuthError`, `retry.AuthError`) and none from Linear.

### Board-side API (merged; these methods have no production caller yet — they are yours)

```go
func (s *Store) ListDueOps(ctx, connectionID uuid.UUID, now time.Time, limit int) ([]CardOp, error) // state='pending' AND next_attempt_at<=now, ORDER BY created_at
func (s *Store) GetOp(ctx, id uuid.UUID) (CardOp, error)
func (s *Store) MarkOpState(ctx, id uuid.UUID, state OpState, lastError string) error
func (s *Store) RescheduleOp(ctx, id uuid.UUID, attempts int, nextAttemptAt time.Time, lastError string) error
func (s *Store) GetCard(ctx, id uuid.UUID) (Card, error)
func (s *Store) GetLink(ctx, cardID uuid.UUID) (CardLink, error)
func (s *Store) PutShadow(ctx, cardID uuid.UUID, snapshot json.RawMessage) error
func (s *Store) SetLinkProviderUpdatedAt(ctx, cardID uuid.UUID, updatedAt time.Time) error
func (s *Store) SetLinkSyncState(ctx, cardID uuid.UUID, state SyncState) error
func (s *Store) ListStatusMap(ctx, connectionID uuid.UUID) ([]StatusMapRow, error)
// CardOp{ID, CardID, ConnectionID, Field OpField, Payload json.RawMessage, IdemKey string, State OpState, Attempts int, NextAttemptAt time.Time, LastError string, CreatedAt time.Time}
// OpField = status|labels|priority|title|body|comment ; OpState = pending|inflight|applied|superseded|parked
```

**`ListDueOps` performs no leasing** — its doc comment says `FOR UPDATE SKIP LOCKED` is "a later issue's concern". This issue owns claiming.

**Why the shadow write is mandatory, not an optimization**: after a successful write, the post-write snapshot goes into `link_shadows` immediately. When our own write comes back through C3's polling, it diffs empty against the shadow → no echo. Actor-based filtering is impossible here — the board worker, the customer's Pilot instance, and the human may all authenticate with the same PAT, so value fingerprinting is the only echo filter that exists. Skipping this step produces a self-inflicted conflict on every write.

## Acceptance

1. **Package `internal/syncoutbound`** (flat, per the `internal/syncengine` precedent). `Worker` with constructor injection and narrow consumer-side interfaces (board store, orgs/connections reader, and a **writer factory** `func(ctx, orgs.Connection) (core.SyncWriter, string /*projectID*/, error)` so tests fake providers). `Run(ctx)` ticks and drains in-flight work before returning (WaitGroup), mirroring `internal/fleet.Reconciler.Run`.

2. **Claim before execute**: mark an op `inflight` via `MarkOpState` before any network call, so a crash leaves a visible `inflight` row rather than an invisible in-progress write. **At most one in-flight op per card** (FIFO by `created_at`) — a second op for the same card waits for the next tick. Ops for *different* cards proceed concurrently up to `MaxConcurrent` (default 4). Stale `inflight` ops older than `InflightTimeout` (default 15m) are reclaimed to `pending` with attempts preserved and the reclaim journaled.

3. **Compare-before-write, always**: `GetIssue(nativeID)` first; if the remote value already equals the op's target, mark `applied` without writing and journal `noop_already_applied`. This is what makes replays and crashed-mid-write retries safe for every value-write field (status/labels/priority/title/body are idempotent by value).

4. **Execute per field**, per provider:
   - `title`/`body` → `UpdateFields` with the **provider-correct key** (`body` on GitHub/Jira, `description` on Linear).
   - `priority` → `UpdateFields` on Linear; on GitHub/Jira mark `applied` with a journaled `unsupported_on_provider` reason (never park, never retry).
   - `labels` → `UpdateFields` with the merged label set. **Hard invariant: additive for the `pilot` trigger label only; a labels write must NEVER remove or rewrite a `pilot-*` status label** (`pilot-in-progress`/`pilot-done`/`pilot-failed`) — Pilot's poller un-marks a processed issue when those disappear, which re-arms dispatch and causes duplicate executions. Enforce in code and pin with a test.
   - `status` → `TransitionState` with the mapped `provider_state` from `ListStatusMap`; **no mapping for the target status → park immediately** with an actionable message (this is a configuration problem, not a transient failure).
   - `comment` → `AddComment(nativeID, body, op.IdemKey)`.

5. **On success**: fetch/derive the post-write snapshot (`UpdateFields` returns it; `TransitionState`/`AddComment` do not — follow with one `GetIssue`), then **write it into `link_shadows` via `PutShadow` and bump `SetLinkProviderUpdatedAt`** before marking the op `applied`. Ordering matters: shadow first, then `applied` — a crash between them replays harmlessly, the reverse loses echo suppression.

6. **Failure handling**: exponential backoff via `RescheduleOp` (base 1s, ×2, cap 5m), **max 5 attempts → `parked`** with `last_error` set, plus `SetLinkSyncState(cardID, SyncStateParked)` so the card badges in the UI. **Auth errors park immediately, no retry** (normalize the three provider `AuthError` types plus Linear's untyped case) and additionally flag the connection unhealthy — a revoked PAT must be visible, not retried 5×. Rate-limit errors respect `Retry-After` where the typed error carries it. A parked op never blocks other cards.

7. **Config** (`PILOT_CONSOLE_SYNC_*`, eager validation in the `loadFleetConfig` style): `OUTBOUND` (`"1"` enables; **default off** — the binary must run with no env set) · `OUTBOUND_INTERVAL` (default `15s`, min `5s` — writes are latency-sensitive, unlike polling) · `OUTBOUND_MAX_CONCURRENT` (default 4, min 1) · `OUTBOUND_MAX_ATTEMPTS` (default 5, min 1) · `OUTBOUND_INFLIGHT_TIMEOUT` (default `15m`).

8. **main.go wiring**: gate on `cfg.Sync.OutboundEnabled && cfg.DatabaseURL != ""`, `go w.Run(ctx)` with the bounded shutdown wait; flag-set-but-no-DB → `logger.Warn` and don't start. Startup log names the knobs.

9. **Tests** (table-driven, stdlib only, hand-rolled fakes — no testify) with a fake `SyncWriter` recording calls: compare-before-write short-circuits with zero writes · successful write puts the shadow **before** marking applied (assert call order) · one in-flight op per card, others deferred · different cards run concurrently up to the cap · backoff schedule and attempt counting · 5th failure parks the op and sets the link parked · auth error parks on attempt 1 · unmapped status parks immediately with a message naming the status · Linear patch uses `description` and GitHub/Jira use `body` · a labels op never removes a `pilot-*` label · priority op on GitHub/Jira applies as a journaled no-op · stale inflight reclaimed after the timeout. DB-gated tests reuse C1's `newTestStore` pattern.

10. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `internal/syncoutbound/worker.go` (loop, claim, dispatch), `internal/syncoutbound/execute.go` (per-field/per-provider execution + patch building), `internal/syncoutbound/writer.go` (SDK writer factory — mirror C3's connector factory; extract a shared helper only if it is genuinely identical), tests alongside, `internal/config/config.go`, `main.go`.

Sequencing: per-provider patch building (pure, table-tested first — this is where the asymmetries live) → claim/FIFO machinery against fakes → compare-before-write → success path incl. shadow ordering → failure/backoff/park → config + main wiring.

**Verify-before-relying**: how a stored tracker credential is read back (C3 establishes this — reuse whatever it landed, do not invent a second path).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** ingest/polling (C3) · status-map auto-seed (C5) · per-connection rate budgets and write-preemption (C6 — this worker may share a connection's budget later; do not build the budgeter) · `CreateIssue`/create-in-tracker flow (a later leg with the UI modal) · HTTP routes (C7) · webhooks · fixing Jira's unpaginated comment scan (upstream SDK issue — journal and move on).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-03 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/91 (labels: pilot, no-decompose; gated on #90)
- Depends on: console#87 (C1), console#88 (C2), C7 (#TBD), C3 (#TBD — serialized; C3 adds the studio-sdk dependency this leg relies on)
- SDK facts verified 2026-08-03 at `studio-sdk` `acee519` / tag `v0.31.2`. Open upstream gap to journal around: Jira comment idem-scan unpaginated (double-post risk on busy issues)
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §3 (outbound, idempotency, park), §4 (shadow echo-suppression), §6 (label hygiene), §7 (failure modes) — embedded above
