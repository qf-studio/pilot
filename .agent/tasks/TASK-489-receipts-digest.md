# feat(briefs): daily receipts digest — per-run cost receipt lines at configurable time (default 18:00)

🔄 IN REVISION 2026-08-29 — [#5257](https://github.com/qf-studio/pilot/issues/5257) → PR#5258 (CI green, +1093/−20). **Review: REQUEST-CHANGES** (comment on PR — same-account blocks formal review; PR converted to draft to hold autopilot). Blocking: (1) digest window `created_at ∈ [midnight, now]` permanently drops runs in-flight at 18:00 or created after — fix = window since last receipts digest, keyed on `completed_at`; (2) unrelated smart-quote corruption in `SetApprovalDecision` doc comment. Note-only: no Telegram 4096-char guard (~90 runs/day breaks it). Everything else clean: brief_type cross-contamination fix well-tested, canary exclusion, failed-run costs counted, empty-day skip, daily brief untouched.

## Problem

Pilot sends one scheduled daily brief (14:00 local via `orchestrator.daily_brief`,
cron `0 8 * * *` America/New_York). There is no end-of-day **receipts digest**:
one line per completed execution — issue/PR ref, diff size, duration, dollar
cost — plus a day total. All the data already exists on `executions` rows; it's
a formatting + second-schedule feature, not new plumbing.

Blocking defect discovered during research: `GetLastBriefSent(channel)`
(`internal/memory/store.go:5176-5194`) filters `brief_history` by channel only,
not `brief_type`. Any second scheduled brief type on the same Telegram channel
makes catch-up logic read the wrong brief's last-sent timestamp (false catch-up
fires / false skips). Must be fixed as part of this task.

## Design

**Approach: sibling config block + lightweight fork of the scheduler idiom.**
Do NOT generalize the existing `briefs.Scheduler` — it hardcodes
`GenerateDaily()` (`scheduler.go:170`) and `BriefType: "daily"`
(`scheduler.go:191`), and the digest content shape (flat per-execution list +
total) doesn't match `briefs.Brief` (Completed/InProgress/Blocked sections).
A ~150-line receipts scheduler reusing the same cron + timezone + catch-up
pattern keeps the working daily brief untouched.

### 1. Config (`internal/config/config.go`)

