# feat(board): kanban board v1 — domain types, adapter methods, columns, drag, conflict snap-back (mock-first)

**Status**: ✅ Delivered — ui#40 → [PR#42](https://github.com/qf-studio/pilot-console-ui/pull/42) merged 2026-08-01 07:15Z (operator approval 08-01). ⚠️ Mock-first: real-stack verify per SOP applies when C7 board API wires httpAdapter. Archived 2026-08-03.

## Context

This is the first leg of the **S4 kanban board** — the screen this repo's design system was built for (the `--color-needs-you` token in `theme.css` is annotated "Board hero lane" since issue #1). It is built **mock-first**, exactly like the S3 screens: full UX against `mockAdapter`, with `httpAdapter` board methods stubbed until the console board API exists (a console-side issue, in flight separately). Do NOT read or clone sibling repos — every cross-repo fact you need is embedded below.

Product model (fixed, from the platform design):

- One board per org. Cards come from mixed trackers side by side — a card has **zero or one** external link (`github | linear | jira`), never mirrored into multiple trackers. Board-only cards (no link) are legal.
- **Canonical statuses = the columns (fixed vocabulary)**: `backlog | todo | queued | in_progress | in_review | done | canceled`.
- **Queued is special**: dropping a LINKED card on Queued dispatches it to the customer's Pilot instance (the platform applies a `pilot` trigger label tracker-side). An UNLINKED card cannot dispatch (nothing to label) — v1 blocks the move with a toast; the create-in-tracker flow arrives in the follow-up issue.
- **"Needs You" is NOT a status** — it is an overlay (approval decisions waiting on the human). A needs-you card stays in its status column AND surfaces in a pinned hero lane. It never writes to any tracker.
- **Conflict UX (remote-wins)**: when the platform rejects a board edit because the tracker changed concurrently, the tracker wins — the card snaps back and the user sees a toast. Exact copy, verbatim: `Updated in ${providerLabel} just now — your change was not applied` (providerLabel ∈ GitHub/Linear/Jira). Losing loudly on our side beats losing silently on theirs.
- Priority vocabulary: `urgent | high | medium | low | none`. Assignee is display-only. `sequenceId` is the provider-prefixed human key rendered verbatim (`PROJ-42`, `#42`). Link `syncState`: `ok | orphaned | parked` (orphaned = native issue vanished; parked = outbound write-back stuck; both badge the card).

**No new runtime dependencies** — drag-and-drop is native HTML5 DnD plus a keyboard fallback (the repo has no dnd lib and stays that way for v1).

This task must NOT be decomposed — implement as a single PR. <!-- pilot:no-decompose -->

## Acceptance

1. **Types** (`src/lib/api/types.ts`): `BoardCardStatus` (7-value union above), `CardProvider = 'github' | 'linear' | 'jira'`, `CardPriority`, `CardLink { provider: CardProvider; sequenceId: string; nativeUrl: string; syncState: 'ok' | 'orphaned' | 'parked' }`, `BoardCard { id, title, body, status: BoardCardStatus, priority: CardPriority, labels: string[], assigneeDisplay: string | null, origin: 'board' | CardProvider, link: CardLink | null, needsYou: boolean, version: number, createdAt, updatedAt }`. `ConsoleAdapter` gains `listCards(): Promise<BoardCard[]>` and `moveCard(cardId: string, to: BoardCardStatus, version: number): Promise<BoardCard>` (compile-time enforcement updates both adapters).

2. **Errors** (`src/lib/api/errors.ts`): `BoardConflictError extends Error { remoteCard: BoardCard; providerLabel: string }` — thrown when a move loses to a concurrent tracker change; `remoteCard` is the authoritative post-conflict card.

3. **mockAdapter**: module-`let` seeded state per house style (`delay()`, `generateId`), ~12 cards spanning all three providers + at least one board-only unlinked card, covering: every column populated, an `orphaned` link, a `parked` link, ≥2 `needsYou` cards in different columns, mixed priorities/labels. `moveCard` validates `version` (stale → `BoardConflictError` with the current card) and mutates status immutably. One deterministic conflict fixture: the card labeled `conflict-demo` throws `BoardConflictError` on its FIRST move (remote status differs), then behaves normally — this drives the snap-back UX and its tests. Moving a LINKED card to `queued` also appends a `pilot` label to it (dispatch side effect, visible on the card); moving an UNLINKED card to `queued` rejects with a typed `CardUnlinkedError` (add to errors.ts).

