# TASK-292: `safeGo()` panic-recovery wrapper sweep

**Wave:** 2 (S) · ~~Merge AFTER TASK-293~~ ✅ TASK-293 done (archived, `84273ab8`) · **Audit ref:** §2 Action #2, §3.4 P1 (CS-3), §3.2 P1

> **🚀 HANDED OFF 2026-06-11 — issue [#3573](https://github.com/qf-studio/pilot/issues/3573)** → **❌ FAILED ("quality gates failed after 2 auto-retries"), root-caused same day:**
>
> 1. **Test gate failed legitimately**: first-attempt `safego.go` had an unsynchronized `panicCounter` global → data race under the gate's `go test -race`. Worker self-validated with targeted `go test` (no `-race`, no full suite) + `golangci-lint --new-from-rev` → believed green. **Issue specs must require running the literal gate commands (`make test` = `go test -v -race ./...`).**
> 2. **Lint gate failure is PRE-EXISTING on main**: clean `aee38002` (origin/main) fails `make lint` — 7 unused funcs in `cmd/pilot/handlers.go` (Jira/Asana/ADO `*WithResult` chain). Reproduced in a clean worktree. Didn't gate the run (`required: false`) but polluted retry feedback. Broken somewhere in `193dc2d6..aee38002`.
> 3. **NEW DAEMON BUG — quality-retry CWD**: `runner.go:3045` re-invokes the backend with `ProjectPath: task.ProjectPath` (daemon repo root) instead of `executionPath` (worktree). Same bug at `:3324` (intent retry). TASK-323 fixed this for `:2375`/`:2739` but missed these two. Retry workers landed in the main repo root, followed interactive CLAUDE.md, created `.claude/worktrees/safego-sweep`, **reimplemented the task from scratch (correctly, with mutex)** — invisible to gates re-testing the unchanged temp worktree → guaranteed failure loop.
> 4. **Retry feedback truncation**: gate output in the retry prompt is head-truncated — all PASS lines kept, the actual race/FAIL report cut off. Retry workers never saw what failed.
> 5. Side finding: executor unit tests spawn the real `claude` binary (epic decomposition prompt, fire-and-forget) on every `make test` — needs mocking/guarding.
>
> **✅ SHIPPED 2026-06-11: PR [#3575](https://github.com/qf-studio/pilot/pull/3575) squash-merged to main (`141ba8a3`, 15:13Z)** — the retry worker's rewrite (`271df9ef`), recovered + merged manually after CI green (lint/test/secrets). Also fixes main's pre-existing lint break (deletes the 7 dead functions). #3573 auto-closed. Follow-ups filed: retry-CWD bug → [#3577](https://github.com/qf-studio/pilot/issues/3577), retry-feedback truncation → [#3578](https://github.com/qf-studio/pilot/issues/3578) (both MANUAL, no pilot label). TASK-320/355 live verification still pending — this run failed, so the next clean dispatched task carries it.

---

## Problem

`rg 'recover\(\)'` returns 0 hits in production code (only in tests). 15+ production `go func() {…}()` sites have no panic recovery. A nil-deref in any adapter callback, RSS sampler, or worker crashes the entire daemon and loses every in-flight task plus the alerting pipeline.

## Approach

### Step 1 — Build the helper (S, ~30 min)

- New: `internal/logging/safego.go`:
  ```go
  func SafeGo(component string, fn func()) {
      go func() {
          defer func() {
              if r := recover(); r != nil {
                  stack := debug.Stack()
                  slog.Error("goroutine panic recovered",
                      "component", component,
                      "panic", r,
                      "stack", string(stack))
                  if panicCounter != nil {
                      panicCounter.WithLabelValues(component).Inc()
                  }
              }
          }()
          fn()
      }()
  }
  ```
- Counter registration: `internal/gateway/prometheus.go` add `pilot_panics_total{component}`
- Wire `panicCounter` via init or setter from `prometheus.go`

### Step 2 — Sweep production goroutines (S, ~90 min)

Replace `go func(){…}()` with `logging.SafeGo("component-name", func(){…})` at:

- `internal/autopilot/controller.go:1700` — release-summary
- `internal/executor/parallel.go:129` — sub-agent fan-out
- `internal/executor/runner.go:2892` — self-review
- `internal/executor/runner.go:2902` — intent judge
- `internal/webhooks/manager.go:78` — event delivery
- `internal/executor/dispatcher.go:437` — worker spawn
- `internal/adapters/github/poller.go:1127` — issue dispatch
- `internal/gateway/server.go:247` — HTTP handler
- `internal/executor/backend_claudecode.go:459,506,544,608,621` — backend goroutines (5 sites)
- `internal/executor/backend_qwencode.go:284,328,362,409,422` — backend goroutines (5 sites)
- `internal/executor/rss_sampler.go:20` — RSS poll loop
- `internal/approval/manager.go` — cancel goroutine

Pick a distinct `component` label per package to enable per-component alerting.

### Step 3 — Tests + manual validation (S, ~30 min)

- `internal/logging/safego_test.go`:
  - `TestSafeGo_RecoversFromPanic` — call `SafeGo` with `func(){panic("x")}`, assert no crash, log captured
  - `TestSafeGo_NormalCompletion` — assert non-panicking fn runs
  - `TestSafeGo_IncrementsCounter` — inject a stub counter, assert increment
- Manual: insert `panic("test")` in one swept site; run a task; observe daemon survives and `pilot_panics_total{component="..."}` increments in `/metrics`

## Files to modify

- New: `internal/logging/safego.go`
- New: `internal/logging/safego_test.go`
- `internal/gateway/prometheus.go` (counter registration)
- 15+ sweep sites listed above

## Test Strategy

- Unit tests on the wrapper
- Manual panic-injection smoke test before merge

## Effort

S (~2.5h total). One PR; mechanical replacements are reviewable as a single diff.

## Out of Scope

- Adding `AlertTypeOOMKilled` (mentioned in §3.4 P1) — separate task in Wave 4+
- Per-component alert rules on `pilot_panics_total` — operator config, not code
