# feat(syncingest): C3 — per-connection ingest worker (delta poll + overlap, 6h full sweep, orphan detect) feeding the reconciler

## Context

Blocked by: #89

S4 board track, leg C3 — **the leg that puts cards on the board**. C1 (`internal/board`, migration 0008) and C2 (`internal/syncengine`) are merged on main (`755899d`); C7 exposes the board over HTTP. Nothing currently pulls issues from any tracker — `pilot-console` has no provider HTTP client at all. This issue adds one, via the SDK.

Do NOT read or clone sibling repos — everything needed is embedded.

### The SDK contract you will consume (verified present at `studio-sdk` HEAD `acee519`, tag `v0.31.2`)

Public module, no GOPRIVATE: `go get github.com/qf-studio/studio-sdk@v0.31.2`. `sdk/core` and `sdk/util/retry` are stdlib-only.

```go
// sdk/core/sync.go
type IssueSnapshot struct {
    NativeID, SequenceID, Title, Body string
    State      string   // provider-native state id or name
    StateGroup string   // provider state category ("todo"/"in progress"/"done"), empty if none
    Labels     []string
    Priority   string   // already normalized
    Assignee   string   // display-only
    URL        string
    CreatedAt, UpdatedAt time.Time
    Deleted    bool     // ⚠ NEVER set by any connector today — do not rely on it
}
type Cursor string
type SyncSource interface {
    ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page Cursor) ([]IssueSnapshot, Cursor, error)
    ListAll(ctx context.Context, projectID string, page Cursor) ([]IssueSnapshot, Cursor, error)
    GetIssue(ctx context.Context, nativeID string) (IssueSnapshot, error)
}
type SyncCapable interface { SyncSource; SyncWriter }   // SyncWriter is C4's concern
```

Construction (credentials are raw strings, no functional options, HTTP timeout hardcoded 30s):

```go
github.NewSyncClient(github.NewClient(pat), owner, repo)                                  // *github.SyncClient
linear.NewSyncAdapter(linear.NewClient(apiKey))                                           // *linear.SyncAdapter
jira.NewSyncClient(jira.NewClient(baseURL, email, apiToken, jira.PlatformCloud), projKey) // *jira.SyncClient
```

**Provider asymmetries that will silently corrupt ingest if ignored — encode each:**

- **`projectID` means three different things**: GitHub `"owner/repo"` (empty → the bound repo; malformed → error, no silent fallback) · Linear **team key** (`"ENG"`) · Jira **project key** (`"PROJ"`). Source it per-connection from `connections.config` (github repos, jira base_url/email) — verify what C1/orgs actually stores before assuming.
- **`NativeID` differs**: GitHub bare number `"42"` (or `"owner/repo#42"` when fetched via a projectID override) · Linear issue UUID · Jira issue key `"PROJ-42"` (NativeID == SequenceID). Persist verbatim; never parse or reconstruct it.
- **Linear's `ListUpdatedSince` uses `updatedAt: {gte: since}`** (on-or-after, RFC3339Nano) — deliberately inclusive so a watermark-boundary issue is never dropped. **The SDK docblock explicitly requires the caller to dedupe by `NativeID` across successive calls at the same watermark.** GitHub/Jira are `since`/`updated >=` respectively. Dedupe per pass regardless of provider.
- **No tombstones**: `IssueSnapshot.Deleted` is never set by any connector. Deletion is only detectable as *absence from a full sweep* — which is exactly what orphan detection is for.
- **GitHub sync pagination is uncapped by design** (`perPage=100`, next cursor emitted whenever the raw batch is full); PRs are filtered out post-fetch, so **a page can be shorter than 100 while still emitting a next cursor** — drive the loop off the returned `Cursor`, never off `len(snapshots)`.
- **Retry adoption is 2 of 3**: GitHub and Jira wire `sdk/util/retry` (`WithRetry`, typed `RateLimitError`/`AuthError`) into their transports. **Linear has none** — no retry, no typed errors; a 429 or revoked key surfaces as a generic error. Wrap Linear calls in `retry.WithRetry` yourself with a conservative classifier, and treat unclassifiable Linear errors as retryable-with-backoff rather than fatal.
- **Auth failures are three distinct types** (`github.AuthError`, `jira.AuthError`, `retry.AuthError`) and Linear produces none. Normalize to one internal "connection unhealthy" signal.

### Board-side API you will drive (all merged, all currently without a production caller)