4. **httpAdapter**: `listCards`/`moveCard` throw `ApiError(501, 'board API not yet available')` with a comment naming the pending console-side board API and the envelope rule (`{cards: [...]}` when it lands). No wire fixtures yet — fixtures are real-JSON-only by house rule, and there is no real wire to copy.

5. **Store** (`src/stores/board.ts`, setup-style like `session.ts`): `cards` ref + `loadCards`/`refreshCards` (loaded-guard idiom), `moveCard(cardId, to)` — **optimistic**: apply the move locally, call the adapter, replace with the returned card on success; on `BoardConflictError` roll back to `remoteCard` and rethrow (view owns the toast); on `CardUnlinkedError` revert and rethrow. Stores never catch-and-swallow.

6. **Toast primitive** (none exists in the repo — build the minimal one): `src/design-system/components/ToastShelf.vue` + `src/composables/useToasts.ts` (module-singleton state, the `onUnauthorized` registration idiom). `role="status"`, `aria-live="polite"`, auto-dismiss ~5s, manual dismiss button, token classes only. Mounted once in `AppShell.vue`.

7. **`BoardView.vue`** at route `/board` (name `board`, lazy import, default layout) + nav entry `Board` in `AppShell.vue`: seven columns in a horizontally scrollable row (`overflow-x-auto`), column header = status label + count. **Needs You hero lane** pinned above the columns, visible only when ≥1 card has `needsYou`, using the `needs-you` token for its accent + `StatusDot status="needs-you"`; cards in the lane also remain in their status column (overlay, not a status).

8. **Card** (`src/design-system/components/BoardCardTile.vue`): sequence chip + provider marker (text `GH`/`LN`/`JR`; `·` for board-only), title, ≤3 labels + `+N` overflow, priority accent (urgent→`error`, high→`warning` text/token accents; medium/low/none unstyled), sync badge (`orphaned`→`StatusDot warning`, `parked`→`StatusDot error`, `ok`→no dot), needs-you dot when flagged, assignee display, native URL as external link (`target="_blank" rel="noopener"`). Tokens only, no raw hex, no `<style>` blocks.

9. **Drag**: native HTML5 (`draggable`, `dragstart`/`dragover`/`drop`), column drop targets with a visible drop-affordance class while dragged-over. **Keyboard/a11y fallback**: each card exposes a "Move to…" menu (button + listbox) driving the same store method — drag is an enhancement, not the only path.

10. **Conflict snap-back**: view catches `BoardConflictError` → toast with the verbatim copy above (providerLabel from the card's link) → board re-renders the rolled-back state. `CardUnlinkedError` → toast `Link this card to a tracker first.`

11. **Tests** (house idiom: `vi.spyOn(mockAdapter, ...)`, `mount` + `flushPromises`, `setActivePinia`): columns render with counts from fixtures · optimistic move calls adapter with `(id, to, version)` and commits returned card · conflict: card snaps back to `remoteCard.status` and toast shows the verbatim copy · keyboard "Move to…" path moves without DnD events · unlinked→queued blocked with toast, linked→queued gains `pilot` label chip · needs-you lane lists exactly the flagged cards and disappears when none.

12. `make build`, `make test`, `make lint` green (vue-tsc strict — no `any`). Conventional-commit PR title.

## Implementation

File plan: `src/lib/api/types.ts` + `errors.ts` + `mockAdapter.ts` + `httpAdapter.ts` · `src/stores/board.ts` (+ `src/stores/__tests__/board.spec.ts`) · `src/composables/useToasts.ts` · `src/design-system/components/ToastShelf.vue`, `BoardCardTile.vue` · `src/views/BoardView.vue` (+ `src/views/__tests__/BoardView.spec.ts`) · `src/router/index.ts` · `src/layouts/AppShell.vue`. Relative imports (no alias). Sequencing: types + mock fixtures → store → toast primitive → view/tile render → drag + fallback → conflict UX → tests throughout.

**Scope fence (follow-up issue, do not build):** card detail drawer, body/title editing, comments, activity feed, parked-op retry, create-in-tracker modal, approve/reject verbs. Also out: real HTTP wire mapping + fixtures (needs the console board API), websockets/polling, virtualized lists, any new runtime dependency.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-07-31 · https://github.com/qf-studio/pilot-console-ui/issues/40 (labels: pilot, no-decompose)
- Blocked by: — (independent of the console-side work; mock-first).
- In-repo precedent: S3 mock-first screens (#1 tokens/StatusDot, #5 connections, instances views), `session.ts` store idioms, `useForm` composable, httpClient error envelope (#39).
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §5 (status vocab), §6 (dispatch requires a link) — embedded above; do not read the sibling repo.
