# TASK-288: GitHub poller dispatch — false-positive fix + repo namespace for `autopilot_processed`

**Status**: queued (not yet handed off to Pilot)
**Priority**: P2
**Estimated Effort**: M (4-7 person-hours)
**Risk Level**: Medium (schema migration)
**Discovered**: 2026-05-21 (GitNation workshop prep session)
**Related**: v2.149.3 (PR #3054), GH-2242 (original skip-on-completed-exec logic), GH-2315 (HasCompletedExecution orphan exclusion)

---

## Problem

During the 2026-05-21 GitNation workshop prep session, three demo issues in `qf-studio/gitnation-companion` — `#21` (M1.3 epic), `#22` (M1.4 epic), `#26` (M1.8 epic) — silently stopped being dequeued by Pilot's GitHub poller. Issues carried the correct `pilot` label, were visible via `GET /repos/{owner}/{repo}/issues?labels=pilot&state=open` (HTTP 200, `[26, 22, 21]`), and the daemon was actively polling at 30s intervals. No `pilot-failed` label was applied, no comment was posted, no execution row was created. Pilot was completely silent.

`navigator-research` traced the skip path on 2026-05-21 ~20:18Z. Two interlocking bugs combine to produce the silent skip.

### Bug A — `HasCompletedExecution` accepts false-positive rows

Skip path in the GitHub poller's `checkForNewIssues`:

```go
// internal/adapters/github/poller.go:1022-1037 (GH-2242)
if p.execChecker != nil {
    taskID := fmt.Sprintf("GH-%d", issue.Number)
    completed, err := p.execChecker.HasCompletedExecution(taskID, p.projectPath)
    if err != nil { /* warn */ }
    else if completed {
        p.logger.Info("Skipping re-dispatch — completed execution exists", …)
        p.markProcessed(issue.Number)   // <- re-writes autopilot_processed
        continue
    }
}
```

`HasCompletedExecution` (`internal/memory/store.go:540-551`, GH-2315):

```sql
SELECT COUNT(*) FROM executions
WHERE task_id = ? AND project_path = ? AND status = 'completed'
    AND (error IS NULL OR error = '')
```

The query has one defense-in-depth filter (`error IS NULL OR error = ''`) to exclude orphan-recovered rows — but it has **no filter on deliverables**. Rows written by the pre-v2.149.3 false-complete dedup path (`Success: true, IsEpic: true, CommitSHA: "", PRUrl: "", tokens_total: 0, files_changed: 0`) satisfy the predicate and trigger the skip. v2.149.3's post-flight comment fix at `cmd/pilot/handlers.go` and `cmd/pilot/commands.go:1165` already adopted the right semantic — `!result.IsEpic && result.CommitSHA == "" && result.PRUrl == ""` — but the poller's pre-dispatch check was not updated to match. The result: identical false-positive rows still block dispatch even on freshly-installed v2.149.3.

The fact that the poller calls `markProcessed` after deciding to skip ALSO means every 30s tick re-inserts a row into `autopilot_processed`. Operators (or me) who delete the row to attempt a manual re-dispatch see it reappear seconds later. The mitigation tonight required deleting from `executions` first (so `HasCompletedExecution` returns false), then deleting from `autopilot_processed`. Without that two-table cleanup, the issue was unrecoverable without code changes or a daemon restart with surgical DB intervention.

### Bug B — `autopilot_processed` has no repo namespace

Schema (`internal/autopilot/state_store.go:65-70`):

```sql
CREATE TABLE IF NOT EXISTS autopilot_processed (
    issue_number INTEGER PRIMARY KEY,
    processed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    result TEXT DEFAULT ''
);
```

`LoadProcessedIssues()` (`state_store.go:306-322`) returns `map[int]bool` of all rows globally. The GitHub poller applies it to every repo it manages. With 5 polled repos (the user's daemon polls `qf-studio/pilot`, `qf-studio/gitnation-companion`, `alekspetrov/boston-team-group`, `alekspetrov/navigator`, `qf-studio/auth-service`), any issue number processed in one repo blocks the same number in all other repos. Issue #21 in `qf-studio/pilot` is collision-equivalent to issue #21 in `gitnation-companion`.

This is the architectural shape that lets Bug A escalate. Even after the user's manual `DELETE FROM autopilot_processed WHERE issue_number IN (21,22,26)`, the very next poll tick on any of the 5 repos finding a stale `executions` row would re-insert. We confirmed during cleanup that pre-existing rows for `task_id='GH-21' project_path=/Users/aleks.petrov/Projects/startups/auth-service` from 2026-04-04 still exist, although they did not directly cause the demo failure (the SQL in `HasCompletedExecution` also filters by `project_path`).

---

## Approach

Three minimal, ordered changes.

### Step 1 — `HasCompletedExecution` requires actual deliverables (S, ~1 h)

Mirror the v2.149.3 post-flight semantic. Treat a row as "completed enough to skip re-dispatch" only if it produced a commit or a PR.

`internal/memory/store.go:540-551` query becomes:

```sql
SELECT COUNT(*) FROM executions
WHERE task_id = ? AND project_path = ? AND status = 'completed'
    AND (error IS NULL OR error = '')
    AND ((commit_sha IS NOT NULL AND commit_sha != '') OR (pr_url IS NOT NULL AND pr_url != ''))
```

Docstring updated to reflect the new precondition. Idempotent: rows with deliverables (the genuine "already done" case) are still counted, so legitimate re-poll of a shipped ticket continues to skip. Rows without deliverables (false-complete dedup leftovers, future epic-already-complete returns) no longer block.

### Step 2 — Repo-scope `autopilot_processed` (M, ~3-4 h)

Schema migration: add `repo TEXT NOT NULL DEFAULT ''` to `autopilot_processed`, change primary key from `(issue_number)` to `(repo, issue_number)`. Backfill existing rows with `''` (empty repo) so we don't have to invent provenance — they'll continue to behave as global until naturally aging out via the existing cleanup at `state_store.go:809`. New writes carry the repo.

API changes:

| Method | Old | New | Caller |
|---|---|---|---|
| `LoadProcessedIssues()` | `() (map[int]bool, error)` | `(repo string) (map[int]bool, error)` | `internal/adapters/github/poller.go:346` |
| `MarkProcessed(issueNumber, result)` | `(int, string) error` | `(repo string, issueNumber int, result string) error` | `state_store.go:279` and call sites |
| `UnmarkProcessed(issueNumber)` | `(int) error` | `(repo string, issueNumber int) error` | `state_store.go:291` |
| `IsProcessed(issueNumber)` | `(int) (bool, error)` | `(repo string, issueNumber int) (bool, error)` | `state_store.go:298` |

Caller updates in `internal/adapters/github/poller.go`:
- `:346` — pass `p.owner+"/"+p.repo` to `LoadProcessedIssues`
- `:420`, `:650`, `:670`, `:685`, `:964`, `:1035`, `:1076`, `:1104`, `:1139` — thread `p.owner+"/"+p.repo` into `markProcessed`/`unmarkProcessed` calls (these are private wrappers; the wrappers themselves call `processedStore` and need to add the repo arg)

Cleanup query at `state_store.go:809` (`DELETE FROM autopilot_processed WHERE processed_at < ?`) is unaffected — operates on time, not key.

### Step 3 — Tests (S-M, ~1.5 h)

Add `internal/memory/store_test.go` cases:
- `TestHasCompletedExecution_RejectsFalsePositiveEpic`: insert a row with `status='completed', error='', commit_sha='', pr_url=''` — expect `false`
- `TestHasCompletedExecution_AcceptsCommitRow`: row with `commit_sha='abc'`, no PR — expect `true`
- `TestHasCompletedExecution_AcceptsPRRow`: row with `pr_url='https://github.com/x/y/pull/1'`, no commit — expect `true`
- `TestHasCompletedExecution_StillRejectsOrphanError`: row with `error='oom_killed'`, even with commit — expect `false` (preserves GH-2315)

Add `internal/autopilot/state_store_test.go` cases:
- `TestLoadProcessedIssues_RepoIsolation`: two rows, same `issue_number`, different `repo` — `LoadProcessedIssues("repo-a")` returns only repo-a's number
- `TestLoadProcessedIssues_LegacyEmptyRepo`: rows with `repo=''` (pre-migration backfill) should be returned by *every* `LoadProcessedIssues(repo)` call as a backward-compat fallback OR explicitly only by `LoadProcessedIssues("")` — pick one and document the choice (see Open Questions below)
- `TestMarkProcessed_NewSchemaWritesRepo`: assert new rows have non-empty `repo` column
- Migration test: open a DB with the old schema, run init, assert column added without data loss

---

## Out of scope (and why)

- **`executions.task_id` repo namespacing.** Same architectural shape (`task_id='GH-21'` is not unique across repos), but the schema migration is heavier (many call sites query by task_id alone) and the `HasCompletedExecution` filter at Step 1 already requires `project_path` as a second key, which gives us repo-equivalent uniqueness for that specific code path. If a future bug surfaces that needs `(repo, task_id)` uniqueness, lift it as **TASK-289**. Not blocking the workshop or any active workflow.
- **Migrating linear/jira/asana/azuredevops/plane `_processed` tables to also carry repo.** Those tables use string identifiers (e.g., Linear's `APP-123`) which already encode workspace/project, so the collision shape is narrower. Defer until a concrete cross-workspace collision is reported.
- **Touching `IsProcessed`/`MarkProcessed` call patterns in non-GitHub adapters.** This task only modifies the GitHub poller's surface; other adapters retain their existing per-adapter `_processed` tables.

---

## Risk + rollback

| Risk | Likelihood | Mitigation |
|---|---|---|
| Step 1 changes the semantics of `HasCompletedExecution` and breaks a code path that depended on the old behavior. | Low | Only one caller in `cmd/pilot`+`internal/`: `github/poller.go:1024`. All other deliverables-aware checks (post-flight at `commands.go:1165`, etc.) already use the stricter semantic. Step 1 brings the poller in line with what was already true elsewhere. |
| Schema migration breaks existing installs. | Medium | The migration adds a column with a default; the `CREATE TABLE IF NOT EXISTS` at `state_store.go:65` won't re-run for existing DBs. Need an explicit `ALTER TABLE autopilot_processed ADD COLUMN repo TEXT NOT NULL DEFAULT ''` guarded by a version check (or `PRAGMA table_info`). |
| Backfill leaves all existing rows with `repo=''`, behavior on next `LoadProcessedIssues(repo)` call is ambiguous. | Medium | See Open Question 1. Whichever choice we make, document explicitly in the migration comment. |
| Pilot users running v2.149.3 in production hit a stale `executions` row from a v2.149.2 false-complete and silently skip on upgrade. | Medium | Step 1 fixes this on upgrade. Until v2.149.3-with-Step-1 ships, the workaround is the two-table delete that resolved tonight's incident. Add a NOTE to release notes. |

Rollback: revert PR. The `repo` column on `autopilot_processed` stays (NOT NULL default `''` is forward-compatible); old code reads `issue_number` only and ignores the new column. No data lost.

---

## Test plan

```bash
# unit tests for the changed surface
go test ./internal/memory/ -run TestHasCompletedExecution -v
go test ./internal/autopilot/ -run TestLoadProcessedIssues -v
go test ./internal/autopilot/ -run TestMarkProcessed -v

# regression: full executor + cmd/pilot suites
go test ./internal/executor/ ./cmd/pilot/

# build + vet
go build ./...
go vet ./...

# manual smoke (post-merge, pre-release):
# 1. Spin up local Pilot pointed at a test repo with two issues #21 (different repo each)
# 2. Confirm processing one does NOT mark the other processed
# 3. Insert a row with status=completed, error='', commit_sha='', pr_url='' into executions
# 4. Confirm poller dispatches the matching issue on next tick (Step 1 working)
```

---

## Open questions (decide before handoff)

1. **Legacy rows with `repo=''`** — when `LoadProcessedIssues("qf-studio/gitnation-companion")` runs, do we return rows where `repo=''` as a backward-compat catch-all, or only rows where `repo='qf-studio/gitnation-companion'`? The conservative answer (only the exact repo) means existing-installs that previously had GH-21 marked-processed will dispatch GH-21 again on first poll after upgrade. The permissive answer (include `repo=''` rows in every load) preserves existing behavior but never converges to the new model.
   *Recommendation*: conservative (exact repo only). Acceptable side-effect: one extra dispatch per previously-shipped issue across all repos on upgrade. The poller's other filters (`HasCompletedExecution` at Step 1 with deliverables guard) will catch genuinely-done issues immediately.
2. **Migration version mechanism** — Pilot currently uses `CREATE TABLE IF NOT EXISTS` for schema. There's no explicit migration framework. Should this task introduce one (e.g., a `schema_version` table + ordered up-migrations), or do the `ALTER TABLE` inline guarded by `PRAGMA table_info` checks? *Recommendation*: inline-guard. Introducing migrations is a separate concern (TASK-29X candidate).
3. **Naming the repo column** — `repo` (free-form) or `repo_full_name` (explicit Pilot convention from `config.Adapters.GitHub.Repo` which holds `owner/repo` strings)? *Recommendation*: `repo` (short, the data is `owner/repo` format and code is already consistent).

---

## Refs

- v2.149.3 PR #3054 — empty-set guard + IsEpic suppression in post-flight (the precedent for Step 1's filter)
- GH-2242 — original skip-on-completed-execution logic in poller
- GH-2315 — orphan-recovery exclusion in `HasCompletedExecution`
- Tonight's session log: 2026-05-21 19:30-20:30Z (GH poller silent-skip diagnosis)
- `internal/memory/store.go:540-551` — `HasCompletedExecution`
- `internal/adapters/github/poller.go:1022-1037` — skip path with `markProcessed`
- `internal/autopilot/state_store.go:65-70, 279-322, 809` — `autopilot_processed` schema, writes, cleanup

---

## Notes

This task is the architectural follow-up to v2.149.3's post-flight comment fix. Both bugs share the same root: legitimate false-success rows (epic-already-complete, Pilot doing nothing because sub-issues handle the work) being treated as "real" completed executions by downstream checks. v2.149.3 fixed the comment surface; TASK-288 fixes the poller surface. After both, the data shape `(commit_sha='' AND pr_url='')` is consistently interpreted as "no deliverables produced" across the codebase.

**Last Updated**: 2026-05-21
