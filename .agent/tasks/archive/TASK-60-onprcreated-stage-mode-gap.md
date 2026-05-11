# TASK-60: Wire `OnPRCreated` for stage-mode PR creation path

**Status**: ❌ **Resolved as NOT A BUG** (2026-05-11 evening)
**Created**: 2026-05-11
**Assignee**: —

---

## Resolution (2026-05-11 evening, via #3014 / PR #3016 trigger experiment)

The original premise — *"`autopilot_pr_state` is missing rows for stage-mode PRs"* — is incorrect. The table is **intentionally ephemeral for successful PRs**. The wires were never broken.

**Lifecycle as designed:**

1. PR created → `OnPRCreated` writes row (stage=`waiting_ci`)
2. PR progresses: `waiting_ci → ci_passed → merging → post_merge_ci → releasing`
3. PR completes → `controller.removePR()` → `persistRemovePR()` → `RemovePRState()` → `DELETE FROM autopilot_pr_state WHERE pr_number = ?` (`state_store.go:272`, called from `controller.go:1381` and 8 other lifecycle completion sites)

The 14 historical rows in the table are all `stage='failed'` — **failed rows persist** because they're only purged by `PurgeTerminalPRStates` (`state_store.go:818`) on a schedule. Successful rows are deleted on lifecycle completion. That's why today's 4 merged PRs (#3000/#3005/#3008/#3011) left no trace — they each ran the full pipeline successfully and were cleaned up.

**Evidence:**

- Triggered PR #3016 (issue #3014, today's tiny doc-comment task) at 18:10:45Z.
- Row appeared at 18:10:51Z (~6 sec after PR creation) — live `OnPRCreated` callback fired correctly.
- Row visible in `autopilot_pr_state` at stage=`waiting_ci`, ci_status=`pending`.
- Once #3016 auto-merges and the v2.146.7 release completes, that row will be deleted — confirming the lifecycle.

**Features dependent on `autopilot_pr_state`** all want active in-flight state only, never history:

| Feature | Reads | Needs history? |
|---|---|---|
| CI monitoring | activePRs in-memory | No |
| Auto-merge | stage=`ci_passed` | No |
| Approval tracking | approval_* columns on active row | No |
| Post-merge SHA | active row at `post_merge_ci` | No |
| Restart recovery | `LoadAllPRStates` at startup → in-memory `activePRs` | No |

None of these break under the current design.

## What was actually built today (kept for the record)

The session produced four real shipped improvements driven by this (now-debunked) hypothesis:

- **PR #3000** — `6df99408` — diagnostic instrumentation in `poller.go` (3 distinct skip-reason logs). **Real value**: these logs are still useful for future regressions, regardless of TASK-60's premise.
- **PR #3005** — Grafana dashboard redesign with accurate queries and missing panels.
- **PR #3008** — Prometheus zero-emit fix for `pilot_active_prs` gauge (pattern `pattern_prometheus_disappearing_series.md`).
- **PR #3011** — Grafana mount-shadow bind + Approval Persist Misses panel coloring.
- **PR #3016** (this resolution) — `19b50cba` — gate-semantics doc comments in `poller.go`. **Real value**: prevents future readers from re-doing the call-graph spelunk.

All five PRs stand on their own; the cascade-recovery narrative in this file's earlier section remains useful as a worked example of decomposer pathology (`pattern_decomposer_thin_subissue_oom.md`).

## Open questions parked

