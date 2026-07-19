# TASK-411: Nightly ledger backup to S3 — script + systemd units + restore SOP

**Created**: 2026-07-19 · **Status**: 🚀 Dispatched to Pilot · **Last Updated**: 2026-07-19

## Problem

The founder box (`i-0e0c1ca34e7b561f9`, TASK-409) holds the only copy of
Pilot's irreplaceable state, with **zero automated backups**:

- `/var/lib/pilot/pilot-home/data/pilot.db` (~25M, SQLite WAL) — executions,
  execution_claims, autopilot_pr_state, events. Single source of truth.
- `/var/lib/pilot/pilot-home/data/knowledge.json` + `global_patterns.json` —
  knowledge graph / learned patterns.

The #4393 split-brain incident proved the risk class: ledger surgery relied on
one **manually created** `pilot.db.pre-4393-merge.bak`. No EBS snapshots exist
for the box's volumes. One volume event loses all execution history.

## Verified facts (do not re-derive; probed 2026-07-19)

- Bucket `s3://pilot-s3-agent-data` exists (bench-era, prefixes `bench/`,
  `moulage/`). Target prefix for this task: `backups/`.
- The box's instance role `pilot-agent` **already has** working
  put/list/delete on the bucket — round-trip verified from the box.
- Bucket policy enforces two guards (explicit deny otherwise):
  1. TLS-only transport (`aws:SecureTransport`)
  2. **Every upload MUST set SSE-KMS** — `aws s3 cp --sse aws:kms` (CLI) or
     `ServerSideEncryption: "aws:kms"` (SDK). A put without it fails
     `AccessDenied ... explicit deny in a resource-based policy`.
- `aws` CLI v2 and `sqlite3` are present on the box.

## Deliverables (all repo-tracked; installation on the box is a separate operator step)

### 1. `scripts/box/pilot-backup-s3.sh`

Bash, `set -euo pipefail`. Behavior:

1. `sqlite3 /home/ec2-user/.pilot/data/pilot.db "VACUUM INTO '$TMPDIR/pilot-YYYYMMDD.db'"`
   — WAL-safe consistent copy; NEVER `cp` the live db.
2. Copy `knowledge.json` + `global_patterns.json` alongside.
3. `tar czf` the three into `pilot-backup-YYYYMMDD.tar.gz`.
4. `aws s3 cp <archive> s3://pilot-s3-agent-data/backups/$(date -u +%Y/%m/%d)/ --sse aws:kms`
   (date-partitioned key; UTC).
5. Verify: `aws s3api head-object` on the uploaded key; non-zero exit if missing.
6. Log one summary line (size, key, duration) to stdout (journald captures it).
7. Cleanup tmp files on EXIT trap.

Config via env with defaults baked in (`PILOT_DATA_DIR`, `BACKUP_BUCKET`,
`BACKUP_PREFIX`) so the script is testable off-box.

### 2. systemd units: `scripts/box/pilot-backup.service` + `scripts/box/pilot-backup.timer`

- Timer: `OnCalendar=*-*-* 03:30 UTC` (queue-quiet window; releases train at
  14:00Z, busy hours 10–19Z), `Persistent=true`, `RandomizedDelaySec=300`.
- Service: `Type=oneshot`, `User=ec2-user`, runs the script.

### 3. Restore SOP: `.agent/sops/operations/ledger-restore-from-s3.md`

Step-by-step: stop daemon (operator consent) → download + untar → integrity
check `sqlite3 restored.db "PRAGMA integrity_check;"` → swap file → restart →
verify per safe-daemon-restart SOP. Include the "list available backups"
one-liner.

### 4. Lifecycle note (doc-only, no code)

Document in the SOP that a lifecycle rule expiring `backups/` after 90 days
should be added on the bucket (operator/console action — NOT part of this
task's code; the executor must not modify bucket config).

### 5. Tests

- `bats` or plain-sh test exercising the script against a temp SQLite db and a
  mocked `aws` (PATH-shim stub asserting `--sse aws:kms` and the key layout
  are present). Table-driven where sensible.
- ShellCheck-clean.

## Constraints

- ❌ Do NOT add AWS SDK / new Go code — this is deliberately daemon-independent
  (backups must work when the daemon is down).
- ❌ Do NOT touch `internal/`, bucket policy, IAM, or anything on the box
  itself — repo files only; operator installs the units.
- ❌ No secrets anywhere — auth is the instance role, script must not
  reference tokens/keys.
- ✅ Keep the script idempotent (re-run same day overwrites same key — fine).

## Acceptance

- [ ] Script + units + SOP + tests in repo, CI green, ShellCheck clean
- [ ] Local test run proves: VACUUM INTO copy, tar layout, mocked upload with
      `--sse aws:kms`, head-object verify, non-zero exit on upload failure
- [ ] SOP covers restore end-to-end incl. integrity check

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4465
- TASK-409 (S6-lite cutover) — box layout, shim rules
- `.agent/sops/operations/safe-daemon-restart.md`
- Incident GH-4393 (split-brain) — why this exists
- `pilot-bench/aws/pattern_sync.py` — prior art for SQLite↔S3 round-trip