- New `ReceiptsDigestConfig` struct mirroring `DailyBriefConfig`
  (config.go:196-204) minus `Time` (deprecated field — don't carry it over)
  and minus `Content`/`Filters` (digest has no content toggles v1):
  `Enabled bool`, `Schedule string`, `Timezone string`,
  `Channels []BriefChannelConfig` (reuse existing type, config.go:206-211).
- Add `ReceiptsDigest *ReceiptsDigestConfig \`yaml:"receipts_digest"\`` to
  `OrchestratorConfig` next to `DailyBrief` (config.go:168).
- Defaults (config.go:565-584 block): `Enabled: false`,
  `Schedule: "0 18 * * *"`, `Timezone: "America/New_York"` (match daily_brief
  default), empty channels.
- `configs/pilot.example.yaml`: add a documented `receipts_digest:` example
  under `orchestrator:` (note: `daily_brief:` has no example block today —
  greenfield; adding a `daily_brief:` example alongside is optional/welcome).

### 2. Memory (`internal/memory/store.go`)

- **Fix**: `GetLastBriefSent(channel string)` → add `briefType string` param,
  `WHERE channel = ? AND brief_type = ?`. Update the single existing call site
  (`internal/briefs/scheduler.go:213`) to pass `"daily"`. Existing
  `brief_history` rows already carry `brief_type = "daily"` so no migration.
- **New query** `GetExecutionsForReceipts(query BriefQuery)`: like
  `GetExecutionsInPeriod` (store.go:2075-2125) but SELECT/Scan the full
  receipt column set — add `estimated_cost_usd, files_changed, lines_added,
  lines_removed, task_source_adapter, task_source_issue_id` (columns exist and
  are populated via `internal/executor/lifecycle.go:330-332`; fuller-column
  Scan pattern precedent: `GetQueuedTasksForProject`, store.go:2375-2380).
  Terminal statuses only (completed + failed — failed runs still cost money;
  mark them in the output). Exclude canary rows
  (`COALESCE(is_canary,0)=0`, same as `GetBriefMetrics` store.go:2281).

### 3. Briefs package (`internal/briefs/`)

New file `receipts.go` (+ `receipts_test.go`):

- `ReceiptsScheduler`: cron via `robfig/cron/v3`, timezone load with
  UTC-fallback-and-warn (copy `scheduler.go:33-37`), catch-up on start using
  the fixed `GetLastBriefSent(channel, "receipts")`, records sends via
  `RecordBriefSent` with `BriefType: "receipts"`.
- Generation: day window in configured timezone (two-places-load-tz shape per
  `generator.go:165-168`), rows from `GetExecutionsForReceipts`.
- **Empty day → skip send entirely** (no "0 runs" noise).
- Telegram formatting (in `receipts.go` or `formatter_receipts.go`):
  - Per-run line: `#5214 merged · +88 −15 · 14m · $2.75` — issue ref via the
    established idiom (strip `GH-` prefix from TaskID, fall back to
    `TaskSourceIssueID`, guard on `TaskSourceAdapter == "github"`;
    `lifecycle.go:427-438`), fall back to task title when no issue number.
    Failed runs marked (e.g. `✗ failed` instead of status).
  - Total line: `N runs · +ΣA −ΣD · $Σ.ΣΣ`.
  - Reuse `formatDuration` (`formatter.go:100-112`),
    `escapeTelegramMarkdown` (`delivery.go:334-344`) on all dynamic strings,
    and the parse-entity-error plain-text retry pattern
    (`delivery.go:246-255, 349-354`).
- Delivery: reuse `DeliveryService`/`TelegramSender` seam (`delivery.go:19-21`)
  or accept the sender directly — whichever needs less surface; no new
  adapter code (`telegramBriefAdapter`, `cmd/pilot/adapters.go:26-41`, wraps
  `SendBriefMessage` already).

### 4. Wiring (`cmd/pilot/main.go`)

- After the `DailyBrief` block (main.go:3775-3836): read
  `cfg.Orchestrator.ReceiptsDigest`, construct + `Start(ctx)` the receipts
  scheduler. Store nil-check same as main.go:3805. Extracting a shared
  `startBriefScheduler`-style helper to cut the ~60-line duplication is
  welcome but optional — do not let it grow the diff into a refactor.

## Acceptance criteria

1. `orchestrator.receipts_digest.enabled: true` with default schedule sends a
   Telegram digest at 18:00 configured-timezone: one line per terminal
   execution that day (issue ref, +adds −dels, duration, $cost) + total line.
2. Empty day sends nothing.
3. `GetLastBriefSent` filters by `brief_type`; daily-brief catch-up and
   receipts catch-up cannot cross-contaminate on a shared channel (test
   covers: both types recorded on same channel, each reads its own).
4. Canary executions excluded from digest rows and totals.
5. Failed runs appear, marked, with their cost counted in the total.
6. Existing daily brief behavior unchanged (its scheduler/generator/delivery
   code paths untouched except the one `GetLastBriefSent` call-site arg).
7. Table-driven tests: formatter (escaping, issue-ref fallback, failed marker,
   totals), `GetExecutionsForReceipts` (column completeness, canary exclusion,
   period boundaries), `GetLastBriefSent` type filter.
8. `configs/pilot.example.yaml` documents `receipts_digest`.
9. `make lint && make test` green.

## Non-goals (v1)

- Slack/email formatting for the digest (Telegram only; channels config shape
  supports adding later).
- `owner/repo` display — `Execution` has only `ProjectPath`, no repo-name
  field; single-project deployments don't need it. Do not add columns.
- Generalizing `briefs.Scheduler` into a multi-brief engine.
- Per-run receipt on PR comments (separate future task).

## Refs

- Issue: https://github.com/qf-studio/pilot/issues/5257
- Research: briefs seam map 2026-08-29 (session research agent; findings
  embedded above with file:line anchors).
