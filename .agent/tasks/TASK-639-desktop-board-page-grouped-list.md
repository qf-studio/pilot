# TASK-639: Desktop Board page per Figma 7-482 — the project-grouped list replaces the lanes; moves go to the drawer; the right column becomes Performance

**Status**: 🟡 **holding (B2 ready; M8 residue folded in)** — 18:45Z: M8 ui#374 → PR#375 conflicted with PR#373 (post-merge fix of PR#369 that already removed the stale `hasMeta` computed on main); the M8 residue (third-line meta, nowrap leak, regex, test, docblock) is now in B2's scope so one leg runs from a clean main. Earlier: 🟢 B1 LANDED — [ui#370](https://github.com/qf-studio/pilot-console-ui/issues/370) → PR#372 merged 18:06Z (6m35s).** Pre-PR review (enforce) REQUEST-CHANGES → fix round: `running` and `shipped` now derive from `status`, not the chip (an overlay card no longer drops out of the running-first sort or the shipped count). Navigator post-merge review APPROVE-w-notes — see § B1 review. Next: TASK-637 M8 (ListRow fixes, same file) → B2 (scope grew by two B1 minors). Earlier: B1 dispatched 17:17Z after M7 PR#369. Earlier 17:15Z: founder decided both calls (§ Decisions 8–9): no Direction card; chip colours follow the frame everywhere. B1 touches `ListRow.vue`, which M7 ([ui#367](https://github.com/qf-studio/pilot-console-ui/issues/367), running) rewrites below md — B1 is filed after M7 merges to avoid a rebase conflict; B2 after B1. Was: 📋 16:50Z frame read, code audited, two legs drafted, dry run clean.
**Created**: 2026-11-05
**Assignee**: Navigator (plan) → Pilot (legs) · founder (design calls, live check at ≥ 1280 px)
**Parent**: [TASK-584](TASK-584-pilot-cloud-execution-roadmap.md) wave 1 — roadmap row U30 (new) · [TASK-583](TASK-583-wave1-design-update-proof-surface.md) (Home U24/U28/U29 set the page geometry this reuses) · [TASK-637](TASK-637-mobile-on-the-new-design.md) (mobile Board stays as M4/M5 built it)

---

## Context

Figma file `BfAqtVPBp0i4iWHg2jzAnB`, frame **7-482 "Kanban"** (1440×1024) is the desktop Board. It is not a lane board. It is one list grouped by project, in the same vocabulary the Home queue already renders (U4 / U24 / U28):

- Header row at y=24: org name left (`Quantflow`, 15 px, 60 %), `40,3B tokens · 142 hours` right — the founder keeps **"Last shipped · Pool"** over tokens · hours (decision 11-05, TASK-583). Same row as Home.
- Main column x=184, **708 px** wide. Project groups 40 px apart: a 24 px circle + project name (15 px), then rows 48 px below with an 8 px gap (21 px rows → 29 px pitch).
- Row, gap 24: **id** 161 px (60 %) · **chip** in a 64 px slot (21 px tall, 11 px uppercase, 4 px radius) · **title** flex (40 %) · **meta** right (`0 · 19 min`, 30 %). Epic children indent 24 px with an 11 px tree glyph at the row's left edge.
- Chip states on the frame: `CI` (neutral, `#333` edge) · `DONE` (`#8cd68c`) · `QUEUED` (`#72c5e4`) · `FAILED` (`#dc9c9d`) · `BLOCKED` (`#eec946`).
- Right column x=986, **270 px**: `Performance | 12 PRs` header, then cards `Direction | JEV 1.136` (Successful / Failed), `Docs | 135.5K`, `Memory | 5.0K`, `Graph | Health` (Reads / Created / Updated).

What the code has today (`origin/main` 11-05):

