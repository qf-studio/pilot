> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-305: Workspace lifecycle hooks (after_create / before_run / after_run / before_remove)

**Status**: queued
**Created**: 2026-05-26
**Severity**: P2
**Effort**: M (~4h)
**Job (JTD)**: J2 Hand-off
**Source**: Symphony research, Wave 5 / `~/.claude/plans/let-s-plan-that-use-staged-seal.md`
**Depends on**: TASK-304 (consumes `hooks:` block from `.pilot/workflow.yaml`)

---

## Context

**Problem**: Pilot's worktree setup is largely hardcoded. Per-project bootstrap (run `mise`, install deps, build artifacts, snapshot logs on exit) requires either Pilot-side code changes or implicit conventions. Customers cannot inject project-specific lifecycle steps without forking.

**Goal**: Four shell-script hook points executed by Pilot around each session, configured from `.pilot/workflow.yaml`:

| Hook | When | Use case |
|---|---|---|
| `after_create` | Right after worktree created, before agent starts | Install deps, run `mise install`, fetch submodules |
| `before_run` | Just before agent prompt is sent | Set env vars, warm caches |
| `after_run` | After agent finishes (any status) | Snapshot artifacts, run cleanup linter |
| `before_remove` | Just before worktree is destroyed | Archive logs, persist artifacts |

Borrowed from Symphony's workspace hooks (`/tmp/symphony/elixir/lib/symphony_elixir/workspace.ex`).

**Why now**: Builds on TASK-304. Without hooks, the workflow.yaml is half a story.

---

## Acceptance Criteria

- [ ] Each hook is an optional shell script (or list of shell scripts) declared under `hooks:` in `.pilot/workflow.yaml`.
- [ ] Hooks execute in the worktree directory with the same env as the executor.
- [ ] `after_create` failure aborts the run with status `setup_failed`.
- [ ] `before_run` failure aborts the run with status `setup_failed`.
- [ ] `after_run` and `before_remove` failures log warnings but don't change the run's success status (cleanup-best-effort).
- [ ] Each hook has a configurable timeout (default 300s).
- [ ] Hook stdout/stderr captured into executor logs (visible in TUI LOGS panel).
- [ ] Hooks can read env vars `$PILOT_TASK_ID`, `$PILOT_BRANCH`, `$PILOT_ISSUE_URL`, `$PILOT_WORKTREE`.

---

## Implementation

### Phase 1: Hook runner
**Tasks**:
- [ ] Create `internal/executor/workflow/hooks.go` with `RunHook(ctx, name, workflow, env)` function.
- [ ] Resolve hook string from `workflow.Hooks[name]`; support single string or list.
- [ ] Execute via `bash -lc "<script>"` in worktree dir.
- [ ] Apply timeout; stream stdout/stderr to executor log.

**Files**:
- `internal/executor/workflow/hooks.go` (new)
- `internal/executor/workflow/hooks_test.go` (new)

### Phase 2: Executor wiring
**Tasks**:
- [ ] In `internal/executor/runner.go`, call hooks at the 4 lifecycle points:
  - After `WorktreeCreate`: `after_create`.
  - Before `SendPrompt`: `before_run`.
  - After agent finishes: `after_run`.
  - Before `WorktreeDestroy`: `before_remove`.
- [ ] Honor abort semantics for `after_create` and `before_run`.

**Files**:
- `internal/executor/runner.go`

### Phase 3: Env + observability
**Tasks**:
- [ ] Populate hook env: `PILOT_TASK_ID`, `PILOT_BRANCH`, `PILOT_ISSUE_URL`, `PILOT_WORKTREE`.
- [ ] Add `executions.status = 'setup_failed'` (new) for hook-driven aborts.
- [ ] Add Prometheus counter `pilot_hook_runs_total{hook="after_create",status="ok"}`.

**Files**:
- `internal/executor/runner.go`
- `internal/memory/store.go`

---

## Out of Scope

- Hook templating (no `{{ task_id }}` substitution; env vars only).
- Hook composition / inheritance (each hook is a single shell snippet or list).
- Per-hook resource limits beyond timeout (no CPU/memory caps in v1).
- Cross-platform hooks (assume Unix shell; Windows worktrees not supported anyway).

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Hook execution | Direct exec, bash -lc, sh -c | `bash -lc` | Matches Symphony; loads user profile (mise, asdf, nvm work) |
| List support | Single string only, list of strings | List of strings (sequential) | Common pattern (e.g., install deps + warm cache) |
| Failure semantics | Always abort, never abort, per-hook | Per-hook (after_create + before_run abort; rest warn) | Mirrors Symphony; matches lifecycle intent |
| Timeout default | 60s, 300s, 600s, none | 300s | Compile/install can be slow; configurable |

---

## Files Affected (estimate)

- `internal/executor/workflow/hooks.go` (new)
- `internal/executor/workflow/hooks_test.go` (new)
- `internal/executor/runner.go`
- `internal/memory/store.go` (new status value)

---

## Verify

```bash
go test ./internal/executor/workflow/...

# Dogfood: add a `hooks.after_create: ["mise install"]` to Pilot's own .pilot/workflow.yaml;
# verify executor logs show hook ran; verify env vars passed correctly.
```

---

## Done

- [ ] Hook runner shipped with tests
- [ ] Executor invokes 4 hooks at correct lifecycle points
- [ ] Abort semantics correct (`after_create`/`before_run` abort; `after_run`/`before_remove` warn)
- [ ] Env vars available inside hooks
- [ ] `setup_failed` status visible in dashboard
- [ ] Pilot's own repo dogfoods at least one hook

---

## Refs

- Master plan: `~/.claude/plans/let-s-plan-that-use-staged-seal.md`
- Symphony evidence: `/tmp/symphony/elixir/lib/symphony_elixir/workspace.ex` (hook lifecycle)
- Symphony spec: `/tmp/symphony/SPEC.md` §3 (workspace lifecycle)
- Parent: `TASK-304` (workflow.yaml)

---

**Last Updated**: 2026-05-26
