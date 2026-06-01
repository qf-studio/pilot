# TASK-352: self-heal is broken — dashboard shows merged work as `failed`

**Status:** ✅ SHIPPED — PR #3363 merged → released **v2.166.4** (2026-06-01); stale rows GH-3344/3353 backfilled. Knowledge: [[learning_selfheal_projectpath_discriminator]]. · **Created:** 2026-06-01 · **Found via:** TASK-322 Wave 3 watch loop (dashboard QUEUE showed GH-3344/3353/3349 as `✗ failed` after they shipped as v2.166.2).

## Context

The dashboard QUEUE renders the `executions` table `Status` column verbatim
(`internal/dashboard/tui.go:614,848`, fed by `GetRecentExecutions`) with **no** reconciliation
against GitHub issue-closed / PR-merged state. A row flips `failed → completed` only via
`SelfHealExecutionAfterMerge`. Three compounding bugs leave shipped work showing `failed`:

- **Bug 0 — D3 regression (PR #3354, merged 2026-06-01).** D3 scoped self-heal with
  `WHERE task_id=? AND project_path=? AND status='failed'`, and the controller passes
  `projectPath := c.owner + "/" + c.repo` (`qf-studio/pilot`). But `executions.project_path` stores
  the **absolute filesystem path** (`/Users/.../pilot` — set at `runner.go:1878 ProjectPath: executionPath`;
  confirmed in the live DB: `~/.pilot/data/pilot.db`). The two never match → self-heal updates **zero
  rows on every merge path**, controller-driven included. D3's intent (prevent cross-repo clobber) was
  right; the discriminator value was wrong.
- **Bug 1 — `ScanRecentlyMergedPRs` never self-heals.** `controller.go:2417` is the only catch-all for
  PRs merged outside the controller (`gh pr merge`, GitHub UI — which is exactly how the Wave-3 watch
  loop merges). It records merge metrics and injects the PR at `StageReleasing` to trigger a release,
  but never calls `SelfHealExecutionAfterMerge`. So manual/UI merges never heal.
- **Bug 2 — parent task never heals.** Self-heal keys on `GH-<merged PR's issue number>`. Pilot
  decomposes a parent issue into a sub-issue (parent GH-3344 → sub GH-3353; the PR is `pilot/GH-3353`).
  Healing GH-3353 never touches the parent GH-3344's no-op `failed` row, which has no PR of its own.

Not the TASK-321/341 phantom-block bug — that's the GitHub-label/dispatch layer (working: these issues
did not redispatch-loop). This is the executions/self-heal layer, untouched by those fixes.

## Approach

All in `internal/autopilot/controller.go` + `internal/memory/store.go` (autopilot core → MANUAL, like M1/M2).

1. **Bug 0 — correct discriminator.** Add `projectPath string` field + `WithProjectPath(path)` option to
   the controller (functional-option pattern, backward-compatible — tests/unset → empty). Thread the
   filesystem `projectPath` at the 3 `NewController` call sites in `cmd/pilot/main.go` (485/1465/1487 —
   it is already in scope, already passed to `WithExecutionChecker`). Make
   `SelfHealExecutionAfterMerge` tolerate empty projectPath:
   `WHERE task_id=? AND status='failed' AND (? = '' OR project_path = ?)` — keeps D3 cross-repo scoping
   when the path is known (production), reverts to task_id-only legacy match when empty (safe single-repo).
2. **Bug 1 + 2 — `selfHealForPR(ctx, issueNum, prURL)` helper.** Promotes the issue's failed rows AND,
   if the issue body matches `(?i)Parent:\s*GH-(\d+)` (same ref as `epic.go`), the parent's rows.
   Resolve parent via `c.ghClient.GetIssue` (already used in 6+ handlers; concrete client, httptest-tested).
   - Replace the inline self-heal block at `controller.go:~1398` with `c.selfHealForPR(...)`.
   - Add a `c.selfHealForPR(ctx, issueNum, pr.HTMLURL)` call in `ScanRecentlyMergedPRs` right after
     `recordMergeSuccess` (fires regardless of the release-tag skip gates), guarded by `issueNum != 0`.
3. **Backfill (operational, one-time).** The current stale rows (GH-3344/3353/3349/etc.) are past the
   30-min scan window and the daemon runs an older binary, so the code fix won't retroactively heal them.
   `UPDATE executions SET status='completed' WHERE task_id IN (...shipped...) AND status='failed'` after
   the merged Wave-3 task IDs are confirmed shipped.

## Acceptance

- [ ] `SelfHealExecutionAfterMerge` heals rows whose `project_path` is the filesystem path (Bug 0); test with a realistic fs-path row asserts the row flips to `completed`.
- [ ] Empty projectPath falls back to task_id-only match (legacy behavior preserved); existing controller tests stay green.
- [ ] `ScanRecentlyMergedPRs` self-heals a discovered externally-merged PR's execution row (Bug 1); test asserts a `failed` row → `completed` after a scan over a merged `pilot/GH-N` PR.
- [ ] A sub-issue PR merge heals the parent task too (Bug 2); test: sub-issue body has `Parent: GH-<n>`, merge heals both `GH-<sub>` and `GH-<n>`.
- [ ] `WithProjectPath` wired at all 3 `NewController` call sites in `main.go`.
- [ ] `make build` + `make test` (autopilot, memory) green; `make lint` clean.
- [ ] Stale Wave-3 rows backfilled so the dashboard QUEUE no longer shows shipped work as `failed`.

## Refs

- Watch-loop discovery thread (this session); roadmap `.agent/tasks/TASK-322-remediation-roadmap.md`
- Regression source: D3 in PR #3354 (TASK-343)
- Files: `internal/autopilot/controller.go` (1398 merge path, 2417 scan, NewController 200), `internal/memory/store.go` (`SelfHealExecutionAfterMerge`), `cmd/pilot/main.go` (485/1465/1487), parent ref `internal/executor/epic.go:1075`
- Prior self-heal history: GH-2279 / GH-2402 (merge self-heal), GH-2251 (`ScanRecentlyMergedPRs`)
