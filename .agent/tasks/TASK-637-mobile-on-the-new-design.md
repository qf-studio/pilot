# TASK-637: Mobile on the new design — port the mobile v1 layer (PR#84, 2026-08-15) to the 2026-10 Figma design, with a 390×844 Playwright check

**Status**: 🚀 **11-05 13:30Z — M1–M4 ALL MERGED (PR#350 Playwright + CI job e2e-mobile with the mobile-390x844 artifact · PR#349 shell · PR#352 Home + fix PR#356 open · PR#351 Board + fix PR#355), deployed 13:03Z. Review against the 390×844 artifact: APPROVE-with-defects → M5 [ui#357](https://github.com/qf-studio/pilot-console-ui/issues/357) filed (two-line ListRow below md; Research composer not pinned on mobile; org name twice on Home; Board rows "GH GH-101" + lane chip on every row).** Was: 📋 PLANNED 2026-11-05 — founder "Go" on the four legs and "Approved" on the Playwright dev dependency (package.json change). Third tab renamed Chat → Research (Navigator call, matches the desktop page name). Dispatch: M1 and M2 first, M3 and M4 after M2.
**Created**: 2026-11-05
**Assignee**: Navigator (plan) → Pilot (legs) · founder (visual check on a phone)
**Parent**: [TASK-584](TASK-584-pilot-cloud-execution-roadmap.md) wave 5 row U17 · [TASK-583](TASK-583-wave1-design-update-proof-surface.md) (desktop legs U20–U29)

---

## Context

The roadmap row U17 said "re-land `restore/GH-71-mobile`". That is stale: mobile v1 landed on 2026-08-15 as ui PR#84 (issue #83) with all five frames of `design/mobile-v1.html` — home glance, board, approve sheet, chat, bell sheet. It runs today on every viewport under 768 px (`isDesktop` in `src/layouts/AppShell.vue`, Tailwind default `md`). It is built on the old v4 classes and the old v4 dashboard, while the desktop moved to the 2026-10 Figma design (TASK-583: tokens U1, shell U23, Home U24/U28/U29, Research U25/U26, side chat off U27).

What mobile shows today (origin/main 79e0172, 2026-11-05):
- Shell: AppHeader's v4 band with the RightRail inline (avatar, bell; the Operator chat circle is hidden below md), `MobileTabBar` with Home / Board / Chat / Menu, `BellPopover` as a bottom sheet, `MenuView` with Connections, Instances, Settings.
- Home: the v4 glance — tiles "Delivered · 30d", "Median → PR" hardcoded "—", "Cost / shipped" in USD on own-key tenants, "Needs you"; then "Latest shipped" and a box health line. No stat row, no chart, no queue, no composer.
- Board: a Needs-You band, a horizontally scrolling lane pill row, `ApproveSheet` for held cards. v4 classes.
- Research (`src/views/ChatView.vue`): already fine — `HomeGridLayout` hides the Context column below md, the composer is pinned.
- Tests: vitest at `window.innerWidth` 390 in a few specs; jsdom applies no CSS. No Playwright, no CI step for 390×844.

Design rule (no mobile frames exist in Figma `BfAqtVPBp0i4iWHg2jzAnB`): each mobile page is its desktop frame stacked in one column. The right-rail circles move into the header row. The left rail becomes the tab bar. The `ds-*` tokens in `src/design-system/theme.css` are the only palette.

Exit (positioning §9.6): the Playwright 390×844 check passes on the new design, on Home, Board, Research and Menu.

---

## Legs (dispatch order M1 ∥ M2 → M3 ∥ M4)

### LEG M1 — repo: qf-studio/pilot-console-ui — title: test(e2e): Playwright 390×844 smoke — one spec per mobile tab with screenshots as CI artifacts

## Context
There is no Playwright in the repo: `package.json` lists vitest only, `.github/workflows/ci.yml` runs make build, make test, make check-wire-fields and make lint, and no file mentions 390 or 844. Vitest runs in jsdom (`vite.config.ts`), which applies no CSS, so `md:hidden` blocks render in every unit test and nothing proves the mobile layout. The exit criterion of the mobile track (positioning §9.6, TASK-637) is a Playwright 390×844 check. The founder approved the dev dependency on 2026-11-05. The app runs against the mock adapter when VITE_API_MODE is mock (see `src/lib/api/mockAdapter.ts`), which gives a deterministic page without a console.

probe: present qf-studio/pilot-console-ui "vitest run --passWithNoTests"
probe: absent  qf-studio/pilot-console-ui @playwright/test

## Scope
- Add @playwright/test as a dev dependency (approved). New file, plain text: playwright.config.ts with one project "mobile" at viewport 390 × 844, deviceScaleFactor 2, a webServer that runs the Vite dev server in mock mode, and screenshots on every test.
- New files, plain text, under e2e/mobile/: one spec per tab — home, board, research, menu — each loads the route at 390 × 844, asserts the tab bar is visible and the desktop right rail is not, and takes a full-page screenshot named after the tab.
- Makefile: a new target e2e that runs the mobile project; it is not part of make test.
- `.github/workflows/ci.yml`: a new job e2e-mobile that installs the Playwright browser, runs make e2e, and uploads the screenshots as an artifact named mobile-390x844; it runs after the existing jobs and does not gate them.
- `src/lib/api/mockAdapter.ts`: no change unless a route needs a fixture the mock lacks; say so in the PR body if touched.

## Acceptance
- [ ] `make e2e` passes; paste the output into the PR body.
- [ ] Set the viewport to 1280 × 800 in the config -> the home spec right-rail assertion fails
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
Visual diffing. Desktop e2e. Any change to the views.

## Refs
- Navigator TASK-637 M1 · positioning §9.6 · ui PR#84 (mobile v1)

### LEG M2 — repo: qf-studio/pilot-console-ui — title: feat(mobile): shell on the new design — org name header with the avatar and bell circles, tab bar in ds tokens with Home / Board / Research / Menu, safe-area padding, Menu gains Review and Docs

## Context
Below 768 px `src/layouts/AppShell.vue` always renders `src/design-system/components/AppHeader.vue` (the v4 band: page title with a mark icon, org chip, and `RightRail` inline with the avatar and bell; the Operator chat circle is hidden below md) and `src/design-system/components/MobileTabBar.vue` (fixed, md:hidden, tabs Home → dashboard, Board → board, Chat → chat, Menu → menu). `src/views/MenuView.vue` lists Connections, Instances and Settings with the theme toggle; Review and Docs are reachable only on desktop. The desktop shell (U23, PR#312) has no top bar: the org name is the page title and the right rail holds the avatar, bell and chat circles. The 2026-10 tokens live in `src/design-system/theme.css` (ds-text, ds-edge, ds-pill-edge, ds-green, ds-blue, text-ds-row, text-ds-label, radius-ds-pill, ds-header-height). Founder rule 2026-11-05: the right-rail circles move into the mobile header row; the left rail becomes the tab bar; the third tab is Research.

probe: present qf-studio/pilot-console-ui md:hidden
probe: present qf-studio/pilot-console-ui <RightRail v-if="!titleOnly" inline />
probe: present qf-studio/pilot-console-ui isActive\(\['chat'\]\)

## Scope
- Mobile header: below md the band shows the org name left in text-ds-row (the page title row each view renders stays, as on desktop) and the avatar and bell circles right, 24 px, from RightRail inline; no mark icon, no org chip. Keep every aria-label and data attribute so the existing AppHeader and RightRail specs still pass.
- `src/design-system/components/MobileTabBar.vue`: ds tokens only (border ds-edge, active ds-text, inactive text-2 opacity, text-ds-label); tabs Home, Board, Research, Menu; the Research tab routes to chat and is active on chat; padding-bottom uses env(safe-area-inset-bottom).
- `src/layouts/AppShell.vue`: the bottom padding of main below md equals the tab bar height plus the safe-area inset, one constant.
- `src/views/MenuView.vue`: add Review and Docs rows above Connections; ds tokens; the theme toggle stays.
- Tests: `src/design-system/components/__tests__/MobileTabBar.spec.ts` asserts the four labels and the Research route; `src/views/__tests__/MenuView.spec.ts` asserts Review and Docs rows; `src/layouts/__tests__/AppShell.spec.ts` at 390 asserts the header shows the org name and no mark icon.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Rename the third tab back to Chat -> the MobileTabBar.spec.ts label assertion fails
- [ ] Drop the Review row from MenuView -> the MenuView.spec.ts assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
The Home glance (M3), the Board sheets (M4), the desktop shell.

## Refs
- Navigator TASK-637 M2 · ui#307 (U23) · ui PR#84 · design/mobile-v1.html frames 1 and 5

### LEG M3 — repo: qf-studio/pilot-console-ui — title: feat(mobile): Home on the new design — the four stat cards in a 2×2 grid, the activity chart, the queue, the composer pinned above the tab bar; the v4 glance goes

## Context
Below md `src/views/DashboardView.vue` renders the v4 "mobile glance" (near lines 277 to 323): tiles "Delivered · 30d", "Median → PR" hardcoded to "—", "Cost / shipped" in USD when the tenant is on its own key, "Needs you" (opens the bell), then "Latest shipped" (three cards by doneAt) and a box health line; the desktop grid is max-md:hidden. The desktop Home (Figma 7-2, U24/U28/U29) is: org name row with "Last shipped X ago · Pool N%", four StatCards (Delivered N/100, Review needed with the Review pill, Time to PR from stats.medianTimeToPrMs, Projects), `src/design-system/components/DashboardActivityChart.vue`, `src/components/dashboard/DashboardQueue.vue` (3 groups × 3 rows, "+N more"), and `src/design-system/components/ResearchComposer.vue` pinned at the bottom through `src/design-system/components/HomeGridLayout.vue` with pin-composer. HomeGridLayout already hides the Context column below md.

probe: present qf-studio/pilot-console-ui Median → PR
probe: present qf-studio/pilot-console-ui pin-composer
probe: present qf-studio/pilot-console-ui data-time-to-pr

Depends on: #{{M2}}

## Scope
- `src/views/DashboardView.vue`: remove the mobile glance block, the "Latest shipped" list and the box health line; render the desktop HomeGridLayout on every width. Below md the stat row becomes a 2 × 2 grid of the same four StatCards, the chart keeps its range switch, the queue keeps its 3 × 3 cap, and the composer stays pinned above the tab bar (the AppShell bottom padding from M2).
- No USD anywhere on mobile Home: the "Cost / shipped" tile is gone. The own-key cost line lives on Settings › Usage only.
- `src/views/__tests__/DashboardView.spec.ts`: at innerWidth 390 the four StatCards render, "Latest shipped" and "Cost / shipped" do not, and no "$" appears; the Time to PR card shows the median when present. Remove the glance describe blocks.
- The M1 home spec screenshot is regenerated; the PR body links the artifact.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Put the Cost / shipped tile back on mobile -> the DashboardView.spec.ts no-USD assertion fails
- [ ] Hide the Time to PR card below md -> the DashboardView.spec.ts 390 four-cards assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
The Board (M4). The Context panel on mobile, which stays hidden. Desktop Home.

## Refs
- Navigator TASK-637 M3 · Figma 7-2 · ui#308 (U24) · ui#334 (U28) · ui#335 (U29) · design/mobile-v1.html frame 1

### LEG M4 — repo: qf-studio/pilot-console-ui — title: feat(mobile): Board, Approve sheet and bell sheet on the new design — ListRow rows with Chip states, lane pills and sheets in ds tokens

## Context
Below md `src/views/BoardView.vue` renders a Needs-You band ("N need you / Tap to review" opening `src/design-system/components/ApproveSheet.vue` for the first held card), a horizontally scrolling lane pill row over BOARD_STATUSES with counts, and the selected lane's cards (near lines 326 to 381); held cards open ApproveSheet, others go to the issue page. `src/design-system/components/BellPopover.vue` becomes a bottom sheet below md. All three use v4 classes (border-ink, bg-ink, text-surface, text-muted, needs-you). The desktop queue (U4, U21) renders rows with `src/design-system/components/ListRow.vue` and `src/design-system/components/Chip.vue` (CI, PILOT, DONE) in ds tokens; the Figma Kanban frame 7-482 shows the same row style with chips CI, DONE, QUEUED, FAILED, BLOCKED.

probe: present qf-studio/pilot-console-ui data-testid="mobile-board"
probe: present qf-studio/pilot-console-ui bg-ink text-surface

Depends on: #{{M2}}

## Scope
- `src/views/BoardView.vue` mobile block: the Needs-You band and the lane pill row in ds tokens (radius-ds-pill, ds-pill-edge, ds-green for the active pill); the lane's cards as ListRow rows with the id, title and a Chip whose variant follows the status (queued, in_progress → PILOT, in_review → CI, done → DONE, canceled → muted); the row tap behaviour unchanged.
- `src/design-system/components/ApproveSheet.vue` and `src/design-system/components/BellPopover.vue` (sheet mode): ds tokens, radius-ds-card on the sheet, the same button style as the Review page actions; no behaviour change.
- Tests: `src/views/__tests__/BoardView.spec.ts` mobile block asserts ListRow rows with a Chip per card and no v4 class names; `src/design-system/components/__tests__/ApproveSheet.spec.ts` and `BellPopover.spec.ts` keep their cases.
- The M1 board spec screenshot is regenerated; the PR body links the artifact.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] Render a lane card without a Chip -> the BoardView.spec.ts chip-per-row assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
The desktop Board page (Figma 7-482 has no ticket yet). Decision logic in useCardDecision.

## Refs
- Navigator TASK-637 M4 · Figma 7-482 · ui#242 (U4) · ui#302 (U21) · design/mobile-v1.html frames 2, 3, 5

### LEG M5 — repo: qf-studio/pilot-console-ui — title: fix(mobile): two-line list rows below md, the Research composer pinned above the tab bar, no duplicate org name on Home, Board lane rows without the lane chip and without a doubled id

## Context
The M1 artifact mobile-390x844 from CI run 42031853721 (main b3c2f84, 2026-11-05 12:59Z) shows four defects after M2, M3 and M4 merged (PR#349, PR#352, PR#351 + PR#355):
1. `src/design-system/components/ListRow.vue` keeps its desktop geometry below md: a 64 px id cell, a 156 px right-aligned meta cell and the chip, so at 390 px the title gets about 110 px and renders as "Spike: …" or "Fi…"; a long id such as LIN-ENG-52 wraps onto two lines inside the 64 px cell. Both the Home queue (`src/components/dashboard/DashboardQueue.vue`) and the Board lane rows (`src/views/BoardView.vue`) use it.
2. On Research the composer renders directly under the title, not above the tab bar: `src/views/ChatView.vue` wraps `src/design-system/components/HomeGridLayout.vue` with pin-composer, but below md the layout is a plain flex column with no height, so the scroll container collapses and the composer follows the content. On Home the content fills the height, so the composer lands above the tab bar by accident.
3. Home shows the org name twice below md: once in the M2 header band and once in the page row with data-header-org (DashboardView near line 165).
4. Board lane rows render the id as the provider marker plus the sequenceId ("GH GH-101", BoardView near line 381; the sequenceId already carries the GH- prefix, the U20 class), and every row carries a chip with the lane's own name (BACKLOG × 5 in the Backlog lane), which says nothing inside a lane.

probe: present qf-studio/pilot-console-ui data-row-meta
probe: present qf-studio/pilot-console-ui PROVIDER_MARKERS\[card.link.provider\]
probe: present qf-studio/pilot-console-ui data-research-layout

## Scope
- `src/design-system/components/ListRow.vue`: below md the row becomes two lines — line 1 the id (no fixed width, no wrap) left and the chip right; line 2 the title full width, truncated, with the meta after it in the muted colour when present. Desktop geometry unchanged. The same markup serves both callers.
- `src/design-system/components/HomeGridLayout.vue`: below md with pin-composer the layout fills the main height (the same min-h-0 flex chain as on desktop), the scroll container scrolls, and the composer slot sits last, above the tab bar padding. Both Home and Research get it from the shared component.
- `src/views/DashboardView.vue`: below md hide the org name in the page row and keep the "Last shipped · Pool" line, left-aligned. The header band keeps the org name.
- `src/views/BoardView.vue` mobile rows: the id uses the sequenceId as served (drop the marker when the sequenceId already starts with it; a board-only card keeps the dot); no status chip in a lane; a held card keeps a single REVIEW chip (ds-green) to mark the sheet target.
- Tests: New file, plain text: src/design-system/components/__tests__/ListRow.spec.ts at innerWidth 390 asserts the two-line structure and that the id cell has no fixed width class; `src/views/__tests__/ChatView.spec.ts` at 390 asserts the composer slot is the last child of the layout; `src/views/__tests__/DashboardView.spec.ts` at 390 asserts one org name in the document; `src/views/__tests__/BoardView.spec.ts` mobile block asserts "GH-101" renders once with no marker and lane rows carry no status chip.
- e2e: the home and board specs add one assertion each — the id cell bounding box is one line high (at most 26 px) and the composer bottom sits within the tab bar height plus 16 px of the viewport bottom on home and research. Regenerate the screenshots; the PR body links the artifact.

## Acceptance
- [ ] `npm test` passes; paste the output into the PR body.
- [ ] `make e2e` passes; paste the output into the PR body.
- [ ] Restore the 64 px id width below md -> the new ListRow.spec.ts 390 assertion fails
- [ ] Render the lane chip on every row -> the BoardView.spec.ts no-chip assertion fails
- [ ] `npm run build` passes; paste the output into the PR body.
- [ ] `npm run lint` passes; paste the output into the PR body.

## Out of scope
Desktop rows. The Approve sheet and the bell sheet (M4, PR#355). The Context column on mobile.

## Refs
- Navigator TASK-637 M5 (review of PR#349/#350/#351/#352 against the 390×844 artifact) · ui#345–#348 · ui#298 (U20)

## Verify

- Playwright artifact mobile-390x844 on the M3 and M4 PRs: Home shows four stat cards, the chart, the queue and the composer above the tab bar with no "$"; Board shows pills and chip rows; Research shows the composer and no Context column; Menu lists Review and Docs.
- Founder check on a phone against the stacked desktop frames.

## Refs

- M1 → https://github.com/qf-studio/pilot-console-ui/issues/345
- M2 → https://github.com/qf-studio/pilot-console-ui/issues/346
- M3 → https://github.com/qf-studio/pilot-console-ui/issues/347
- M4 → https://github.com/qf-studio/pilot-console-ui/issues/348
- M5 → https://github.com/qf-studio/pilot-console-ui/issues/357
