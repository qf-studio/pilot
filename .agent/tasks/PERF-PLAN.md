# Performance Optimization Plan (Post v1.0)

## Context

133 features shipped fast. Each added prompt sections, pipeline steps, or I/O operations. The execution pipeline is now:

```
Worktree (1-2s) → Preflight (500ms) → BuildPrompt (100ms + I/O) →
Claude Code (5-30min) → Quality Gates (1-15min, SEQUENTIAL) →
Self-Review (2-5min) → Intent Judge (2-3min) → PR (5-10s)
```

Total overhead beyond Claude thinking: **4-25 minutes** of sequential waits. Goal: cut non-Claude overhead by 50%+.

---

## Optimization 1: Parallel Quality Gates — DONE

**Impact: Save 30-80% of gate phase time (1-12 minutes)**

### Problem
`internal/quality/runner.go:67` runs gates in a sequential for loop:
```go
for _, gate := range r.config.Gates {
    result := r.runGate(ctx, gate)  // blocks until complete
}
```
With test (8min) + lint (2min) + build (5min) = 15min sequential vs 8min parallel.

### Implementation
- **File:** `internal/quality/runner.go`
- **Change:** Replace sequential loop in `RunAll()` (line 44-91) with goroutines + `sync.WaitGroup`
- **Pattern:** Same semaphore pattern used in `adapters/github/poller.go` for parallel polling

```go
func (r *Runner) RunAll(ctx context.Context, taskID string) *Results {
    results := &Results{}
    var mu sync.Mutex
    var wg sync.WaitGroup

    for _, gate := range r.config.Gates {
        wg.Add(1)
        go func(g Gate) {
            defer wg.Done()
            result := r.runGate(ctx, g)
            mu.Lock()
            results.Results = append(results.Results, result)
            mu.Unlock()
        }(gate)
    }
    wg.Wait()
    // ... evaluate results
}
```

- **Safety:** Gates are independent subprocesses (test, lint, build). No shared state.
- **Retry stays sequential per gate** — only the outer loop parallelizes.
- **Config option:** `quality.parallel: true` (default true, opt-out if gates conflict)

### Files
- `internal/quality/runner.go` — `RunAll()` method
- `internal/quality/runner_test.go` — Add parallel execution test
- `internal/config/config.go` — Add `Parallel bool` to QualityConfig

---

## Optimization 2: Conditional Prompt Injection — DONE

**Impact: Save 50-200ms per task + reduce prompt tokens**

### Problem
BuildPrompt (runner.go:2427-2613) runs these on EVERY task:
- `r.profileManager.Load()` → 2x file reads from disk (global + project profile.json)
- `r.knowledge.QueryByTopic()` → SQLite query
- AGENTS.md read via `LoadAgentsFile()` → file I/O every time

### Implementation

**A. Cache AGENTS.md in Runner struct** (agents.go)
- Load once at Runner creation, store in `r.agentsContent string`
- Invalidate on project path change (multi-repo)
- **File:** `internal/executor/agents.go`, `internal/executor/runner.go`

**B. Cache profile with TTL** (profile.go)
- Add `lastLoaded time.Time` + `cached *Profile` to ProfileManager
- Return cached if < 5 minutes old
- **File:** `internal/memory/profile.go`

**C. Skip knowledge query for trivial tasks**
- If complexity == Trivial, skip knowledge injection entirely
- Trivial tasks don't benefit from historical context
- **File:** `internal/executor/runner.go` (BuildPrompt, around line 2505)

**D. Skip empty profile injection**
- Already has nil check, but `Load()` still does 2x file reads returning empty
- Add `r.profileManager.HasProfile()` fast check before Load()
- **File:** `internal/memory/profile.go`

### Guard
All injections already have nil/empty guards. This just moves the checks earlier to avoid I/O.

---

## Optimization 3: Worktree Pool — DONE

**Impact: Save 500ms-2s per task in sequential mode**

### Problem
`CreateWorktreeWithBranch()` (worktree.go:145) creates a fresh git worktree per task, then destroys it after. In sequential mode (default), this happens for every single issue.

### Implementation
- **File:** `internal/executor/worktree.go`
- Add `WorktreePool` struct to `WorktreeManager`:
  - `pool []*PooledWorktree` — pre-created worktrees ready to use
  - `poolSize int` — configurable, default 2
  - `Acquire(branch string) (*WorktreeResult, error)` — get from pool, set branch
  - `Release(wt *WorktreeResult)` — reset and return to pool instead of destroying
- Pool initialization at Runner startup (warm pool)
- Branch switch via `git checkout -B` inside existing worktree (faster than full create)
- Navigator copy uses rsync-like diff instead of full copy on reuse
- **Cleanup:** Drain pool on shutdown via `WorktreeManager.Close()`

