# Wire Budget Enforcement into Polling Mode and Executor Stream

**Status**: 🚧 Ready for Pilot
**Created**: 2026-02-06

---

## Context

**Problem**:
Budget enforcement (`internal/budget/`) is fully implemented but only wired into `pilot task --budget`. The two main execution paths — polling mode (`pilot start`) and per-task token limits — don't check or enforce budgets.

**Goal**:
Wire `Enforcer.CheckBudget()` into polling mode handlers and `TaskLimiter` into the executor's stream parser so budget limits are enforced across all execution paths.

**Success Criteria**:
- [ ] `pilot start --github` checks budget before executing each issue
- [ ] `pilot start --telegram` checks budget before executing each task
- [ ] Per-task token/duration limits kill runaway tasks mid-execution
- [ ] All existing tests pass
- [ ] New tests cover budget enforcement in polling and executor

---

## Implementation Plan

### Phase 1: Wire Budget Check into Polling Mode Handlers

**Goal**: Every task picked up by polling adapters goes through `Enforcer.CheckBudget()` before execution.

**Tasks**:
- [ ] Add budget check to `handleGitHubIssueWithResult()` in `cmd/pilot/main.go` (~line 1836, before `task := &executor.Task{...}`)
- [ ] Add budget check to `handleGitHubIssue()` in `cmd/pilot/main.go` (~line 1601, before task creation)
- [ ] Add budget check to `handleLinearIssueWithResult()` in `cmd/pilot/main.go` (~line 2020, before task creation)
- [ ] Add budget check to Telegram task execution path
- [ ] If budget exceeded, return error with reason (don't execute task)
- [ ] Log budget status (remaining daily/monthly) when task is allowed

**Reference pattern** (`pilot task --budget`, lines 2556-2606 in main.go):
```go
budgetConfig := cfg.Budget
if budgetConfig == nil {
    budgetConfig = budget.DefaultConfig()
}
enforcer := budget.NewEnforcer(budgetConfig, store)
result, err := enforcer.CheckBudget(ctx, "", "")
if !result.Allowed {
    // Block task, return error with result.Reason
}
```

**Key**: Memory store is already initialized in `runPollingMode()` at line 900. Budget config is available via `cfg.Budget`. Pass these to handlers rather than re-creating.

**Files**:
- `cmd/pilot/main.go` — Add budget checks to 3-4 handler functions

### Phase 2: Wire TaskLimiter into Executor Stream

**Goal**: Mid-execution kill switch when per-task token or duration limits are exceeded.

**Tasks**:
- [ ] Accept `*budget.TaskLimiter` in `runner.Execute()` or set it on the Runner
- [ ] In `processBackendEvent()` (~line 1852 in runner.go), after `reportTokens()`, call `limiter.AddTokens(event.TokensInput + event.TokensOutput)`
- [ ] If `limiter.AddTokens()` returns false, cancel execution context
- [ ] Periodically call `limiter.CheckDuration()` (e.g., every N events, not every event)
- [ ] Report budget exceeded via `reportProgress()` before cancelling
- [ ] Create TaskLimiter in polling handlers using `enforcer.GetPerTaskLimits()`

**Token flow** (existing):
```
Execute() → EventHandler callback → processBackendEvent()
  → state.tokensInput += event.TokensInput   [line 1844]
  → state.tokensOutput += event.TokensOutput  [line 1845]
  → reportTokens()                            [line 1852] ← INSERT HERE
```

**Files**:
- `internal/executor/runner.go` — Add limiter check in `processBackendEvent()`
- `cmd/pilot/main.go` — Create TaskLimiter and pass to runner

### Phase 3: Tests

**Tasks**:
- [ ] Test: polling handler blocks task when daily budget exceeded
- [ ] Test: polling handler allows task when budget OK
- [ ] Test: executor cancels when token limit exceeded mid-execution
- [ ] Test: executor cancels when duration limit exceeded
- [ ] Test: TaskLimiter reset doesn't affect other tasks
- [ ] Verify all existing tests still pass

**Files**:
- `cmd/pilot/main_test.go` or new `budget_integration_test.go`
- `internal/executor/runner_test.go` — Add limiter tests

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Enforcer lifecycle | Per-handler vs shared | Shared per polling session | Avoid re-creating store connections; memory store already exists in polling mode |
| TaskLimiter injection | Constructor param vs setter | Setter on Runner | Less invasive, optional feature, doesn't change Execute() signature |
| Duration check frequency | Every event vs periodic | Every 100 events or 10s | Avoid overhead on high-frequency token events |

---

## Dependencies

**Requires**:
- `internal/budget/enforcer.go` — CheckBudget() ✅ exists
- `internal/budget/middleware.go` — TaskLimiter ✅ exists
- `internal/memory/store.go` — UsageProvider ✅ exists

**Blocks**:
- Nothing

---

## Verify

```bash
# Run all tests
make test

# Run budget-specific tests
go test ./internal/budget/... -v

# Run executor tests
go test ./internal/executor/... -v

# Build check
make build
```

---

## Done

- [ ] Budget check runs before every polling-mode task execution
- [ ] TaskLimiter kills runaway tasks mid-stream
- [ ] Tests cover both enforcement paths
- [ ] `make test` passes
- [ ] `make build` succeeds

---

**Last Updated**: 2026-02-06
