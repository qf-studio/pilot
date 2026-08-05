# feat(statusmap): C5 — status-map auto-seed (static GitHub + observation fill) + CRUD API

## Context

S4 board track, leg C5. C1–C4 + C7 are merged on main (`f1658e3`). The board is live end-to-end EXCEPT for one gap this leg closes: **`status_maps` is always empty** — no code path ever writes it.

Why that hurts, verified at HEAD:

- Outbound status ops have **zero fallback**: `internal/syncoutbound/execute.go:295` `resolveProviderState(statusMap, status)` is a linear scan over `status_maps` rows; unmapped → the op is **parked immediately** (`execute.go:240-245`, message `no status mapping for canonical status %q`). So today every `queued` drag's status op parks.
- Ingest has a safety-net fallback (`internal/syncingest/mapping.go:153` `defaultStatusForState`) but never persists anything, and finer states (Linear `backlog`/`canceled` groups, custom Jira states) stay unmapped forever.

Do NOT read or clone sibling repos — everything needed is embedded.

### The schema you are filling (migration `0008_board.up.sql:94-107`, already on main)

```sql
CREATE TABLE status_maps (
    connection_id uuid NOT NULL REFERENCES connections(id),
    canonical_status text NOT NULL CHECK (canonical_status IN ('backlog','todo','queued','in_progress','in_review','done','canceled')),
    provider_state text NOT NULL,        -- provider-specific state id/name/transition target
    is_primary boolean NOT NULL,         -- reverse-map winner when N canonical → 1 provider state
    PRIMARY KEY (connection_id, canonical_status)
);
CREATE UNIQUE INDEX status_maps_primary_uniq ON status_maps (connection_id, provider_state) WHERE is_primary;
```

Key shape fact: the map is **forward-keyed** — at most ONE provider_state per canonical status per connection. Seeding = writing ≤7 rows. The reverse (provider→canonical) direction is derived by `newStatusResolver` (`syncingest/mapping.go:110`): an `is_primary` row wins; a provider_state with exactly one row uses it; >1 rows with no primary = ambiguous, unresolved.

Existing store surface (`internal/board/ops.go`): `StatusMapRow{ConnectionID uuid.UUID; CanonicalStatus Status; ProviderState string; IsPrimary bool}` (`:305`) · `PutStatusMapRow` (`:327`, upsert `ON CONFLICT ... DO UPDATE`, maps constraint `status_maps_primary_uniq` 23505 → `ErrDuplicatePrimary`) · `ListStatusMap` (`:350`) · `DeleteStatusMapRow` (`:377`, zero rows → `ErrNotFound`). There is no seed-if-absent variant and no bulk/transactional replace — this leg adds both.

### Why auto-seed cannot query provider APIs

Verified against `studio-sdk` v0.31.2 (`acee519`): the SDK has **no exported taxonomy enumerator**. Linear's workflow-states query lives inside unexported `SyncAdapter.resolveWorkflowStateID`; GitHub's project-field options inside unexported `ProjectBoardSync.resolveFieldAndOptions`; Jira's `GetTransitions` is per-issue and position-dependent. Do NOT add an SDK dependency for seeding. Instead:

- **GitHub is static** — its issue taxonomy is exactly `open`/`closed`, forever. Seed it from a constant table.
- **Linear/Jira are observed** — every ingested `IssueSnapshot` carries the REAL provider state name (`State`) plus its category (`StateGroup`: Linear workflow-state `type`, Jira status category via `stateGroup()`). First-observed state per category fills the map with correct, correctly-cased names (Linear's `TransitionState` name matching is case-sensitive; Jira's is case-insensitive). This samples only occupied states — the CRUD API is the customer's refinement path, and the later wizard UI (separate ui-repo leg) sits on that API.

### Seed rules (design doc §5, adapted to the forward-keyed schema)

Static GitHub seed (all 7 rows at once):

| canonical | provider_state | is_primary |
|---|---|---|
| backlog, queued, in_progress, in_review | `open` | false |
| todo | `open` | **true** |
| done | `closed` | **true** |
| canceled | `closed` | false |

Observation fill for Linear/Jira — when a snapshot's `StateGroup` normalizes to a bucket (same normalization as `defaultStatusForState`: `todo|to do|unstarted|new` → todo-bucket · `in progress|in_progress|started|indeterminate` → progress-bucket · `done|completed` → done-bucket, plus Linear's `backlog` → backlog-bucket and `canceled|cancelled` → canceled-bucket, which the existing fallback deliberately ignores but seeding should use), write `snapshot.State` into the canonical rows of that bucket **only where absent**:

| bucket | fills canonical rows | primary row |
|---|---|---|
| backlog | backlog | backlog |
| todo | todo, queued | todo |
| progress | in_progress, in_review | in_progress |
| done | done | done |
| canceled | canceled | canceled |

First-observed wins per row; a later state in the same bucket seeds nothing. **Never overwrite an existing row** — customer edits are sacred.

## Acceptance

1. **Store: `SeedStatusMapRow`** (`internal/board/ops.go`): like `PutStatusMapRow` but `ON CONFLICT (connection_id, canonical_status) DO NOTHING`; returns `(inserted bool, err)`. If the insert with `IsPrimary=true` fails on `status_maps_primary_uniq` (another canonical already holds primary for that provider_state), retry once as non-primary — seeding must never error a poll pass over primary contention. Table-test both paths (DB-gated per `newTestStore`, `store_test.go:22`).

