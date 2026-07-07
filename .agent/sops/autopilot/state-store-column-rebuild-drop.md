# SOP: `state_store.go` column rebuild migrations silently drop new columns

**Category:** Autopilot / SQLite migrations
**Implemented:** 2026-07-07
**Source incident:** GH-3990 implementation — every `autopilot_pr_state` test failed with "no such column: scope_key" despite the `ALTER TABLE ... ADD COLUMN scope_key` migration running successfully.

## Problem

Adding a new column to `autopilot_pr_state` via `ALTER TABLE ... ADD COLUMN` in the `migrations` slice is not sufficient. `migratePRStateRepoScoping` (GH-3903) rebuilds the entire table via `CREATE TABLE autopilot_pr_state_gh3903 (...)` with an **explicit, hardcoded column list**, then copies rows with an explicit `INSERT INTO ... (col list) SELECT (col list) FROM autopilot_pr_state`. On a fresh `:memory:`/new install, this rebuild runs unconditionally right after the `ALTER TABLE ADD COLUMN` migrations — so any column added to the `migrations` slice but *not* also added to `migratePRStateRepoScoping`'s `CREATE TABLE` + `INSERT`/`SELECT` column lists is silently dropped during the rebuild, one migration pass later in the same `NewStateStore()` call.

## Root cause

`migratePRStateRepoScoping` is guarded by `tableHasColumnInPK("autopilot_pr_state", "repo")` — it only runs once, on a table that doesn't yet have `repo` in its primary key (i.e. every fresh install). On an *already-migrated* production DB it correctly no-ops, so this only bites fresh installs and `:memory:` test databases — exactly the ones covered by the test suite, which is why it failed loudly instead of shipping silently broken.

## Fix

Any new column added to `autopilot_pr_state` must be added in **three** places:
1. The `ALTER TABLE autopilot_pr_state ADD COLUMN ...` migration.
2. `migratePRStateRepoScoping`'s `CREATE TABLE autopilot_pr_state_gh3903 (...)` column list.
3. `migratePRStateRepoScoping`'s `INSERT INTO ... SELECT ...` column lists (both sides).

## Prevention

When adding a column to `autopilot_pr_state`, run `go test ./internal/autopilot/... -run TestStateStore` against a fresh `:memory:` DB (the default in `newTestStateStore`) before writing any feature tests — a missing column shows up immediately as "no such column" on the very first `SavePRState`/`GetPRState` call, rather than as a subtle silent data-loss bug in production.