```go
func (s *Store) FindLinkByNative(ctx, connectionID uuid.UUID, nativeIssueID string) (CardLink, error)  // ErrNotFound = unknown issue
func (s *Store) CreateCard(ctx, p CreateCardParams) (Card, error)
func (s *Store) LinkCard(ctx, p LinkCardParams) (CardLink, error)
func (s *Store) GetCard(ctx, id uuid.UUID) (Card, error)
func (s *Store) GetShadow(ctx, cardID uuid.UUID) (Shadow, error)      // ErrNotFound = first ingest
func (s *Store) ListCardOps(ctx, cardID uuid.UUID, limit int) ([]CardOp, error)
func (s *Store) SetLinkSyncState(ctx, cardID uuid.UUID, state SyncState) error
func (s *Store) GetCursor(ctx, connectionID uuid.UUID, kind CursorKind) (string, error)  // ErrNotFound = unset
func (s *Store) PutCursor(ctx, connectionID uuid.UUID, kind CursorKind, value string) error
func (s *Store) ListStatusMap(ctx, connectionID uuid.UUID) ([]StatusMapRow, error)
func (s *Store) GetOrCreateDefaultBoard(ctx, orgID uuid.UUID) (Board, error)
// CursorKindDeltaWatermark | CursorKindFullSweepAt ; SyncStateOK|Orphaned|Parked
```

```go
// internal/syncengine
type Snapshot struct { Title, Body, Status, ProviderState, Priority string; Labels []string }
type Input struct { Base *Snapshot; Remote, Local Snapshot; PendingLocal map[Field]bool }
func NewReconciler(store committerAPI) *Reconciler   // committerAPI = { CommitReconcile(...) }; *board.Store satisfies it
func (r *Reconciler) ReconcileAndCommit(ctx context.Context, cardID uuid.UUID, in Input, providerUpdatedAt time.Time) (Result, error)
```

`Reconcile` detects status change on **`ProviderState`, not canonical `Status`** — so populate `Remote.ProviderState` from `IssueSnapshot.State` and `Remote.Status` from the status map. `Base` is the unmarshaled shadow (`nil` on first ingest). `PendingLocal` must be true for every field with a `pending`/`inflight` op.

## Acceptance

1. **Dependency**: add `github.com/qf-studio/studio-sdk v0.31.2` (the clean tag — the sibling `pilot` repo pins a pseudo-version; do not copy that). `go.mod`/`go.sum` committed.