- `src/views/BoardView.vue` desktop = v4 lanes: a Needs-You strip of `NeedsYouCard`, seven `BOARD_STATUSES` columns of `BoardCardTile` with drag-and-drop and a keyboard "Move to…" menu, the create-in-tracker modal on an unlinked card → Queued (`attemptMove` → `confirmDispatch`), the daily-cap notice (U19), the sync line, a collapsible Activity list, `BoardCardDrawer` on click. Mobile (`data-testid="mobile-board"`, M4/M5) is a separate block.
- `src/components/dashboard/queue.ts` already does the grouping: `buildQueue(cards, executions)` groups by `card.project`, nests `parentId` children, computes `meta` (`12 min · +184 −32`, parts omitted when absent) from `GET /board/cards/{id}/execution`, and `chipFor` → `CI | PILOT | DONE`; capped 3×3 with `+N more → /board`. `DashboardQueue.vue` renders it with `ListRow` + `Chip` and a 24 px initial avatar per project.
- `ListRow.vue`: id 64 px · title · meta 156 px right · chip last (Figma 7-2 geometry; M5 adds a two-line form below md). The 7-482 row puts the chip **after the id** and widens the id to 161 px.
- `Chip.vue` variants: `neutral` · `pilot` (ds-blue) · `done` (ds-text) · `review` (ds-green, M5). Tokens: `--color-ds-green` (`#187a4e` / `#8cd68c`), `--color-ds-blue` (`#1f78a0` / `#72c5e4`). No red, no yellow.
- `cardDTO` (console `internal/boardapi/dto.go`) serves `project {key,name}`, `parentId`, `needsYou`, `hasParkedOps`, `link.sequenceId`. No duration on the card; duration comes from the per-card execution call the queue already makes.
- `ContextPanel.vue` (Docs / Memory / Graph, data-honest from the docs tree + daemon summary) is the Home right column inside `HomeGridLayout` (main + 344 px). No "Direction / JEV" source exists anywhere on the wire.
- `BoardCardDrawer.vue` has Approve / Reject, priority, labels, parked-op Retry, comments — **no status control**. Moves exist only on the tile (drag, "Move to…").

## Decisions (Navigator; founder to confirm the two open ones)

