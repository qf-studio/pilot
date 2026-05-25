# TASK-289: Flip `quality.parallel` default to false

**Wave:** 1 (XS) · **Parallel-safe with TASK-290, TASK-291** · **Audit ref:** §2 Action #5, §3.7 P1, §3.3 P1

---

## Problem

2026-05-21 workshop hit 11 spurious quality-gate failures in 3 hours. Root cause: concurrent `make build` / `make test` / `make lint` in `internal/quality/runner.go:74` race on shared `~/.cache/go-build` and `~/.cache/golangci-lint`. User's local config already patched (`parallel: false`); the project-wide default still ships as `true`.

Until per-gate `GOCACHE` isolation lands as a separate effort, flipping the default eliminates the incident class.

## Approach

### Step 1 — Change default (XS, ~15 min)

- `internal/quality/types.go:133-138`: change `IsParallel()` so a nil `Parallel` pointer returns `false` (currently returns `true`)
- `internal/quality/runner.go:62-83`: no logic change — just confirm the new default flows through

### Step 2 — Update test (XS, ~15 min)

- Update existing `TestRunner_RunAll_ParallelExecution` to explicitly set `Parallel: true` rather than relying on default
- Add `TestRunner_RunAll_DefaultsToSequential` asserting the new behavior

### Step 3 — Document (XS, ~15 min)

- New SOP: `.agent/sops/quality/parallel-gate-cache-race.md` — describes the race, points users at `parallel: true` opt-in with caveat about cache isolation, references this task

## Files to modify

- `internal/quality/types.go`
- `internal/quality/runner_test.go`
- New: `.agent/sops/quality/parallel-gate-cache-race.md`

## Test Strategy

- Unit: `TestRunner_RunAll_DefaultsToSequential` — construct `Runner` with default config, assert sequential execution
- Integration: existing parallel test still passes with explicit opt-in
- Manual: run `pilot start` and confirm logs show sequential gate execution by default

## Effort

XS (~45 min total). One PR. No file collisions with TASK-290 or TASK-291.

## Out of Scope

Per-gate `GOCACHE` isolation (would require setting per-gate `GOCACHE`/`GOLANGCI_LINT_CACHE` env vars). Defer to Wave 4+.