If we ever decide we *want* a persistent audit of every PR Pilot created (we don't today — metrics + `executions` table cover it), the design choice would be:
- Add an `autopilot_pr_history` table written *alongside* the active state row, never deleted, or
- Treat the metrics safety-net (`pilot_pr_merge_recorded_total`, etc.) + the `executions` table's `pr_url` column as the canonical history. **Current design.**

No action item.

---

**Original Phase 1 investigation below kept for narrative continuity.**

---



---

## Phase 1 outcome (2026-05-11 evening)

**Original hypothesis was WRONG.** `OnPRCreated` IS wired — in both gateway and polling modes.

| Mode | Wiring site | Verified |
|---|---|---|
| Gateway | `cmd/pilot/main.go:726` `WithOnPRCreated(gwAutopilotController.OnPRCreated)` | ✅ |
| Polling | `cmd/pilot/main.go:2096` `WithOnPRCreated(controller.OnPRCreated)` | ✅ |
| Rate-limit retry (orch) | `cmd/pilot/main.go:2165` direct call | ✅ |

The handler fires when `result != nil && result.PRNumber > 0 && p.OnPRCreated != nil` at:
- `internal/adapters/github/poller.go:589` (sequential)
- `internal/adapters/github/poller.go:1174` (parallel)

`6df99408` instrumentation logs the skip reason (nil-result / PRNumber=0 / callback-nil) to slog. Logs go to dashboard TTY only — no log file. Captured live, not historical.

**Real gap is upstream in the result chain:**

```
runner.executeWithOptions ─► ExecutionResult{PRUrl: ?}
              │
              ▼
handler_common.go:250  if result.PRUrl != "" { hr.PRNumber = github.ExtractPRNumber(PRUrl) }
              │           ▲── if PRUrl is empty, PRNumber stays 0
              ▼
handlers.go:298  IssueResult{PRNumber: hr.PRNumber}
              │
              ▼
poller.go:589  if result.PRNumber > 0 { OnPRCreated(...) }   ◄── gate fails when PRNumber=0
```

PR-creation succeeds (we see #3000/#3005/#3008/#3011 on GitHub), but somewhere between `git.CreatePR` returning a URL (`runner.go:3142`) and `result.PRUrl = prURL` (`runner.go:3151`), the URL is being lost OR the runner path that sets `result.PRUrl` is never reached.

**Confirmed counter-evidence:**
- `autopilot_pr_state` last row: 2026-05-05 #2694 (env=dev). All 14 historical rows in `stage='failed'` (state-machine stage, not env). None from this past week.
- Today's 4 successful stage-mode PRs (env=stage) wrote zero rows.
- The gap predates env=stage rollout — even env=dev rows stopped writing on 2026-05-05.

**Recommended fix: A** (wire OnPRCreated into live path) — confidence 0.85. Dependents on `autopilot_pr_state` (CI monitoring, approval tracking, post-merge SHA, restart recovery) are too load-bearing for Option B (deprecate).

## Phase 1.5 — narrow the upstream gap (NEXT)

Phase 1 identified the chain. Phase 1.5 must answer **why `result.PRUrl` is empty** before we can implement.

**Two candidate root causes:**

1. **CR-A: Runner returns before reaching line 3151.** Some early-return path between `else if task.CreatePR && task.Branch != ""` (line 2998) and `result.PRUrl = prURL` (3151) — e.g., no-commits guard at 3022, push failure at 3049, title rejection at 3111. These all set `result.Success = false`, but they're "successful" from GitHub's POV because the PR already exists (created elsewhere — by Claude Code subprocess maybe).
2. **CR-B: PR is created by Claude Code (gh CLI in subprocess), not by `git.CreatePR` in Pilot daemon.** The subprocess pushes branch + runs `gh pr create`, so by the time runner.go:3142 calls `git.CreatePR`, the PR exists. `git.CreatePR` handles "already exists" by extracting URL from stderr (git.go:208) — but if the URL extraction fails or this path isn't hit because the no-commits guard fires first, `result.PRUrl` stays empty.

**To disambiguate, capture next live stage-mode PR creation:**

```bash
# Tail the dashboard TTY (find the tty from ps)
tail -f /dev/ttys001 | grep "OnPRCreated skipped\|Notifying autopilot\|Pull request created"
# Or trigger a synthetic test:
pilot github run <issue_number> --repo qf-studio/pilot
```

Expected diagnostic patterns:
- `Pull request created` log at runner.go:3152 → if missing, runner short-circuited (CR-A)
- `OnPRCreated skipped: PRNumber=0` with non-empty `pr_url` field → `git.ExtractPRNumber` regex bug
- `OnPRCreated skipped: PRNumber=0` with empty `pr_url` → runner never set PRUrl (confirms CR-A or CR-B)
- `OnPRCreated skipped: result is nil` → handler chain bug



## Decomposition cascade recovery (2026-05-11)

First attempt at #2987 (broad multi-phase spec) was over-decomposed by Pilot into 5 sub-issues. Outcome:
- #2988 (investigate) failed with `unknown: exit status 1` after 90min
- #2989 (decide option) declined-preflight as too vague (correct call)
- #2990 (fix) and #2992 (verify) never picked up — dispatcher saturated
- #2991 (test) **OOM-killed** (SIGKILL, exit 137) after 95min — Claude Code ran out of memory trying to write tests for unwritten code

Root cause of cascade: sub-issues each carried only ~1 paragraph of context (`<!--autopilot-meta inherited-spec: true -->` pattern) + a `Parent: GH-2987` reference. The executor had no concrete grip, spelunked the codebase, ballooned memory.

Recovery actions (2026-05-11 ~14:00):
- Closed sub-issues #2989, #2990, #2991, #2992 as broken decomposition
- #2988 already closed-with-failure
- Stripped `pilot-in-progress` from parent
- Cleaned orphan queued execution rows
- Re-scoped #2987 to **investigation-only** (Phase 1) with explicit anti-decompose guidance
- Fix / test / verify phases will be filed as separate focused issues *after* investigation lands

---

## Context

**Problem**:
`autopilot_pr_state` SQLite table has only 14 historical rows total. Today's stage-mode merges (#2959, #2962, #2966) never entered the table. The `Controller.OnPRCreated` handler (`internal/autopilot/controller.go:354`) — the canonical entry point that registers a new PR with the autopilot state machine and persists a row to `autopilot_pr_state` — is not being called during stage-mode PR creation.

v2.146.3 (PR #2985) shipped a scanner safety-net in `ScanRecentlyMergedPRs` that records merge metrics regardless, so observability isn't broken. But the upstream gap remains: features that depend on `autopilot_pr_state` (state transitions, lifecycle audit, approval tracking, post-merge SHA tracking) lose visibility for every stage-mode PR.

**Goal**:
Identify why `OnPRCreated` isn't called on the stage-mode PR-creation path, then either wire it up or document why the state table is no longer authoritative.

**Success Criteria**:
- [ ] Root cause identified: trace the stage-mode PR-creation call graph and pinpoint where `OnPRCreated` should be invoked
- [ ] Either: `OnPRCreated` invoked during stage-mode PR creation, with new stage-mode merges writing rows to `autopilot_pr_state`
- [ ] Or: explicit decision recorded that the state table is legacy, with affected features migrated to the new source of truth
- [ ] Test covering the stage-mode → `autopilot_pr_state` row write
- [ ] Verification: trigger one stage-mode PR end-to-end and confirm a row lands in `autopilot_pr_state`

---

## Implementation Plan

### Phase 1: Investigate call graph

**Goal**: Map every code path that creates a PR in stage mode and identify where `OnPRCreated` should fire.

**Tasks**:
- [ ] Grep all `OnPRCreated(` callsites — known so far: `controller.go:2110` (inside `ScanExistingPRs`), plus tests
- [ ] Trace stage-mode PR-creation entry points starting from `internal/executor/runner.go` and `internal/adapters/github/client.go`. The executor calls `gh pr create` via shell — what bridges that result back to autopilot?
- [ ] Inspect `controller.go` for `handlePostMergeCI`, `handleMerging`, `ScanExistingPRs` and any subscriber wiring to PR-created events
- [ ] Check whether the scanner (`ScanExistingPRs`) is the *only* path that ever calls `OnPRCreated` — that would explain why fresh stage-mode PRs miss it (scanner-lag race)
- [ ] Check git log on `internal/autopilot/controller.go` for "OnPRCreated" references — has the invocation drifted out during recent refactors?

**Files** (suspected):
- `internal/autopilot/controller.go` — handler definition + callers
- `internal/executor/runner.go` — PR creation by Pilot
- `internal/adapters/github/client.go` — GitHub PR API wrapper
- `internal/gateway/*` — webhook handling for PR events (if any)

**Outcome**: a precise statement of which path *should* call `OnPRCreated` and why it currently doesn't.

### Phase 2: Decide fix shape

**Goal**: Choose between two paths once root cause is known.

| Option | Description | When to choose |
|---|---|---|
| A. Wire `OnPRCreated` into the live creation path | Fire the handler immediately after the GitHub API returns a PR number. Matches existing contract. | Default — assumes the gap is an oversight |
| B. Deprecate `autopilot_pr_state` for stage-mode | Document that the table is dev/prod-only; rebuild dependent features on alternate sources (`activePRs` map, scanner, GitHub API). | Only if the table's design conflicts with stage-mode's async model |

**Tasks**:
- [ ] Enumerate features reading `autopilot_pr_state`: state transitions, approval flow, post-merge CI tracking, merge-notification dedup
- [ ] For each, verify whether stage-mode currently functions without the row (it does — scanner safety-net + in-memory `activePRs`)
- [ ] Record decision in Technical Decisions table below

### Phase 3: Implement

**If Option A**:
- Add `controller.OnPRCreated(...)` call at the stage-mode PR-creation site
- Idempotency: `OnPRCreated` must tolerate being called twice (creation + scanner). Verify or add guard in `state_store.go::InsertPRState`
- Unit test that creates a stage-mode PR and asserts a row in `autopilot_pr_state`

**If Option B**:
- Add comment block in `state_store.go` explaining stage-mode exemption
- For each dependent feature, redirect to alternate source
- Update `.agent/system/ARCHITECTURE.md` if it mentions `autopilot_pr_state` as canonical

### Phase 4: Verify

**Tasks**:
- [ ] Unit tests pass: `go test ./internal/autopilot/...`
- [ ] Lint clean: `make lint`
- [ ] Manual: trigger one stage-mode Pilot run, observe new row in `~/.pilot/data/pilot.db` via `sqlite3 ~/.pilot/data/pilot.db "SELECT COUNT(*), MAX(created_at) FROM autopilot_pr_state"` — count should increment, timestamp should be fresh

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|---|---|---|---|
| Fix shape | A: wire handler / B: deprecate table | TBD | Depends on Phase 1 findings |
| Idempotency strategy | Per-call check / INSERT OR IGNORE / unique index | TBD | Whatever matches existing `state_store.go` conventions |

---

## Dependencies

**Requires**:
- v2.146.3 already shipped — metrics safety-net is in place, so this task is *not* blocking observability

**Blocks**:
- Any future work that wants `autopilot_pr_state` to be a complete record of stage-mode PRs (e.g. state-machine debugging, lifecycle audit, approval observability for stage)

---

## Verify

```bash
go test ./internal/autopilot/...
make lint
# After fix lands and a stage-mode PR runs:
sqlite3 ~/.pilot/data/pilot.db "SELECT pr_number, state, created_at FROM autopilot_pr_state ORDER BY created_at DESC LIMIT 5"
```

---

## Done

- [ ] Root cause documented in PR description
- [ ] Implementation matches chosen option (A or B)
- [ ] Tests cover the new behavior
- [ ] Manual stage-mode PR run confirms expected `autopilot_pr_state` behavior

---

## Notes

- **Do not regress v2.146.3 metrics safety-net.** The scanner-side `recordMergeSuccess` call must remain. If Option A lands, `OnPRCreated` and the scanner both writing means `state_store` upserts must be idempotent.
- See pattern memory: `pattern_observability_before_gates.md` — observability recorders must sit above action-dedup gates.
- See marker: `.agent/.context-markers/2026-05-11-v2146.3-metrics-fix-shipped.md`

---

**Last Updated**: 2026-05-11
