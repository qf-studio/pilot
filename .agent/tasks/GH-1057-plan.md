# GH-1057: Split runner.go into 7 logical files

## Analysis Summary

`runner.go` is 3,865 lines with 70 functions. The issue proposes 6 new files, but the actual contents need adjustment based on what's **already extracted** into separate files:

- **Git ops** → already in `git.go` (319 lines) and `worktree.go` (547 lines)
- **Epic handling** → already in `epic.go` (386 lines)
- **Navigator init** → already in `navigator.go` (445 lines)

So the planned `runner_git.go` and `runner_epic.go` files from the issue have less content than expected. The real extraction targets, based on actual code analysis:

| New File | Lines | Functions | What Moves |
|----------|-------|-----------|------------|
| `runner_prompt.go` | ~310 | 4 | `BuildPrompt()`, `buildRetryPrompt()`, `buildSelfReviewPrompt()`, `appendResearchContext()` |
| `runner_review.go` | ~170 | 3 | `runSelfReview()`, `buildQualityGatesResult()`, `simpleQualityChecker` struct + `Check()` |
| `runner_progress.go` | ~620 | 14 | `OnProgress()`, `AddProgressCallback()`, `RemoveProgressCallback()`, `AddTokenCallback()`, `RemoveTokenCallback()`, `reportTokens()`, `SuppressProgressLogs()`, `EmitProgress()`, `parseStreamEvent()`, `processBackendEvent()`, `parseNavigatorPatterns()`, `parseNavigatorStatusBlock()`, `handleStructuredSignals()`, `handleNavigatorPhase()` |
| `runner_metrics.go` | ~310 | 9 | `estimateCost()`, `extractCommitSHA()`, `isValidSHA()`, `handleToolUse()`, `formatToolMessage()`, `truncateText()`, `min()`, `emitAlertEvent()`, `dispatchWebhook()` |
| `runner_git.go` | ~200 | 6 | `syncMainBranch()`, `syncNavigatorIndex()`, `maybeInitNavigator()`, `extractTaskNumber()`, `containsTaskNumber()`, `ValidateRepoProjectMatch()`, `ExtractRepoName()` |
| `runner_decompose.go` | ~200 | 1 | `executeDecomposedTask()` |

After extraction, `runner.go` retains: types/structs (lines 1-230), `Runner` struct + constructor/setters (lines 232-535), `Execute()`, `executeWithOptions()`, `Cancel()`, `CancelAll()`, `IsRunning()` — approximately **1,800 lines** (the monolithic `executeWithOptions()` is 1,523 lines alone).

**Key constraint**: `executeWithOptions()` at 1,523 lines means runner.go will NOT reach the <1,000 line target with pure mechanical extraction alone. The function itself stays on `r *Runner` and cannot be split across files without logic changes. The plan below extracts everything possible mechanically; a follow-up issue would decompose `executeWithOptions()` into helper methods.

---

## Subtasks

### 1. Extract prompt building → `runner_prompt.go`

**Description:** Create `runner_prompt.go` containing the 4 prompt-building functions. These have zero coupling to the orchestration loop — they build strings and return them.

**Functions to move (with line ranges):**
- `BuildPrompt()` (2427–2613) — 187 lines
- `buildRetryPrompt()` (2617–2635) — 19 lines
- `buildSelfReviewPrompt()` (2716–2775) — 60 lines
- `appendResearchContext()` (2780–2805) — 26 lines

**Imports needed:** `fmt`, `os`, `path/filepath`, `strings`

**Verification:** `go build ./... && go vet ./internal/executor/...`

---

### 2. Extract review + quality gates → `runner_review.go`

**Description:** Create `runner_review.go` with self-review execution and quality gate helpers. Includes the `simpleQualityChecker` struct and its `Check()` method.

**Functions to move:**
- `runSelfReview()` (2640–2711) — 72 lines
- `buildQualityGatesResult()` (3768–3786) — 19 lines
- `simpleQualityChecker` struct (3790–3794) + `Check()` (3797–3829) — 44 lines

**Imports needed:** `context`, `log/slog`, `strings`, `github.com/.../quality`

**Verification:** `go build ./... && go vet ./internal/executor/...`

---

### 3. Extract progress/event parsing → `runner_progress.go`

**Description:** Create `runner_progress.go` with all progress callback wiring and stream event parsing. This is the largest extraction (~620 lines) and cleanly separable — all functions either wire callbacks or parse events.

