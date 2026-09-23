---
name: ebs-volume-swap-runbook-xfs-nouuid-and-fstab-by-uuid
description: Founder-box data volume swap to encrypted (2026-09-23, ~4 min pause): snapshot copies keep the XFS UUID → side-by-side restore test needs `-o ro,nouuid`, in-place swap needs no fstab change; runbook stop → fuser/umount → detach/attach → mount -a → start → verify (integrity, counts, version, queue metric)
type: learning
---

# EBS data-volume swap runbook (founder box → encrypted volume, 2026-09-23)

**Context:** the founder box data volume (`/var/lib/pilot`, XFS, mounted by UUID in `/etc/fstab`) was unencrypted. Nelya built an encrypted copy chain (snapshot → copy under `alias/pilot` → volume). We ran a read-only restore test, then a ~4-minute swap.

## Two facts that decide the procedure
1. **A snapshot copy keeps the filesystem UUID.** XFS refuses to mount a second filesystem with a UUID already mounted → side-by-side restore test must use `mount -o ro,nouuid`. The same fact makes the in-place swap config-free: `/etc/fstab` mounts by that UUID, so `mount -a` picks the new device.
2. **KMS grants for EBS are created for the instance role at attach time** (by whoever attaches). An operator attaching needs `kms:CreateGrant/Decrypt/DescribeKey` via `ec2.<region>.amazonaws.com` on the key, or the attach fails with the misleading `CustomerKeyHasBeenRevoked`. Nelya's fix: IAM group `pilot-founder-box-operators` (attach/detach on `pilot:role=founder-box` volumes + the key via EC2 only). Decrypt then lives with the instance across stop/start.

## Restore test (read-only, no pause)
attach copy at `/dev/sdg` → `mount -o ro,nouuid /dev/nvmeXn1 /mnt/restore-test` → copy `pilot.db*` to tmp → `PRAGMA integrity_check` → compare `executions` count + `max(created_at)`, `autopilot_pr_state` count, recordings count vs live → `umount` (kill stray `find`s holding it: `fuser -vm`) → detach.

## Swap (operator-consent: daemon stops)
1. Queue idle. `tmux send-keys -t pilot C-c` → 0 `pilot` procs within ~5 s.
2. `fuser -vm /var/lib/pilot` (a stray `go` process held it) → `fuser -km` → `umount /var/lib/pilot`; confirm `findmnt` empty.
3. Say "go": infra takes a fresh snapshot, copies encrypted, builds, detaches old, attaches new at the same device.
4. `mount -a` → `findmnt` shows the new serial → integrity + counts on the live file (ro URI) → `tmux new-session -d -s pilot … start-pilot.sh` → 1 proc, `pilot version`, `pilot_queue_depth`, autopilot scanning, `pilot-board` renders.
5. Keep the old volume as rollback for a few days (Nelya's `pilot:delete-after` tag, 09-26).

Whole thing: 11:47 → 11:51 CEST.
