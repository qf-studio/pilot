#!/bin/bash
# GH-5063 tripwire: catch whatever writer flips core.bare from false to true.
#
# Background
# ----------
# `.git/config`'s `core.bare` has flipped false -> true four times so far
# (2026-07-28, 2026-08-18, 2026-08-21, and the occurrence that filed this
# issue). Because worktrees share the common `.git/config`, the flip breaks
# `git status`/build/lint/test for EVERY concurrent session (root + all
# linked worktrees), not just whichever session triggered it.
#
# Three prior investigations grepped this repo's shell scripts, git hooks,
# and Go code for anything that writes `core.bare`, `--bare`, `GIT_DIR`, or
# `--work-tree`, and found nothing. No `trap` exists anywhere in the
# pre-push scripts either, so there's no restore-on-EXIT-but-not-TERM
# asymmetry to explain it. The writer is still unidentified.
#
# KNOWN NEGATIVE (2026-08-24): SIGTERM'ing `scripts/pre-push-gate.sh` mid
# `go test -short -race ./...` (the leading hypothesis, since the gate's
# ~188-238s runtime exceeds a 120s push timeout) did NOT reproduce the
# flip. That weakens, but does not rule out, the pre-push-gate hypothesis.
# Do NOT assume the gate is the writer — this watcher must catch the flip
# regardless of what causes it, not just while the gate is running.
#
# What this does
# --------------
# Polls the *shared* git config (resolved via `git rev-parse
# --git-common-dir`, so it works whether launched from the repo root or
# from a linked worktree) for `core.bare` every GCW_POLL_INTERVAL seconds
# (default 3, range 2-5s per the issue). The instant it observes a flip
# from false/unset to true, BEFORE healing it captures a forensic snapshot
# to the log file:
#   - UTC timestamp of detection
#   - the config file's mtime
#   - `lsof` holders of the config file (who has it open right now)
#   - a full `ps` process-table snapshot with parent chains (forest view
#     where supported)
# ...then heals the value back to `false` and logs a loud confirmation
# line. It keeps polling indefinitely until killed.
#
# This is PASSIVE, OPERATOR-OPT-IN TOOLING (GH-5218). Nothing in this repo
# ARMS it automatically — no cron, no automatic start from any daemon code
# path. The Makefile target, systemd unit, and launchd plist below are all
# shipped disabled: they package the same manual invocation and don't
# replace the "an operator decides to run this" contract. You (or an
# operator) must explicitly start/enable it, specifically around push
# activity you suspect will reproduce the flip — it must stay armed
# continuously around pushes, not just while scripts/pre-push-gate.sh runs
# (SIGTERM'ing the gate mid-test did NOT reproduce the flip on 2026-08-24,
# so the watcher must not be wired into the gate either).
#
# How to run it — three equivalent paths
# ---------------------------------------
# 1. Ad hoc (either machine, foreground in a terminal/tmux pane you leave
#    open):
#      tmux new -s git-config-watch
#      ./scripts/git-config-watch.sh
#      # ... leave it running, do your normal `git push` in another pane ...
#      # Ctrl-b d to detach; `tmux attach -t git-config-watch` to check back.
#
# 2. `make watch-git-config` — thin foreground wrapper around this script
#    (inherits GCW_* env vars from your shell). Same tmux caveat applies:
#    the make invocation still needs a pane/session that outlives your
#    shell if you want it to survive detach.
#
# 3. Packaged as a service — for the founder box, where pushes originate
#    from autopilot and no tmux pane survives a session boundary:
#      systemd --user unit: scripts/git-config-watch.service
#        cp scripts/git-config-watch.service ~/.config/systemd/user/
#        systemctl --user daemon-reload
#        systemctl --user enable --now git-config-watch.service  # <- arms it
#    ...or for the laptop (macOS push context):
#      launchd agent: scripts/com.pilot.git-config-watch.plist
#        cp scripts/com.pilot.git-config-watch.plist ~/Library/LaunchAgents/
#        launchctl load ~/Library/LaunchAgents/com.pilot.git-config-watch.plist  # <- arms it
#    Both unit files are committed SHIPPED DISABLED — copying the file does
#    not start anything; the `enable --now` / `launchctl load` step is the
#    explicit, manual, operator-initiated arming step. See each file's own
#    header for full install/disarm instructions.
#
# Both machines are push contexts where scripts/pre-push-gate.sh runs, so
# both are candidate reproduction sites for occurrence #5.
#
# Single-instance guard
# ----------------------
# All three launch paths (ad hoc, make target, service) go through the same
# script, and all take a lock at GCW_LOCK_FILE before doing anything else. A
# second concurrent launch (e.g. a stray tmux copy left running alongside
# the systemd unit) refuses to start, names the pid of the already-running
# instance, and exits non-zero — so two watchers never double-heal
# core.bare or interleave forensic captures in the same log file. A lock
# held by a pid that's no longer alive (stale, e.g. after a crash) is
# reclaimed automatically.
#
# Manual verification (acceptance check)
# ---------------------------------------
#   git config core.bare true   # from the repo root, while the watcher is running
#   # within GCW_POLL_INTERVAL seconds: watcher logs holders + process
#   # snapshot, then heals core.bare back to false and logs the heal.
#
# Env vars
# --------
#   GCW_POLL_INTERVAL  seconds between polls (default 3)
#   GCW_LOG_FILE       forensic log path (default "$TMPDIR/git-config-watch.log",
#                      falling back to /tmp if TMPDIR is unset)
#   GCW_LOCK_FILE      single-instance lock path (default
#                      "$TMPDIR/git-config-watch.lock", falling back to /tmp
#                      if TMPDIR is unset)

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

