# TASK-441 kill-drill — prove seam breakage pages within an hour

**Purpose**: the TASK-441 acceptance criterion "a seam that silently stops working
raises an alert within 1 hour — verified by killing one subsystem and observing the
alert." Code merged 2026-08-04 (PR#4712 `DeadManTracker` · PR#4724 Finish tripwire
sweep); this SOP is the operator-run verification.

## Preconditions

- Box binary contains the 08-04 merges: board `ver` must be **> 2.253.0** (first
  train after 2026-08-04 16:00). Check: `~/bin/pilot-board` header. If still
  2.253.0, the seam code is NOT live — do not run the drill.
- Use ONLY a sandbox/demo repo (`counter`/`greeter` class). Never drill against
  `startups/pilot` or any tenant repo. The canonical sandbox is
  `qf-studio/pilot-canary-sandbox` — it was **de-onboarded from the box config**
  after 07-17, so re-add its project block (backup at
  `config.yaml.bak-20260813-drill441` has the shape) **+ restart** before drilling.
- Drill issues MUST pass the spec-guard: body needs an H2 structural header
  (`## Context` / `## Implementation` / `## Acceptance`) or the issue is struck
  with `pilot-spec-incomplete`+`pilot-blocked` and never dispatches (learned
  2026-08-13, GH-267 first strike).
- No daemon restart is needed for either drill (beyond the onboarding one).

## Drill A — dead-man streak (notify/label seam)

Guards: GH-4687/GH-4692 class ("wired to nothing" produces zero errors).
Trackers fire `dead_man_streak`-family events; alert at consecutive-failure
threshold (default 10, `alerts.DefaultDeadManFailureThreshold`).

1. Make the bot's label/comment mutations 403. ⚠️ **The as-written method —
   remove the bot's triage/write permission — is IMPOSSIBLE while the daemon
   authenticates as an org owner** (`alekspetrov` since the 08-07 OAuth move;
   org owners cannot be demoted per-repo). Working deviation (verified
   2026-08-13): **file the drill issues FIRST, then `gh repo archive` the
   sandbox** — archived repos 403 every mutation (`Repository was archived so
   is read-only`) while polling reads keep working; reverse with
   `gh repo unarchive`. Once the TASK-461 GitHub App cutover is done, the
   faithful method becomes uninstalling the App from the sandbox repo.
2. File ~12 trivial `pilot`-labeled issues on the sandbox repo (past the
   threshold of 10). Note the poller still QUEUES them (serial per-project) —
   each execution runs and then fails at push with the same 403; bounded
   cost (~$0.30/issue on sonnet-low), and the extra failures feed the streak.
3. Expect within the hour: notify attempts fail per dispatch → failure streak →
   one alert (Telegram/alert channels) naming the tracker. WARN lines in
   `daemon.log` per failure.
4. **Cleanup**: restore the bot permission; close the sandbox issues. The next
   success resets the streak.

## Drill B — Finish tripwire (root-clean check)

Guards: GH-4702 class (phantom work staged in the shared repo root). Tracker
names (from `internal/executor/finish_tripwires.go`):
`finish_tripwire_root_clean` · `finish_tripwire_label_lifecycle` ·
`finish_tripwire_children_terminal` · `finish_tripwire_worktree`.

1. File one trivial `pilot` issue on the sandbox repo.
2. While its execution runs, make an unstaged edit in the sandbox project's
   repo root on the box (e.g. `echo drill >> README.md` via SSM as ec2-user).
3. On the task's terminal `Finish`, expect a WARN in `daemon.log` with
   `tracker=finish_tripwire_root_clean` + the execution id, and a dead-man
   failure event. A single violation records a failure but does NOT page (streak
   threshold is 10 by design — violations alert on sustained runs); the log +
   event row is the drill's proof the wire is live.
4. **Cleanup**: `git -C <sandbox path> checkout -- README.md` on the box.

## Verification queries

```bash
# daemon log, both drills (this IS the proof — see note below):
grep -E "finish_tripwire|dead.?man|failure streak" ~/.pilot/logs/daemon.log | tail -20   # via SSM
```

⚠️ The original "event ledger" sqlite query here could never work:
`execution_events` has columns `(execution_id, stage, occurred_at, …)` — no
`event_type`/`metadata`/`created_at` (verified 2026-08-13). Tripwire
violations and dead-man failures are log + in-memory tracker state; only the
threshold ALERT produces a durable artifact (alert channels). The daemon.log
WARN line with `tracker=` + execution id is the drill's proof.

## After the drill

