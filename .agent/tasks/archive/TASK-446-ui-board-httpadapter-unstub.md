# feat(board): un-stub httpAdapter board methods against the merged console board API (7 of 10, wire types + 409/412 translation + polling)

## Context

S4 board track, UI lane. The console board REST API (C7) plus its C5 statusmap extension are **merged on `pilot-console` main** (`b98873e`). This repo's board screens (PR#42/#43) are fully built against `mockAdapter`; `httpAdapter` has 10 board methods that `throw new ApiError(501, 'board API not yet available')` (`src/lib/api/httpAdapter.ts:303-346`).

**Scope decision, verified against the merged console code: only 7 of the 10 stubs have backing endpoints.** The other 3 stay stubbed with updated messages naming their gating leg:

- `listCardComments` — the console has **no comment read endpoint** (comments are outbound-only ops; the `card_comments` read-model is a fenced later leg).
- `createInTracker` — that's console C8 (issue #97), in flight, not merged.
- `decideCard` — approval mirror (C14), not built.

Do NOT read or clone sibling repos — every wire shape you need is embedded verbatim below, copied from `pilot-console internal/boardapi/{dto.go,handlers.go}` at `b98873e`.

### The wire contract (verbatim from the merged console source)

DTOs are **camelCase** (deliberate divergence from orgs' snake_case; matches the instances API you already consume). All routes session-cookie-authed; mutations require the `X-Requested-With` header your `httpClient.request` already sets.

```go
type cardDTO struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Body            string       `json:"body"`
	Status          string       `json:"status"`
	Priority        string       `json:"priority"`
	Labels          []string     `json:"labels"`      // always [] not null (nonNilLabels)
	AssigneeDisplay string       `json:"assigneeDisplay"`
	Origin          string       `json:"origin"`
	Version         int64        `json:"version"`
	CreatedAt       time.Time    `json:"createdAt"`   // RFC3339
	UpdatedAt       time.Time    `json:"updatedAt"`
	ArchivedAt      *time.Time   `json:"archivedAt,omitempty"`
	Link            *cardLinkDTO `json:"link"`        // null when unlinked
	NeedsYou        bool         `json:"needsYou"`    // always false in v1 (approval mirror later)
	HasParkedOps    bool         `json:"hasParkedOps"`
}
type cardLinkDTO struct {
	Provider      string `json:"provider"`
	NativeIssueID string `json:"nativeIssueId"`
	SequenceID    string `json:"sequenceId"`
	NativeURL     string `json:"nativeUrl"`
	SyncState     string `json:"syncState"`
}
type opDTO struct {
	ID            string    `json:"id"`
	Field         string    `json:"field"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	LastError     string    `json:"lastError,omitempty"`   // ABSENT when empty
	CreatedAt     time.Time `json:"createdAt"`
}
type activityDTO struct {
	ID          int64           `json:"id"`      // NUMERIC on the wire
	Kind        string          `json:"kind"`    // always "conflict" in v1
	CardID      string          `json:"cardId"`
	Field       string          `json:"field"`
	BoardValue  json.RawMessage `json:"boardValue,omitempty"`
	RemoteValue json.RawMessage `json:"remoteValue,omitempty"`
	Winner      string          `json:"winner"`  // "board" | "remote"
	CreatedAt   time.Time       `json:"createdAt"`
}
```

Routes + response shapes (from `handlers.go`, exact):

| Adapter method | Verb + path | Success | Errors you must translate |
|---|---|---|---|
| `listCards` | `GET /api/v1/board/cards` | 200 `{"cards":[cardDTO]}` — archived excluded | — |
| `moveCard` | `POST /api/v1/board/cards/{id}/status` body `{"status","version"}` | 200 cardDTO | 409 `{"error":"version conflict","card":cardDTO}` → `BoardConflictError` · 412 `{"error":"card is not linked to a tracker","code":"card_unlinked"}` → `CardUnlinkedError` |
| `updateCard` | `PATCH /api/v1/board/cards/{id}` body `{title?,body?,priority?,labels?,version}` | 200 cardDTO | 409 as above → `BoardConflictError` |
| `addCardComment` | `POST /api/v1/board/cards/{id}/comment` body `{"body"}` | 200 `{"status":"queued"}` — **no comment object comes back** (the comment is an outbound op that converges on the tracker) | 412 as above |
| `listCardOps` | `GET /api/v1/board/cards/{id}/ops` | 200 `{"ops":[opDTO]}` | — |
| `retryCardOp` | `POST /api/v1/board/ops/{opId}/retry` | 200 `{"status":"pending"}` — **no op object comes back** | 409 (non-parked op) |
| `listBoardActivity` | `GET /api/v1/board/activity` | 200 `{"activity":[activityDTO]}` newest-first | — |

House 404 rule: malformed/unknown/cross-org card ids are all 404 — never 403.

## Acceptance

1. **Wire types + hand mapping** for every implemented method, per the fixtures-README root-cause guard (this is the 4th drift class — #19/#22/#32/#39): `WireBoardCard`, `WireCardLink`, `WireOp`, `WireActivityEntry`, plus envelopes `WireCardsEnvelope{cards}`, `WireOpsEnvelope{ops}`, `WireActivityEnvelope{activity}`. Never `request<T>` straight into a domain type. Mapping decisions the domain types force (encode + test each):
   - `cardDTO.hasParkedOps` / `archivedAt` have no domain field — `hasParkedOps` is dropped in v1 mapping (the drawer computes parked from ops; leave a comment) unless you find a 1-line use; `archivedAt` is dropped (list excludes archived).
   - `opDTO.lastError` absent → domain `lastError: null` (domain is `string | null`, non-optional).
   - `activityDTO` → `BoardActivityEntry`: `id: String(wire.id)` · `kind: 'conflict'` · `sequenceId: null` (not on the wire in v1) · `message` synthesized deterministically: `` `Conflict on ${wire.field} — ${wire.winner === 'remote' ? 'tracker version kept' : 'board version kept'}` ``.
   - `addCardComment` returns a **synthesized** `CardComment` (`id: 'local_'+crypto.randomUUID()`, `author: 'you'`, `body`, `createdAt: new Date().toISOString()`) after a successful post, documented as optimistic-local (no read model exists yet; the comment converges on the tracker).
   - `retryCardOp` re-fetches `GET .../ops` after the 200 and returns the op matching `opId` (throw `ApiError(500, ...)` if absent — should not happen).

2. **Typed error translation**, mirroring `provisionInstance`'s 412/402 idiom:
   - 409 with a `card` in the body → parse via `WireBoardCard`, map to domain, `throw new BoardConflictError(remoteCard, providerLabel)` where `providerLabel` comes from `TRACKER_LABELS[remoteCard.link?.provider]` with the mock's `'the tracker'` fallback for a null link (import from `lib/provisionRequirements.ts`; kill or reuse the mock's private duplicate, do not add a third copy).
   - 412 whose body `code === 'card_unlinked'` → `throw new CardUnlinkedError(cardId)`.
   - Everything else stays `ApiError` (the store/view generic paths already handle it).

3. **The 3 unbacked stubs stay, with honest messages**: `listCardComments` → `ApiError(501, 'comment history requires the tracker read-model (later console leg)')` · `createInTracker` → `ApiError(501, 'dispatch API not yet available (console C8, in flight)')` · `decideCard` → `ApiError(501, 'approvals API not yet available (console C14)')`.

4. **Drawer resilience** (`BoardCardDrawer.vue` calls `getAdapter()` directly for comments/ops with NO catch — a 501/network error is an unhandled rejection today): wrap `loadComments` and `loadOps` failures into visible inline states — comments panel shows a muted "Comment history isn't available yet — comments you post still reach the tracker." on 501 (and a generic error line on other failures) while the post-comment form stays functional; ops panel shows an error line on failure. No unhandled rejections from the drawer under `VITE_API_MODE=http`.

5. **Board polling**: `BoardView.vue` refreshes `board.refreshCards()` + `board.refreshActivity()` every 30s while mounted, following the `InstancesView.pollUntilReady` idiom exactly (`setInterval` stored in a ref, cleared in `onUnmounted`; also clear on auth loss). Pause the interval while the create-in-tracker modal or an in-flight drag would race — simplest honest rule: skip a tick when a `moveCard`/`updateCard` promise is in flight (track a counter in the store or view). No websockets, no visibility API cleverness.

6. **Fixtures**: add `src/lib/api/__tests__/fixtures/board-cards.json`, `board-cards-empty.json`, `board-ops.json`, `board-activity.json`, `board-conflict-409.json`, `board-unlinked-412.json` — **derived field-for-field from the DTO definitions embedded above** (which were copied verbatim from the merged console source at `b98873e`). Note in the fixtures README that these are source-derived, not wire-captured, and that the operator's real-stack verify (below) upgrades them: capturing live payloads and diffing against these fixtures is the post-merge verification step.

7. **Tests** (existing patterns: `vi.stubGlobal('fetch')` + `jsonResponse` + fixture-driven for the adapter; `vi.spyOn(mockAdapter, ...)` untouched elsewhere): per implemented method — URL/verb/body/`X-Requested-With`/`credentials: 'include'` asserted · envelope unwrap + `[]`-not-null labels · `lastError` absent → null · activity id/message mapping · 409 → `BoardConflictError` carrying the parsed remote card and correct provider label (+ null-link fallback) · 412 `card_unlinked` → `CardUnlinkedError` · comment returns synthesized local comment on `{"status":"queued"}` · retry re-fetches ops and returns the matching op · the 3 remaining stubs still throw 501 with the new messages. Component test: drawer shows the comments-unavailable state on 501 and still posts. View test: polling interval registered on mount, cleared on unmount (use `vi.useFakeTimers()`).

8. `make build`, `make test`, `make lint` green (vue-tsc strict — no `any`). Conventional-commit PR title.

## Implementation

Files: `src/lib/api/httpAdapter.ts` (+`Wire*` types), `src/lib/api/__tests__/httpAdapter.spec.ts`, `src/lib/api/__tests__/fixtures/board-*.json` (+README note), `src/views/BoardCardDrawer.vue`, `src/views/BoardView.vue` (+ its test), possibly `src/lib/provisionRequirements.ts` (export reuse only).

Sequencing: Wire types + fixtures → read methods (cards/ops/activity) → mutation methods + 409/412 translation → comment/retry special cases → drawer resilience → polling.

**Post-merge operator step (goes in the PR description, not your job to run):** real-stack verify per the fixtures README — `make local-up` in `pilot-console`, seed a connection, run `VITE_API_MODE=http bun run dev`, capture live wire payloads for the six fixtures and diff; any mismatch is a P1 follow-up.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** statusmap editor UI (next chained issue) · `createInTracker`/`decideCard`/`listCardComments` real implementations (gated on console C8/C14/comments-read-model) · websockets or live logs · optimistic-update changes in `stores/board.ts` (the store contract is correct as-is; only the adapter and the two views change) · any new design tokens.

## Refs

- **Status**: ✅ **MERGED 2026-08-05 ~11:4xZ** — PR https://github.com/qf-studio/pilot-console-ui/pull/46 (CI green, Navigator-reviewed: all 7 backed methods + Wire types, 409/412 translation with null-link fallback, honest 501s for the 3 unbacked stubs, drawer resilience, 30s polling with in-flight skip + auth-loss stop). Chain: ui#45 unblocked. **Operator follow-up open**: real-stack fixture verify per fixtures README.
- Console wire facts verified 2026-08-05 at `pilot-console` `b98873e` (C7 merged PR#92 + C5 merged PR#99); UI facts at `pilot-console-ui` `6c04455`
- The stub-site comment at `httpAdapter.ts:303-307` pre-specifies this exact approach (envelope + `WireBoardCard` + hand translation) — follow it
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §5 (status vocab), §6 (dispatch requires a link — why createInTracker waits for C8)