POLL_INTERVAL="${GCW_POLL_INTERVAL:-3}"
LOG_FILE="${GCW_LOG_FILE:-${TMPDIR:-/tmp}/git-config-watch.log}"
LOCK_FILE="${GCW_LOCK_FILE:-${TMPDIR:-/tmp}/git-config-watch.lock}"

# Resolve the shared git dir once. `--git-common-dir` returns the *shared*
# dir even when run from a linked worktree, which is exactly the file all
# concurrent sessions are fighting over.
GIT_COMMON_DIR="$(git -C "$PROJECT_ROOT" rev-parse --git-common-dir 2>/dev/null)"
if [ -z "$GIT_COMMON_DIR" ]; then
    echo "❌ ERROR: not a git repository (or git-common-dir lookup failed) at $PROJECT_ROOT" >&2
    exit 1
fi
# rev-parse returns a path relative to PROJECT_ROOT unless it's already absolute.
case "$GIT_COMMON_DIR" in
    /*) : ;;
    *) GIT_COMMON_DIR="$PROJECT_ROOT/$GIT_COMMON_DIR" ;;
esac
CONFIG_FILE="$GIT_COMMON_DIR/config"

log() {
    printf '%s\n' "$1" | tee -a "$LOG_FILE"
}

get_bare_value() {
    git -C "$PROJECT_ROOT" config --get core.bare 2>/dev/null || echo "false"
}

get_config_mtime() {
    stat -c '%y (epoch %Y)' "$CONFIG_FILE" 2>/dev/null \
        || stat -f '%Sm (epoch %m)' "$CONFIG_FILE" 2>/dev/null \
        || echo "unavailable (stat failed)"
}

get_lsof_holders() {
    if command -v lsof >/dev/null 2>&1; then
        lsof -- "$CONFIG_FILE" 2>/dev/null || echo "(lsof found no holders)"
    else
        echo "(lsof not installed on this host)"
    fi
}

get_process_snapshot() {
    if ps -eo pid,ppid,pgid,user,lstart,args --forest >/dev/null 2>&1; then
        ps -eo pid,ppid,pgid,user,lstart,args --forest
    else
        # BSD/macOS ps has no --forest; fall back to a flat table that
        # still includes ppid so parent chains can be reconstructed by hand.
        ps -eo pid,ppid,pgid,user,lstart,command 2>/dev/null \
            || ps -ef
    fi
}

capture_and_heal() {
    local detected_at="$1"
    {
        echo "================================================================"
        echo "CORE.BARE FLIP DETECTED"
        echo "  detected_at (UTC): $detected_at"
        echo "  config_file:       $CONFIG_FILE"
        echo "  config_file_mtime: $(get_config_mtime)"
        echo "----------------------------------------------------------------"
        echo "lsof holders of $CONFIG_FILE:"
        get_lsof_holders
        echo "----------------------------------------------------------------"
        echo "process table (pid/ppid/pgid/user/start/cmd):"
        get_process_snapshot
        echo "================================================================"
    } | tee -a "$LOG_FILE"

    git -C "$PROJECT_ROOT" config core.bare false
    log "🔧 HEALED: core.bare set back to false at $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
}

# Single-instance guard (GH-5218): a second concurrent watcher would race
# the first on capture_and_heal (double-heal, interleaved forensic writes
# to the same log file), so refuse to start if another instance already
# holds GCW_LOCK_FILE. A lock left behind by a pid that's no longer alive
# (crash, kill -9) is stale and gets reclaimed automatically.
release_lock() {
    # Only remove the lock if it still names this process — avoids
    # clobbering a newer instance's lock in the unlikely event this
    # instance's own stale-lock reclaim raced another launch.
    if [ -f "$LOCK_FILE" ] && [ "$(cat "$LOCK_FILE" 2>/dev/null)" = "$$" ]; then
        rm -f "$LOCK_FILE"
    fi
}

acquire_lock() {
    if [ -f "$LOCK_FILE" ]; then
        local existing_pid=""
        existing_pid="$(cat "$LOCK_FILE" 2>/dev/null || true)"
        if [ -n "$existing_pid" ] && kill -0 "$existing_pid" 2>/dev/null; then
            echo "❌ ERROR: git-config-watch is already running as pid $existing_pid (lock: $LOCK_FILE)." >&2
            echo "   A second concurrent watcher would double-heal core.bare and interleave" >&2
            echo "   forensic captures in the same log file. Attach to the running instance" >&2
            echo "   instead (it's already covering this repo)." >&2
            echo "   If pid $existing_pid is wrong/stale, remove the lock manually: rm $LOCK_FILE" >&2
            exit 1
        fi
        echo "⚠ stale lock at $LOCK_FILE (pid ${existing_pid:-unknown} not running) — reclaiming" >&2
    fi
    echo "$$" > "$LOCK_FILE"
    trap release_lock EXIT
}

acquire_lock

echo "git-config-watch: watching $CONFIG_FILE every ${POLL_INTERVAL}s"
echo "git-config-watch: forensic log -> $LOG_FILE"
echo "git-config-watch: press Ctrl-C to stop"
log "--- watch session started $(date -u +'%Y-%m-%dT%H:%M:%SZ') (pid $$) ---"

PREV_VALUE="$(get_bare_value)"
if [ "$PREV_VALUE" = "true" ]; then
    log "⚠ core.bare is ALREADY true at watch start — healing immediately, but this means the flip already happened before this watcher attached (no forensic capture possible for that occurrence)."
    git -C "$PROJECT_ROOT" config core.bare false
    PREV_VALUE="false"
fi

while true; do
    sleep "$POLL_INTERVAL"
    CURRENT_VALUE="$(get_bare_value)"
    if [ "$CURRENT_VALUE" = "true" ] && [ "$PREV_VALUE" != "true" ]; then
        capture_and_heal "$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
        CURRENT_VALUE="false"
    fi
    PREV_VALUE="$CURRENT_VALUE"
done
