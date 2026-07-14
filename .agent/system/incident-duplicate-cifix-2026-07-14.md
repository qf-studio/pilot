Evidence confirmed against the source. Writing the synthesis.

---

# Incident Report: Duplicate Autopilot CI-Fix Issues (#4301 / #4302 / #4304 / #4305)

**Date:** 2026-07-14 · **Repo:** qf-studio/pilot · **Severity:** Medium (noise + wasted executor cycles, no data loss)

Autopilot filed four identical issues — `fix(ci): resolve post-merge CI failure from PR #4299` — for a PR that merged cleanly and introduced no regression. The "failure" was a perpetually-red scheduled canary check (`epic-lifecycle / run`, issue #4265), not a real post-merge code check. This is the third occurrence of this class of bug (prior: #4258 as 1×; #4261/#4262/#4263 as 3×).

---

## 1. Root Cause

Three independent defects stacked. Each is necessary to reach the observed outcome; only one is truly *primary*.

### Ranked contributing causes

**#1 — PRIMARY: No idempotency on fix-issue creation (the "count multiplier" *and* the reason a retry is ever a duplicate).**
`FeedbackLoop.CreateFailureIssue` (`internal/autopilot/feedback_loop.go:137-189`) performs zero existence checking. It builds a title (`feedback_loop.go:198`) and body (`feedback_loop.go:214`) and unconditionally calls `ghissue.CreatePilotIssue`, which itself only validates the repo allowlist and conventional-commit title format before POSTing (`internal/ghissue/create.go:60-72`). There is no search for an existing open fix-issue, no label check, no persisted dedup key. The only respawn suppression anywhere is `removePR` (`controller.go:2613/2640`) evicting the PR from the in-memory `activePRs` map and its SQLite row (`persistRemovePR`, `controller.go:3532-3563`) — per-process bookkeeping, not an issue-level guard. A grep across `internal/autopilot` finds idempotency machinery for merges, releases, alerts, and scopes, but **none for fix-issue creation**. This is why *any* re-entry into the `CIFailure` branch produces a brand-new issue rather than a no-op.

**#2 — Canary attribution: post-merge monitor equates "any failing check on the merge SHA" with "this PR's CI failed" (the reason there was a "failure" at all).**
`handlePostMergeCI` captures the default-branch HEAD (the merge commit) as `PostMergeSHA` and calls `CheckCI(mainSHA)`. `checkStatus` (`ci_monitor.go:129`) lists **every** check-run on that SHA via `ListCheckRuns` with no event-type or workflow filter. GitHub pins a *scheduled* workflow's check-runs to the default-branch HEAD at trigger time — so when the "Pilot Synthetic Pipeline Canary" (`.github/workflows/pilot-canary.yml`, `on: schedule + workflow_dispatch`, **no push trigger**, cron `0 */6 * * *`) fired, its always-failing `epic-lifecycle / run` job attached a failing run to the exact SHA the #4299 monitor was watching. The configured `required_checks: [test, lint]` allowlist that would have filtered this is **dead config**: `NewCIMonitor` (`ci_monitor.go:50-69`) only honors `Required` when `ci_checks.mode == "manual"`; the pilot config sets `ci_checks: {mode: auto, exclude: []}`, so `requiredChecks` stays nil, `checkAutoDiscoveredRuns` aggregates *all* non-excluded runs, and a single canary failure ⇒ `CIFailure`.

**#3 — Double-daemon + release-scan re-discovery (the amplifiers: 1 → 4).**
Two mechanisms re-enter the `CIFailure` branch after `removePR` has "cleaned up":
- *Concurrent controllers:* two `pilot start` daemons ran simultaneously (orphan + user's + an earlier stale one). Each holds its own in-memory `activePRs` and its own PRState for #4299 at `StagePostMergeCI`; `removePR`/`persistRemovePR` in one process cannot suppress the other's in-memory copy, and there is no cross-process guard in the shared store. Both tick, both see the same red canary, both spawn.
- *Release-scan re-discovery:* the `CIFailure` branch does **not** set `prState.Stage = StageFailed` (contrast the timeout branch at `controller.go:2577`); it only `removePR`s. The merged-PR release scan (`controller.go:4193-4308`) then re-discovers #4299 on a later tick — merged, absent from `activePRs`, no release tag (CI never passed ⇒ no tag cut), and none of the skip-gates match a post-merge-CI-failed PR — so it re-registers the PR into `StagePostMergeCI` and the next tick spawns again.

**How they compound:** #2 manufactures a false failure signal on the merge SHA. #1 means every observation of that signal mints a fresh issue instead of being idempotent. #3 causes the signal to be observed N times (controllers × re-scan cycles). The occurrence count tracks how many controller-instances × re-scan cycles overlapped the failing window — which is exactly why the prior clusters were 1× and 3×, and this one 4×.

---

## 2. Was It the Restarts?

**Partly — the restarts/double-daemon are causal to reaching *four* (not to there being a bug at all).**

- **What the restarts caused:** the count. Two (briefly three) concurrent controllers each rehydrated #4299 at `StagePostMergeCI` on startup (per commit `28e1ff9c`, "hydrate monitor from DB on restart") and independently re-drove `handlePostMergeCI`. With no cross-process spawn dedup, each daemon filed its own issue; release-scan re-discovery compounded within each. Concurrency × restart-rehydration × zero dedup = 4.
- **What the restarts did NOT cause:** the existence of a spurious issue. Even a single, cleanly-running daemon files at least one bogus issue here, because #1 (no dedup) and #2 (canary attribution) are present regardless of process count. A dedup guard keyed on `(PR, failureType, SHA)` collapses all four to one even with two daemons live.
- **Why concurrency was possible:** the only single-instance guard in the codebase is Telegram's 409-conflict check (`cmd/pilot/main.go:1964` → `telegram/client.go:166-194`), fully gated behind `if hasTelegram` (`main.go:1885`). A github-only or headless daemon has **zero** single-instance protection — no pidfile, flock, socket, or lockfile exists (grep confirms). `--replace` (`main.go:1152`) only acts when Telegram is enabled *and* a 409 is observed, and even then `killExistingTelegramBot` (`commands.go:913-946`) is a coarse `pgrep -f "pilot start"` + SIGTERM with no confirmation the target exited.

**Verdict:** restarts amplified 1→4 but are not the root cause. The bug ships duplicates with a single daemon.

---

## 3. Fix Plan (prioritized, independently shippable)

### A. Prevents recurrence

**A1 — Idempotency guard on fix-issue creation (PRIMARY; ship first).**
*Scope:* Add a durable dedup claim in the shared SQLite store, checked before every `CreateFailureIssue`, so a re-tick, a re-scan, or a second daemon cannot file a duplicate.
*Files:* `internal/autopilot/state_store.go` (new `spawned_fix_issues` table + `RecordSpawnedFix`/`SpawnedFixExists`; PRIMARY KEY `(repo, dedup_key)`, `INSERT OR IGNORE` returning newly-inserted); `internal/autopilot/feedback_loop.go:137` (compute key, check-then-create-then-record); call sites `controller.go:2621` and `controller.go:1589`.
*Key:* `fix:{owner}/{repo}:pr{PRNumber}:{failureType}[:{sorted failedChecks sig}]` — including the check signature lets a genuinely new failure spawn while blocking same-check repeats. Because the claim lives in the **shared** store, it holds even during transient double-daemon overlap.
*Belt-and-suspenders:* before creating, GitHub-search open issues by exact title + pilot label to cover a lost DB row.

**A2 — Suppress scheduled-canary check-runs in post-merge attribution.**
*Scope:* Stop trusting all check-runs on the merge SHA. Filter to push/pull_request-triggered runs (drop schedule/workflow_dispatch-only workflows) and honor a required-checks allowlist. Fix the `NewCIMonitor` precedence so a non-empty `required_checks` is respected even when `ci_checks.mode == auto`.
*Files:* `internal/autopilot/ci_monitor.go:50-69` (precedence), `ci_monitor.go:224-320` (`checkAutoDiscoveredRuns` event/workflow filter), `ci_monitor.go:438-452` (`GetFailedChecks` same filter).
*Immediate mitigation (config-only, do now):* add `epic-lifecycle / run` and the canary workflow names to `ci_checks.exclude` in `~/.pilot/config.yaml:224-231`.

**A3 — Adapter-agnostic single-instance lock + `pilot stop`/`restart`.**
*Scope:* Acquire an exclusive OS lock (`flock` on `<Memory.Path>/pilot.lock`, `LOCK_EX|LOCK_NB`, held for process lifetime, pid written in) at `runPollingMode` before wiring adapters. Refuse to start if held; with `--replace`, read pid → SIGTERM → wait for release → acquire. Add `pilot stop` (read pid, SIGTERM, wait) and `pilot restart`.
*Files:* `cmd/pilot/main.go:1341` (lock acquisition), `cmd/pilot/main.go:1885-2000` (replace the Telegram-409-only gate as primary), `cmd/pilot/commands.go` (new stop/restart). This works for github-only/headless daemons where the Telegram guard is absent.

**A4 — Close the release-scan respawn loop.**
*Scope:* In the `CIFailure` branch, set `prState.Stage = StageFailed` and persist before `removePR`, so a post-merge-CI-failed PR is not re-discoverable by the release scan.
*Files:* `internal/autopilot/controller.go:2615-2640`; verify skip-gates in `controller.go:4193-4308` exclude `StageFailed`.

### B. Cleanup

**B1 — Close the four duplicates** #4301/#4302/#4304/#4305 as bogus (canary-attribution false positive).
**B2 — Fix the canary scenario design** (issue #4265): the epic subtasks all live in one directory → `isSinglePackageScope` folds decomposition → `child-count:0`. Split into distinct package scopes so the canary reflects real behavior instead of being permanently red.
**B3 — Port post-merge cascade/size guards** from the pre-merge path (the `MaxCIFixIterations` and `MaxCIFixPRSize` guards around `controller.go:1560-1581`); post-merge currently hardcodes `iteration=1` with no guard.

---

## 4. Safe-Restart SOP

Until A3 lands there is **no** automatic single-instance protection for a github-only/headless daemon, so restarts must be done manually and serially.

**Procedure:**
1. **Enumerate every running daemon:** `pgrep -fa "pilot start"`. Expect exactly one PID. If you see more than one, you already have a double-daemon condition — proceed to kill all before starting a new one.
2. **Stop cleanly, all of them:** `pkill -f "pilot start"`, then re-run `pgrep -fa "pilot start"` and confirm **zero** matches. Do not skip the confirmation — SIGTERM is best-effort and the process may still be draining a tick. Wait until the list is empty.
3. **Verify no orphaned tunnel/dashboard child** lingers (a detached `--tunnel` cloudflared or a headless dashboard). Kill any strays.
4. **Start exactly one** with the full flag set you intend to run for the whole session (`pilot start --telegram --github [--dashboard] [--tunnel]`). Do not start a second "just for github" or "just headless" — that is precisely how the orphan controller arose.
5. **Single-telegram-bot constraint:** only one process may poll the Telegram bot (getUpdates 409s otherwise). This is your *incidental* single-instance signal today — but it only fires with `--telegram`, so never rely on it for a github-only run.
6. **`--dashboard`/`--tunnel`:** run them as part of the single `pilot start`, not as a second invocation. One process owns the TUI, the tunnel, and the Telegram poll.
7. **Do not use `--replace` as a routine restart** until A3 lands: today it only acts when Telegram 409s and coarsely SIGTERMs every `pilot start` match with no confirmation. Manual stop-verify-start is safer.

**Should a single-instance lock be added? — Yes (A3).** The manual SOP is a stopgap. The durable fix is the adapter-agnostic `flock` lock plus `pilot stop`/`pilot restart` so one clean handoff replaces the race-prone `pkill`. Critically, the A1 spawn-dedup must live in the **shared SQLite store** regardless, so that even a momentary lock gap during handoff cannot produce duplicates.

---

## 5. Top 3 Actions Right Now

1. **Config mitigation + close dupes (minutes):** add `epic-lifecycle / run` (and the canary workflow names) to `ci_checks.exclude` in `~/.pilot/config.yaml`, and close #4301/#4302/#4304/#4305 as bogus. Stops the bleeding immediately without a code deploy.
2. **Confirm exactly one daemon is running** via the Safe-Restart SOP steps 1-2 (`pgrep -fa "pilot start"` → reduce to one). The double-daemon is what turned one false positive into four.
3. **Dispatch A1 (idempotency guard)** as the first code issue — it is the primary root cause and the single change that makes every future re-tick/re-scan/double-daemon path a no-op instead of a duplicate.

**Key evidence anchors:** `feedback_loop.go:137-189` (no dedup), `controller.go:2621` (unconditional spawn, `iteration=1`), `controller.go:2640` vs `2577` (no `StageFailed`), `controller.go:4193-4308` (release re-discovery), `ci_monitor.go:50-69` (dead `required_checks`), `ci_monitor.go:224-320` (all-runs aggregation), `pilot-canary.yml:22-25` (schedule-only trigger), `cmd/pilot/main.go:1885/1964` (Telegram-gated singleton), `commands.go:913-946` (coarse pkill).