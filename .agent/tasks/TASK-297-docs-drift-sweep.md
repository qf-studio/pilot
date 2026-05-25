# TASK-297: Documentation drift sweep + `{CURRENT_VERSION}` interpolation

**Wave:** 3 (M) · **Can ship anytime; zero code dependencies** · **Audit ref:** §2 Action #6, §3.6 (P1×5, P2×11, P3×11, P4×5)

---

## Problem

Documentation is severely out of sync with code reality:
- CLAUDE.md claims `v2.53.0 | 316 features` (real: v2.149.3) — auto-loaded into every session
- FEATURE-MATRIX.md frozen at v2.53.0 — 90+ versions of features undocumented
- ARCHITECTURE.md frozen at v2.56.0 (3 months / 93 versions behind)
- CLI flag `--autopilot=` documented as primary in 4 places (it's a hidden alias for `--env=`)
- `pitfall_git_reset_hard_in_automated_sync.md` cites live call sites for fixes that already shipped (v2.146.7)
- `docs-and-history.md` says "Nextra v2.13.0 + Next.js 14" (real: ^4.0.0 + ^15.0.0); the "pin Nextra v2 deps" gotcha is obsolete and dangerous
- 4+ `docs/content/**/*.mdx` files hardcode `v2.102.2` strings

Auto-loaded context lying to every session has very high leverage.

## Approach

### Step 1 — Top-level docs (M, ~60 min)

- `CLAUDE.md`:
  - Delete "Current Status" v2.53/316 block (line 239); replace with one-liner linking to `docs/lib/version.ts` and `.agent/DEVELOPMENT-README.md "Current State"`
  - Fix Nextra v2/v4 contradiction (line 145 says v2, line 256 says v4 — keep v4)
  - Replace "Project Structure" tree (lines 130-147) — either list all 28 internal packages or replace with link to ARCHITECTURE.md
  - Fix "Required env vars" overstatement (lines 196-198) — re-label as per-adapter
  - Expand "Project Overview" adapter list (lines 100-104) to match current support (10+ adapters)
  - Soften "Forbidden Actions" package.json rule (line 224) given Nextra v4 migration
  - Remove duplicated "Pilot runs in a separate terminal" text (line 69)
- `.agent/DEVELOPMENT-README.md`:
  - Replace `--autopilot=ENV` with `--env=ENV` (lines 114, 274)
  - Fix Required env vars (lines 267-269)
  - Update Release Workflow example to v2.X.Y (line 249)
  - Re-verify `~/.local/bin` PATH "Known Issue (GH-204)" (line 261); update or remove
- `.agent/system/PR-CHECKLIST.md`: replace `--autopilot=prod` with `--env=prod`

### Step 2 — Architecture + Feature Matrix (M, ~75 min)

- `.agent/system/FEATURE-MATRIX.md`:
  - Rewrite header (line 3) to drop fossilized `(v2.53.0)` label
  - Add "Recent v2.x" appendix linking to git log + GitHub Releases OR auto-generate per-version sections from `git log --grep "^feat"`
  - Fix v2.147/v2.148 mistag on OOM hardening rows (lines 40-42)
  - Add rows for v2.149.x work: repo-allowlist Phase B, syncMainBranch `--ff-only`, empty-set guard + IsEpic suppression
- `.agent/system/ARCHITECTURE.md`:
  - Refresh "Last Updated" (line 3)
  - Refresh "Recent Changes" table (lines 712-717) — currently ends at v2.56.0
  - Re-run package audit (line 374 area) — currently lists 34, actual is 28
  - Fix `internal/autopilot/board_sync.go` reference → `internal/adapters/github/project_board.go` (line 374)
- `.agent/system/docs-and-history.md`:
  - Fix Nextra v4 / Next 15 references (lines 6, 28)
  - Remove obsolete "Nextra v2 deps must be pinned" gotcha

### Step 3 — Nextra docs site (M, ~60 min)

- Introduce `{CURRENT_VERSION}` MDX interpolation in:
  - `docs/content/getting-started/installation.mdx:48` (currently `# pilot version v2.102.2`)
  - `docs/content/cli/commands.mdx:22, 41, 50, 589, 2601, 2611-12` (multiple `v2.102.2` + `--autopilot=`)
  - `docs/content/features/dashboard.mdx:210, 219` (upgrade-dialog example)
- Verify `docs/lib/version.ts` syncs from latest tag — auto-sync workflow apparently didn't fire for v2.149.3 PR #3054; investigate `.github/workflows/docs-version-sync.yml`

### Step 4 — Navigator memory hygiene (S, ~45 min)

- New convention: add `resolved: <date> (<version>)` frontmatter field; introduce `.agent/knowledge/memories/pitfalls/resolved/` subdir
- `.agent/knowledge/memories/pitfalls/pitfall_git_reset_hard_in_automated_sync.md`:
  - Add `resolved: 2026-05-20 (v2.146.7)` frontmatter
  - Move file to new `pitfalls/resolved/`
  - Rewrite lead as "Pattern resolved — see SOP at `.agent/sops/git/never-reset-hard-in-automated-flows.md`"
- `.agent/knowledge/memories/learnings/feedback_subprocess_not_api.md:24`:
  - Verify `internal/executor/subtask_parser.go` deletion (grep confirms file missing)
  - Either add `resolved:` line OR correct the citation if functionality moved elsewhere
- `.agent/knowledge/memories/patterns/pattern_decomposer_label_evaporation.md:9-10`: refresh line citations (currently `epic.go:883, :1003`; today line 883 is in `recoverExistingSubIssues`)
- `.agent/knowledge/memories/pitfalls/bug_handleconflict_no_refile.md:7`: refresh `controller.go:1688-1729` → current location (~line 1741)
- `.agent/knowledge/graph.json`: rebuild after archiving TASK-288 + adding TASK-296/298 (graph `updated` is 2026-05-21, missing v2.149.x)

### Step 5 — Verification (~30 min)

- `make docs-build` succeeds
- `make docs-dev` shows interpolated current version on installation page
- `nav-graph "git reset"` returns 0 active pitfalls (the resolved one filtered out)
- Spot-check 3-5 of the changed sections render correctly

## Files to modify

- `CLAUDE.md`
- `.agent/DEVELOPMENT-README.md`
- `.agent/system/{FEATURE-MATRIX,ARCHITECTURE,PR-CHECKLIST,docs-and-history}.md`
- `.agent/knowledge/memories/pitfalls/*.md` (file move + edits)
- `.agent/knowledge/memories/learnings/feedback_subprocess_not_api.md`
- `.agent/knowledge/memories/patterns/pattern_decomposer_label_evaporation.md`
- `.agent/knowledge/graph.json` (rebuild)
- `docs/content/getting-started/installation.mdx`
- `docs/content/cli/commands.mdx`
- `docs/content/features/dashboard.mdx`
- Possibly `.github/workflows/docs-version-sync.yml` (debug missing v2.149.3 trigger)
- New: `.agent/knowledge/memories/pitfalls/resolved/` directory

## Test Strategy

- Build: `make docs-build` clean
- Visual: `make docs-dev`, verify version interpolation on installation/commands pages
- Navigator: `nav-graph` queries return current state
- Manual: grep `--autopilot=` across docs/ + .agent/ → 0 hits in non-deprecation contexts

## Effort

M (~4.5h total — large surface, mostly mechanical). One coordinated PR is fine; alternatively split into "top-level docs" + "nextra docs" + "navigator hygiene" if review fatigue hits.

## Out of Scope

- Full Navigator memory re-audit (every memory's line citations) — covered for the 4 most stale; full sweep is a separate task
- Auto-generation of FEATURE-MATRIX from git log — large new tooling; this task does the manual update as a stopgap
- Rewriting CHANGELOG / release notes (currently absent) — separate task if desired
