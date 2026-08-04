# feat(board): C7 — board REST API (cards, drag=status op, comments, conflict/activity feed, parked-op retry)

## Context

S4 board track, leg C7. C1 (`internal/board` store + migration 0008) and C2 (`internal/syncengine` reconciler) are **merged on main** (`755899d`). `pilot-console-ui` has a fully-built kanban board (columns, drag, drawer, activity feed, conflict snap-back) running against a mock adapter, with its `httpAdapter` board methods deliberately **501-stubbed** waiting for exactly this API. This issue connects the two halves.

Everything you need is embedded below — do NOT read or clone sibling repos.

**What already exists on main (use it, do not rebuild it):**

`internal/board` — plain Go structs, **no json tags anywhere** (wire marshalling is this issue's job). Store methods you will call:

```go
func (s *Store) GetOrCreateDefaultBoard(ctx context.Context, orgID uuid.UUID) (Board, error)
func (s *Store) CreateCard(ctx context.Context, p CreateCardParams) (Card, error)
func (s *Store) GetCard(ctx context.Context, id uuid.UUID) (Card, error)
func (s *Store) ListCards(ctx context.Context, boardID uuid.UUID) ([]Card, error)
func (s *Store) UpdateCardFields(ctx context.Context, id uuid.UUID, expectedVersion int64, patch CardPatch) (Card, error)
func (s *Store) ArchiveCard(ctx context.Context, id uuid.UUID) error
func (s *Store) GetLink(ctx context.Context, cardID uuid.UUID) (CardLink, error)
func (s *Store) EnqueueOp(ctx context.Context, p EnqueueOpParams) (CardOp, error)
func (s *Store) ListCardOps(ctx context.Context, cardID uuid.UUID, limit int) ([]CardOp, error)
func (s *Store) MarkOpState(ctx context.Context, id uuid.UUID, state OpState, lastError string) error
func (s *Store) RescheduleOp(ctx context.Context, id uuid.UUID, attempts int, nextAttemptAt time.Time, lastError string) error
func (s *Store) ListConflictsByCard(ctx context.Context, cardID uuid.UUID, limit int) ([]SyncConflict, error)
func (s *Store) ListConflictsByBoard(ctx context.Context, boardID uuid.UUID, limit int) ([]SyncConflict, error)
```

Types: `Card{ID, BoardID uuid.UUID; Title, Body string; Status Status; Priority Priority; Labels []string; AssigneeDisplay string; Origin Origin; Version int64; CreatedAt, UpdatedAt time.Time; ArchivedAt *time.Time}` · `CardLink{CardID, ConnectionID uuid.UUID; Provider Provider; NativeIssueID, SequenceID, NativeURL string; ProviderUpdatedAt *time.Time; SyncState SyncState}` · `CardOp{ID, CardID, ConnectionID uuid.UUID; Field OpField; Payload json.RawMessage; IdemKey string; State OpState; Attempts int; NextAttemptAt time.Time; LastError string; CreatedAt time.Time}` · `SyncConflict{ID int64; CardID uuid.UUID; Field string; BoardValue, RemoteValue json.RawMessage; Winner Winner; CreatedAt time.Time}` · `CardPatch{Title, Body *string; Status *Status; Priority *Priority; Labels *[]string}` (nil = untouched) · `EnqueueOpParams{CardID, ConnectionID uuid.UUID; Field OpField; Payload json.RawMessage; IdemKey string}`.

Vocab (all `string`-based with `Valid() bool`): `Status` = backlog|todo|queued|in_progress|in_review|done|canceled · `Priority` = urgent|high|medium|low|none · `Origin` = board|github|linear|jira · `Provider` = github|linear|jira · `SyncState` = ok|orphaned|parked · `OpField` = status|labels|priority|title|body|comment · `OpState` = pending|inflight|applied|superseded|parked.

Errors: `ErrNotFound`, `ErrInvalidVocab`, `ErrInvalidLimit`, `ErrVersionConflict`, `ErrDuplicateLink`, `ErrAlreadyLinked`, `ErrDuplicateOp`, `ErrDuplicatePrimary`.

Load-bearing behaviors: `ListCards` **returns archived cards too** (caller filters), ordered `created_at, id`. `List*` with `limit==0` → default 50, `limit<0` → `ErrInvalidLimit`. `UpdateCardFields` bumps `version` by exactly 1; zero rows → `ErrNotFound` vs `ErrVersionConflict` (already distinguished for you). `EnqueueOp` supersedes existing **pending** ops on the same (card, field) in-tx; inflight untouched; duplicate `idem_key` → `ErrDuplicateOp`. **Cards carry no `org_id`** — scoping is cards → `boards.board_id` → `boards.org_id`.

**No Go helper computes `card_ops.idem_key`** — the schema documents it as `sha256(card_id, field, new_value_hash, card.version)` and `EnqueueOpParams.IdemKey` is caller-supplied. This issue writes that helper.

## Acceptance

1. **Package `internal/boardapi`** (flat, matching C2's `internal/syncengine` precedent — this repo keeps `internal/` one level deep) with `Register(mux *http.ServeMux, deps Deps)` following the house shape exactly (see `internal/instances/handlers.go`): `Deps{Board BoardStore; Orgs OrgStore; Authenticate func(http.Handler) http.Handler; Logger *slog.Logger}` where `BoardStore`/`OrgStore` are **narrow interfaces declared in this package** (faked in tests), `disabled()` returns 503 when a dep is nil. Mutating routes wrapped in `bff.CSRFGuard`; all routes wrapped in `deps.Authenticate`.

2. **Org scoping, house rule**: every handler resolves `bff.PrincipalFromContext` → `orgs.GetOrgByUser(ctx, principal.Subject)` → `org.ID` → `GetOrCreateDefaultBoard`. Any card id that does not resolve to a card on the caller's board — malformed UUID, unknown, or another org's — responds **404, never 403** (a caller must not be able to distinguish "not yours" from "doesn't exist"). No org id appears in any URL or body.

3. **Routes** (Go 1.22 method-prefixed patterns, all `/api/v1/...`):
   - `GET /api/v1/board/cards` → `{"cards": [...]}` (envelope rule; pre-allocated non-nil slice so it serializes `[]` not `null`). Excludes archived cards. Each card carries its link (or `null`), `needsYou` (false in v1 — the approval mirror is a later leg; keep the field so the UI contract is stable), and parked-op presence.
   - `PATCH /api/v1/board/cards/{id}` — body `{title?, body?, priority?, labels?, version}` → optimistic update via `UpdateCardFields`; on success enqueue one `card_ops` row **per changed field** when the card is linked; returns the updated card.
   - `POST /api/v1/board/cards/{id}/status` — body `{status, version}`; the drag verb. Updates the card and enqueues a `status` op when linked. **Queued is special**: moving a LINKED card to `queued` additionally enqueues a `labels` op that ADDS the `pilot` trigger label — never removes or rewrites labels (see AC 6). Moving an UNLINKED card to `queued` → **412** `{"error":"card is not linked to a tracker","code":"card_unlinked"}` and no state change.
   - `POST /api/v1/board/cards` — create a board-only card (`origin=board`, no link).
   - `POST /api/v1/board/cards/{id}/comment` — body `{body}`; enqueues a `comment` op (linked cards only; unlinked → 412 as above).
   - `GET /api/v1/board/cards/{id}/ops` → `{"ops": [...]}` (drawer's parked-op list).
   - `POST /api/v1/board/ops/{opId}/retry` — parked → pending, `next_attempt_at = now`, attempts preserved; only ops belonging to the caller's board; non-parked op → 409.
   - `GET /api/v1/board/activity` → `{"activity": [...]}` — conflict journal via `ListConflictsByBoard` mapped into the UI's activity shape (`kind: "conflict"`), newest first, `limit` query param (default 50, cap 200).
   - `DELETE /api/v1/board/cards/{id}` → `ArchiveCard` (idempotent), 204.

4. **Version conflicts are the product's trust surface**: `ErrVersionConflict` → **409** with the current server-side card in the body so the UI can snap back: `{"error":"version conflict","card":{...}}`. This is the contract behind the UI's "Updated in ⟨tracker⟩ just now — your change was not applied" toast.

5. **idem_key helper** (exported from `internal/board`, or a clearly-named helper in this package — state which and why): `sha256(card_id, field, new_value_hash, card.version)` over a canonical, stable encoding. Same logical write must produce the same key; a different value or a bumped version must produce a different one. Table-test it directly, including labels-order stability (a set-valued field must not produce two keys for the same set in different orders).

6. **Label hygiene (hard invariant)**: outbound label ops are **additive for the `pilot` trigger label only**. Pilot's own poller un-marks a processed issue when its `pilot-*` status labels are removed, so a board write that strips or rewrites `pilot-in-progress`/`pilot-done`/`pilot-failed` re-arms dispatch and causes duplicate executions. Encode this as a guard in the op builder plus a test asserting a labels op never removes a `pilot-*` label.

7. **DTOs** use camelCase json tags (matching `internal/instances`, which the UI's existing adapters already consume — `internal/orgs` uses snake_case; deliberately follow instances here and note the divergence in a comment). Error envelope is exactly `{"error": "..."}` via the package-local `writeJSONError` copy, per house convention.

8. **main.go + config wiring**: open a `board.Store` when `DATABASE_URL != ""` (mirror `orgStore`), register the routes; when unset, log `logger.Warn("DATABASE_URL not set; board routes disabled")` and register nothing (the "not registered at all" posture used by orgs/instances). Add `registerBoard` alongside the existing registrars. **The binary must still run with no env set** (repo invariant).

9. **Tests**: table-driven, stdlib `testing`, hand-rolled fakes (no testify — it is in go.mod but used by exactly one unrelated file). Cover: list envelope + archived excluded + `[]` not `null` · cross-org card id → 404 · malformed uuid → 404 · status drag enqueues exactly one status op · unlinked→queued 412 with no mutation · linked→queued adds `pilot` label additively · version conflict → 409 carrying the current card · patch enqueues one op per changed field and none for unchanged · comment op on unlinked → 412 · retry flips parked→pending and rejects non-parked with 409 · activity limit default/cap · CSRF header missing → 403 · unauthenticated → 401 · nil dep → 503. DB-gated tests use the C1 `newTestStore` pattern (skip without `DATABASE_URL`, hard-fail in CI).

10. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Files: `internal/boardapi/handlers.go` (+ `routes.go` if it helps), `internal/boardapi/handlers_test.go`, an idem-key helper (+ test), `main.go` wiring. No migration — 0008 has everything.

Sequencing: DTOs + mappers → read routes (`GET cards`, `activity`, `ops`) → write routes (status drag, patch, comment, create, archive) → op enqueueing + idem key + label guard → retry → main.go wiring → tests throughout.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build here):** ingest/polling workers or any provider HTTP client (C3) · outbound op *execution*, leasing, compare-before-write (C4 — this issue only ENQUEUES ops; nothing drains them yet, which is expected and correct) · status-map auto-seeding (C5) · rate budgeting (C6) · the approval mirror feeding "Needs You" (later leg — ship the field as `false`) · `card_comments` read-model (comments are outbound-only here) · websockets/live updates · any studio-sdk dependency.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-03 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/89 (labels: pilot, no-decompose)
- Depends on (merged): console#87 (C1 data model), console#88 (C2 reconciler)
- Consumer waiting on this: `pilot-console-ui` board (ui#40/#41 merged) — its `httpAdapter` board methods currently throw `ApiError(501, 'board API not yet available')`; un-stubbing them is a follow-up UI issue once this lands
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §3 (per-field ops), §5 (status vocab + Queued dispatch), §6 (label hygiene) — embedded above
- House conventions mirrored: `internal/instances/handlers.go` (Register/Deps/404-not-403/envelope), `internal/bff/middleware.go` (Authenticate, CSRFGuard)