2. **Package `internal/syncingest`** (flat, per C2's `internal/syncengine` precedent). A `Worker` with constructor injection and narrow consumer-side interfaces (house idiom — declare them here, doc-comment the concrete implementor): the board store, the reconciler, an orgs/connections reader, and a **connector factory** `func(ctx, orgs.Connection) (core.SyncSource, string /*projectID*/, error)` so tests fake providers entirely. `Run(ctx)` ticks, returns only after in-flight work drains (WaitGroup), mirroring `internal/fleet.Reconciler.Run`.

3. **Per-tick, per-connection delta poll**: read `delta_watermark`; call `ListUpdatedSince(projectID, watermark − overlap, cursor)`, following the returned `Cursor` until empty; **overlap = 2× poll interval** (tolerates provider clock skew and second-granularity timestamps). Dedupe by `NativeID` within a pass. Advance the watermark to the max `UpdatedAt` **only after the page is fully reconciled** — never before.

4. **Per snapshot**: `FindLinkByNative(connectionID, NativeID)`.
   - **Known** → load card + shadow + pending-op fields → build `Input` (map provider state → canonical via `ListStatusMap`; `Base` = unmarshaled shadow or nil) → `ReconcileAndCommit(cardID, input, snapshot.UpdatedAt)`.
   - **Unknown** → `CreateCard` (origin = provider, status from the map, `assignee_display` from `Assignee`) then `LinkCard` (provider, NativeID, SequenceID, URL) then seed the shadow by reconciling with `Base == nil` (R8 adopt-wholesale) so the very next poll diffs clean rather than re-adopting.
   - Ingest must be **idempotent**: re-ingesting an unchanged snapshot produces `Echo` and writes nothing but the shadow refresh. Assert this.

5. **Status mapping with a safe fallback**: C5 (auto-seed from provider taxonomy) is a later leg, so when `ListStatusMap` returns no rows use a documented built-in default derived from `StateGroup` (todo→`todo`, "in progress"→`in_progress`, done→`done`), and for GitHub's `open`/`closed` map open→`todo`, closed→`done`. **Never let an unmapped state move a card**: if the provider state cannot be mapped, leave `Remote.Status` equal to the card's current status so the reconciler sees no status change, and log once per (connection, state).

6. **Full sweep + orphan detection**: every `SweepInterval` (default 6h, tracked via `full_sweep_at`) run `ListAll` to completion and collect all seen `NativeID`s. Links for that connection whose native is **absent from a fully-successful sweep** → `SetLinkSyncState(cardID, SyncStateOrphaned)` + log. **Never auto-archive or delete a card** (a tracker permission hiccup must not eat the customer's board). A sweep that errors on any page is abandoned wholesale — no orphan marking from a partial enumeration. A previously-orphaned link whose native reappears returns to `SyncStateOK`.

7. **Config** (`PILOT_CONSOLE_SYNC_*`, mirroring `loadFleetConfig` style with eager validation): `INGEST` (`"1"` enables; **default off** — the binary must run with no env set) · `INGEST_INTERVAL` (default `60s`, min `10s`) · `INGEST_SWEEP_INTERVAL` (default `6h`, must exceed the poll interval) · `INGEST_PAGE_LIMIT` (default 20 pages per connection per tick — bounds a cold start; **log when the cap truncates a pass** so silent partial ingest is impossible).

8. **main.go wiring**: gate on `cfg.Sync.IngestEnabled && cfg.DatabaseURL != ""`, build store + reconciler + factory, `go w.Run(ctx)` with the `reconcilerDone`-style bounded shutdown wait. Flag set but `DATABASE_URL` empty → `logger.Warn` and do not start (existing posture). Startup log names the knobs.

9. **Single-instance assumption is explicit**: v1 assumes one console process polls a given connection (the control plane runs as a single instance today). Per-connection leases (`FOR UPDATE SKIP LOCKED` + heartbeat) are a later leg. Log this assumption once at startup so a future second replica is diagnosable rather than mysterious, and record it in the package doc.

10. **Tests** (table-driven, stdlib only, hand-rolled fakes — no testify): fake `SyncSource` returning scripted pages/cursors. Cover: new native → card created, linked, shadow seeded · unchanged snapshot re-ingested → `Echo`, no card write · remote-only change → card updated · watermark advances only after a fully reconciled page · a mid-pass page error leaves the watermark untouched · duplicate `NativeID` at the watermark boundary (the Linear `gte` case) ingests once · multi-page cursor loop terminates on cursor, not page length · unmapped provider state does not move the card · sweep marks absent link orphaned, and a partial/errored sweep marks nothing · orphan recovery back to `ok` · page-cap truncation logs. DB-gated tests reuse C1's `newTestStore` pattern (skip without `DATABASE_URL`, hard-fail in CI).

11. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `internal/syncingest/worker.go` (loop, tick, sweep), `internal/syncingest/mapping.go` (snapshot→Input, status map + fallback), `internal/syncingest/connector.go` (SDK factory per provider), tests alongside, `internal/config/config.go`, `main.go`.

Sequencing: connector factory + mapping (pure, table-tested) → single-connection delta pass against a fake → card creation/link/shadow seeding → sweep + orphans → config/main wiring → cap + logging.

**Verify-before-relying**: (a) how a stored tracker credential is *read back* — `internal/secrets` has a writer and an env-gated postgres/SSM driver; confirm a read path exists and use it, or add the minimal reader in this PR and say so; (b) exactly which fields `connections.config` holds per tracker (github repos, jira base_url/email) — derive `projectID` from real data, not from this spec's guess.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** outbound op execution/leasing/compare-before-write (C4) · status-map auto-seed + wizard (C5) · rate budgeting / write preemption (C6) · webhooks (deferred by design — polling is authoritative) · per-connection leases (see AC 9) · any HTTP route (C7) · touching `card_ops` other than reading pending fields for `PendingLocal`.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-03 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/90 (labels: pilot, no-decompose; gated on #89)
- Depends on: console#87 (C1), console#88 (C2), C7 (#TBD — serialized to avoid main.go/config.go collisions)
- SDK facts verified 2026-08-03 at `studio-sdk` `acee519` / tag `v0.31.2`; known open SDK gaps that do NOT block this leg: Jira's comment idem-scan is unpaginated (affects C4), `IssueSnapshot.Deleted` unused, `SyncCapable` docblock stale ("no connector implements this yet" — three do)
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §2 (ingest, cursor discipline, sweep), §4 (shadow/echo), §5 (status mapping) — embedded above
