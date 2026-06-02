---
name: learn_restart_vs_rebuild_stale_binary
description: "Restart ≠ rebuild" — a daemon restart picks up new code only if the binary was rebuilt from updated source AND the process started after that build; verify, don't assume
type: learning
---
TASK-358 (2026-06-02). After merging the dashboard fix, the user restarted the pilot daemon and still saw the old `784 failed`. Cause chain:

1. The daemon runs `~/.local/bin/pilot`, built earlier from **root `main` at `7658f6b0` — two commits before the fix merged**. Root `main` had not been fast-forwarded after the merge, so the local build was stale.
2. A running process keeps executing the binary inode it was `exec`'d with; replacing the file on disk does nothing until the process is restarted *after* the replace. One restart was even on a binary built ~before the new file landed (start time 13:00 < install mtime 13:11).

So a "restart" proved nothing three separate times.

**Why:** "I restarted it" conflates three independent facts: (a) source is current (`git pull --ff-only` on the repo the binary builds from), (b) the binary was rebuilt from that source (`make build` / reinstall), (c) the *running* process started after (b). Only all three together mean the running daemon has the change.

**How to apply:** To confirm a daemon is on new code, check evidence, not the action:
- `pilot version` → expected version/commit.
- `ps -o lstart -p <pid>` start time **>** binary mtime (`ls -la <binary>`).
- For a data fix, query the store directly (e.g. `sqlite3 ~/.pilot/data/pilot.db "SELECT status,COUNT(*) …"`).
When a migration/backfill is gated behind daemon startup but the daemon is on a stale build, applying the same SQL directly (with a backup) corrects state immediately; the rebuilt binary still matters for *forward* behavior. Cross-ref [[pitfall_dashboard_failed_count_conflation]], [[feedback_check_remote_state]].
