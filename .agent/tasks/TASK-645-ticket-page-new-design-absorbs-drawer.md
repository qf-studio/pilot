# TASK-645: Ticket page in the new design — a Board row opens the page, the page absorbs the drawer's settings, the drawer retires

**Status**: 🚀 **A2 DISPATCHED 11-07 ~11:35Z → [ui#407](https://github.com/qf-studio/pilot-console-ui/issues/407)** (`pilot` + `no-decompose`, pre-check 0; scope carries the A1 review notes + the live-found root fix). **A1 ✅ LIVE**: PR#405 merged 11:13Z, deployed 11:19Z, tenant check passed (1072 @ 208, four context cards, no band); ui#406 (post-merge minors) closed as folded into A2. B1 after A2's post-merge round. **Status**: 🟡 **A1 PR#405 reviewed APPROVE-w-notes 11-07 ~11:20Z (+310 −175: `KeyValueCard`, `TicketSideCards` rendered twice, title row, `issue` out of `UNTITLED_ROUTES`, timeline on ds tokens; 1142 tests green, CI test pending). Notes folded into A2: status chip variant via `STATUS_CHIP` (moot: becomes a select), mobile cards straight after the body, share input without `bg-surface`, class guard with the share enabled.** Earlier: 🚀 A1 dispatched → [ui#404](https://github.com/qf-studio/pilot-console-ui/issues/404).
**Created**: 2026-11-07
**Assignee**: Pilot (ui legs) · Navigator (reviews, live checks)

---

## Context

**Problem**: after TASK-639 (desktop Board) and TASK-644 (1072 px column) every drawn Figma frame is live, and a new-design Board row still lands on two old-design surfaces: the ticket page `src/views/IssueView.vue` (route `/board/:cardId`, 52 v4 classes / 0 ds) and the quick-view drawer `src/views/BoardCardDrawer.vue` (29 / 0), a centered `ModalDialog` (512 px) that opens on a desktop row click. The founder (11-07): the popup on a row click is wrong — the click should open the ticket page; the popup's settings (editable title and body, Status and Priority selects, labels, comments, parked changes with Retry, the Needs-you decision) must survive, so they move into the page. The founder chose a **port by vocabulary extension** (no new Figma frame; ui CLAUDE.md rule 4: build from the host page's design-system vocabulary and mark `no mock — vocabulary extension`).

**Goal**: one ticket page in the design-system vocabulary, inside the 1072 px column, that shows everything the page and the drawer show today and lets the user do everything the drawer lets them do; a desktop Board row click navigates to it; `BoardCardDrawer.vue` is deleted. No behaviour change beyond the navigation; every string and hook the existing specs rely on stays.