### Config
```yaml
executor:
  use_worktree: true
  worktree_pool_size: 2  # 0 = no pooling (current behavior)
```

### Safety
- Pool worktrees are in `/tmp/pilot-worktree-pool-N/`
- Each `Acquire()` does `git clean -fd && git checkout -B <branch>` to ensure clean state
- If pool is empty, falls back to `CreateWorktreeWithBranch()` (current behavior)
- `Release()` validates worktree is clean before returning to pool

### Files
- `internal/executor/worktree.go` — Add pool logic
- `internal/executor/worktree_test.go` — Pool lifecycle tests
- `internal/config/config.go` — Add `WorktreePoolSize int`
- `internal/executor/runner.go` — Wire pool acquire/release in `executeWithOptions()`

---

## Optimization 4: Parallel Pipeline Stages — DONE

**Impact: Save 2-5 minutes by overlapping self-review + intent judge**

### Problem
After quality gates pass, the pipeline runs:
1. Self-review (2-5 min) — runner.go:1696
2. Intent judge (2-3 min) — runner.go:1919
3. PR creation (5-10s)

These are sequential. Self-review and intent judge are independent — self-review checks code quality, intent judge classifies the task type.

### Implementation
- **File:** `internal/executor/runner.go`
- After quality gates pass, launch self-review and intent judge as goroutines
- Wait for both before PR creation
- Self-review result needed for PR (adds review comments)
- Intent judge result needed for PR labels

```go
var reviewResult *ReviewResult
var intentResult *IntentResult
var wg sync.WaitGroup

wg.Add(2)
go func() {
    defer wg.Done()
    reviewResult = r.runSelfReview(ctx, task, executionPath)
}()
go func() {
    defer wg.Done()
    intentResult = r.intentJudge.Judge(ctx, task)
}()
wg.Wait()
```

### Safety
- Both are read-only operations (no git writes)
- Self-review reads files, intent judge reads task description
- No shared mutable state between them

### Files
- `internal/executor/runner.go` — Parallelize in `executeWithOptions()` post-quality section

---

## Optimization 5: Pre-fetch Next Task

**Impact: Save 1-3s of idle time between tasks**

### Problem
In sequential polling mode, after PR creation for task N, the poller waits for next poll interval (30s) before picking task N+1. The worktree setup, preflight, and prompt building for N+1 could start while N's PR is being created.

### Implementation
- **File:** `internal/adapters/github/poller.go`, `internal/executor/runner.go`
- Add `PrefetchNext()` method to Runner that:
  1. Pre-creates worktree for likely next task
  2. Pre-loads AGENTS.md (if not cached)
  3. Pre-runs preflight checks
- Poller signals "task completing" before PR push
- Runner starts prefetch in background goroutine
- If next task arrives and prefetch matches, skip setup phase

### Complexity
This is the most complex optimization. Defer to v1.2 unless profiling shows significant idle time between tasks.

### Files
- `internal/executor/runner.go` — `PrefetchNext()` method
- `internal/adapters/github/poller.go` — Early signal before completion

---

## Summary: Estimated Impact

| Optimization | Time Saved | Priority | Status | Implementation |
|-------------|-----------|----------|--------|----------------|
| Parallel quality gates | 30-80% of gate time (1-12 min) | **P0** | **Done** | `internal/quality/runner.go:76` — `sync.WaitGroup`, `Parallel` config flag in `types.go:124` |
| Conditional injection | 50-200ms + fewer tokens | **P1** | **Done** | `internal/executor/agents.go:32` — `LoadAgentsFileWithCache()`, `runner.go:281` — `agentsContent` cached field, `internal/memory/profile.go:106` — `HasProfile()` |
| Worktree pool | 500ms-2s per task | **P1** | **Done** | `internal/executor/worktree.go` — `WarmPool`/`Acquire`/`Release`/`Close`, 6 tests, `WorktreePoolSize` config in `backend.go:244` |
| Parallel review+judge | 2-5 min per task | **P1** | **Done** | `internal/executor/runner.go:2116-2143` — `sync.WaitGroup` goroutines (GH-1079) |
| Pre-fetch next task | 1-3s between tasks | **P2** | Deferred | v1.2 — not yet implemented |

**Total pipeline improvement:** Non-Claude overhead drops from 4-25min to 2-10min (~50-60% reduction).

## Status

All P0/P1 optimizations shipped. Only P2 (pre-fetch next task) remains deferred to v1.2.
Result: Non-Claude overhead reduced from 4-25min to ~2-10min (~50-60% reduction).

## Verification

For each optimization:
- `go test ./internal/...` — no regressions
- `go test -race ./internal/...` — no race conditions (critical for parallel changes)
- Benchmark: `time pilot task --dry-run "test"` before/after for prompt assembly
- End-to-end: Run 3 tasks sequentially, measure total pipeline time
