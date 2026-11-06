# TASK-644: Desktop content container per the Figma layout grid — 1072 px centered, 6 × 162 px columns, 20 px gutter

**Status**: 🟢 **G1 MERGED 11-06 17:35Z → PR#393 `3e83f222`, ui#390 `pilot-done`; deploy in flight.** Post-merge review 17:38Z REQUEST-CHANGES — both blocking items are the pre-PR's accepted deviation from the leg text (`h-full` → `min-h-full` + `grow`), not defects → autopilot [ui#394](https://github.com/qf-studio/pilot-console-ui/issues/394) (`autopilot-fix`, queued behind the K5 retry) **steered by Navigator before pick-up: specs-only, do not revert to `h-full`** (body section + comment) → **PR#395 18:05Z (+18 −2: docblocks naming the divergence, `data-dashboard-root`, spec pins against `h-full`) reviewed APPROVE-w-notes ~18:12Z** — the pre-PR round still reads ui#390's literal class string and calls the divergence "kept, not fixed"; answered with the live measurements (container = `<main>`'s box, scroll region definite, composer pinned). CI pending at review time. **LEG G2 (Research page container) dispatches after #394 lands** (same file `AppShell.vue`). **Live tenant check PASSED 17:4xZ (deploy 17:40Z, bundle `index-ClyInbwa.js`, viewport 1492 px): `[data-page-container]` 1072 @ 208 = 68 + (1352 − 1072)/2, `[data-home-context]` 344 @ 936, stat cards 162 @ 208/390/572/754, composer pinned at the bottom (1545–1586 of 1608).** Earlier: 🟡 G1 PR#393 reviewed APPROVE-w-notes ~17:35Z (geometry verified in the mock dev build at 1440 / 1920 / 1200 px); 🚀 G1 dispatched → [ui#390](https://github.com/qf-studio/pilot-console-ui/issues/390) 16:2xZ (pre-check hit PR#350 "390×844", ownership guard held; one stall 16:26Z, re-run 16:47Z).
**Created**: 2026-11-06
**Assignee**: Pilot (ui leg) · Navigator (review, live check)

---

## Context

**Problem**: the Figma file `BfAqtVPBp0i4iWHg2jzAnB` draws every desktop frame (Home 7-2, Chat session 13-994, Kanban 7-482) on one layout grid: **6 columns × 162 px, 20 px gutter, 1072 px wide, centered on the 1440 px frame** (184 px from either edge, which includes the 68 px rail). The founder's annotated screenshot (11-06, grid overlay on Home) shows the content column inside that grid with the stat cards one column each, the Activity chart and the queue over four columns, and the Context panel over the last two (2 × 162 + 20 = 344 px, which `HomeGridLayout` already has). The built shell does none of this: `src/layouts/AppShell.vue` gives `<main>` `flex-1 overflow-auto px-[26px] py-[22px]`, so the content stretches to whatever the viewport leaves between the 68 px `AppRail` and the 72 px `RightRail`; on a wide monitor the Home grid is 1700 px wide and the stat cards are 400 px each. `HomeGridLayout` uses `gap-4` (16 px) between the main and the context column; the Home stat grid uses `gap-3.5` (14 px). Neither is the 20 px gutter.

**Goal**: one Tailwind container token for the page width, applied once in the shell so every padded desktop route (Home, Research, Board, Connections, Instances, Issue, Settings, Review) centers in a 1072 px column with the 26 px side gaps kept as the minimum clearance from the rails; the two grids that model the Figma columns (`HomeGridLayout`, Home stat cards) take the 20 px gutter. The full-bleed routes (`/docs`, mobile `/chat`) do not change.

Founder's numbers (11-06): max width ~1072 px, gutter 20 px, "use tailwind to set up the container width". 6 × 162 + 5 × 20 = 1072.

---

## Known Pitfalls & Patterns

- **PITFALL** (98%, mem-195): a browser milestone needs one wire proof — the prod bundle once shipped the mock adapter and two days of "passed" browser checks were fixtures. → the live check below reads the container geometry on console.pilotcloud.dev after the deploy, not in the mock build.