**Functions to move:**
- Callback setters: `OnProgress()`, `AddProgressCallback()`, `RemoveProgressCallback()`, `AddTokenCallback()`, `RemoveTokenCallback()`, `reportTokens()`, `SuppressProgressLogs()`, `EmitProgress()` (lines 547–619) — 73 lines
- `reportProgress()` (3507–3552) — 46 lines
- `parseStreamEvent()` (2809–2879) — 71 lines
- `processBackendEvent()` (2883–2950) — 68 lines
- `parseNavigatorPatterns()` (2951–3028) — 78 lines
- `parseNavigatorStatusBlock()` (3029–3088) — 60 lines
- `handleStructuredSignals()` (3089–3146) — 58 lines
- `handleNavigatorPhase()` (3147–3196) — 50 lines
- `handleToolUse()` (3197–3340) — 144 lines

**Imports needed:** `context`, `encoding/json`, `fmt`, `log/slog`, `path/filepath`, `strconv`, `strings`

**Verification:** `go build ./... && go vet ./internal/executor/...`

---

### 4. Extract metrics, utilities, and event dispatch → `runner_metrics.go`

**Description:** Create `runner_metrics.go` with cost estimation, commit SHA extraction, text utilities, and alert/webhook dispatch.

**Functions to move:**
- `estimateCost()` (3446–3485) — 40 lines
- `extractCommitSHA()` (3398–3426) — 29 lines
- `isValidSHA()` (3429–3442) — 14 lines
- `formatToolMessage()` (3343–3375) — 33 lines
- `truncateText()` (3378–3386) — 9 lines
- `min()` (3389–3394) — 6 lines
- `emitAlertEvent()` (3488–3493) — 6 lines
- `dispatchWebhook()` (3496–3502) — 7 lines

**Imports needed:** `context`, `fmt`, `path/filepath`, `strings`, `github.com/.../webhooks`

**Verification:** `go build ./... && go vet ./internal/executor/...`

---

### 5. Extract git helpers, navigator sync, and decomposed execution → `runner_git.go` + `runner_decompose.go`, then verify all

**Description:** Create the final two files and run the complete verification suite.

**`runner_git.go` functions:**
- `syncMainBranch()` (3699–3733) — 35 lines
- `syncNavigatorIndex()` (3576–3688) — 113 lines
- `maybeInitNavigator()` (3556–3571) — 16 lines
- `extractTaskNumber()` (3736–3746) — 11 lines
- `containsTaskNumber()` (3749–3765) — 17 lines
- `ExtractRepoName()` (3833–3839) — 7 lines
- `ValidateRepoProjectMatch()` (3844–3864) — 21 lines

**`runner_decompose.go` functions:**
- `executeDecomposedTask()` (2150–2349) — 200 lines

**Final verification (full suite):**
```bash
go build ./...
go test ./internal/executor/...
go vet ./internal/executor/...
```

**Post-extraction runner.go contents:**
- Package declaration + imports
- Type definitions: `StreamEvent`, `UsageInfo`, `AssistantMsg`, `ContentBlock`, `ToolResultContent`, `progressState`, `Task`, `QualityGateResult`, `QualityGatesResult`, `ExecutionResult`, callback types
- `Runner` struct definition
- Constructor: `NewRunner()`, `NewRunnerWithBackend()`, `NewRunnerWithConfig()`, `Config()`
- Setters: all `Set*()` and `Enable*()` methods (~20 functions)
- `getRecordingsPath()`
- `Execute()` + `executeWithOptions()` (the 1,523-line orchestration loop)
- `Cancel()`, `CancelAll()`, `IsRunning()`

**Estimated final runner.go size:** ~1,800 lines (due to `executeWithOptions()`)

---

## File Naming Adjustment from Issue

The issue proposed `runner_epic.go` — but epic logic is already in `epic.go`. Instead, `runner_decompose.go` captures the decomposed task execution that lives in runner.go. Similarly, the issue's `runner_git.go` was intended for `createBranch`/`pushBranch` etc. — those are already in `git.go`/`worktree.go`. Our `runner_git.go` holds the remaining git-adjacent helpers (`syncMainBranch`, `syncNavigatorIndex`, `ValidateRepoProjectMatch`).

## Note on <1,000 Line Target

The acceptance criterion "runner.go is under 1,000 lines" requires decomposing `executeWithOptions()` (1,523 lines) into sub-methods. That's a logic change, not a pure mechanical extraction. Recommend completing this refactor first (mechanical extraction → ~1,800 lines), then filing a follow-up issue for `executeWithOptions()` decomposition.