2. **Store: `ReplaceStatusMap`** (`internal/board/ops.go`): `(ctx, connectionID uuid.UUID, rows []StatusMapRow) error` — single transaction: `DELETE` all rows for the connection, insert the given rows, validate every `CanonicalStatus.Valid()` and non-empty `ProviderState` before touching the DB (`ErrInvalidVocab`). Duplicate primary within the payload → `ErrDuplicatePrimary` (surfaced from the index, tx rolled back).

3. **Package `internal/statusmap`** (flat, per `internal/syncengine` precedent) — pure seed rules, no I/O: `GitHubRows(connectionID uuid.UUID) []board.StatusMapRow` (the static table above) and `ObservedRows(connectionID uuid.UUID, provider board.Provider, state, stateGroup string) []board.StatusMapRow` (the bucket table; returns nil for GitHub — static covers it — and for unrecognized groups). Exhaustively table-test the rules, including the canceled/cancelled spelling and that GitHub observation returns nil.

4. **Ingest hook** (`internal/syncingest/worker.go`, `deltaPoll`): after `ListStatusMap` (`worker.go:252`), if the connection's map is missing any canonical row, seed: GitHub → `SeedStatusMapRow` each of `statusmap.GitHubRows`; Linear/Jira → for each deduped snapshot in the pass, `SeedStatusMapRow` each of `statusmap.ObservedRows(...)`. Then **re-list and rebuild the resolver** so the pass that seeded also benefits. When the map already has all 7 canonical rows, the hook must do zero extra store calls (assert this in a test — no per-tick write amplification). Seeding failures log and never abort the pass. Extend the package's `boardStore` interface (`worker.go:50`) with `SeedStatusMapRow` only — keep it narrow.

5. **CRUD API** (`internal/boardapi`, following `routes.go` house shape exactly — `authenticate` wrapper, `bff.CSRFGuard` on mutations, org scoping via `resolveBoard`, 404-never-403, camelCase DTOs, `{"error":...}` envelope):
   - `GET /api/v1/board/statusmaps/{tracker}` → `{"rows":[{canonicalStatus, providerState, isPrimary}]}` (non-nil slice → `[]` not `null`). `{tracker}` ∈ github|linear|jira; resolved to the org's connection via a new narrow `OrgStore` method `GetConnection(ctx, orgID uuid.UUID, tracker orgs.Tracker) (orgs.Connection, error)` (exists on `*orgs.Store` at `store.go:321`); no connection → 404.
   - `PUT /api/v1/board/statusmaps/{tracker}` — body `{"rows":[...]}` → `ReplaceStatusMap`. Invalid vocab/empty providerState → 400; duplicate primary → 409 `{"error":"duplicate primary for provider state"}`.
   - `DELETE /api/v1/board/statusmaps/{tracker}/{canonicalStatus}` → `DeleteStatusMapRow`; 404 on unknown row; 204 on success.
   - `POST /api/v1/board/statusmaps/{tracker}/seed` — re-runs the static/observed seed WITHOUT deleting existing rows (GitHub: full static seed; Linear/Jira: 409 `{"error":"seeding for this tracker is observation-based","code":"seed_observed_only"}` — their rows arrive via ingest).

6. **No behavior change to unmapped-state posture**: ingest's never-move-a-card rule (`worker.go:454-466`) and outbound's park-on-unmapped stay exactly as they are. C5 makes maps exist; it does not weaken the guards.

7. **Tests**: handler tests with the `fakeBoardStore`/`fakeOrgStore`/`newTestMux` pattern (`handlers_test.go:319`) covering: GET envelope + unknown tracker 404 · PUT replace + validation 400 + duplicate-primary 409 · DELETE 204/404 · seed endpoint github-vs-observed 409 · cross-org 404 · CSRF 403 · unauth 401. Ingest-hook tests with the `fakeBoardStore` + `staticConnector` pattern (`syncingest/worker_test.go`): github connection auto-seeds 7 rows on first pass · linear snapshot with `StateGroup:"started"` fills in_progress+in_review and the resolver maps it within the same pass · existing row never overwritten · complete map ⇒ zero seed calls · primary contention degrades to non-primary without failing the pass.

8. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `internal/statusmap/rules.go` + `rules_test.go` (pure), `internal/board/ops.go` (+store tests), `internal/syncingest/worker.go` (+tests), `internal/boardapi/statusmap.go` + tests (+ route registration in `routes.go`, DTOs in `dto.go`), no `main.go`/`config.go` change (no new knobs; the hook rides the existing ingest worker).

Sequencing: rules package (pure, tested) → store methods → ingest hook → CRUD routes.

**Verify-before-relying**: (a) exact normalization set `defaultStatusForState` uses (`mapping.go:164-185`) — keep `statusmap`'s bucket normalization a superset of it, and leave the existing fallback untouched; (b) `orgs.Store.GetConnection` signature at `internal/orgs/store.go:321` before extending the boardapi `OrgStore` interface.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** wizard UI (separate `pilot-console-ui` leg on this API) · SDK taxonomy-enumeration exports (deferred SDK follow-up) · rate budgeting (C6) · dispatch verb (C8) · metrics (C9) · any change to outbound execution or `resolveProviderState` · webhook anything.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-05 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/95 (labels: pilot, no-decompose; head of the wave-3 chain #95→#96→#97→#98)
- Depends on (merged): console#87 (C1) · #88 (C2) · #92 (C7) · #93 (C3) · #94 (C4)
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §5 (status mapping, seed rules) — embedded above
- SDK facts verified 2026-08-05 at `studio-sdk` `acee519` / tag `v0.31.2`; console facts at `pilot-console` `f1658e3`