---

## Design

1. **Token, not a magic number**: `src/design-system/theme.css` `@theme static` gets `--container-ds-page: 1072px` next to the other `ds-*` tokens. Tailwind v4 maps the `--container-*` namespace to `max-w-*`, so the class is `max-w-ds-page`. The gutter is `gap-5` / `gap-x-5` on the existing `--spacing: 4px` base (20 px); no second token.
2. **One wrapper in the shell**: `AppShell.vue` wraps `<slot />` on the default `mainClass` branch in `<div class="mx-auto h-full w-full max-w-ds-page" data-page-container>`. `<main>` keeps `flex-1 overflow-auto px-[26px] py-[22px] …`: it stays the scroll region (scrollbar at the column edge, GH-172), its 26 px padding is the minimum side gap, and `mx-auto` centers the wrapper in what is left. `h-full` keeps the height chain for the pinned composer (`DashboardView` root `section.h-full`, `HomeGridLayout pin-composer`). The docs branch and the chat branch (mobile-only route) are untouched.
3. **Centering reference**: the wrapper centers in `<main>`, i.e. between the 68 px left rail and the 72 px right rail. On a 1440 px viewport that puts the container 182 px from the window edge; the frame says 184 px. The 2 px asymmetry comes from the rails and is accepted — do not pad it away.
4. **Gutter on the two column grids**: `HomeGridLayout.vue` `md:grid-cols-[minmax(0,1fr)_344px]` keeps its widths and changes `gap-4` → `gap-x-5 gap-y-4` (horizontal gutter 20 px; the vertical stack rhythm inside the main column stays 16 px — the frame's vertical gaps are 13–16 px, not 20). `DashboardView.vue` stat grid `grid grid-cols-2 gap-3.5 xl:grid-cols-4` → `gap-x-5 gap-y-3.5`. At 1072 px: main column 708 px = 4 × 162 + 3 × 20; each stat card 162 px.
5. **Below `md`** nothing changes: `max-w-ds-page` never binds on a phone, the 26 px padding is already the mobile gap, and the mobile Home block is a separate branch.
6. **What stays wide on purpose**: `ReviewView` keeps its own `max-w-[880px]` column and `PageColumn` its `max-w-[640px]` — both are narrower than 1072 and center inside the container; `/docs` keeps its full-bleed three-column layout.

---

## Acceptance Criteria

- [ ] `theme.css` has `--container-ds-page: 1072px`; no view or component carries a literal `1072`.
- [ ] On every route except `docs` and `chat`, `<main>` has one child `[data-page-container]` with `mx-auto`, `w-full`, `max-w-ds-page`; the docs route has none.
- [ ] `HomeGridLayout` root carries `gap-x-5`; the Home stat grid carries `gap-x-5`; the Home composer stays pinned (existing GH-335 specs green).
- [ ] Live, 1440 px viewport on console.pilotcloud.dev `/`: `[data-page-container].getBoundingClientRect()` width 1072, left 182 (± 1); the four stat cards are 162 px wide with 20 px between them; `[data-home-context]` is 344 px wide and starts 20 px after the main column. At 1920 px the container is still 1072 px wide and centered in the main column. At 1200 px the container is 1200 − 68 − 72 − 52 = 1008 px wide (padding binds, no overlap with the rails).

---

## Dispatch

### LEG G1 — repo: qf-studio/pilot-console-ui — title: feat(layout): desktop content container per the Figma layout grid — a 1072 px page column centered in the shell, one container token, 20 px gutter on HomeGridLayout and the Home stat grid

## Context
Figma `BfAqtVPBp0i4iWHg2jzAnB` (Home 7-2, Chat session 13-994, Kanban 7-482) draws every desktop frame on one layout grid: 6 columns × 162 px, 20 px gutter, 1072 px wide, centered on the 1440 px frame. `src/layouts/AppShell.vue` today gives `<main>` `flex-1 overflow-auto px-[26px] py-[22px] max-md:pb-[…]` on the default route branch, so page content stretches across the whole column between the 68 px `AppRail` and the 72 px `RightRail` — on a wide monitor the Home grid is 1700 px wide. `src/design-system/components/HomeGridLayout.vue` already models the frame's main + context split (`md:grid-cols-[minmax(0,1fr)_344px]`, 344 = 2 × 162 + 20) but with `gap-4` (16 px); the stat grid in `src/views/DashboardView.vue` (`grid grid-cols-2 gap-3.5 xl:grid-cols-4`) uses 14 px. `src/design-system/theme.css` `@theme static` holds the `ds-*` tokens on a `--spacing: 4px` base and has no container token. Height chain to keep: `DashboardView` root `section.flex.h-full.min-h-0.flex-col` → `HomeGridLayout pin-composer` relies on `<main>`'s height reaching it (GH-335 pinned composer).

probe: present qf-studio/pilot-console-ui data-home-grid
probe: present qf-studio/pilot-console-ui const[[:space:]]mainClass
probe: present qf-studio/pilot-console-ui spacing:[[:space:]]4px;
probe: absent qf-studio/pilot-console-ui container-ds-pag[e]

## Scope
- `src/design-system/theme.css`: add `--container-ds-page: 1072px` inside `@theme static`, with a one-line comment naming the Figma grid (6 × 162 px, 20 px gutter). Tailwind v4 maps `--container-*` to `max-w-*`, so the utility is `max-w-ds-page`. No other token; the gutter is `gap-5` on the existing spacing base.
- `src/layouts/AppShell.vue`: on the default `mainClass` branch only (not `docs`, not `chat`), wrap the slot: `<main :class="mainClass"><div v-if="usesPageContainer" class="mx-auto h-full w-full max-w-ds-page" data-page-container><slot /></div><slot v-else /></main>` (or an equivalent single-slot shape — the slot must render exactly once). `usesPageContainer` is the complement of the docs/chat route checks already in the file. `<main>` keeps every class it has: it stays the scroll region and its `px-[26px]` is the minimum side gap. Extend the GH-172/GH-71 docblock: the container is the Figma 1072 px grid, centered between the rails (182 px from the window edge at 1440 px vs the frame's 184 px — the 68/72 px rails are asymmetric by design, accepted).
- `src/design-system/components/HomeGridLayout.vue`: root `gap-4` → `gap-x-5 gap-y-4`; the column widths stay. Docblock: the 20 px gutter is the Figma grid's; the vertical rhythm stays 16 px.
- `src/views/DashboardView.vue`: stat grid `gap-3.5` → `gap-x-5 gap-y-3.5`. No other change on the view.
- Tests: `src/layouts/__tests__/AppShell.spec.ts` — on `dashboard` (and one more padded route, e.g. `board`) `<main>` has exactly one `[data-page-container]` child with classes `mx-auto`, `w-full`, `max-w-ds-page`, `h-full`; on `docs` there is no `[data-page-container]` and the slot content still renders; the existing default-branch class assertions stay. `src/design-system/components/__tests__/HomeGridLayout.spec.ts` — `[data-home-grid]` has `gap-x-5` and not `gap-4`. `src/views/__tests__/DashboardView.spec.ts` (or the existing Home spec) — the stat grid element has `gap-x-5`. A `theme.css` read in a spec is not needed; the class names pin the token.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Remove the `data-page-container` wrapper from the default branch in `AppShell.vue` (render the bare slot) -> the AppShell.spec "dashboard route renders one page container" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.
- [ ] `grep -rn "1072" src --include=*.vue` returns nothing (the number lives in `theme.css` only); paste the empty result into the PR body.

## Out of scope
`ReviewView`'s own `max-w-[880px]`, `PageColumn`'s `max-w-[640px]`, `/docs`, the mobile layouts, the vertical rhythm, the `AppHeader` (desktop renders it only on untitled routes), the 2 px rail asymmetry, the `ds-*` typography tokens.

## Refs
- Navigator TASK-644 · Figma `BfAqtVPBp0i4iWHg2jzAnB` 7-2 / 13-994 / 7-482 (layout grid) · GH-309 (`HomeGridLayout`) · GH-335 (pinned composer) · GH-172 (`<main>` as the scroll region) · GH-239 (theme tokens)

### G1 review — PR#393 (Navigator, 11-06 ~17:35Z, comment) — APPROVE-WITH-NOTES
Scope met. Pre-PR enforce RC→fixed a defect in the leg's spec: `h-full` on the wrapper locks it to `<main>`'s box (long pages lose the bottom padding) → `min-h-full flex flex-col` wrapper, `DashboardView` root `grow basis-0`, `SettingsView` `md:grow`. Browser check on the PR head (mock dev, `c1aa457`): 1440 → container 1072 @ 182, context 344 @ 910, stat cards 162 @ 182/364/546/728, composer 837–878 pinned; 1920 → 1072 @ 422; 1200 → 1008 (padding binds). `git grep 1072 -- src/**/*.vue` empty. **Residue (Navigator spec error #1 on this task): the leg called `chat` "mobile-only" from the GH-71 docblock; since GH-309/GH-311 `/chat` is the desktop Research page and renders through the chat branch of `mainClass` — with G1 it has no container (`[data-home-grid]` 1248 px @ 94 at 1440).** → LEG G2. Pre-PR minors stand (stat-grid selector `.grid.grid-cols-2`; `gap-y-3.5` not pinned) → fold into G2. **Post-merge round 17:38Z REQUEST-CHANGES** reads the leg text literally: `h-full` required, Dashboard root "no other change" — i.e. it flags the pre-PR's correct fix as a deviation (the issue text was wrong, the code is right). Lesson: when a pre-PR fix round changes a spec'ed class on purpose, amend the issue body (or the leg) before merge, else the post-merge reviewer files a fix that can revert it. ui#394 steered to specs-only.

### LEG G2 — repo: qf-studio/pilot-console-ui — title: fix(layout): the Research page gets the 1072 px page container — the chat branch of AppShell wraps its slot too, ChatView grows inside it, the chat side of usesPageContainer is pinned

## Context
G1 (ui#390 → PR#393) added the Figma page column to `src/layouts/AppShell.vue`: `usesPageContainer = !isDocsRoute && !isChatRoute` wraps the default-branch slot in `<div class="mx-auto flex min-h-full w-full max-w-ds-page flex-col" data-page-container>`. The leg excluded the `chat` route as "mobile-only" — wrong since GH-309/GH-311: `/chat` is the desktop Research page (`src/views/ChatView.vue`, Figma 13-994), rendered through the chat branch of `mainClass` (`flex min-h-0 flex-1 flex-col overflow-hidden … md:px-[26px] md:py-[22px]`). Measured on the PR#393 head at 1440 px: Home `[data-page-container]` 1072 wide at left 182, but `/chat` has no container and its `[data-home-grid]` spans 1248 px at left 94. `ChatView.vue`'s root is `flex h-full min-h-0 flex-col` and `HomeGridLayout pin-composer` relies on that height for the pinned composer (GH-335); `DashboardView.vue`'s root became `flex min-h-0 grow basis-0 flex-col` in G1 for the same reason inside the wrapper.

probe: present qf-studio/pilot-console-ui const[[:space:]]usesPageContainer
probe: present qf-studio/pilot-console-ui data-page-container
probe: present qf-studio/pilot-console-ui data-research-layout
probe: present qf-studio/pilot-console-ui max-w-ds-page

## Scope
- `src/layouts/AppShell.vue`: `usesPageContainer` becomes `!isDocsRoute.value` (docs stays full-bleed). The wrapper's classes follow the branch: default branch unchanged (`mx-auto flex min-h-full w-full max-w-ds-page flex-col`); chat branch `mx-auto flex min-h-0 w-full max-w-ds-page flex-1 flex-col` (it is a flex child of the `overflow-hidden` column, so it must flex, not `min-h-full`). One `<div data-page-container>` element, one slot render. Docblock: drop "mobile-only" for chat; say the Research page is the chat route on desktop and gets the column.
- `src/views/ChatView.vue`: root `flex h-full min-h-0 flex-col` → `flex min-h-0 grow basis-0 flex-col` (the same shape as `DashboardView`), so the pinned composer keeps working inside the wrapper. Docblock line.
- `src/views/DashboardView.vue`: add `data-stat-grid` to the stat grid element.
- Tests: `src/layouts/__tests__/AppShell.spec.ts` — `it.each(['dashboard', 'board', 'chat'])` renders one `[data-page-container]`; for `chat` the container has `min-h-0` and `flex-1` and not `min-h-full`; for `dashboard` it has `min-h-full` and not `flex-1`. `src/views/__tests__/ChatView.spec.ts` — the root carries `grow` and `basis-0`, not `h-full`; the existing "keeps the composer outside the scrolling column" spec stays green. `src/views/__tests__/DashboardView.spec.ts` — the GH-390 stat-grid spec selects `[data-stat-grid]` and also asserts `gap-y-3.5`.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Set `usesPageContainer` back to `!isDocsRoute.value && !isChatRoute.value` -> the AppShell.spec "chat route renders one page container" assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
`/docs`, the mobile `/chat` layout below `md` (the wrapper's `max-w` never binds there; the chat branch has no `md:` padding below `md` and that stays), `ReviewView` / `PageColumn` widths, the vertical rhythm.

## Refs
- Navigator TASK-644 § G1 review · ui#390 / PR#393 · GH-309 (`HomeGridLayout`, Research on the Home geometry) · GH-311 (RightRail chat circle → `/chat`) · GH-335 (pinned composer) · Figma `BfAqtVPBp0i4iWHg2jzAnB` 13-994

## Refs

- G1 → https://github.com/qf-studio/pilot-console-ui/issues/390
- G2 → (filled by the dispatch script)

---

## Out of Scope

- A vertical rhythm token (the frame's vertical gaps are 13–16 px; the current 16 px stays).
- Re-centering the container on the window instead of the main column (would need rail-aware padding for 2 px).
- The v4-era pages' internal grids (Connections, Instances, Issue, Settings) — they inherit the container width only; their columns are the D4 / roadmap work.
- `/docs` (designed, built full-bleed) and the mobile frames.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the width lives | per-view `max-w-[1072px]` · a `PageContainer` component · a wrapper in `AppShell` `<main>` | wrapper in `AppShell` | one place, every padded route, no per-view drift; `<main>` stays the scroll region |
| How the number is expressed | arbitrary `max-w-[1072px]` · `--container-ds-page` token | token | founder asked for a Tailwind container; v4 `--container-*` → `max-w-*`; one grep finds it |
| Gutter | new `--spacing-ds-gutter` token · `gap-5` | `gap-5` | 20 px is on the 4 px base already; a second token adds nothing |
| Centering reference | window (rail-aware padding) · main column | main column | 2 px off the frame at 1440 px; not worth asymmetric padding |

---

## Verify

```bash
cd ../pilot-console-ui && npm test && npm run build && npm run lint
grep -rn "1072" src --include=*.vue   # expect nothing
```

Live (Claude in Chrome, 1440 px): `document.querySelector('[data-page-container]').getBoundingClientRect()` → width 1072, left 182 ± 1; `[data-home-context]` width 344; stat cards 162 wide, 20 apart.

---

## Done

- [x] G1 merged (PR#393 17:35Z), ui deployed 17:40Z, live geometry check passed on the tenant at 1492 px (mock dev build at 1440 / 1920 / 1200 px before merge).
- [ ] G2 merged: the Research page (`/chat`) in the same column.
- [ ] README row + graph node `completed`; marker.

---

## Refs

- Founder screenshot 11-06 (Home with the Figma grid overlay) · Figma `BfAqtVPBp0i4iWHg2jzAnB` · TASK-639 (Board on `HomeGridLayout`) · TASK-583 (wave-1 design update) · ui CLAUDE.md § design source of truth

---

**Last Updated**: 2026-11-06
