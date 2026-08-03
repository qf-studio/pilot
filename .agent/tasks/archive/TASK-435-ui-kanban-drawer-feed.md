# feat(board): card drawer, activity feed, parked-op retry, create-in-tracker, approve/reject (mock-first)

**Status**: ✅ Delivered — ui#41 → [PR#43](https://github.com/qf-studio/pilot-console-ui/pull/43) merged 2026-08-01 14:24Z (auto after #40 unblock). ⚠️ Mock-first: real-stack verify per SOP applies when C7 board API wires httpAdapter. Archived 2026-08-03.

## Context

Blocked by: #40

Second leg of the **S4 kanban board** (mock-first, same rules as the board-v1 issue: no sibling-repo reads, no new runtime deps, httpAdapter board methods stay 501-stubbed until the console board API lands). Board v1 shipped columns/drag/conflict snap-back. This issue completes the interaction surface: the card detail drawer, the board-level activity feed, recovery UX for stuck sync ops, and the two remaining write verbs — **create-in-tracker → dispatch** and **approve/reject**.

Product model additions (embedded; fixed by the platform design):

- **Editing is optimistic, per-field, versioned.** A card edit (title/body/priority/labels) applies locally and enqueues a per-field write-back op platform-side. Version conflicts behave exactly like moves: remote wins, snap back, toast (same verbatim copy and `BoardConflictError` from v1).
- **Comments are not card state**: read-only inbound mirror + board-authored append (no edit, no delete). Rendered oldest-first.
- **Parked ops**: an outbound write-back that exhausted retries parks; the card badges `parked` (v1 shipped the badge) and each parked op is user-retryable from the drawer.
- **Activity feed** (board-level): conflict entries ("Jira overwrote status on PROJ-42"), parked entries, dispatch entries. Observability, not navigation — newest first, plain list.
- **Create-in-tracker (dispatch requires a link)**: dragging an UNLINKED card to Queued was blocked in v1. Now it opens a modal — "Create this issue in ⟨tracker⟩ first?" — offering the org's connected trackers; confirming creates the native issue, links the card (provider, sequenceId, nativeUrl), THEN applies the Queued move + `pilot` label side effect in one flow.
- **Approve/reject**: needs-you cards carry a pending decision (e.g. a plan or PR awaiting the human). The drawer surfaces Approve / Reject buttons; deciding clears `needsYou` (approve keeps the card in place; reject moves it out of the needs-you lane and journals an activity entry). These verbs never write tracker-side state.

This task must NOT be decomposed — implement as a single PR. <!-- pilot:no-decompose -->

## Acceptance

1. **Types** (`src/lib/api/types.ts`): `CardComment { id, author, body, createdAt }` · `CardOp { id, field, state: 'pending' | 'inflight' | 'applied' | 'superseded' | 'parked', lastError: string | null, createdAt }` · `BoardActivityEntry { id, kind: 'conflict' | 'parked' | 'dispatch' | 'decision', cardId, sequenceId: string | null, message, createdAt }` · `BoardCardPatch { title?, body?, priority?, labels? }`. `ConsoleAdapter` gains: `updateCard(cardId, patch: BoardCardPatch, version): Promise<BoardCard>` · `listCardComments(cardId)` · `addCardComment(cardId, body)` · `listCardOps(cardId)` · `retryCardOp(cardId, opId): Promise<CardOp>` · `listBoardActivity(): Promise<BoardActivityEntry[]>` · `createInTracker(cardId, provider): Promise<BoardCard>` · `decideCard(cardId, decision: 'approve' | 'reject'): Promise<BoardCard>`. httpAdapter: all 501-stubbed as in v1.

