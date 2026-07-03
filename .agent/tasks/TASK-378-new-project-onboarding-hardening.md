# TASK-378: New-Project Onboarding Hardening

**Status**: 🚧 In Progress (Lever A shipped; Lever B dispatched to Pilot)
**Created**: 2026-07-03

---

## Context

**Problem**: Running Pilot on a fresh, non-Go repo fails on the first run, every time. Live-observed 2026-06-30 on `alekspetrov/ai-coding-summit` (Next.js/pnpm): wrong spec-rejections (H3 headers), self-bootstrapped conflicting scaffolds, autopilot conflict close→re-execute churn across 3 PRs. Root cause is a cluster of known-but-unmitigated failure modes that compound only on new projects — Pilot treats every repo like the Pilot repo (Go + Makefile + globally-tuned config).

**Goal**: A new project succeeds on the **first** run with no manual babysitting.

## Acceptance Criteria

- [ ] Lever A SOP exists and is linked from CLAUDE.md workflow (`.agent/sops/onboarding/new-project-issue-authoring.md`)
- [ ] B0–B5 filed as `pilot` issues with H2 headers, `#N` deps, conventional titles (dogfooding the SOP)
- [ ] End-to-end proof: a throwaway Node/pnpm repo with a scaffold-first, `#N`-linked issue set goes issue→PR cleanly on the first daemon run — scaffold merges first, dependents build on it, zero conflict loop

## Implementation

### Lever A — Process (manual, done first)
- SOP: `.agent/sops/onboarding/new-project-issue-authoring.md` (6 rules + pre-flight checklist)

### Lever B — Code fixes (dispatched to Pilot, ranked)

| ID | Issue | Fix | Where | Effort |
|----|-------|-----|-------|--------|
| B0 | [#3713](https://github.com/qf-studio/pilot/issues/3713) | Spec validator accepts H2–H6 headers (`^##\s+` → `^#{2,6}\s+`) | `internal/adapters/github/spec_validator.go:22` | XS |
| B1 | [#3716](https://github.com/qf-studio/pilot/issues/3716) | Per-project quality gates (`ProjectConfig.Quality`) + pnpm/yarn/bun detection | `internal/config/config.go:216`, `internal/quality/types.go:266-323`, factory sites `cmd/pilot/main.go:387/1016/1425` | M |
| B2 | [#3714](https://github.com/qf-studio/pilot/issues/3714) | Overlap guard detects root-file collisions (package.json/tsconfig/lockfiles) | `internal/adapters/github/poller.go:1040` | M |
| B3 | [#3715](https://github.com/qf-studio/pilot/issues/3715) | Cap auto-rebase oscillation (no counter at `controller.go:2259`) + persist in-memory `maxFailedRetries` (`poller.go:381`) | `internal/autopilot/controller.go`, `internal/adapters/github/poller.go` | S–M |
| B4 | [#3717](https://github.com/qf-studio/pilot/issues/3717) | Wire `execution.mode: "auto"` → `ExecutionModeAuto` (both call-sites; today it silently runs sequential) ⚠️ behavior change, release-note | `cmd/pilot/main.go:750-768`, `~2191` | S |
| B5 | [#3718](https://github.com/qf-studio/pilot/issues/3718) | `gh auth token` fallback + loud doctor 401 check | token resolution sites in `cmd/pilot/main.go`, `doctor` cmd | S |

Dependency chain for main.go-touching fixes: #3716 (B1) → #3717 (B4) → #3718 (B5), serialized via `Blocked by: #N`. B0/B2/B3 independent.

## Out of Scope

- Decomposer rewrite (`bug_inherited_spec_full_reimplement` cluster) — B2 is the partial mitigation
- B6 ghost-close skip (`bug_ghost_close_db_lockout`, tracked #2476) — separate track
- M7 studio-sdk github cutover (TASK-368)
- Non-GitHub adapters' onboarding

## Verify

```bash
make build && make test && make lint   # per B-fix, on the Pilot repo
```
E2E: throwaway pnpm repo, scaffold-first issue set, one clean PR per issue, no conflict loop.

## Refs

- Plan: `~/.claude/plans/new-project-onboarding-hardening-compiled-bengio.md` (session artifact)
- SOP: `.agent/sops/onboarding/new-project-issue-authoring.md`
- Evidence memories: `learning_pilot_issue_spec_guard_headers`, `bug_handleconflict_no_refile`, `bug_inherited_spec_full_reimplement`, `bug_ghost_close_db_lockout`
- Issue links: filled in below after dispatch

**Last Updated**: 2026-07-03
