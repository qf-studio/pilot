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
  `startups/pilot` or any tenant repo.
- No daemon restart is needed for either drill.

## Drill A — dead-man streak (notify/label seam)

Guards: GH-4687/GH-4692 class ("wired to nothing" produces zero errors).
Trackers fire `dead_man_streak`-family events; alert at consecutive-failure
threshold (default 10, `alerts.DefaultDeadManFailureThreshold`).

1. On the sandbox repo, remove the bot account's triage/write permission
   (Settings → Collaborators), so label/comment mutations 403.
2. File ~12 trivial `pilot`-labeled issues on the sandbox repo (past the
   threshold of 10).
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
# daemon log, both drills:
grep -E "finish_tripwire|dead.?man|failure streak" ~/.pilot/logs/daemon.log | tail -20   # via SSM
# event ledger:
sqlite3 -column /home/ec2-user/.pilot/data/pilot.db \
  "SELECT event_type, substr(metadata,1,80), datetime(created_at) FROM execution_events \
   WHERE event_type LIKE '%dead_man%' OR metadata LIKE '%tripwire%' \
   ORDER BY rowid DESC LIMIT 20;"
```

## After the drill

Tick the TASK-441 acceptance box ("kill-drill alert observed end-to-end") in
`.agent/tasks/TASK-441-contract-hardening-tune-up.md` and archive the task if
legs 6/8 are also done.
