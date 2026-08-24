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
# This is PASSIVE, OPERATOR-OPT-IN TOOLING. Nothing in this repo starts it
# automatically — no cron, no systemd unit, no CI wiring, no Makefile
# target. You must run it by hand, in a terminal or tmux pane you leave
# open, specifically around a push you suspect will reproduce the flip.
#
# How to run it ad hoc
# ---------------------
# On the laptop (interactive push context):
#   tmux new -s git-config-watch
#   ./scripts/git-config-watch.sh
#   # ... leave it running, do your normal `git push` in another pane ...
#   # Ctrl-b d to detach; `tmux attach -t git-config-watch` to check back.
#
# On the founder box (autopilot/CI push context — see the pilot-aws skill
# for how to get a shell there):
#   tmux new -s git-config-watch
#   /var/lib/pilot/repo/scripts/git-config-watch.sh   # path may differ; cd to the repo checkout first
#
# Both are push contexts where scripts/pre-push-gate.sh runs, so both are
# candidate reproduction sites for occurrence #5.
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

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

POLL_INTERVAL="${GCW_POLL_INTERVAL:-3}"
LOG_FILE="${GCW_LOG_FILE:-${TMPDIR:-/tmp}/git-config-watch.log}"

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
