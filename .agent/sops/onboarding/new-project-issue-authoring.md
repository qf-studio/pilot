# SOP: Authoring Pilot Issues for a New Project

**Category:** onboarding
**Created:** 2026-07-03
**Trigger:** Onboarding any new repo into `~/.pilot/config.yaml` `projects:`, or filing the first batch of `pilot`-labeled issues against a fresh codebase.

## Why this exists

2026-06-30 live incident (`alekspetrov/ai-coding-summit`, Next.js/pnpm): first Pilot run on a new repo failed end-to-end — issues wrongly spec-rejected, tasks self-bootstrapped conflicting scaffolds, autopilot spun a conflict close→re-execute loop through 3 PRs. Every failure mode was already documented in the knowledge graph; the issues were authored without consulting it. This SOP is the checklist that prevents the repeat.

Related memories: `learning_pilot_issue_spec_guard_headers`, `feedback_pilot_issue_section_headers`, `feedback_use_nav_task_for_issues`, `feedback_conventional_title_required`, `bug_handleconflict_no_refile`, `bug_inherited_spec_full_reimplement`.

## Rule 1 — Scaffold lands first, alone

The **first issue on any new repo is scaffold-only** (package manifest, tsconfig/go.mod, test config, lint config, base lib files). No dependencies, no features. **Wait for its PR to merge to `main` before any dependent issue is labeled `pilot`.**

Why: dependent tasks that find no scaffold self-bootstrap their own (each invents its own `package.json`/`Makefile`) → every PR conflicts with every other → autopilot close→re-execute churn.

## Rule 2 — H2 section headers, exact set

The spec validator (`internal/adapters/github/spec_validator.go`) requires at least one **H2** header from: `## Acceptance`, `## Implementation`, `## Context`, `## Background`, `## Approach`, `## Design`, `## Refs`.

- `### Acceptance criteria` (H3) **fails** the check today → issue gets `pilot-spec-incomplete` then `pilot-blocked` within two poll cycles.
- Always author `## Context` + `## Acceptance` + `## Implementation` as H2. Sub-structure below them is free.
- Escape hatch for trivial issues: `pilot-skip-spec-check` label.

## Rule 3 — Dependencies are `#N` refs, never prose

The poller's dependency gate (`ParseDependencies`, poller.go) only matches `Blocked by: #N` / `Depends on: #N` / `Requires: #N` with a **numeric issue ref**. Prose like `Blocked by: TASK-01 scaffold` is **silently ignored** — the issue dispatches immediately.

Flow: file the scaffold issue first → capture its number → every dependent body carries `Blocked by: #<that number>`.

## Rule 4 — Serialize anything that touches shared root files

The parallel scope-overlap guard keys on **directories** named in issue bodies; two issues that both create root files (`package.json`, `tsconfig.json`, lockfiles) are NOT detected as overlapping. Until that's fixed in code, chain such issues with `Blocked by: #N` so they run one at a time.

## Rule 5 — Conventional-commit titles

`type(scope): description` — autopilot rejects PR creation otherwise.

## Rule 6 — Per-project config before the first run

In `~/.pilot/config.yaml` for the new project:

1. **Quality gates**: the global `quality.gates` (typically `make build/test/lint` for the Pilot repo) applies to **every** project — a repo without a Makefile fails every gate. Until per-project overrides exist, either adjust global gates or ensure the scaffold provides matching `make` targets that map to the repo's real commands (e.g. Makefile wrapping `pnpm build/test/lint`).
2. **Execution mode**: prefer `orchestrator.execution.mode: auto` (parallel + overlap guard) over bare `parallel` once the `auto` wiring fix ships; before that, `sequential` is the safe choice for a brand-new repo's first batch.
3. **Token durability**: launch with `GITHUB_TOKEN=$(gh auth token) pilot start ...` or a fine-grained PAT in config. A dead token 401s **all** polling silently.

## Pre-flight checklist (before adding the `pilot` label)

- [ ] Scaffold issue merged to `main` (Rule 1) — or this IS the scaffold issue
- [ ] Body has H2 `## Context` / `## Acceptance` / `## Implementation` (Rule 2)
- [ ] All deps expressed as `Blocked by: #N` (Rule 3)
- [ ] No two open issues create the same root files (Rule 4)
- [ ] Title is `type(scope): description` (Rule 5)
- [ ] Project entry + gates + token verified in config (Rule 6)
- [ ] Issue authored via `nav-task` → task doc → `nav-pilot` handoff (not ad-hoc `gh issue create`)

## Refs

- Plan: `.agent/tasks/TASK-378-new-project-onboarding-hardening.md`
- Code fixes tracked as `pilot` issues B0–B5 (see task doc)
