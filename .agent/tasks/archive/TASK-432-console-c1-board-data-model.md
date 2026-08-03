# feat(board): C1 — board data model: migration 0008 (cards/links/shadows/ops/cursors/status-maps/conflicts) + internal/board store

**Status**: ✅ Delivered — console#85 → [PR#87](https://github.com/qf-studio/pilot-console/pull/87) merged 2026-08-01 07:15Z (operator approval 08-01). Archived 2026-08-03.

## Context

pilot-console is growing the S4 **mixed-tracker kanban sync engine**: one customer board materializing cards from N tracker connections (GitHub / Linear / Jira in v1), with field-level bidirectional sync. This issue is **C1, the data layer** — one migration plus a typed store. The reconciler (C2), ingest/outbound workers (C3/C4), status-map seeding (C5), and board API (C7) are separate follow-up issues that build on this store — do NOT implement any of them here (scope fence below).

Design facts you need (the design doc lives in a sibling repo you must NOT read or clone; everything required is embedded here):

- **Pairwise sync, not N-way.** A card has exactly **zero or one** external link (`card_links` is 1:0..1 per card). A card born in Jira lives in Jira; a board-only card has no link. This is load-bearing: it removes vector clocks, cross-provider ID fan-out, and the N² conflict matrix.
- **The shadow is the heart of the engine.** `link_shadows` stores the last-synced field snapshot per link; it is the base of C2's 3-way diff and the echo-suppression mechanism. Writers can NEVER be distinguished by identity (the board's sync worker, the customer's Pilot instance, and the human may all authenticate with the same PAT) — only by value fingerprint against the shadow. The shadow is mandatory, not an optimization.
- **Canonical status vocabulary (fixed, small)**: `backlog | todo | queued | in_progress | in_review | done | canceled`.
- **Priority vocabulary**: `urgent | high | medium | low | none`.
- `card_ops` is the outbound write-back op log: **one op per field** (partial failure is per-field; a newer op on a field supersedes older pending ones), idempotency via `idem_key`, states `pending | inflight | applied | superseded | parked`.
- `status_maps` translates canonical↔provider states per connection. `is_primary` marks the reverse-map winner when N canonical statuses map onto 1 provider state (e.g. GitHub `open` covers backlog/todo/queued/in_progress/in_review) — at most one primary row per `(connection, provider_state)` is a real integrity constraint (partial unique index, same technique as `instances_org_active_uniq` in 0003).
- `sync_conflicts` is the append-only conflict journal (observability; C2 populates it).

Existing schema this builds on: `organizations` (0001/0006) and `connections` (0006 — `id uuid PRIMARY KEY`, `UNIQUE (org_id, tracker)`, tracker CHECK deliberately closed to github/linear/jira). **Next free migration version is 0008.** No board/card/sync code or table names exist anywhere at HEAD — the namespace is clean.

This task must NOT be decomposed — implement as a single PR. <!-- pilot:no-decompose -->

## Acceptance

1. **Migration `0008_board.up.sql` / `0008_board.down.sql`** with why-comments in the 0004–0007 style. Up creates exactly:

