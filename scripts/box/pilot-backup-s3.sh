#!/bin/bash
# Nightly backup of Pilot's ledger (SQLite DB) + knowledge graph JSON files to S3.
#
# Runs on the founder box (TASK-409) via pilot-backup.timer/pilot-backup.service.
# Deliberately daemon-independent — no AWS SDK, no Go code — so a backup (or a
# restore) works whether or not `pilot start` is running. See GH-4465 / incident
# GH-4393 (split-brain) for why this exists: before this script the only backup
# was a single manually-created pilot.db.pre-4393-merge.bak, and there were zero
# EBS snapshots for the box's volumes.
#
# Why VACUUM INTO instead of `cp`: pilot.db runs in WAL mode; a plain file copy
# can capture a torn/inconsistent snapshot mid-checkpoint. `VACUUM INTO` asks
# SQLite itself for a transactionally consistent copy.
#
# Config (env, all optional — defaults match the box layout from TASK-409):
#   PILOT_DATA_DIR   default /home/ec2-user/.pilot/data
#   BACKUP_BUCKET    default pilot-s3-agent-data
#   BACKUP_PREFIX    default backups
#
# Bucket policy requires SSE-KMS on every PUT (explicit deny otherwise) and
# TLS-only transport — see .agent/sops/operations/ledger-restore-from-s3.md.

set -euo pipefail

PILOT_DATA_DIR="${PILOT_DATA_DIR:-/home/ec2-user/.pilot/data}"
BACKUP_BUCKET="${BACKUP_BUCKET:-pilot-s3-agent-data}"
BACKUP_PREFIX="${BACKUP_PREFIX:-backups}"

DB_PATH="${PILOT_DATA_DIR}/pilot.db"
KNOWLEDGE_PATH="${PILOT_DATA_DIR}/knowledge.json"
PATTERNS_PATH="${PILOT_DATA_DIR}/global_patterns.json"

DATE_STAMP="$(date -u +%Y%m%d)"
DATE_PREFIX="$(date -u +%Y/%m/%d)"
ARCHIVE_NAME="pilot-backup-${DATE_STAMP}.tar.gz"

TMPDIR_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/pilot-backup.XXXXXX")"
cleanup() {
    rm -rf "$TMPDIR_ROOT"
}
trap cleanup EXIT

log() {
    echo "pilot-backup-s3: $*"
}

fail() {
    echo "pilot-backup-s3: ERROR: $*" >&2
    exit 1
}

if [ ! -f "$DB_PATH" ]; then
    fail "ledger not found at $DB_PATH"
fi

START_TIME=$(date +%s)

log "taking consistent snapshot of $DB_PATH"
DB_SNAPSHOT="${TMPDIR_ROOT}/pilot-${DATE_STAMP}.db"
sqlite3 "$DB_PATH" "VACUUM INTO '${DB_SNAPSHOT}'"

TAR_ENTRIES=("pilot-${DATE_STAMP}.db")

for src in "$KNOWLEDGE_PATH" "$PATTERNS_PATH"; do
    if [ -f "$src" ]; then
        cp "$src" "$TMPDIR_ROOT/"
        TAR_ENTRIES+=("$(basename "$src")")
    else
        log "WARNING: $src not found, skipping"
    fi
done

ARCHIVE_PATH="${TMPDIR_ROOT}/${ARCHIVE_NAME}"
tar czf "$ARCHIVE_PATH" -C "$TMPDIR_ROOT" "${TAR_ENTRIES[@]}"

S3_KEY="${BACKUP_PREFIX}/${DATE_PREFIX}/${ARCHIVE_NAME}"
S3_URI="s3://${BACKUP_BUCKET}/${S3_KEY}"

log "uploading to $S3_URI"
aws s3 cp "$ARCHIVE_PATH" "$S3_URI" --sse aws:kms

log "verifying upload"
if ! aws s3api head-object --bucket "$BACKUP_BUCKET" --key "$S3_KEY" >/dev/null 2>&1; then
    fail "head-object check failed for $S3_URI — upload not confirmed"
fi

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))
ARCHIVE_SIZE=$(wc -c < "$ARCHIVE_PATH" | tr -d ' ')

log "OK key=${S3_KEY} size=${ARCHIVE_SIZE}bytes duration=${DURATION}s"
