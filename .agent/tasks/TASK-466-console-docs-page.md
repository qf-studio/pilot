# TASK-466: Console Docs page — surface Navigator `.agent/` docs, later make them manageable

**Status**: 🎨 Design APPROVED 2026-08-11 (`design/docs-v1.html` @ pilot-console-ui, commit 741b8d9). No issue filed, nothing dispatched — read leg needs contract research first.
**Created**: 2026-08-11
**Assignee**: —

## Idea (founder, 2026-08-11)

Every Pilot-managed repo carries Navigator docs (`.agent/` — system docs, SOPs, task plans,
knowledge memories). Today they are invisible outside the repo. Add a **Docs page** to the
console so the operator can *see* the project's living documentation; a later phase makes it
*manageable* (edit/create/archive from the console).

## Phasing

1. **Design only (now)** — ✅ DONE, founder-approved 2026-08-11: `design/docs-v1.html` @
   pilot-console-ui. Third v4 layout template — full-height tree sidebar (flush-left text,
   hairline border-right) | centered 740px reading column | borderless "On this page"
   heading anchors (hidden <1560px). Project picker + Search (⌘K) in the sidebar top;
   no in-content page title (topbar owns it — same dedupe rule as board v1).
2. **Read leg (later)** — console serves the connected repo's `.agent/` tree (likely via
   daemon proxy, C13/C14 idiom; contract TBD — needs research like C16/C17 got).
3. **Manage leg (much later)** — edit/create from console; writes must respect Navigator
   conventions (graph sync, frontmatter). Explicitly out of scope until read leg proves out.

## Design notes

- Rail gains a Docs icon (all v4 pages get it for shell consistency when this ships).
- Knowledge counts are real per repo (pilot today: 10 patterns · 10 pitfalls · 19 learnings ·
  1 decision) — good glanceable signal of how much the agent has learned per project.
- Filters/project selector scope the tree; ties into chat panel (`nav-graph`-style queries —
  "what do we know about X?" — natural companion on this page).

## Refs

- Design language: `design/dashboard-v4.html` · `design/board-v1.html` · `design/dashboard-v4-spec.md` @ pilot-console-ui
- Mockup (working copy): `/tmp/pilot-docs-design.html` on :8899