```sql
CREATE TABLE boards (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id)          -- one board per org in v1; multi-board drops this later
);

CREATE TABLE cards (
    id uuid PRIMARY KEY,
    board_id uuid NOT NULL REFERENCES boards(id),
    title text NOT NULL,
    body text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('backlog','todo','queued','in_progress','in_review','done','canceled')),
    priority text NOT NULL DEFAULT 'none' CHECK (priority IN ('urgent','high','medium','low','none')),
    labels text[] NOT NULL DEFAULT '{}',
    assignee_display text,   -- READ-ONLY mirror in v1 (no cross-tracker identity map)
    origin text NOT NULL CHECK (origin IN ('board','github','linear','jira')),
    version bigint NOT NULL DEFAULT 1,   -- bumped on every accepted change; optimistic check on user edits
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);
CREATE INDEX cards_board_idx ON cards (board_id, created_at);

CREATE TABLE card_links (
    card_id uuid PRIMARY KEY REFERENCES cards(id),   -- PK = the 1:0..1 pairwise invariant
    connection_id uuid NOT NULL REFERENCES connections(id),
    provider text NOT NULL CHECK (provider IN ('github','linear','jira')),
    native_issue_id text NOT NULL,       -- UUID/GID/nodeID — the stable key
    sequence_id text NOT NULL,           -- provider-prefixed human key: PROJ-42, #42
    native_url text NOT NULL,
    provider_updated_at timestamptz,     -- last remote updated_at ingested
    sync_state text NOT NULL DEFAULT 'ok' CHECK (sync_state IN ('ok','orphaned','parked')),
    UNIQUE (connection_id, native_issue_id)
);

CREATE TABLE link_shadows (
    card_id uuid PRIMARY KEY REFERENCES card_links(card_id),
    snapshot jsonb NOT NULL,             -- canonical-field snapshot incl. provider-native state
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE card_ops (
    id uuid PRIMARY KEY,
    card_id uuid NOT NULL REFERENCES cards(id),
    connection_id uuid NOT NULL REFERENCES connections(id),
    field text NOT NULL CHECK (field IN ('status','labels','priority','title','body','comment')),
    payload jsonb NOT NULL,              -- {old, new} or {comment_body}
    idem_key text NOT NULL UNIQUE,       -- sha256(card_id, field, new_value_hash, card.version)
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','inflight','applied','superseded','parked')),
    attempts int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX card_ops_card_idx ON card_ops (card_id, created_at);
CREATE INDEX card_ops_worker_idx ON card_ops (connection_id, state, next_attempt_at);

CREATE TABLE sync_cursors (
    connection_id uuid NOT NULL REFERENCES connections(id),
    cursor_kind text NOT NULL CHECK (cursor_kind IN ('delta_watermark','full_sweep_at')),
    value text NOT NULL,
    PRIMARY KEY (connection_id, cursor_kind)
);

CREATE TABLE status_maps (
    connection_id uuid NOT NULL REFERENCES connections(id),
    canonical_status text NOT NULL CHECK (canonical_status IN ('backlog','todo','queued','in_progress','in_review','done','canceled')),
    provider_state text NOT NULL,        -- provider-specific state id/name/transition target
    is_primary boolean NOT NULL,         -- reverse-map winner when N canonical → 1 provider state
    PRIMARY KEY (connection_id, canonical_status)
);
CREATE UNIQUE INDEX status_maps_primary_uniq ON status_maps (connection_id, provider_state) WHERE is_primary;

CREATE TABLE sync_conflicts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,   -- journal idiom per instance_events
    card_id uuid NOT NULL REFERENCES cards(id),
    field text NOT NULL,
    board_value jsonb,
    remote_value jsonb,
    winner text NOT NULL CHECK (winner IN ('board','remote')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sync_conflicts_card_idx ON sync_conflicts (card_id, created_at);
```

   Down drops in reverse dependency order (children first). The existing `TestMigrateUpDownUp` round-trip must pass, which enforces the down file.

2. **`internal/board/store.go`** (split into `store.go` / `cards.go` / `ops.go` if it grows past ~500 lines): `Open(dsn)/New(db)/Close` triple, `row` interface + `scanX` helpers + `xColumns` consts, `$1` placeholders — mirror `internal/orgs/store.go` and `internal/fleet/store.go` exactly. Vocab: typed string types `Status`, `Priority`, `Origin`, `SyncState`, `OpField`, `OpState` with const blocks, `validX` maps, `Valid()` methods, doc comments naming migration 0008. Sentinel errors with `board:` prefix; `sql.ErrNoRows` → `ErrNotFound` everywhere; unique-violation mapping via `pgconn.PgError` code `23505` + named constraint consts (the `instances_org_active_uniq` idiom).