**Research (navigator-research, 11-07)** — what exists:
- `IssueView.vue` (349 lines): breadcrumb; provider chip + sequence id + `Open in {Tracker} ↗`; h1 title; labels; `held` chip; "Mirrored from … read-only · edit at source ↗"; body (`whitespace-pre-wrap`); Needs-you card (`PR #n held` / `Needs your approval`, Approve / Reject via `useCardDecision`, `Open PR ↗`); `RunStoryCard` (already ds; `@retried`); share switch (`data-share`, `data-share-switch`, `data-share-url`, `data-share-copy`, `data-share-rotate`); "Latest run" `PilotRunTimeline` with the degraded states (`no-instance` / `not-running` / `unreachable` / `not-found`, exact texts in the spec); a 256 px aside (Status text, Source, Run + `PR #n ↗`, Created · Updated). Layout `flex-col gap-8 lg:flex-row`. Imports `BaseButton`, `RunStoryCard`, `PilotRunTimeline`; stores `board`, `bell`, `session`; adapter `getCardRunStory`, `getCardShare`, `createCardShare`, `deleteCardShare`, `getPilotTaskEvents`. It is also the **mobile** ticket page (`BoardView.vue:112-119` routes a mobile tap to `{name:'issue'}`; `MobileTabBar.vue:61` marks `['board','issue']`). `issue` is in `AppShell.vue:30-31` `UNTITLED_ROUTES` → desktop renders a title-only `AppHeader` band "Board" (`AppHeader.vue:64`).
- `BoardCardDrawer.vue` (390 lines, `ModalDialog`, `labelled-by="board-card-drawer-title"`): title `TextInput` (save on blur, Enter blurs) · `RouterLink` "Open full page ↗" · `×` · sequence-id chip + `StatusDot` (`orphaned` warning / `parked` error) + `syncExplanation` · Done tile (`data-done-tile`, lazy `RunStoryCard`) · Needs-you strip ("Needs your decision", Approve / Reject) · body `TextArea` (save on blur) · Status `SelectInput` over `BOARD_STATUSES` (emits `move(to)`, never calls the adapter; snaps the draft back) · Priority select (urgent / high / medium / low / none, save on change) · labels (chips with `×` `aria-label="Remove label X"`, "Add label" input + button) · `<dl>` Assignee / Created / Updated · Parked changes (`field` + `lastError` + `Retry` → `board.retryOp`; "Couldn't load parked changes.") · Comments (loading / 501 "Comment history isn't available yet — comments you post still reach the tracker." / "Couldn't load comments." / "No comments yet."; `<form novalidate>` + `TextArea` "Add a comment" + "Post comment", `useForm` + `required('Comment cannot be empty')`, `addCardComment`). Writes: `saveField` → emits `mutating(true/false)` (pauses the Board poll) → `board.updateCard(id, patch, version)` → conflict toast (verbatim) + snap-back. Props `cardId`; emits `close`, `mutating`, `move`.
- `BoardView.vue` wiring: `openCard(row.cardId)` from the desktop `[data-board-row]` button (`:325-332`); `<BoardCardDrawer v-if="openCardId" :card-id @close="closeCard" @mutating="onDrawerMutating" @move="(to) => attemptMove(openCardId!, to)">` (`:406-412`); `attemptMove` (`:144-169`: unlinked card → `queued` sets `pendingDispatchCardId`; else `board.moveCard` with `BoardConflictError` / `CardUnlinkedError` toasts); `confirmDispatch` (`:175-204`: `board.createInTracker`, `DispatchError` hint `Retry in Ns.` / `Safe to retry.`); the dispatch modal (`:414-437`, `#dispatch-modal-title`, one ghost `BaseButton` per tracker gated by `isConnected`, Cancel). `closeCard` resets `drawerMutations` (the guard for a drawer unmounted mid-save).
- Design-system vocabulary (`src/design-system`): `HomeGridLayout` (main + 344 px `context`, `pinComposer?`), `OrgHeaderRow`, `SectionHeader` (title + `action` slot, 40 px), `ListRow` (`layout 'queue'|'board'`, slots id / chip / title / meta), `Chip` (`neutral | pilot | done | failed | blocked`), `Pill`, `StatCard`, `StatusDot`, `RunStoryCard` (ds, shared with `SharedStoryView` — do not touch), `StepsBlock`, `PilotRunTimeline` (v4 look, imported only by `IssueView`), `ModalDialog`, `BaseButton` / `SelectInput` / `TextInput` / `TextArea` (v4 look, app-wide). Tokens: `ds-text` (+ `/60`, `/40`), `ds-edge`, `ds-green/blue/red/yellow`, `ds-num-*`, text `ds-chip 11 / ds-label 12 / ds-context 13 / ds-row 15 / ds-value 24`, `rounded-ds-card 4px`, `rounded-ds-pill`, `--container-ds-page 1072px`. The right-column **card idiom** is inline markup in `ContextPanel.vue` (`<section class="rounded-ds-card border border-ds-edge p-4">`, `h3 text-[13.5px]`, `flex justify-between py-1 text-[13px]` rows, label `text-ds-text/60`) — there is no reusable key/value card yet. Research (`ChatView.vue:184-187`) has the inline title row idiom (h1 15 px + 13 px muted meta, `data-research-title`).
- Specs that pin behaviour: `IssueView.spec.ts` (17 cases: exact texts `Loading issue`, "could not be found", `Open in GitHub ↗`, `Mirrored from GitHub`, `edit at source ↗`, `Needs you`, `PR #87 held`, `Needs your approval`, an `<a>` with text exactly `Open PR ↗`, a `<button>` with text exactly `Approve` (disabled after click), `getPilotTaskEvents('inst_1','GH-87','acme/repo')`, `Latest run`, `awaiting_approval`, `Queued`, `CI passed`, the three degraded texts, `In Review`, `Created`, `Updated`, an `<a>` with text exactly `PR #87 ↗`, no `Sub-issues`; share hooks `[data-share-switch]` `@change`, `[data-share-url]`, `[data-share-rotate]`, the note text, failed create leaves it unchecked). `BoardCardDrawer.spec.ts` (14 cases: `[role="dialog"]` + `aria-modal`, initial focus on an `INPUT`, Escape → `close`; Status `select` found by `closest('label').textContent.startsWith('Status')` with 7 options, `setValue('queued')` emits `move` and calls no adapter; `#board-card-drawer-title` save → `updateCard('card_1',{title},5)` and the verbatim conflict toast + snap-back; comments (`textarea[placeholder="Add a comment…"]`, form submit, author shown, the three state texts, `addCardComment`); parked ops (`Parked changes`, `Jira transition 'Code Review' not available`, a `<button>` with text exactly `Retry`, strip disappears, "Couldn't load parked changes."); Needs-you (`Needs your decision`, `Approve` → `decideCard('card_4','approve')`, strip clears; 409/404/502 toasts)). `BoardView.spec.ts` drawer cases: `[data-board-row][data-card-id=…]` click opens `[role="dialog"]`; `changeStatus` via the Status select inside the dialog; `#dispatch-modal-title` presence/absence; `createInTracker('p7','github',1)`; `Approve` inside the dialog; `findComponent(BoardCardDrawer)` + `$emit('mutating', true)` / `$emit('close')`; the mobile describe asserts no `border-ink | bg-ink | text-surface | text-muted | needs-you` in the mobile lane HTML (the class-guard precedent) and a plain-card tap routes to `issue`.
- Roadmap: `system/project/project_pilot_cloud_roadmap_tickets.md:114` row **D4** "Ticket page with the Run Story (4 states, Retry, share toggle) … 📋 NEXT UI-UPDATE GAP"; inventory `:119` (IssueView 52/0, BoardCardDrawer 29/0; next: settings D7, instances/connections, docs, onboarding D6, auth).

---

## Known Pitfalls & Patterns

