# SOP: Restore Pilot's Ledger from S3 Backup

**Category:** operations
**Created:** 2026-07-19
**Trigger:** The founder box's `pilot.db` (or knowledge graph JSON files) is
lost, corrupted, or needs to be rolled back — e.g. a volume event, a bad
manual edit, or a repeat of the GH-4393 split-brain class of incident.

## Why this exists

GH-4393 (split-brain, 2026-07-17) proved the risk class: ledger surgery
relied on a single **manually created** `pilot.db.pre-4393-merge.bak`, and
there were zero EBS snapshots for the box's volumes. `pilot-backup.timer`
(GH-4465, `scripts/box/pilot-backup-s3.sh`) now uploads a nightly,
transactionally-consistent snapshot to `s3://pilot-s3-agent-data/backups/`.
This SOP is the other half: how to pull one back down and put it in place
safely.

## Before you start

- **Get explicit operator consent before stopping the daemon.** Restoring
  the ledger requires the daemon to be down for the file swap — this is a
  visible, disruptive action per the "Executing actions with care" rules.
  Do not do this unilaterally.
- Know which backup you want: today's, or a specific earlier date (e.g. to
  roll back a bad state before a known-bad change).

## List available backups

```bash
aws s3 ls s3://pilot-s3-agent-data/backups/ --recursive | sort
# Narrow to one day:
aws s3 ls s3://pilot-s3-agent-data/backups/2026/07/19/
```

Keys are date-partitioned: `backups/YYYY/MM/DD/pilot-backup-YYYYMMDD.tar.gz`.

## Restore procedure

1. **Stop the daemon** (operator's action, in their own terminal — see
   `.agent/sops/operations/safe-daemon-restart.md`; do not background/detach
   it from an assistant shell):
   ```bash
   pgrep -fa "pilot start"   # confirm what's running
   pilot stop                # or: pkill -f "pilot start", then confirm zero matches
   ```

2. **Download and extract** the chosen backup into a scratch directory —
   never extract directly over the live data dir:
   ```bash
   WORKDIR=$(mktemp -d)
   aws s3 cp s3://pilot-s3-agent-data/backups/2026/07/19/pilot-backup-20260719.tar.gz "$WORKDIR/"
   tar xzf "$WORKDIR/pilot-backup-20260719.tar.gz" -C "$WORKDIR"
   ls "$WORKDIR"   # pilot-20260719.db, knowledge.json, global_patterns.json
   ```

3. **Integrity check** the restored DB before it goes anywhere near the live
   path — a corrupt download or a bad backup must not overwrite a working
   ledger:
   ```bash
   sqlite3 "$WORKDIR/pilot-20260719.db" "PRAGMA integrity_check;"
   # Must print exactly: ok
   ```
   If this does not print `ok`, stop — try an earlier date's backup instead
   of proceeding.

4. **Back up the current (possibly-broken) live file first** — never
   overwrite without a fallback:
   ```bash
   cp /home/ec2-user/.pilot/data/pilot.db "/home/ec2-user/.pilot/data/pilot.db.pre-restore-$(date -u +%Y%m%dT%H%M%SZ).bak"
   ```

5. **Swap the files in**:
   ```bash
   cp "$WORKDIR/pilot-20260719.db" /home/ec2-user/.pilot/data/pilot.db
   cp "$WORKDIR/knowledge.json" /home/ec2-user/.pilot/data/knowledge.json
   cp "$WORKDIR/global_patterns.json" /home/ec2-user/.pilot/data/global_patterns.json
   # Any *-wal / *-shm sidecar files from the old DB are now stale — remove them
   # so SQLite doesn't try to replay a WAL against the restored file:
   rm -f /home/ec2-user/.pilot/data/pilot.db-wal /home/ec2-user/.pilot/data/pilot.db-shm
   ```

6. **Restart the daemon** per `.agent/sops/operations/safe-daemon-restart.md`
   (single instance, operator's terminal, verify `pgrep -f "pilot start" | wc -l` → `1`).

7. **Verify**:
   - Daemon log shows normal startup, no "fresh ledger" / schema-init
     warnings.
   - `sqlite3 /home/ec2-user/.pilot/data/pilot.db "SELECT COUNT(*) FROM executions;"`
     returns the expected pre-incident row count, not zero.
   - Dashboard / `pilot-board` shows recent execution history, not an empty
     queue.
   - Watch the first poll cycle for unexpected re-dispatch of already-done
     issues (would indicate the restored ledger is missing recent claims —
     wrong backup date chosen).

8. **Cleanup**: `rm -rf "$WORKDIR"`. Keep the `pilot.db.pre-restore-*.bak`
   from step 4 for at least a few days in case the restore itself needs to
   be undone.

## Bucket lifecycle (doc-only — not this task's code)

`backups/` should eventually have an S3 lifecycle rule expiring objects
after 90 days so the bucket doesn't grow unbounded. This is a bucket-config
change (console or a separate IaC change with its own review), **not**
something `pilot-backup-s3.sh` or this SOP's restore path touches — the
backup script only ever does `s3 cp`/`s3api head-object` against the
existing bucket policy. Track adding the lifecycle rule as a follow-up
operator task.

## Related

- `scripts/box/pilot-backup-s3.sh` — the nightly backup script (this SOP's
  counterpart)
- `scripts/box/pilot-backup.service` / `pilot-backup.timer` — systemd units
  that run it nightly at 03:30 UTC
- `.agent/sops/operations/safe-daemon-restart.md` — daemon stop/start
  discipline (single-instance lock, TTY ownership)
- Incident GH-4393 (split-brain) — why automated backups exist
- TASK-409 — box layout (`/home/ec2-user/.pilot` → `/var/lib/pilot/pilot-home`
  symlink; the paths above are the symlink target's canonical form)