1. **The list replaces the lanes on desktop.** One `buildQueue` list, uncapped, grouped by project, epic trees at depth 1, the Home avatar + name + `N shipped` as the group header (the frame's hollow circle is the same slot). Mobile keeps the M4/M5 lane pills — its own frame.
2. **Every row carries a chip** (there is no lane to name the status). Vocabulary, data-honest from served fields: `backlog → BACKLOG` · `todo → TO DO` · `queued → QUEUED` (ds-blue) · `in_progress → PILOT` (ds-blue) · `in_review → CI` · `done → DONE` · `canceled → CANCELED` (neutral) · overlays win the slot: `hasParkedOps → FAILED` (ds-red, the drawer's Retry) · `needsYou → BLOCKED` (ds-yellow) — **open question 2**.
3. **Moves go to the drawer**: a Status `SelectInput` over `BOARD_STATUSES` calls the same `attemptMove` (optimistic, conflict snap-back, create-in-tracker modal for an unlinked card → Queued, `pilot` label side effect). Drag-and-drop, `BoardCardTile`, the Needs-You strip and the Activity list leave the desktop Board. Needs You lives on Home's card and the Review page (U8); activity on Home's chart.
4. **Right column = `ContextPanel` with a heading prop**: `Performance` and `N PRs` (`stats.shipped` from the dashboard window) — Docs / Memory / Graph cards as on Home. The **Direction / JEV card is not built**: no wire field carries a judge verdict count; a card of dashes is noise — **open question 1**.
5. **Row geometry per the frame, as a `ListRow` layout**: `layout="board"` → id `w-[161px]`, chip slot after the id, title flex, meta auto right; desktop pitch stays `--ds-row-pitch` (26 px) — the frame's 29 px is the same 15 px type with 8 px gaps; not worth a second token. The queue's `CI | PILOT | DONE` colours stay as they are on Home.
6. **Tokens**: add `--color-ds-red` (light `#a23b3d`, dark `#dc9c9d`) and `--color-ds-yellow` (light `#8a6d00`, dark `#eec946`) — dark from the frame, light derived the way green was (`#8cd68c → #187a4e`: same hue, dark enough for 4.5:1 on white).
7. **Meta** is duration only (`12 min · +184 −32` as the queue builds it; no tokens, no cost — D2 "duration-only rows"); the frame's leading `0` is not rendered.

8. **No Direction / JEV card** (founder, 11-05 17:10Z): skipped until a wire field carries judge counts; a card of dashes is noise.
9. **Chip colours follow the frame everywhere** (founder, 11-05 17:10Z): `DONE` green (`Chip` `done` → `border-ds-green text-ds-green`) on Home, mobile and desktop Board; a held card is `BLOCKED` yellow on every surface — the mobile `REVIEW` green chip from M5/M7 becomes `BLOCKED` in B1. One vocabulary on three pages.

## Open questions (founder)

None — both answered 11-05 (decisions 8–9).

## Legs (dispatch order B1 → B2)

### LEG B1 — repo: qf-studio/pilot-console-ui — title: feat(design): Board row primitives — ds-red and ds-yellow tokens, Chip failed and blocked variants, ListRow board layout, buildQueue uncapped with the full status chip vocabulary

## Context
Figma 7-482 (desktop Board) is a project-grouped list in the Home queue vocabulary: rows `id (161 px) · chip (64 px slot) · title · meta`, with chips `CI`, `DONE`, `QUEUED`, `FAILED` (`#dc9c9d`), `BLOCKED` (`#eec946`). Today `src/design-system/components/ListRow.vue` fixes the chip last and the id at 64 px; `src/design-system/components/Chip.vue` has `neutral | pilot | done | review`; `src/design-system/theme.css` has `--color-ds-green` and `--color-ds-blue` only; `src/components/dashboard/queue.ts` caps groups at `MAX_GROUPS` × `MAX_ROWS` and `chipFor` knows `CI | PILOT | DONE`. The Board page leg (B2) composes these. This leg adds the primitives; its only visible changes are the founder's colour decision: `DONE` chips turn green on Home and the mobile Board, and a held mobile card shows `BLOCKED` (yellow) instead of `REVIEW` (green).

probe: present qf-studio/pilot-console-ui export[[:space:]]function[[:space:]]buildQueue
probe: present qf-studio/pilot-console-ui color-ds-green:
probe: present qf-studio/pilot-console-ui data-row-chip

## Scope
- `src/design-system/theme.css`: `--color-ds-red` light `#a23b3d` / dark `#dc9c9d`; `--color-ds-yellow` light `#8a6d00` / dark `#eec946`, next to the green and blue tokens in both theme blocks.
- `src/design-system/components/Chip.vue`: variants `failed` (`border-ds-red text-ds-red`) and `blocked` (`border-ds-yellow text-ds-yellow`); `done` becomes `border-ds-green text-ds-green` (Figma 7-482 `#8cd68c`); the `review` variant is removed once no caller is left; `data-variant` carries them. Update the component docblock.
- `src/views/BoardView.vue` mobile rows (after M7, ui#367): the held card's chip is `<Chip variant="blocked">BLOCKED</Chip>`; the BoardView mobile spec's held-chip assertions follow (`BLOCKED`, `data-variant="blocked"`). Nothing else on the mobile block changes.
- `src/design-system/components/ListRow.vue`: prop `layout?: 'queue' | 'board'` (default `queue`, markup unchanged). `board` on md and up: the id cell `w-[161px]`, the chip slot rendered after the id in a `w-16` slot (empty slot keeps the width), the title flex, the meta `shrink-0` auto width right. Below md the M5 two-line form is the same for both layouts.
- `src/components/dashboard/queue.ts`: `QueueChip` gains `'QUEUED' | 'BACKLOG' | 'TO DO' | 'CANCELED' | 'FAILED' | 'BLOCKED'`; `chipFor(card, exec)` maps by status (`queued → QUEUED`, `in_progress → PILOT`, `in_review → CI`, `done → DONE`, `backlog → BACKLOG`, `todo → TO DO`, `canceled → CANCELED`) with overlays first: `card.hasParkedOps → FAILED`, else `card.needsYou → BLOCKED`. `buildQueue(cards, executions, options?: { cap?: boolean })` — `cap: false` returns every group and every row, `more = 0`; the default stays capped so the Home queue is unchanged. `group.shipped` and `running` semantics unchanged.
- `src/lib/api/types.ts`: `BoardCard.hasParkedOps: boolean`. The console serves it and the vendored list `src/lib/api/__contracts__/console-wire-fields.txt` carries it, but `src/lib/api/httpAdapter.ts` drops it on purpose (comment near `toCardLink`: the drawer computes the parked state from `listCardOps`). Pass `wire.hasParkedOps` through for the list chip; the drawer keeps its own computation; update that comment. No wire-list change.
- A Chip variant map for the new names where the Home queue's `CHIP_VARIANT` lives (`src/components/dashboard/DashboardQueue.vue`): `QUEUED → pilot`, `BACKLOG | TO DO | CANCELED → neutral`, `FAILED → failed`, `BLOCKED → blocked`; export it from `queue.ts` so B2 reuses it.
- Tests: `src/design-system/__tests__/Chip.spec.ts` the two variants and their classes; `src/design-system/__tests__/ListRow.spec.ts` `layout="board"` order (id, chip, title, meta) and the `w-[161px]` id on md; New file, plain text: src/components/dashboard/__tests__/queue.spec.ts — the chip table above incl. overlay precedence (`hasParkedOps` beats `needsYou` beats status) and `cap: false` returning all rows with `more = 0`; `DashboardQueue.spec.ts` unchanged and green.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Set `cap: false` to return the first three groups only -> the queue.spec uncapped assertion fails
- [ ] Map `needsYou` before `hasParkedOps` in `chipFor` -> the overlay-precedence assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
The desktop block of `src/views/BoardView.vue`, the drawer, `ContextPanel`, any page layout. Light-theme colour tuning beyond the two values above.

## Refs
- Navigator TASK-639 B1 · Figma `BfAqtVPBp0i4iWHg2jzAnB` 7-482 · ui#239 (U1 tokens) · ui#257 (U4 queue) · ui#357 (M5 ListRow two-line form)

## B1 review (PR#372, 11-05 18:15Z) — APPROVE-with-notes

Verified in the diff: tokens in both theme blocks; `Chip` `done` green, `failed` red, `blocked` yellow, `review` removed; `ListRow layout="board"` (id 161 px, 64 px chip slot kept when empty, meta auto); `buildQueue(..., { cap: false })` lists every status; `CHIP_VARIANT` exported from `queue.ts`; `hasParkedOps` passed through the adapter; mobile Board held chip → `BLOCKED`. Visible Home change beyond the colour decision: queued rows show `QUEUED` (blue) instead of `CI`, held rows `BLOCKED` — consistent with decision 9, recorded here. Notes carried into B2 (the post-merge minors sink is capped 1/project/day and ui#365 used it): the mock adapter seeds `card_3` with a parked op but `hasParkedOps: false` and `retryCardOp` never clears the flag (lockstep rule); the adapter passthrough test only uses `false` fixtures; `DashboardQueue.vue` docblock still says `CI / PILOT / DONE`; the board chip span has no `md:ml-3` (spacing unspecified by the frame — accepted).

### LEG B2 — repo: qf-studio/pilot-console-ui — title: feat(board): desktop Board per Figma 7-482 — the project-grouped list replaces the lanes, moves move to the drawer's Status select, the right column becomes Performance

## Context
`src/views/BoardView.vue` renders the desktop Board as seven v4 lanes of `src/design-system/components/BoardCardTile.vue` with drag-and-drop, a Needs-You strip of `NeedsYouCard`, the daily-cap notice (`data-testid="board-notice"`), the sync line (`data-testid="board-sync"`), a collapsible Activity list and `src/views/BoardCardDrawer.vue` on click; the mobile block (`data-testid="mobile-board"`, M4/M5) is separate and stays. Figma 7-482 is one list grouped by project in the Home queue vocabulary (`src/components/dashboard/queue.ts` `buildQueue`, `DashboardQueue.vue` group header: 24 px initial avatar · name · `N shipped`), inside the Home page geometry (`HomeGridLayout` main + 344 px context column, org-name header row with `Last shipped · Pool`, U24/U28), with the right column headed `Performance | N PRs` over the Docs / Memory / Graph cards of `src/design-system/components/ContextPanel.vue`. Leg B1 (#{{B1}}) shipped the primitives: `Chip` `failed`/`blocked`, `ListRow layout="board"`, `buildQueue(..., { cap: false })` with the full chip vocabulary and its variant map.

probe: present qf-studio/pilot-console-ui data-queue-avatar
probe: present qf-studio/pilot-console-ui data-context-panel
probe: present qf-studio/pilot-console-ui function[[:space:]]attemptMove
probe: present qf-studio/pilot-console-ui data-header-org

## Scope
- Header: extract the org-name row from `src/views/DashboardView.vue` (`data-dashboard-header`, `data-header-org`, `data-header-meta`, incl. the M5 below-md rule that drops the org name) into a shared component — New file, plain text: src/design-system/components/OrgHeaderRow.vue — with props `orgName`, `lastShippedLabel`, `poolPercent`; Home and Board render it. Move Home's `lastShippedLabel` derivation (newest `projects[].cards[].doneAt`) into `src/lib/dashboardMetrics.ts` as `lastShippedFrom(projects)`. Board calls `getDashboard()` and `getAllowance()` once on mount, exactly as Home: `Last shipped` from the projects, Pool from `allowance.percentUsed` (hidden when the allowance call fails), `N PRs` from `stats.shipped`. No new wire field.
- Layout: the desktop Board is `HomeGridLayout` (no composer) — main: the header row, the daily-cap notice and the sync line as today, then the list; context: `ContextPanel` with new props `heading` (default `Context`) and `headingValue` (optional, right-aligned, same 15.5 px row) — Board passes `Performance` and `${stats.shipped} PRs`; Home passes nothing. No Direction card.
- List: `buildQueue(board.cards, executions, { cap: false })`; executions fetched for every rendered row the way `DashboardQueue.vue` does (one `getCardExecution` per row, `allSettled`, a rejected fetch leaves the meta empty); group header = the Home avatar + name + `N shipped`; rows = `ListRow layout="board"` with `depth`, id (`text-ds-green` while running, else `text-ds-text/60`), chip from the B1 variant map, title, meta; a row click calls `openCard(card.id)` (the drawer). Empty state: `Nothing on the board.` No project chrome when no card carries `project` (as the queue).
- Drawer: `src/views/BoardCardDrawer.vue` gains a Status `SelectInput` over `BOARD_STATUSES` / `STATUS_LABELS` next to Priority; a change emits `move(to)`; `BoardView` wires it to `attemptMove(cardId, to)` so the optimistic update, the 409 snap-back toast, the create-in-tracker modal for an unlinked card → Queued and the `pilot` label side effect are the same code path. The modal stays in `BoardView`.
- Removed from the desktop Board: the lanes and drag-and-drop handlers (`onDragOver`, `onDragLeave`, `onDrop`, `dragOverStatus`), the Needs-You strip, the Activity section and `ACTIVITY_STATUS` / `activityLinkUrl` / `isActivityOpen` if nothing else uses them; `BoardCardTile.vue` and `NeedsYouCard.vue` are deleted only when `git grep` shows no other caller (the Review page may use `NeedsYouCard`). Polling, `refreshAll`, the mobile block and `ApproveSheet` unchanged.
- Tests: `src/views/__tests__/BoardView.spec.ts` desktop describes replaced — groups by project with the avatar and `N shipped`, epic children at depth 1, the chip per status incl. `FAILED` for `hasParkedOps` and `BLOCKED` for `needsYou`, the meta from the execution, the cap notice verbatim, the drawer Status change → adapter `moveCard(id, to, version)`, an unlinked card → Queued via the drawer → the create-in-tracker modal (existing cases re-pointed), 409 snap-back; the polling and mobile describes unchanged and green. `src/views/__tests__/DashboardView.spec.ts` still finds one org name at 390 px and the header on desktop (through `OrgHeaderRow`). `ContextPanel.spec.ts`: `heading` / `headingValue` render; default unchanged. `BoardCardDrawer.spec.ts`: the Status select emits `move`.
- ListRow residue from TASK-637 M8 (ui#374 / PR#375 conflicted with PR#373 and was closed; the `hasMeta` bug is fixed on main by PR#373): in `src/design-system/components/ListRow.vue` below md the meta renders on the title line — title `min-w-0 flex-1 truncate`, then the meta `shrink-0 text-ds-text/60` with a 12 px gap, in one `flex` wrapper spanning both grid columns on row 2, no `max-md:row-start-3`; `whitespace-nowrap` on the id becomes `max-md:whitespace-nowrap` so a long id wraps in the 64 px desktop cell again; docblock updated. `src/views/BoardView.vue` `mobileRowId` prefix test becomes `/^[A-Za-z0-9]+-/`. `src/views/DashboardView.vue` docblock gets the GH-367 entry (org name hidden below md). Tests: `src/design-system/components/__tests__/ListRow.spec.ts` asserts title and meta share row 2 and no `row-start-3` class, and the id has `max-md:whitespace-nowrap` and no bare `whitespace-nowrap`; `src/views/__tests__/DashboardView.spec.ts` mounts `AppHeader` with the view at 390 px and counts the org name once in `document.body.textContent` without stripping `md:hidden` nodes; `src/views/__tests__/BoardView.spec.ts` renders a `sequenceId` of `AB1-5` once with no marker.
- Mock lockstep (B1 review): `src/lib/api/mockAdapter.ts` seeds `hasParkedOps: true` on the card that carries a parked op (`card_3`) and `retryCardOp` clears it when no parked op remains, so the mock Board shows and clears `FAILED`; `src/lib/api/__tests__/httpAdapter.spec.ts` passthrough test uses a `true` fixture value; `src/components/dashboard/DashboardQueue.vue` docblock lists the full chip vocabulary.
- The e2e mobile suite (`make e2e`) stays green — the mobile block is untouched.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] `make e2e` passes; paste the output into the PR body.
- [ ] Render the lanes again (any `BoardCardTile`) -> the BoardView.spec desktop assertion that no lane column exists fails
- [ ] Make the drawer Status change call the adapter directly instead of `attemptMove` -> the unlinked-card-to-Queued modal assertion fails
- [ ] Restore `max-md:row-start-3` on the ListRow meta -> the shared-row assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
The mobile Board (M4/M5). The Direction / JEV card. The Review page. Any console change. A desktop Playwright project (verification is the live tenant at ≥ 1280 px, Navigator).

## Refs
- Navigator TASK-639 B2 · Figma `BfAqtVPBp0i4iWHg2jzAnB` 7-482 · ui#242/#257 (U4 queue) · ui#298 (U20 id prefix) · ui#334 (U28 header) · ui#280 (U19 cap notice) · ui#275 (U8 Review page)

## Verify

- Live tenant at ≥ 1280 px (Claude in Chrome, `getBoundingClientRect` of the main column and the context column): header row = org name + `Last shipped · Pool`; groups with avatar + name + `N shipped`; every row has a chip; a held card shows `BLOCKED`; the drawer Status select moves a card and an unlinked card → Queued opens the tracker modal; right column headed `Performance · N PRs`.
- `make e2e` artifact `mobile-390x844` unchanged on Board.
- Founder check against the frame at 1440 px.

## Refs

- B1 → https://github.com/qf-studio/pilot-console-ui/issues/370
- B2 → (filled by the dispatch script)