- **PITFALL** (90%, `disabled-gate-whose-only-clearing-path-is-the-disabled-action-locks-out`): a UI gate that disables the one action that can clear it is a lockout. → A2: the Status select and the Save paths stay enabled while a move or save is in flight; a conflict snaps the draft back (the drawer's behaviour today); nothing on the page is disabled by its own pending state except the decision buttons' `loading`.
- **PITFALL** (90%, mem-295): when a pre-PR fix round changes a spec'ed class on purpose, amend the issue body before merge, or the post-merge reviewer files a revert. → each leg below says so in its Scope; Navigator amends the issue on review.
- **PITFALL** (98%, mem-195): a browser milestone needs one wire proof. → each leg's Done has a live check on console.pilotcloud.dev, not the mock build only.
- **LEARNING** (mem-298): a consumer leg must cite the producer's handler line for any "the response carries X" claim. → no new wire fields in this task; every field named below is read by the current code (cited above).

---

## Design

1. **Page composition** = `HomeGridLayout` without a composer, inside the 1072 px column (TASK-644). **Main column**: title row (Research idiom, `data-ticket-title`: sequence-id `Chip neutral` linking to `link.nativeUrl` or a `board` chip · title as the editable `TextInput` in A2, plain h1 in A1 · meta `Open in {Tracker} ↗`) · the "Mirrored from … read-only · edit at source ↗" line in `text-ds-text/60` · body (A1 read-only `whitespace-pre-wrap`, A2 `TextArea` save-on-blur) · Needs-you block (`Chip blocked` + the headline + context line + Approve / Reject `BaseButton` + `Open PR ↗` anchor) · `RunStoryCard` (unchanged) · `SectionHeader "Latest run"` + status `Chip` + `PilotRunTimeline` · (A2) `SectionHeader "Parked changes"` rows + `SectionHeader "Comments"` list + form. **Context column (344 px)**: `KeyValueCard`s — **Ticket** (Status: A1 `Chip` from the status vocabulary, A2 the `SelectInput`; Priority select in A2; Assignee; Created · Updated), **Source** (provider, sequence id, `Open in … ↗`, sync `StatusDot` + `syncExplanation`), **Run** (run status, `PR #n ↗`), **Labels** (A2: chips with `×`, Add label), **Share** (the switch, url, Copy, Rotate, note). Below `md` the context cell is hidden, so the same cards render in the main column after the body (`md:hidden` block; one component `TicketSideCards` rendered twice, or a slot used twice — Pilot's call, but one source of markup).
2. **`KeyValueCard`** (new ds component, `src/design-system/components/KeyValueCard.vue`): props `title`, optional `headingValue`; slots `action` (header right), default (rows). A row helper is not needed — rows are `<div class="flex justify-between py-1 text-ds-context"><span class="text-ds-text/60">label</span><span>value</span></div>` as in `ContextPanel`. `ContextPanel` is **not** refactored onto it in this task (shared with Home / Research / Board; a later ds leg).
3. **Header band**: `issue` leaves `UNTITLED_ROUTES` (`AppShell.vue`) and the `AppHeader` title map; the page owns its title row like Home / Research / Board (the frames have no band). Mobile keeps the `AppHeader` (it renders below `md` regardless).
4. **`PilotRunTimeline`** is restyled in place to ds tokens (`border-ds-edge`, `text-ds-text`, `/60`, `bg-ds-edge` spine); only `IssueView` imports it. `RunStoryCard` is untouched.
5. **Status moves on the page (A2)** go through the same logic the drawer uses today, extracted from `BoardView.vue` into `src/composables/useCardMove.ts` (`attemptMove`, `pendingDispatchCardId`, `confirmDispatch`, `cancelDispatch`, `isDispatching`, toasts) plus `src/design-system/components/DispatchModal.vue` (the create-in-tracker modal, `#dispatch-modal-title`, trackers gated by `isConnected`). `BoardView` keeps using both until leg B1 removes its drawer; after B, `BoardView` keeps the composable only if the mobile block still moves cards (Pilot verifies; the mobile `ApproveSheet` path does not move).
6. **Shared form primitives** (`BaseButton`, `SelectInput`, `TextInput`, `TextArea`, `ModalDialog`) are **not** restyled here (app-wide blast radius; a later ds leg). The acceptance metric is therefore "no v4 tone class in the two files' own markup", pinned by a spec in the Board-mobile precedent, not "0 v4 classes in the rendered HTML".
7. **Navigation (B)**: the desktop `[data-board-row]` button becomes a `RouterLink :to="{ name: 'issue', params: { cardId } }"` (or `router.push` in the click handler — keep `data-board-row` and `data-card-id`); `BoardCardDrawer.vue` + its spec are deleted; `BoardView`'s `openCardId` / `closeCard` / `drawerMutations` / `onDrawerMutating` go; the ui `CLAUDE.md` surface table drops the drawer row and points `/board/:cardId` at `Figma BfAqtVPBp0i4iWHg2jzAnB — vocabulary extension`.
8. **Poll pause**: the drawer's `mutating` emit paused the Board poll. The ticket page has no poll; `board.updateCard` returns the new version and the store updates the card, so no pause is needed. The `BoardView` guard code goes with the drawer in B.

---

## Acceptance Criteria

- [ ] A desktop Board row click opens `/board/:cardId`; no dialog opens on the Board (after B).
- [ ] The ticket page renders inside `[data-page-container]` as `HomeGridLayout` with a 344 px context column of `KeyValueCard`s; below `md` the same cards render in the main column.
- [ ] Everything the drawer did is on the page: edit title / body (save on blur, conflict toast + snap-back), Status select → `attemptMove` (unlinked → `queued` opens the dispatch modal), Priority, labels add / remove, Assignee / Created / Updated, Parked changes + Retry, Comments (three states + post), Needs-you Approve / Reject.
- [ ] Every existing `IssueView.spec.ts` case stays green; every behavioural `BoardCardDrawer.spec.ts` case is ported to `IssueView.spec.ts`; the `BoardView.spec.ts` drawer cases become navigation cases.
- [ ] Class guard: a spec asserts the rendered `IssueView` HTML (desktop, linked card, needs-you) contains none of `text-ink`, `text-text-2`, `text-muted`, `bg-rail`, `border-hairline`, `needs-you`, `rounded-xl` outside the shared primitives (`BaseButton`, `SelectInput`, `TextInput`, `TextArea`, `RunStoryCard`) — the leg says how the exclusion is done (e.g. assert on the page's own `data-ticket-*` regions).
- [ ] `BoardCardDrawer.vue` and `BoardCardDrawer.spec.ts` are gone from `main`; `rg BoardCardDrawer src` is empty.
- [ ] Live on console.pilotcloud.dev: a Board row click lands on the ticket page in the 1072 px column; a title edit saves; the Status select moves a linked card and snaps back on conflict; the mobile ticket page shows the cards in one column.

---

## Dispatch

Serial: A1 → (post-merge round) → A2 → (post-merge round) → B1. Each leg lands alone (mem-295: Navigator amends the issue body when a pre-PR fix round changes a spec'ed class).

### LEG A1 — repo: qf-studio/pilot-console-ui — title: feat(ticket): ticket page on the Home geometry — HomeGridLayout with a 344 px context column of KeyValueCards, title row in the page, run timeline on ds tokens; read-only port, every IssueView spec stays green

## Context
`src/views/IssueView.vue` (route `/board/:cardId`, `name: 'issue'`) renders the ticket in the v4 vocabulary: a breadcrumb, a 20 px h1, `rounded-xl border-hairline` cards, `text-text-2` / `text-muted` tones, a 256 px `lg:w-64 lg:border-l` aside (Status text, Source, Run + `PR #n ↗`, Created · Updated), the Needs-you card (`PR #n held` / `Needs your approval`, Approve / Reject via `useCardDecision`, `Open PR ↗`), `RunStoryCard` (already ds), the share switch (`data-share-*`), and "Latest run" with `src/design-system/components/PilotRunTimeline.vue` (v4 tokens; only this view imports it) and its four degraded-state texts. Desktop shows a title-only `AppHeader` band "Board" over it because `issue` is in `UNTITLED_ROUTES` (`src/layouts/AppShell.vue`) and the `AppHeader` title map. Home, Research and the Board compose their pages with `src/design-system/components/HomeGridLayout.vue` (main + 344 px `context`), `SectionHeader`, `Chip`, and the right-column card idiom that is inline markup in `ContextPanel.vue` (`rounded-ds-card border border-ds-edge p-4`, `h3 text-[13.5px]`, `flex justify-between py-1 text-[13px]` rows, label `text-ds-text/60`); Research has the inline title row idiom (`data-research-title`: 15 px title + 13 px `/60` meta). `IssueView` is also the mobile ticket page (`BoardView.vue` routes a mobile tap to `issue`), and `HomeGridLayout`'s context cell is `hidden md:block`. Since TASK-644 every route but `/docs` renders inside `[data-page-container]` (1072 px, `min-h-full flex-col`; a root `h-full` would clip — use `grow basis-0` like `DashboardView`). `src/views/__tests__/IssueView.spec.ts` (17 cases) pins exact texts and elements (`Open in GitHub ↗`, `Mirrored from GitHub`, `edit at source ↗`, `Needs you`, `PR #87 held`, `Needs your approval`, an `<a>` whose text is exactly `Open PR ↗`, a `<button>` whose text is exactly `Approve`, `Latest run`, the degraded texts, `In Review`, `Created`, `Updated`, an `<a>` whose text is exactly `PR #87 ↗`, no `Sub-issues`, the `data-share-*` hooks and the note text). This leg is the read-only port; A2 adds the drawer's editing; B routes the Board click here and deletes the drawer.

probe: present qf-studio/pilot-console-ui const[[:space:]]UNTITLED_ROUTES
probe: present qf-studio/pilot-console-ui data-home-grid
probe: present qf-studio/pilot-console-ui data-share-switch
probe: present qf-studio/pilot-console-ui data-page-container
probe: absent qf-studio/pilot-console-ui KeyValueCar[d]

## Scope
- New file (not on main yet) src/design-system/components/KeyValueCard.vue: props `title: string`, `headingValue?: string`; slots `action` (header right, like `SectionHeader`'s) and default (rows). Markup = the `ContextPanel` card idiom: `<section class="rounded-ds-card border border-ds-edge p-4" data-kv-card>`, header row `flex items-center justify-between mb-2` with `h3 class="text-[13.5px] text-ds-text"` and the `headingValue` / `action` on the right. Rows are written by the caller as `<div class="flex justify-between gap-3 py-1 text-ds-context"><span class="text-ds-text/60">Label</span><span class="text-right">Value</span></div>`; add a small `KeyValueRow.vue` only if three or more callers repeat it (A1 has one caller: this page). Docblock: `no mock — vocabulary extension` of `ContextPanel`'s cards; `ContextPanel` itself is not changed.
- `src/views/IssueView.vue`: root `flex min-h-0 grow basis-0 flex-col` (not `h-full`); `HomeGridLayout` without `pin-composer`. **Main slot**: title row `data-ticket-title` (`Chip variant="neutral"` with the sequence id as an `<a>` to `link.nativeUrl`, or the text `board` in the chip when unlinked; h1 15 px `text-ds-row text-ds-text`; meta `Open in {Tracker} ↗` as an `<a>` in `text-ds-context text-ds-text/60`) · labels as `Chip neutral` · the `held` chip as `Chip variant="blocked"` with the text `held` · the "Mirrored from {Tracker} · read-only · edit at source ↗" line (`text-ds-context text-ds-text/60`, the anchor text stays `edit at source ↗`) · body in a `rounded-ds-card border border-ds-edge p-4 whitespace-pre-wrap text-ds-context` block · Needs-you block `data-ticket-needs-you` (`SectionHeader title="Needs you"` + `Chip blocked` with `held`; the headline `PR #n held` / `Needs your approval`; the context line `projectPath · held <rel>`; Approve / Reject `BaseButton`s with `:loading`; the `Open PR ↗` `<a>` unchanged) · `RunStoryCard` unchanged · "Latest run" as `SectionHeader title="Latest run"` with the run-status `Chip` in its `action` slot, then the degraded-state texts (verbatim) or `PilotRunTimeline`. **Context slot** (`TicketSideCards`, a local component or a named slot rendered twice): `KeyValueCard title="Ticket"` rows Status (`STATUS_LABELS[card.status]` as a `Chip` using the same variant map the Board rows use — `CHIP_VARIANT` in `src/components/dashboard/queue.ts` keyed by the status label, `neutral` when unmapped), Created, Updated · `KeyValueCard title="Source"` rows Tracker (`TRACKER_LABELS[provider]` or `Board-only`), Id (`<a>` to `nativeUrl`) · `KeyValueCard title="Run"` (linked only) rows Status (`runStatus ?? '—'`), PR (`<a>` with text exactly `PR #n ↗`) · `KeyValueCard title="Share"` (linked only) containing the existing share markup (`data-share`, switch `role="switch"` `@change.prevent`, `data-share-url` readonly input, `data-share-copy`, `data-share-rotate`, the note "Anyone with the link can read a redacted copy. Rotate to refresh.", the `role="alert"` error) restyled to ds tones. Below `md`, render the same `TicketSideCards` in the main column after the body inside a `md:hidden` wrapper (`data-ticket-cards-mobile`); the context slot copy is `hidden md:block` by `HomeGridLayout` already. Remove the breadcrumb and the 256 px aside. Docblock: cite `Figma BfAqtVPBp0i4iWHg2jzAnB (Home 7-2 geometry) — no mock — vocabulary extension`; keep the sanctioned trims list (sub-issues, attempt history, cost stats).
- `src/design-system/components/PilotRunTimeline.vue`: tokens only — spine `bg-ds-edge`, dot ring `border-ds-edge` / filled `bg-ds-text`, heading `text-ds-context text-ds-text`, sub and time `text-ds-text/60`; structure, props and the `loading` row unchanged.
- `src/layouts/AppShell.vue`: remove `'issue'` from `UNTITLED_ROUTES`; `src/design-system/components/AppHeader.vue`: remove the `issue → { title: 'Board', icon: 'mountain' }` entry if nothing else reads it (grep); docblocks updated. Mobile still renders `AppHeader` (it is desktop-only gating).
- Tests: `src/views/__tests__/IssueView.spec.ts` — all 17 cases stay (same strings, same element kinds); add: the page renders `[data-home-grid]` with `[data-home-context] [data-kv-card]` ×3 for a linked card (Ticket, Source, Run; ×4 with Share once loaded) and `[data-ticket-cards-mobile] [data-kv-card]` the same count; the Status chip in the Ticket card reads `In Review`; **class guard**: the HTML of `[data-ticket-title]`, `[data-ticket-needs-you]`, `[data-home-context]` and `[data-ticket-cards-mobile]` contains none of `text-ink`, `text-text-2`, `text-muted`, `bg-rail`, `border-hairline`, `needs-you`, `rounded-xl` (the `BaseButton`s inside Needs-you are v4 by design — assert on the markup around them by excluding `button` elements' own class strings, e.g. strip `<button[^>]*>` before the assertion). a new spec src/design-system/components/__tests__/KeyValueCard.spec.ts — title, `headingValue`, `action` slot, default slot. `src/layouts/__tests__/AppShell.spec.ts` — the `issue` route renders no `AppHeader` on desktop (mutation pin: put `'issue'` back → fails). `PilotRunTimeline` spec: existing cases green.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Remove the `[data-ticket-cards-mobile]` block from `IssueView.vue` -> the IssueView.spec "cards render in the main column below md" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.
- [ ] `rg -n "lg:w-64|border-l lg:pl-6|rounded-xl" src/views/IssueView.vue` returns nothing; paste the empty result into the PR body.

## Out of scope
Editing (title, body, status, priority, labels, comments, parked ops) — leg A2. The Board row click and the drawer's deletion . `RunStoryCard`, `ContextPanel`, the shared form primitives (`BaseButton`, `SelectInput`, `TextInput`, `TextArea`, `ModalDialog`) stay as they are. The mobile e2e. New wire fields.

## Refs
- Navigator TASK-645 § Design 1–4 · TASK-644 (page container, `grow basis-0`) · TASK-639 B2 (Board rows, `CHIP_VARIANT`) · GH-309 (`HomeGridLayout`) · ui CLAUDE.md § design source of truth rule 4 · Figma `BfAqtVPBp0i4iWHg2jzAnB`

### A1 review — PR#405 (Navigator, 11-07 ~11:20Z, comment) — APPROVE-WITH-NOTES
Scope met (see the PR comment). Pilot split the cards into `src/views/TicketSideCards.vue` (props: card, approval, runStatus, showShare, share state; emits `setShare`, `copy`) rendered twice — one source of markup, as the leg allowed. Pre-PR APPROVE-w-notes minors (status chip variant keyed by label instead of `STATUS_CHIP`; mobile cards after the timeline instead of after the body; `bg-surface` on the share input; class guard without the share-enabled branch) → all folded into A2's Scope. **Live check 11-07 11:2xZ (tenant, viewport 1492, bundle `index-CQKUa-Ce.js`, ticket GH-9): container 1072 @ 208, context 344 @ 936 with Ticket / Source / Run / Share, no header band, mobile block `display: none`, share switch present, no v4 tone in the guarded regions — PASS. One defect found live: the root's `grow basis-0 min-h-0` fixes the page to the container height; the grid (2029 px) overflows it and the bottom padding is lost (0 px gap at the page end) → fixed in A2 (plain flex root).** Post-merge APPROVE-w-notes → ui#406 review-minors (same items) closed as folded into A2.

### LEG A2 — repo: qf-studio/pilot-console-ui — title: feat(ticket): the ticket page absorbs the drawer's editing — title and body save on blur, Status via useCardMove + DispatchModal, Priority, labels, parked changes with Retry, comments, Needs-you decision

## Context
After A1 (#{{A1}}) `src/views/IssueView.vue` is the read-only ticket page on the Home geometry with `KeyValueCard`s in the context column. `src/views/BoardCardDrawer.vue` still holds the editing the founder wants on the page: title `TextInput` (save on blur; Enter blurs), body `TextArea` (save on blur), Status `SelectInput` over `BOARD_STATUSES` (emits `move(to)`; the draft snaps back after the emit), Priority select (urgent / high / medium / low / none; save on change), labels (chips with `×` `aria-label="Remove label X"`, "Add label" `TextInput` + Add), `<dl>` Assignee (`assigneeDisplay ?? 'Unassigned'`) / Created / Updated, Parked changes (`board.loadOps` → rows `field` + `lastError` + a `Retry` button → `board.retryOp` then reload; "Couldn't load parked changes."), Comments (`board.loadComments` states: loading / 501 "Comment history isn't available yet — comments you post still reach the tracker." / "Couldn't load comments." / "No comments yet."; `<form novalidate>` + `TextArea` placeholder "Add a comment…" + "Post comment"; `useForm` + `required('Comment cannot be empty')`; `addCardComment`), Needs-you strip ("Needs your decision" + Approve / Reject via `useCardDecision`). Writes go through `saveField` → `board.updateCard(id, patch, version)` with the verbatim `BoardConflictError` toast and a snap-back. The move logic lives in `src/views/BoardView.vue`: `attemptMove` (unlinked → `queued` sets `pendingDispatchCardId`, else `board.moveCard` with `BoardConflictError` / `CardUnlinkedError` toasts), `confirmDispatch` (`board.createInTracker`; `DispatchError` hint `Retry in Ns.` / `Safe to retry.`), and the dispatch `ModalDialog` (`#dispatch-modal-title`, one ghost `BaseButton` per tracker gated by `isConnected`, Cancel). `src/views/__tests__/BoardCardDrawer.spec.ts` (14 cases) pins the behaviours by text and element (Status `select` found via `closest('label').textContent.startsWith('Status')`, `#board-card-drawer-title`, `textarea[placeholder="Add a comment…"]`, `Parked changes`, a `<button>` whose text is exactly `Retry`, `Needs your decision`, `Approve`). Recalled pitfall: a control must not be disabled by the pending state it alone can clear — the drawer's selects stay enabled and snap back; keep that.

probe: present qf-studio/pilot-console-ui KeyValueCar[d]
probe: present qf-studio/pilot-console-ui function[[:space:]]attemptMove
probe: present qf-studio/pilot-console-ui dispatch-modal-title
probe: present qf-studio/pilot-console-ui data-ticket-title
probe: absent qf-studio/pilot-console-ui useCardMov[e]

## Scope
- New file (not on main yet) src/composables/useCardMove.ts: extract from `BoardView.vue` — `attemptMove(cardId, to)`, `pendingDispatchCardId`, `confirmDispatch(tracker)`, `cancelDispatch()`, `isDispatching`, the toasts (same texts) — parameterised by the board store and `useToasts`; `BoardView.vue` switches to it with no behaviour change (its drawer wiring stays until B).
- New file (not on main yet) src/design-system/components/DispatchModal.vue: the create-in-tracker modal moved out of `BoardView.vue` verbatim (`ModalDialog labelled-by="dispatch-modal-title"`, `<h2 id="dispatch-modal-title">`, one ghost `BaseButton` per tracker gated by `isConnected`, Cancel); props `open`, `isDispatching`, `isConnected(tracker)`; emits `confirm(tracker)`, `cancel`. `BoardView.vue` uses it.
- `src/views/IssueView.vue`: title row — the h1 becomes the title `TextInput` (`id="ticket-title"`, save on blur, Enter blurs; `aria-label="Title"`); body — `TextArea label="Body"` save on blur; Ticket card — Status row becomes `SelectInput label="Status"` over `BOARD_STATUSES` / `STATUS_LABELS` (draft mirrors `card.status`; `@change` → `attemptMove(card.id, draft)` from `useCardMove`; after the call the draft resets to the store value — conflict snap-back as today) and a Priority `SelectInput label="Priority"` (save on change); Assignee row; Labels `KeyValueCard title="Labels"` (chips `Chip neutral` with a `×` button `aria-label="Remove label X"`, `TextInput` "Add label" + Add `BaseButton ghost`); `DispatchModal` mounted on the page for the unlinked → `queued` case; after the body: `SectionHeader title="Parked changes"` (only when `opsError || parkedOps.length`) with one row per op (`field`, `lastError`, `BaseButton ghost` `Retry`) and the error text; `SectionHeader title="Comments"` with the list (author, relative time, body; the three state texts verbatim) and the `<form novalidate>` (`TextArea` placeholder "Add a comment…", `BaseButton` "Post comment", `useForm` + `required('Comment cannot be empty')`). Needs-you block: keep the A1 headline texts; add nothing (Approve / Reject already there). Writes: a local `saveField(patch)` → `board.updateCard(card.id, patch, card.version)`; `BoardConflictError` → the verbatim toast + drafts reset from the store. No `mutating` emit (the page has no poll). The Status `SelectInput` replaces A1's read-only Status chip row in the Ticket card; the chip vocabulary stays on the Board rows. **From the A1 review (PR#405) and the live check:** the `IssueView` root loses `grow basis-0 min-h-0` and becomes `flex flex-col` — that triple is for pages with an internal scroll column (Home/Research pin a composer); on this long document page it fixed the root to the container's height, the grid overflowed it (grid 2029 px vs container 1586 px at a 1608 px viewport on the tenant) and `<main>`'s 22 px bottom padding landed above the content (0 px gap at the page bottom) — the spill mem-295 / ui#390 describe; with a plain flex root the container's `min-h-full` grows with the content; spec: the root carries no `basis-0` and no `min-h-0` (mutation pin). Move `[data-ticket-cards-mobile]` to sit straight after the body card (before Needs-you); drop `bg-surface` from the share URL input in `src/views/TicketSideCards.vue`; extend the class-guard spec with a share-enabled mount (`getCardShare` resolving `{ enabled: true, url }`).
- Tests: port every behavioural case of `src/views/__tests__/BoardCardDrawer.spec.ts` into `src/views/__tests__/IssueView.spec.ts` against the page (title save → `updateCard('card_1',{title},5)` + the conflict toast + snap-back; Status select with 7 options, `setValue('queued')` on a linked card → `moveCard`, on an unlinked card → `#dispatch-modal-title` appears and `createInTracker(id,'github',version)` on confirm; Priority save; label add / remove; comments three states + post; parked ops + `Retry`; `Needs your decision` is **not** required — keep A1's headline texts). Keep all A1 cases. a new spec src/composables/__tests__/useCardMove.spec.ts — the unlinked → `queued` branch sets `pendingDispatchCardId` without calling `moveCard` (mutation pin). `BoardView.spec.ts` — unchanged expectations (the drawer is still there until B); if the dispatch-modal cases break because the modal moved, update the selectors only. `BoardCardDrawer.spec.ts` stays untouched in this leg.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Remove the `attemptMove` call from the Status `@change` handler in `IssueView.vue` -> the IssueView.spec "Status select moves a linked card" assertion fails
- [ ] Put `basis-0` back on the `IssueView` root -> the IssueView.spec "the root is a plain flex column (no basis-0 / min-h-0)" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
Deleting the drawer and changing the Board row click (leg B1). `RunStoryCard`, `ContextPanel`, the shared form primitives. The Done tile (`data-done-tile`) — the page shows `RunStoryCard` directly. New wire fields or endpoints.

## Refs
- Navigator TASK-645 § Design 5–6, 8 · TASK-639 B2 (`attemptMove` / `confirmDispatch`, drawer Status select) · GH-86 (drawer restyle) · memory `disabled-gate-whose-only-clearing-path-is-the-disabled-action-locks-out`

### LEG B1 — repo: qf-studio/pilot-console-ui — title: feat(board): a desktop Board row opens the ticket page — BoardCardDrawer deleted, BoardView drawer wiring removed, specs and the surface table follow

## Context
After A2 (#{{A2}}) the ticket page `src/views/IssueView.vue` carries everything `src/views/BoardCardDrawer.vue` does. `src/views/BoardView.vue` still opens the drawer from the desktop `[data-board-row]` button (`openCard(row.cardId)` → `openCardId` → `<BoardCardDrawer v-if="openCardId" … @close="closeCard" @mutating="onDrawerMutating" @move="(to) => attemptMove(openCardId!, to)">`), keeps `drawerMutations` to pause its poll while the drawer saves, and the mobile block already routes a tap to `{ name: 'issue' }`. `src/views/__tests__/BoardView.spec.ts` has drawer cases (`[data-board-row][data-card-id=…]` click → `[role="dialog"]`, `changeStatus` through the dialog's Status select, `#dispatch-modal-title`, `createInTracker('p7','github',1)`, `Approve` inside the dialog, `findComponent(BoardCardDrawer)` + `$emit('mutating', true)` / `$emit('close')`) and a mobile describe that must not change. The ui `CLAUDE.md` surface table has the row "Board quick-view drawer → `BoardCardDrawer` | no mock — design-system vocabulary (restyled GH-86)" and maps `/board/:cardId` → `IssueView` to `design/issue-v1.html`.

probe: present qf-studio/pilot-console-ui useCardMov[e]
probe: present qf-studio/pilot-console-ui BoardCardDrawe[r]
probe: present qf-studio/pilot-console-ui openCardI[d]

## Scope
- `src/views/BoardView.vue`: the desktop `[data-board-row]` button navigates — `router.push({ name: 'issue', params: { cardId: row.cardId } })` in the click handler (keep `data-board-row`, `data-card-id`, the button element and its keyboard behaviour); delete `openCardId`, `openCard`, `closeCard`, `drawerMutations`, `onDrawerMutating`, the `<BoardCardDrawer>` element and its import; keep `useCardMove` + `DispatchModal` only if a remaining caller exists in the file after the deletion (the mobile block or a keyboard move) — otherwise remove them from this view too and say so in the PR body. Docblock: the row opens the ticket page; the drawer is gone (TASK-645 B).
- Delete `src/views/BoardCardDrawer.vue` and `src/views/__tests__/BoardCardDrawer.spec.ts`. `rg -n BoardCardDrawer src e2e` must return nothing.
- `src/views/__tests__/BoardView.spec.ts`: the drawer cases become one navigation case (clicking `[data-board-row][data-card-id="card_1"]` calls `router.push` / lands on `{ name: 'issue', params: { cardId: 'card_1' } }` — use the spec's existing router mock) and the dispatch / Approve / `mutating` cases are removed (they live in `IssueView.spec.ts` since A2); the mobile describe is untouched.
- `CLAUDE.md` (ui repo): remove the drawer row; the `/board/:cardId → IssueView` row reads `Figma BfAqtVPBp0i4iWHg2jzAnB (Home 7-2 geometry) — no mock — vocabulary extension (TASK-645)`.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Remove the `router.push` from the desktop row click handler in `BoardView.vue` -> the BoardView.spec "row click navigates to the ticket page" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.
- [ ] `rg -n BoardCardDrawer src e2e CLAUDE.md` returns nothing; paste the empty result into the PR body.

## Out of scope
The mobile Board block and `ApproveSheet`. The ticket page itself (A1/A2). `useCardMove` / `DispatchModal` internals.

## Refs
- Navigator TASK-645 § Design 7–8 · TASK-639 B2 · GH-376 · ui CLAUDE.md § surface table

## Refs

- A1 → https://github.com/qf-studio/pilot-console-ui/issues/404
- A2 → https://github.com/qf-studio/pilot-console-ui/issues/407
- B1 → (filled by the dispatch script)

---

## Out of Scope

- A drawn Figma frame for the ticket page (founder: port by vocabulary).
- Restyling the shared form primitives (`BaseButton`, `SelectInput`, `TextInput`, `TextArea`, `ModalDialog`) — a later design-system leg; the class-guard metric excludes them on purpose.
- Refactoring `ContextPanel` onto `KeyValueCard` (shared with Home / Research / Board).
- The other inventory rows (Settings D7, Instances / Connections, Docs, Onboarding D6, auth).
- New wire fields (sub-issues, attempt history, cost stats remain sanctioned trims).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the drawer's settings live | keep the drawer · side panel · the ticket page | the ticket page | founder 11-07: a row click opens the page; one surface, one set of specs |
| Page geometry | single column · `HomeGridLayout` + 344 px context | `HomeGridLayout` | the Home / Research / Board idiom; the settings fit the context column; mobile re-renders the cards in the main column |
| Key/value card | inline markup ×5 · new `KeyValueCard` · refactor `ContextPanel` | new `KeyValueCard`, `ContextPanel` untouched | one source for the idiom without touching three live pages |
| Header band | keep `AppHeader` "Board" band · title row in the page | title row in the page | the frames have no band; Home / Research / Board have none (brief default, not objected) |
| Move logic | duplicate in `IssueView` · composable + `DispatchModal` | composable + `DispatchModal` | one copy; `BoardView` keeps using it until B |
| Shared primitives | restyle now · leave | leave | app-wide blast radius; the acceptance metric is the page's own markup |
| Leg order | one leg · A1 → A2 → B | three serial legs | each lands alone; no function gap (the drawer stays until the page has everything) |

---

## Verify

```bash
cd ../pilot-console-ui && npm test && npm run build && npm run lint
rg -n "BoardCardDrawer" src e2e CLAUDE.md        # empty after B
rg -n "lg:w-64|rounded-xl|text-text-2|text-muted|border-hairline" src/views/IssueView.vue   # empty after A1 (own markup)
```

Live after each leg (Claude in Chrome, the tenant): A1 — a ticket page inside `[data-page-container]`, `[data-home-context] [data-kv-card]` ×3–4, the title row, the run timeline on ds tones, the share switch works. A2 — a title edit saves (reload shows it), the Status select moves a linked card, an unlinked card → `queued` opens the dispatch modal, a comment posts, Parked changes shows when present. B — a Board row click lands on `/board/:cardId`; no dialog; the mobile Board tap still lands there.

---

## Done

- [ ] A1 merged + live check; A2 merged + live check; B merged + live check.
- [ ] `BoardCardDrawer` gone from `main`; the ui CLAUDE.md surface table updated.
- [ ] Roadmap D4 row → ✅; inventory row for `IssueView` / `BoardCardDrawer` updated; README row; graph `completed`; marker.

---

## Refs

- Founder 11-07: "I don't like we show popup on click by the ticket, would be better to show the page with a ticket, but the popup keeps a lot of settings, we need to keep em" · roadmap D4 `system/project/project_pilot_cloud_roadmap_tickets.md:114` + inventory `:119` · TASK-639 (Board, drawer Status select) · TASK-644 (container) · navigator-research report 11-07 (in this doc's Context)

---

**Last Updated**: 2026-11-07