2. **mockAdapter**: comments seeded on ≥3 cards (mixed authors incl. `pilot`); `updateCard` version-checks (stale → `BoardConflictError`; the `conflict-demo` card also conflicts on its first `updateCard`); a parked card seeded with ≥1 parked op whose `lastError` is realistic (`Jira transition 'Code Review' not available`); `retryCardOp` flips parked→pending, then applied after ~1.5s (`setTimeout` mutation idiom) and clears the card's `parked` badge when no parked ops remain; `listBoardActivity` derives entries from seeded conflicts/parks + appends on new conflicts, dispatches, and decisions; `createInTracker` requires a connected tracker (reuse the mock connections state), links the card (`sequenceId` generated per provider convention: `#N` github, `LIN-N` linear, `PROJ-N` jira), errors `ApiError(412)` if that tracker isn't connected; `decideCard` clears `needsYou` and appends a `decision` activity entry.

3. **Drawer** (`src/views/BoardCardDrawer.vue` or component under design-system if reused): opens on card click (route stays `/board`; local state, `Escape` + backdrop close, `role="dialog"` `aria-modal="true"`, focus moves in on open and returns to the card on close): title (inline edit), body (textarea edit), priority select, labels editor (add/remove chips), assignee display read-only, timestamps, sequence chip + native link, sync badge with plain-language explanation for orphaned/parked, comments thread (read-only list + append form via `useForm`), parked-ops section (field, lastError, Retry button with busy state), Approve/Reject buttons when `needsYou`. Edits go through the board store (optimistic + rollback on `BoardConflictError` + verbatim conflict toast from v1).

4. **Store** (`src/stores/board.ts` extensions): `updateCard` optimistic with rollback (same shape as `moveCard`), `retryOp`, `decideCard`, `createInTracker` (updates the card's link then delegates the queued move to the existing `moveCard`), `activity` ref + `loadActivity`/`refreshActivity`.

5. **Activity feed**: a panel on `/board` (collapsible side column or below-board section — keep it simple, no new route), newest-first, each entry: kind marker (StatusDot: conflict→`warning`, parked→`error`, dispatch→`info`, decision→`success`), message, sequence chip when linked, relative-free absolute timestamp (house style — render the ISO date, no date lib).

6. **Create-in-tracker modal**: unlinked card dropped on Queued (or "Move to… → Queued") now opens the modal listing connected trackers (from the existing connections state; disabled entries for unconnected ones); confirm → `createInTracker` → card shows its new sequence chip → Queued move + `pilot` chip applied; cancel → card stays put, no toast. `ApiError` surfaces via the v1 toast shelf.

7. **Tests** (house idiom): drawer opens/closes with focus management · title edit calls `updateCard(id, {title}, version)` and commits returned card · stale-version edit snaps back + verbatim toast · comment append renders optimistically after adapter resolves · retry flips a parked op and clears the badge when last parked op resolves · activity feed renders seeded entries newest-first and gains a decision entry after `decideCard` · create-in-tracker: modal lists only connected trackers, confirm links then queues (adapter call order asserted), 412 path surfaces toast · approve clears `needsYou` and the hero lane updates.

8. `make build`, `make test`, `make lint` green (vue-tsc strict). Conventional-commit PR title.

## Implementation

File plan: extend `types.ts`/`errors.ts`/`mockAdapter.ts`/`httpAdapter.ts` · `src/stores/board.ts` + spec · `src/views/BoardCardDrawer.vue` + spec · activity panel inside `BoardView.vue` (+ spec additions) · modal component (`src/design-system/components/ModalDialog.vue` — minimal, token-styled, reusable; none exists in the repo). Sequencing: types + mock behaviors → store extensions → drawer read-only → edits + conflict path → parked/retry → activity feed → create-in-tracker → approve/reject → tests throughout.

**Scope fence (do not build):** real HTTP wire mapping/fixtures (needs the console board API) · execution enrichment (run status, logs, PR checks — separate plane, arrives with the instance-proxy join later) · per-card execution timeline · websockets/live updates · comment edit/delete · assignee write-back · multi-board.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-07-31 · https://github.com/qf-studio/pilot-console-ui/issues/41 (labels: pilot, no-decompose; gated on #40)
- Blocked by: #40 (board v1 — columns, drag, toast shelf, conflict snap-back this issue builds on).
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §3 (per-field ops, parked), §6 (dispatch requires a link) — embedded above; do not read the sibling repo.
