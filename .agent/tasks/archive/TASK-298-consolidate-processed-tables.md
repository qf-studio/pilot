# TASK-298: Consolidate 7 `*_processed` SQLite tables into `adapter_processed`

**Wave:** 3 (M) · **⚠️ Depends on TASK-296** (must merge after) · **Closes TASK-288 Step 2** · **Audit ref:** §2 Action #10, §3.1 P2, §3.2 P2

---

## Problem

`internal/autopilot/state_store.go:65-114, 276-600` currently maintains 7 per-adapter tables: `autopilot_processed` (github), `linear_processed`, `gitlab_processed`, `jira_processed`, `asana_processed`, `azuredevops_processed`, `plane_processed`. Each has 4 copy-pasted methods (`Mark`/`Unmark`/`Is`/`Load`) — ~28 methods totalling ~600 LOC of boilerplate. A generic `adapter_processed` table was added "for new adapters" but legacy 7 still exist; new adapters are tempted to add an 8th.

Additionally, `autopilot_processed` has no repo namespace (PRIMARY KEY is `issue_number INTEGER` only). In multi-repo polling setups, issue #21 in repo A blocks dispatch of #21 in repo B. TASK-288 Step 2 documents this.

Generic `adapter_processed` table with `(source, issue_id)` composite key supersedes both problems.

## Approach

### Step 1 — Schema migration (M, ~60 min)

`internal/memory/store.go` — add migration #N+1:

```sql
-- For each legacy table: copy rows into adapter_processed with source label, then drop
INSERT INTO adapter_processed (source, issue_id, processed_at, result)
  SELECT 'github', CAST(issue_number AS TEXT), processed_at, ''
    FROM autopilot_processed
   WHERE NOT EXISTS (
     SELECT 1 FROM adapter_processed
      WHERE source = 'github' AND issue_id = CAST(autopilot_processed.issue_number AS TEXT)
   );
DROP TABLE autopilot_processed;

-- Repeat for each of: linear, gitlab, jira, asana, azuredevops, plane
```

- Wrap in a single SQL transaction
- Add `repo` column to `adapter_processed` if not present, OR include repo namespace in `issue_id` as `owner/repo#NN` for github (resolves TASK-288 Step 2)
- Decision: prefer adding `repo TEXT` column to `adapter_processed` and updating PK to `(source, repo, issue_id)`; cleaner schema, allows nil repo for tracker-style adapters
- Add `schema_versions` table reference (or use existing if migrating to versioned migrations is in scope — audit §3.7 P3 recommends this)

### Step 2 — Delete legacy state_store methods (M, ~90 min)

- `internal/autopilot/state_store.go:65-114` — delete 7 `CREATE TABLE` migration lines (the generic one stays)
- `internal/autopilot/state_store.go:276-600` — delete ~28 methods:
  - `MarkLinearProcessed`, `UnmarkLinearProcessed`, `IsLinearProcessed`, `LoadLinearProcessed`
  - Same 4-method set for: gitlab, jira, asana, azuredevops, plane
  - `MarkIssueProcessed`/`UnmarkIssueProcessed`/`IsIssueProcessed`/`LoadProcessedIssues` (github-only autopilot_processed) — generalize to use generic table
- Expose single API:
  ```go
  Mark(ctx, source string, repo string, issueID string) error
  Unmark(ctx, source string, repo string, issueID string) error
  IsProcessed(ctx, source string, repo string, issueID string) (bool, error)
  Load(ctx, source string, repo string) (map[string]time.Time, error)
  ```

### Step 3 — Update adapter callers (M, ~90 min)

- `internal/adapters/github/poller.go` — change `state.MarkIssueProcessed(num)` → `state.Mark(ctx, "github", "owner/repo", strconv.Itoa(num))`. Apply to all sites (Mark/Unmark/Is/Load).
- `internal/adapters/gitlab/poller.go` — `MarkGitLabProcessed(...)` → `Mark(ctx, "gitlab", "...", ...)`. Same for sibling methods.
- Repeat for: linear, jira, asana, azuredevops, plane
- Use `ProcessedStore` interface defined at `internal/adapters/registry.go:59` as the typed contract

### Step 4 — TASK-296 integration (S, ~30 min)

- The cross-site invariant test in TASK-296 (`internal/integration/task_completion_invariant_test.go`) currently uses the old per-adapter APIs. Update to use the new generic API. Tests should still pass; assertion is the same.

### Step 5 — Tests (M, ~75 min)

- **Migration test** in `internal/memory/store_test.go`:
  - Seed each legacy table with 5 rows
  - Run migration
  - Assert `adapter_processed` contains 7 × 5 = 35 rows with correct `source` labels
  - Assert legacy tables dropped
  - Idempotency: run migration twice, assert no duplicates (the `WHERE NOT EXISTS` guard)
- **Generic API tests** in `internal/autopilot/state_store_test.go`:
  - `TestMark_RoundTrip` for each of 7 sources
  - `TestMark_DuplicateIsNoop`
  - `TestLoad_FiltersBySource`
  - `TestLoad_FiltersByRepo` (proves TASK-288 cross-repo fix)

### Step 6 — Manual smoke (~30 min)

- **CRITICAL**: backup a copy of production-shape `~/.pilot/pilot.db` BEFORE merging
- On a test DB:
  - Confirm legacy row count per table
  - Run migration
  - Confirm `adapter_processed` row count = sum of legacy counts
  - Run `pilot start`; observe poller correctly dedups against migrated rows

## Files to modify

- `internal/memory/store.go` (migration)
- `internal/autopilot/state_store.go` (big delete + new methods)
- `internal/autopilot/state_store_test.go` (migration test + generic API tests)
- `internal/adapters/registry.go` (verify `ProcessedStore` interface matches new method set)
- `internal/adapters/{github,gitlab,linear,jira,asana,azuredevops,plane}/poller.go` (7 caller updates)
- `internal/integration/task_completion_invariant_test.go` (from TASK-296 — update to generic API)

## Test Strategy

- Unit: migration + generic API tests as above
- Integration: TASK-296's cross-site invariant test passes through the new API
- Manual: backup-and-migrate dry run on real DB

## Effort

M (~6h total). One PR but large diff — review can split into "migration + state_store" and "adapter callers + tests" commits if desired.

## Closes / blocks

- **Closes TASK-288 Step 2** (repo-namespace `autopilot_processed`) — superseded by `(source, repo, issue_id)` composite key
- **Blocked by TASK-296** — must merge after

## Wave 3 final action

After this merges, close TASK-288 with a comment linking TASK-296 (Steps 1+3) + TASK-298 (Step 2) PRs.

## Out of Scope

- Versioned migration framework (audit §3.7 P3) — TASK-298 adds one new migration; building a `schema_versions` table is a separate task
- SQLite backup/rotation policy (audit §3.7 P3) — separate operational task
- Generic `Poller[T]` extraction (audit §3.2 P2) — Wave 4+; this task only consolidates state-store boilerplate, not the polling logic itself