3. **Store methods** (validate vocab before SQL; ids client-side `uuid.New()`):
   - `GetOrCreateDefaultBoard(ctx, orgID) (Board, error)` — idempotent (`INSERT ... ON CONFLICT (org_id) DO NOTHING` + select), name `"Board"`.
   - Cards: `CreateCard(ctx, CreateCardParams) (Card, error)` · `GetCard(ctx, id)` · `ListCards(ctx, boardID)` (ordered `created_at, id`; includes archived — callers filter) · `UpdateCardFields(ctx, id, expectedVersion int64, patch CardPatch) (Card, error)` where `CardPatch` uses pointer fields (`Title *string`, `Body *string`, `Status *Status`, `Priority *Priority`, `Labels *[]string`); one UPDATE with `WHERE id=$1 AND version=$2`, sets only non-nil fields, `version = version + 1`, `updated_at = now()`, RETURNING; zero rows → probe existence to distinguish **`ErrVersionConflict`** from `ErrNotFound` · `ArchiveCard(ctx, id)`.
   - Links: `LinkCard(ctx, LinkCardParams) (CardLink, error)` (duplicate `(connection_id, native_issue_id)` → `ErrDuplicateLink`; duplicate card_id PK → `ErrAlreadyLinked`) · `GetLink(ctx, cardID)` · `FindLinkByNative(ctx, connectionID, nativeIssueID)` · `SetLinkSyncState(ctx, cardID, SyncState)` · `SetLinkProviderUpdatedAt(ctx, cardID, time.Time)`.
   - Shadows: `GetShadow(ctx, cardID) (Shadow, error)` · `PutShadow(ctx, cardID, snapshot json.RawMessage)` (upsert `ON CONFLICT (card_id) DO UPDATE`).
   - Ops: `EnqueueOp(ctx, EnqueueOpParams) (CardOp, error)` — in one tx: mark this card's **pending** ops on the same field `superseded`, then insert (idem_key unique violation → `ErrDuplicateOp`) · `GetOp(ctx, id)` · `ListCardOps(ctx, cardID, limit)` · `ListDueOps(ctx, connectionID, now time.Time, limit)` (`state='pending' AND next_attempt_at <= now`, ordered `created_at`) · `MarkOpState(ctx, id, OpState, lastError string)` · `RescheduleOp(ctx, id, attempts int, nextAttemptAt time.Time, lastError string)`. Plain CRUD only — leasing/claiming (`FOR UPDATE SKIP LOCKED`) is C4's problem, do not build it.
   - Cursors: `GetCursor(ctx, connectionID, kind) (string, error)` (`ErrNotFound` when unset) · `PutCursor(ctx, connectionID, kind, value)` (upsert).
   - Status maps: `PutStatusMapRow(ctx, StatusMapRow)` (upsert on PK; partial-unique violation on `status_maps_primary_uniq` → `ErrDuplicatePrimary`) · `ListStatusMap(ctx, connectionID)` · `DeleteStatusMapRow(ctx, connectionID, canonicalStatus)`.
   - Conflicts: `AppendConflict(ctx, AppendConflictParams) (SyncConflict, error)` · `ListConflictsByCard(ctx, cardID, limit)` · `ListConflictsByBoard(ctx, boardID, limit)` (join through cards) — limit 0 → default 50, negative → `ErrInvalidLimit` (the `ListEvents` idiom).

4. **Verify-before-relying**: confirm `[]string ↔ text[]` round-trips through the pgx/v5 stdlib driver under `database/sql` (labels column). If it does not, switch `labels` to `jsonb NOT NULL DEFAULT '[]'` in THIS migration and document why — decide in-PR, cover with a store test either way.

5. **Tests** (`internal/board/store_test.go`, DB-gated `newTestStore` mirroring `internal/fleet/store_test.go` — skip locally without `DATABASE_URL`, hard-fail in CI; fixtures insert `organizations` + `connections` rows via raw SQL with fresh `uuid.New()` ids so suites share the CI database): default-board idempotency (two calls, one row) · card create/get/list ordering · `UpdateCardFields` happy path bumps version exactly once, stale version → `ErrVersionConflict`, missing card → `ErrNotFound`, nil-patch fields untouched · labels round-trip · link uniqueness both ways (`ErrDuplicateLink`, `ErrAlreadyLinked`) · shadow upsert round-trip · `EnqueueOp` supersedes only same-card same-field **pending** ops (not inflight, not other fields) · `ListDueOps` ordering + `next_attempt_at` filter + limit · cursor get/put/overwrite · `ErrDuplicatePrimary` on second primary row for one provider_state · conflict append + list limit defaulting.

6. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

File plan: `internal/db/migrations/0008_board.{up,down}.sql` · `internal/board/store.go` (+ optional `cards.go`/`ops.go` split) · `internal/board/store_test.go`. No config changes, no main.go changes (a store is inert until C2+ wire it), no HTTP.

Sequencing: migration + `TestMigrateUpDownUp` green first, then vocab types + scan helpers, then methods in the order listed, tests alongside each.

**Scope fence (do NOT build here):** reconcile/diff logic (C2 — next issue, already specced) · pollers, ingest, or any provider/SDK client usage (C3) · op execution, leasing, `FOR UPDATE SKIP LOCKED` (C4) · status-map auto-seeding from provider taxonomies (C5) · HTTP routes (C7) · a `card_comments` table (comment mirroring is deferred; `card_ops.field='comment'` exists for the outbound leg later) · any `PILOT_CONSOLE_*` env vars.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-07-31 · https://github.com/qf-studio/pilot-console/issues/85 (labels: pilot, no-decompose)
- Prior issues in this repo (conventions precedent): #11 (B5 fleet store — store idioms), #24/PR#25 (reconciler — pure-core precedent), #82 (credentials listing), #84 (secrets postgres driver — env-gated driver e2e).
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §1 (data model), §4 (why the shadow exists) — embedded above; do not read the sibling repo.
- In-repo conventions to mirror: `internal/orgs/store.go`, `internal/fleet/store.go`, `internal/db/migrate_test.go`.