Tick the TASK-441 acceptance box ("kill-drill alert observed end-to-end") in
`.agent/tasks/TASK-441-contract-hardening-tune-up.md` and archive the task if
legs 6/8 are also done.

## Drill record — 2026-08-13 (first run)

- **Drill B: PASS.** `finish_tripwire_root_clean` WARN fired 10ms after
  GH-267's dispatcher Finish (tracker name + execution id in the line).
  Bonus real catch: the same tripwire had been firing on `startups/pilot`
  since 08-11 — root cause was a leaked LocalMode q-doc
  (`q-1784736791.md`, untracked in the box root; removed, backup in box
  `/tmp`). 9-deep genuine violation streak, reset by the cleanup.
- **Drill A: FAIL — the dead-man streak alert NEVER fired.** 57 minutes of
  continuous seam failures on the archived sandbox (12+ notify-started
  403s, executor.lifecycle label-strip ERRORs, push failures, ~36 infra
  executions across 12 issues with 3 retries each ≈ $10) produced ZERO
  `dead_man`/`failure streak` lines and no streak alert. The generic
  per-task `task_failed` rule DID fire (≈1/failed run → slack-engineering)
  — per-task alerting works; the seam-death signal does not. This is the
  exact GH-4687/4692 "wired to nothing" class the drill exists to catch.
  Defect dispatched: [pilot#4866](https://github.com/qf-studio/pilot/issues/4866)
  (TASK-477) — root cause: the four dead-man rules are never loaded by the
  daemon (they exist only in `alerts.defaultRules()`, CLI-only path), the
  streak is global-not-per-repo, two seams are unwired, and the no-match
  path is silent. Acceptance box stays UNTICKED until a re-drill passes
  after #4866 reaches the box.
- Ops notes: infra-classified failures retry ×3 per issue — close the drill
  issues IMMEDIATELY after the alert observation window to stop cost bleed
  (requires unarchive first; mutations are blocked while archived).
  Auto-merge on the sandbox is live — pre-archive drill PRs can MERGE
  (three did); keep drill changes no-op-safe.

## Drill record — 2026-08-13 (re-drill, post-#4866 fix)

- **Drill A: PASS.** Box v2.259.2 (PR#4867 live, running-version metric-confirmed).
  12 issues (#285–296) filed 18:25Z, repo archived 18:26Z. Streak crossed in
  **10 minutes**: 18:36:11Z `dead-man tracker reached failure threshold`
  (`component=alerts.deadman`, `tracker=label_lifecycle:qf-studio/pilot-canary-sandbox`,
  `consecutive_failures=10 threshold=10`) → 18:36:11.873Z
  `alert fired rule=label_lifecycle_failure_streak severity=critical
  delivered_to=[slack-engineering]`. **Bonus**: the newly-wired push seam also
  proved live — 18:41:07Z `tracker=push_retry_exhausted` crossed and
  `rule=push_retry_exhausted_failure_streak` paged. Per-repo keying confirmed
  on the wire (tracker name carries the repo). Cleanup 18:44Z: unarchive +
  all 12 issues closed. TASK-441 acceptance box 1 TICKED; task archived.
- Amusing find: from ~18:40Z the pre-flight intent judge began REJECTING the
  drill issues as out-of-scope (`reject_out_of_scope` — it read the honest
  "this is a drill" body and declined to implement). Honest drill bodies
  self-limit cost, but a future drill that needs sustained executions should
  write the body as a plausible trivial task instead.

## Postscript 2026-08-14 — de-onboarding IS part of drill cleanup

Leaving the sandbox onboarded after the drill **re-armed the dormant canary
scenario suite** (`canary-scenario-*` labels): a 4-issue probe batch fired
within 2 minutes of the unarchive, then every ~6h (18:45Z · 00:59Z · 07:05Z).
Ledger rows false-FAILED (`waiting_ci -> failed`) on every helper PR even
though all of them MERGED green — the sandbox `required_checks` config names
a check the repo never posts (its CI posts `test`); daemon self-diagnosed it
(`required-checks config mismatch` WARN). One probe stuck open tripped a
`lane_starvation` alert. Resolution (operator-consented): closed GH-315 +
canary-cascade PR#309, restored `config.yaml.bak-20260813-drill441` (diff was
exactly the 7-line canary project block; with-canary state saved as
`config.yaml.bak-20260814-with-canary`), daemon restarted — sandbox poller
gone, board green on v2.259.2.

**Rule: the drill cleanup checklist ends with de-onboard + restart**, not
with closing the drill issues. If the canary suite is ever wanted long-term,
fix the sandbox `required_checks` to `test` first or every cycle pollutes
the 30d delivery metrics with false failures.
