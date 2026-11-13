#!/bin/bash
# Host setup for the agent user (TASK-651 / leg U0, design in
# .agent/system/agent-isolation-design.md).
#
# Agent sessions run under a second Unix user so that code written by a model
# cannot read the daemon's secrets, push with the daemon's token or become root.
# This script prepares the Linux host for that. An operator runs it by hand as
# root; no daemon code calls it.
#
# Apply mode (default), each step skipped when it is already done:
#   group             system group (default: pilot)
#   agent-user        system user, no login shell, home DATA_DIR/agent-home
#   daemon-membership daemon user joins the group
#   directories       agent-home, agent-workspaces, go/cache, go/mod
#   shared-read       group read on exactly what the agent needs, never the working
#                     tree: the bin directory and the binary; the repos top
#                     directory; the directories on the way to a repository (group
#                     traverse only); and per repository its git directory (objects,
#                     refs, packed-refs, HEAD, config). Every path whose group or
#                     mode changes is recorded in DATA_DIR/.agent-setup-state first.
#                     When repos is reached through a symlink chain (or --repos-link
#                     is given) every directory of the chain must be traversable by
#                     the agent user; the step prints the command for each one that
#                     is not and fails.
#   sudoers-rule      daemon user may run any command as the agent user
#                     (NOPASSWD, SETENV); validated BEFORE it is moved into place
#   firewall-<family> dedicated chain, owner match on the agent user: reject the
#                     metadata address and TCP to the gateway port on loopback; the
#                     chain is rebuilt under a temporary name and switched in, so
#                     the reject rules are never missing. IPv6 is skipped when
#                     ip6tables is missing.
#   firewall-unit     oneshot systemd unit that applies the chain at boot
#   cloud-init-sudoers  LAST: comment out the lines that give the daemon user (and
#                     nobody else) sudo to root in the cloud-init sudoers file
#                     (numbered backups, validated), then prove with
#                     sudo -n -l -U that no NOPASSWD rule is left but the agent rule.
#                     Needs --confirm-root-path.
#
# Usage: setup-agent-user.sh [--daemon-user U] [--agent-user U] [--group G]
#                            [--data-dir D] [--repos-link P]... [--dry-run]
#                            [--undo] [--keep-daemon-sudo] [--confirm-root-path]
#
#   --repos-link P       a path through which the agent user reaches the repos
#                        directory (a symlink chain); every directory on it is
#                        checked for traverse; repeatable
#   --confirm-root-path  the operator holds another root session; required when
#                        the cloud-init file still has a line to comment out (not
#                        for --dry-run, --undo, --keep-daemon-sudo or a host that is
#                        set up already); asked before the first change
#   --dry-run            print every command, change nothing
#   --undo               stop and disable the boot unit, remove the firewall chain
#                        and the sudoers rule, restore the cloud-init file from its
#                        newest backup, restore the group and mode of every path in
#                        the state file; the user, the group and the directories
#                        stay (the commands to delete them are printed, not run)
#   --keep-daemon-sudo   leave the cloud-init sudoers file alone
#
# Paths (env, real paths are the defaults; the test points them at a temp root):
#   SUDOERS_DIR          default /etc/sudoers.d
#   SUDOERS_MAIN         default /etc/sudoers (searched for a remaining rule)
#   SYSTEMD_DIR          default /etc/systemd/system
#   CLOUD_INIT_SUDOERS   default $SUDOERS_DIR/90-cloud-init-users
#   NOLOGIN_SHELL        default /usr/sbin/nologin
#   IPTABLES_CMD         default iptables
#   IP6TABLES_CMD        default ip6tables
#
# Exit status: 0 when every step is done or skipped, 1 when a step failed (the
# run stops at the first failed step; the steps after it are listed in the
# summary as skipped), 2 on bad arguments (nothing is changed).

set -euo pipefail

DAEMON_USER="ec2-user"
AGENT_USER="pilot-agent"
GROUP="pilot"
DATA_DIR="/var/lib/pilot"
DRY_RUN=0
UNDO=0
KEEP_DAEMON_SUDO=0
CONFIRM_ROOT_PATH=0
REPOS_LINKS=()

SUDOERS_DIR="${SUDOERS_DIR:-/etc/sudoers.d}"
SUDOERS_MAIN="${SUDOERS_MAIN:-/etc/sudoers}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
CLOUD_INIT_SUDOERS="${CLOUD_INIT_SUDOERS:-${SUDOERS_DIR}/90-cloud-init-users}"
NOLOGIN_SHELL="${NOLOGIN_SHELL:-/usr/sbin/nologin}"
IPT4="${IPTABLES_CMD:-iptables}"
IPT6="${IP6TABLES_CMD:-ip6tables}"

SUDOERS_NAME="90-pilot-agent-runas"
UNIT_NAME="pilot-agent-firewall.service"
CHAIN="PILOT_AGENT_EGRESS"
CHAIN_NEW="${CHAIN}_NEW"
CHAIN_ALT="${CHAIN}_ALT"
METADATA_V4="169.254.169.254"
METADATA_V6="fd00:ec2::254"
GATEWAY_PORT="9091"
BINARY_NAME="pilot"
STATE_NAME=".agent-setup-state"

# Directories the data dir must never be, nor contain (Linux and the macOS
# aliases of the test host).
SYSTEM_DIRS="/ /bin /boot /dev /etc /home /lib /lib32 /lib64 /libx32 /media /mnt /opt /proc /root /run /sbin /srv /sys /tmp /usr /usr/bin /usr/lib /usr/lib64 /usr/libexec /usr/local /usr/local/bin /usr/sbin /usr/share /var /var/cache /var/lib /var/log /var/mail /var/run /var/spool /var/tmp /private /private/etc /private/tmp /private/var /System /Library /Applications /Users"

SUMMARY=()
RESULT=""
NOTE=""

die() {
    echo "setup-agent-user: $*" >&2
    exit 2
}

usage() {
    sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --daemon-user | --agent-user | --group | --data-dir | --repos-link)
            [ $# -ge 2 ] || die "$1 needs a value"
            case "$1" in
                --daemon-user) DAEMON_USER="$2" ;;
                --agent-user) AGENT_USER="$2" ;;
                --group) GROUP="$2" ;;
                --data-dir) DATA_DIR="$2" ;;
                --repos-link) REPOS_LINKS+=("$2") ;;
            esac
            shift 2
            ;;
        --daemon-user=*) DAEMON_USER="${1#*=}"; shift ;;
        --agent-user=*) AGENT_USER="${1#*=}"; shift ;;
        --group=*) GROUP="${1#*=}"; shift ;;
        --data-dir=*) DATA_DIR="${1#*=}"; shift ;;
        --repos-link=*) REPOS_LINKS+=("${1#*=}"); shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --undo) UNDO=1; shift ;;
        --keep-daemon-sudo) KEEP_DAEMON_SUDO=1; shift ;;
        --confirm-root-path) CONFIRM_ROOT_PATH=1; shift ;;
        -h | --help) usage; exit 0 ;;
        *) usage; die "unknown argument: $1" ;;
    esac
done

# ------------------------------------------------------- argument validation
# Everything below runs before the first change; a refusal exits 2.

if [ "$(id -u)" -ne 0 ]; then
    die "must run as root"
fi

NAME_RE='^[a-z][a-z0-9_-]*$'
for pair in "daemon-user:$DAEMON_USER" "agent-user:$AGENT_USER" "group:$GROUP"; do
    pair_value="${pair#*:}"
    [[ "$pair_value" =~ $NAME_RE ]] || die "--${pair%%:*} must match ${NAME_RE}: '${pair_value}'"
    [ "${#pair_value}" -le 32 ] || die "--${pair%%:*} is longer than 32 characters"
done

# check_path_arg FLAG VALUE — absolute, no leading dash, no ".." part.
check_path_arg() {
    case "$2" in -*) die "$1 must not start with a dash: '$2'" ;; esac
    [[ "/$2/" != */../* ]] || die "$1 must not have a '..' part: '$2'"
    [[ "$2" =~ ^/[A-Za-z0-9._/-]*$ ]] || die "$1 must be an absolute path of [A-Za-z0-9._/-]: '$2'"
}

id -u "$DAEMON_USER" >/dev/null 2>&1 || die "daemon user '${DAEMON_USER}' does not exist"
DAEMON_UID="$(id -u "$DAEMON_USER")"
[ "$DAEMON_USER" != root ] && [ "$DAEMON_UID" -ge 1000 ] || die "the daemon user must not be root or have a uid below 1000 (${DAEMON_USER} has ${DAEMON_UID})"
[ "$DAEMON_USER" != "$AGENT_USER" ] || die "the agent user must differ from the daemon user"
[ "$AGENT_USER" != root ] || die "the agent user must not be root"

if id -u "$AGENT_USER" >/dev/null 2>&1; then
    [ "$(id -u "$AGENT_USER")" -ne 0 ] || die "the agent user '${AGENT_USER}' exists with uid 0"
    agent_shell="$(getent passwd "$AGENT_USER" | cut -d: -f7 || true)"
    case "$agent_shell" in
        /sbin/nologin | /usr/sbin/nologin | /usr/bin/nologin | /bin/false | /usr/bin/false | "$NOLOGIN_SHELL") ;;
        *) die "the agent user '${AGENT_USER}' exists with a login shell ('${agent_shell}'); it needs a no-login shell" ;;
    esac
    for g in $(id -nG "$AGENT_USER"); do
        case "$g" in wheel | sudo) die "the agent user '${AGENT_USER}' is a member of ${g}" ;; esac
    done
fi

DAEMON_PRIMARY_GROUP="$(id -gn "$DAEMON_USER")"
case "$GROUP" in
    root | wheel | sudo | adm) die "--group must not be ${GROUP}" ;;
esac
[ "$GROUP" != "$DAEMON_PRIMARY_GROUP" ] || die "--group must not be the primary group of the daemon user (${DAEMON_PRIMARY_GROUP})"

# refuse_system_dir PATH — PATH must not be a system directory nor an ancestor
# of one. Checked on the value as given and on its canonical form.
refuse_system_dir() {
    local p="$1" s
    while [ "$p" != / ] && [ "${p%/}" != "$p" ]; do p="${p%/}"; done
    for s in $SYSTEM_DIRS; do
        [ "$p" != "$s" ] || die "--data-dir '${p}' is a system directory"
        case "${s}/" in "${p}"/*) die "--data-dir '${p}' contains the system directory ${s}" ;; esac
    done
}

check_path_arg --data-dir "$DATA_DIR"
refuse_system_dir "$DATA_DIR"
[ -e "$DATA_DIR" ] || die "--data-dir does not exist: '${DATA_DIR}'"
DATA_DIR="$(realpath "$DATA_DIR")" || die "cannot resolve --data-dir"
refuse_system_dir "$DATA_DIR"
[ -d "$DATA_DIR" ] || die "--data-dir is not a directory: '${DATA_DIR}'"
[ "$(stat -c %U "$DATA_DIR")" = "$DAEMON_USER" ] || die "--data-dir '${DATA_DIR}' is not owned by the daemon user (${DAEMON_USER})"

for link in ${REPOS_LINKS[@]+"${REPOS_LINKS[@]}"}; do
    check_path_arg --repos-link "$link"
done

AGENT_HOME="${DATA_DIR}/agent-home"
WORKSPACES="${DATA_DIR}/agent-workspaces"
STATE_FILE="${DATA_DIR}/${STATE_NAME}"
SUDOERS_FILE="${SUDOERS_DIR}/${SUDOERS_NAME}"
SUDOERS_TMP="${SUDOERS_DIR}/.${SUDOERS_NAME}.new"
CLOUD_INIT_BACKUP="${CLOUD_INIT_SUDOERS}.pilot-agent.bak"
UNIT_FILE="${SYSTEMD_DIR}/${UNIT_NAME}"

# contained PATH — true when the resolved form of PATH is strictly inside the
# data dir. Sets RESOLVED (empty when PATH cannot be resolved).
RESOLVED=""
contained() {
    RESOLVED="$(realpath "$1" 2>/dev/null)" || RESOLVED=""
    case "$RESOLVED" in
        "$DATA_DIR"/*) return 0 ;;
    esac
    return 1
}

# resolve_inside LABEL PATH TYPE — sets RESOLVED to the resolved path of PATH,
# or "" when it does not exist. A path whose resolved form is not strictly
# inside the data dir, or has the wrong type, is refused.
resolve_inside() {
    local label="$1" path="$2" type="$3"
    RESOLVED=""
    if [ ! -e "$path" ]; then
        [ ! -L "$path" ] || die "${label} '${path}' is a dangling symbolic link"
        return 0
    fi
    contained "$path" || die "${label} '${path}' resolves to '${RESOLVED:-?}', outside the data dir '${DATA_DIR}'"
    case "$type" in
        d) [ -d "$RESOLVED" ] || die "${label} '${RESOLVED}' is not a directory" ;;
        f) [ -f "$RESOLVED" ] || die "${label} '${RESOLVED}' is not a regular file" ;;
    esac
}

resolve_inside "the repos directory" "${DATA_DIR}/repos" d
REPOS_REAL="$RESOLVED"
resolve_inside "the bin directory" "${DATA_DIR}/bin" d
BIN_REAL="$RESOLVED"
resolve_inside "the binary" "${DATA_DIR}/bin/${BINARY_NAME}" f
BINARY_REAL="$RESOLVED"

# The directories the script creates must be real directories inside the data dir.
for managed in "$AGENT_HOME" "$WORKSPACES" "${AGENT_HOME}/go" "${AGENT_HOME}/go/cache" "${AGENT_HOME}/go/mod"; do
    [ ! -L "$managed" ] || die "'${managed}' is a symbolic link; refused"
    resolve_inside "the directory" "$managed" d
done

# run prints a command and, outside dry-run, executes it. Read-only checks do
# not go through run: they run in dry-run too, so the plan matches the host.
run() {
    printf '+'
    printf ' %q' "$@"
    printf '\n'
    if [ "$DRY_RUN" = 1 ]; then
        return 0
    fi
    "$@"
}

# write_file PATH MODE CONTENT — root-only until MODE is set.
write_file() {
    local path="$1" mode="$2" content="$3"
    printf '+ write %s (mode %s)\n' "$path" "$mode"
    if [ "$DRY_RUN" = 1 ]; then
        printf '%s\n' "$content" | sed 's/^/|   /'
        return 0
    fi
    (umask 077 && printf '%s\n' "$content" >"$path") || return 1
    run chmod "$mode" "$path"
}

file_has_content() {
    [ -f "$1" ] && [ "$(cat "$1")" = "$2" ]
}

# ensure_dir PATH OWNER GROUP MODE — sets CHANGED=1 when it had to act.
CHANGED=0
ensure_dir() {
    local path="$1" owner="$2" group="$3" mode="$4" cur
    cur="$(stat -c '%U:%G:%a' "$path" 2>/dev/null || true)"
    if [ "$cur" = "${owner}:${group}:${mode}" ]; then
        return 0
    fi
    run install -d -o "$owner" -g "$group" -m "$mode" "$path" || return 1
    CHANGED=1
}

# ---------------------------------------------------------------- apply steps

step_group() {
    if getent group "$GROUP" >/dev/null 2>&1; then
        RESULT=skipped
        return 0
    fi
    run groupadd --system "$GROUP" || return 1
}

step_agent_user() {
    if id -u "$AGENT_USER" >/dev/null 2>&1; then
        RESULT=skipped
        return 0
    fi
    run useradd --system --gid "$GROUP" --home-dir "$AGENT_HOME" --no-create-home \
        --shell "$NOLOGIN_SHELL" "$AGENT_USER" || return 1
}

step_daemon_membership() {
    if id -nG "$DAEMON_USER" | tr ' ' '\n' | grep -qx "$GROUP"; then
        RESULT=skipped
        return 0
    fi
    run usermod -aG "$GROUP" "$DAEMON_USER" || return 1
    NOTE="running daemon processes pick up the group at the next restart"
}

step_directories() {
    CHANGED=0
    ensure_dir "$AGENT_HOME" "$AGENT_USER" "$GROUP" 750 || return 1
    ensure_dir "$WORKSPACES" "$AGENT_USER" "$GROUP" 2770 || return 1
    ensure_dir "${AGENT_HOME}/go" "$AGENT_USER" "$GROUP" 750 || return 1
    ensure_dir "${AGENT_HOME}/go/cache" "$AGENT_USER" "$GROUP" 750 || return 1
    ensure_dir "${AGENT_HOME}/go/mod" "$AGENT_USER" "$GROUP" 750 || return 1
    [ "$CHANGED" = 1 ] || RESULT=skipped
    return 0
}

# state_file_ok — the state file is a regular file, root:root 0600.
state_file_ok() {
    [ ! -L "$STATE_FILE" ] && [ -f "$STATE_FILE" ] && [ "$(stat -c '%U:%G:%a' "$STATE_FILE")" = "root:root:600" ]
}

# record_change PATH GROUP MODE — the earlier group and mode of a path, written
# to the state file BEFORE the path is changed (dry-run: printed only). The file
# is root-only; a link or a file of another owner in its place is refused.
record_change() {
    local path="$1" grp="$2" mode="$3"
    case "$path" in
        *$'\t'* | *$'\n'*)
            echo "path with a tab or newline, cannot record it: ${path}" >&2
            return 1
            ;;
    esac
    printf '+ record %s group=%s mode=%s in %s\n' "$path" "$grp" "$mode" "$STATE_FILE"
    if [ "$DRY_RUN" = 1 ]; then
        return 0
    fi
    if [ -L "$STATE_FILE" ] || { [ -e "$STATE_FILE" ] && [ ! -f "$STATE_FILE" ]; }; then
        echo "state file ${STATE_FILE} is not a regular file; refusing to use it" >&2
        return 1
    fi
    if [ ! -e "$STATE_FILE" ]; then
        run install -m 0600 -o root -g root /dev/null "$STATE_FILE" || return 1
    fi
    if ! state_file_ok; then
        echo "state file ${STATE_FILE} is not root:root 0600; refusing to use it" >&2
        return 1
    fi
    printf '%s\t%s\t%s\n' "$grp" "$mode" "$path" >>"$STATE_FILE"
}

# shared_read_ok PATH BITS — true when PATH is already in the shared group with
# BITS (r=4, x=1, r-x=5, as a number) added to the group class and no group
# write. Sets SR_GROUP, SR_MODE (current, octal text) and SR_WANT (octal text).
# A file the owner can execute keeps that for the group (the chmod g+rX rule).
# BITS 1 (a repository or a middle directory) is traverse only: a group read bit
# the directory had is dropped, as the chgrp would hand it to the shared group.
SR_GROUP="" SR_MODE="" SR_WANT=""
shared_read_ok() {
    local path="$1" bits="$2" cur mode
    cur="$(stat -c '%G:%a' "$path")" || return 1
    SR_GROUP="${cur%%:*}"
    SR_MODE="${cur#*:}"
    if [ "$SR_GROUP" = UNKNOWN ]; then
        SR_GROUP="$(stat -c %g "$path")"
    fi
    mode=$((8#${SR_MODE}))
    if [ "$bits" = 4 ] && [ $(((mode >> 6) & 1)) -eq 1 ]; then
        bits=5
    fi
    if [ "$bits" = 1 ]; then
        SR_WANT="$(printf '%o' $(((mode & ~8#070) | 8#010)))"
    else
        SR_WANT="$(printf '%o' $(((mode | (bits << 3)) & ~8#020)))"
    fi
    [ "$SR_GROUP" = "$GROUP" ] && [ $((8#${SR_WANT})) -eq "$mode" ]
}

# set_shared PATH BITS — bring PATH to the state shared_read_ok describes. A
# symbolic link is left alone, tested directly before each change. Sets CHANGED=1.
set_shared() {
    local path="$1" bits="$2"
    if [ -L "$path" ]; then
        echo "symbolic link, left alone: ${path}"
        return 0
    fi
    if shared_read_ok "$path" "$bits"; then
        return 0
    fi
    record_change "$path" "$SR_GROUP" "$SR_MODE" || return 1
    if [ "$SR_GROUP" != "$GROUP" ]; then
        if [ -L "$path" ]; then
            echo "symbolic link, left alone: ${path}"
            return 0
        fi
        run chgrp -h "$GROUP" "$path" || return 1
    fi
    if [ $((8#${SR_WANT})) -ne $((8#${SR_MODE})) ]; then
        if [ -L "$path" ]; then
            echo "symbolic link, left alone: ${path}"
            return 0
        fi
        run chmod "$SR_WANT" "$path" || return 1
    fi
    CHANGED=1
}

# is_git_dir DIR — a bare repository or a .git directory.
is_git_dir() {
    [ -d "$1" ] && [ ! -L "$1" ] && [ -f "$1/HEAD" ] && [ -d "$1/objects" ] && [ -d "$1/refs" ]
}

# share_git_dir GITDIR — the directory itself, then HEAD, config, packed-refs,
# objects and refs; symbolic links are not followed, nothing else is touched.
share_git_dir() {
    local gd="$1" entry sub
    set_shared "$gd" 5 || return 1
    for entry in HEAD config packed-refs; do
        if [ -f "${gd}/${entry}" ] && [ ! -L "${gd}/${entry}" ]; then
            set_shared "${gd}/${entry}" 4 || return 1
        fi
    done
    for sub in objects refs; do
        if [ -d "${gd}/${sub}" ] && [ ! -L "${gd}/${sub}" ]; then
            while IFS= read -r -d '' entry; do
                if [ -d "$entry" ]; then
                    set_shared "$entry" 5 || return 1
                elif [ -f "$entry" ]; then
                    set_shared "$entry" 4 || return 1
                fi
            done < <(find "${gd}/${sub}" ! -type l -print0)
        fi
    done
}

# share_repo DIR — DIR is a repository (a working tree with .git, or bare); the
# working tree directory gets group traverse only.
share_repo() {
    local repo="$1"
    if [ -d "${repo}/.git" ] && [ ! -L "${repo}/.git" ]; then
        set_shared "$repo" 1 || return 1
        share_git_dir "${repo}/.git" || return 1
    elif is_git_dir "$repo"; then
        share_git_dir "$repo" || return 1
    fi
}

# has_repo DIR — true for a working tree with a .git directory or a bare repository.
has_repo() {
    { [ -d "${1}/.git" ] && [ ! -L "${1}/.git" ]; } || is_git_dir "$1"
}

# agent_can_traverse DIR — from owner, group and mode (ACLs are not read; the
# suggestion printed for a failure is the setfacl one).
agent_can_traverse() {
    local meta owner grp mode groups
    meta="$(stat -c '%U:%G:%a' "$1")" || return 1
    owner="${meta%%:*}"
    grp="${meta#*:}"
    grp="${grp%%:*}"
    mode=$((8#${meta##*:}))
    if [ "$owner" = "$AGENT_USER" ]; then
        [ $(((mode >> 6) & 1)) -eq 1 ]
        return
    fi
    if id -u "$AGENT_USER" >/dev/null 2>&1; then
        groups="$(id -nG "$AGENT_USER")"
    else
        groups="$GROUP" # not created yet (dry run): it will be a member
    fi
    if printf '%s\n' "$groups" | tr ' ' '\n' | grep -qx "$grp"; then
        [ $(((mode >> 3) & 1)) -eq 1 ]
        return
    fi
    [ $((mode & 1)) -eq 1 ]
}

# chain_dirs PATH — every directory the kernel walks to reach PATH: each lexical
# prefix resolved (so each symlink hop) with all the ancestors of the result.
chain_dirs() {
    local path="$1" prefix="" part real parts=()
    IFS=/ read -r -a parts <<<"${path#/}"
    echo /
    for part in ${parts[@]+"${parts[@]}"}; do
        prefix="${prefix}/${part}"
        real="$(realpath "$prefix")" || continue
        while [ "$real" != "/" ] && [ -n "$real" ]; do
            echo "$real"
            real="$(dirname "$real")"
        done
    done
}

# check_chain PATH — one line per directory of the chain the agent user cannot
# traverse, with the command that grants it. Returns 1 when there is one.
CHAIN_SEEN=""
check_chain() {
    local path="$1" dir bad=0
    if [ ! -e "$path" ]; then
        echo "repos path does not exist: ${path}" >&2
        return 1
    fi
    while IFS= read -r dir; do
        case $'\n'"$CHAIN_SEEN" in *$'\n'"$dir"$'\n'*) continue ;; esac
        CHAIN_SEEN="${CHAIN_SEEN}${dir}"$'\n'
        # The chain is checked before this step changes anything; the repos
        # directory itself gets group r-x from it.
        if [ "$dir" = "$REPOS_REAL" ]; then
            continue
        fi
        if ! agent_can_traverse "$dir"; then
            printf 'agent user %s cannot traverse %s: setfacl -m u:%s:x %q  (or: chmod o+x %q)\n' \
                "$AGENT_USER" "$dir" "$AGENT_USER" "$dir" "$dir" >&2
            bad=1
        fi
    done < <(chain_dirs "$path")
    [ "$bad" = 0 ]
}

# check_repos_chains — runs when repos is reached through a symlink or a
# --repos-link is given; the plain path is the operator's own directory.
check_repos_chains() {
    local link failed=0 plain="${DATA_DIR}/repos"
    CHAIN_SEEN=""
    if [ "$REPOS_REAL" != "$plain" ] && [ -n "$REPOS_REAL" ]; then
        check_chain "$plain" || failed=1
    fi
    for link in ${REPOS_LINKS[@]+"${REPOS_LINKS[@]}"}; do
        if [ -z "$REPOS_REAL" ] || [ "$(realpath "$link" 2>/dev/null || true)" != "$REPOS_REAL" ]; then
            echo "--repos-link ${link} does not lead to the repos directory ${REPOS_REAL:-"(absent)"}" >&2
            failed=1
            continue
        fi
        check_chain "$link" || failed=1
    done
    [ "$failed" = 0 ]
}

step_shared_read() {
    local d1 d2 middle_done
    CHANGED=0
    # The chain first: a failed check leaves nothing changed.
    check_repos_chains || return 1
    if [ -z "$REPOS_REAL" ]; then echo "absent, left alone: ${DATA_DIR}/repos"; fi
    if [ -z "$BIN_REAL" ]; then echo "absent, left alone: ${DATA_DIR}/bin"; fi
    if [ -z "$BINARY_REAL" ]; then echo "absent, left alone: ${DATA_DIR}/bin/${BINARY_NAME}"; fi
    if [ -n "$BIN_REAL" ]; then set_shared "$BIN_REAL" 5 || return 1; fi
    if [ -n "$BINARY_REAL" ]; then set_shared "$BINARY_REAL" 5 || return 1; fi
    if [ -n "$REPOS_REAL" ]; then
        set_shared "$REPOS_REAL" 5 || return 1
        for d1 in "${REPOS_REAL}"/*/; do
            d1="${d1%/}"
            [ -d "$d1" ] || continue
            if [ -L "$d1" ]; then
                echo "symbolic link, left alone: ${d1}"
                continue
            fi
            if has_repo "$d1"; then
                share_repo "$d1" || return 1
                continue
            fi
            # Not a repository: a middle directory (org/repo) when a repository is below.
            middle_done=0
            for d2 in "${d1}"/*/; do
                d2="${d2%/}"
                if [ ! -d "$d2" ] || [ -L "$d2" ] || ! has_repo "$d2"; then
                    continue
                fi
                if [ "$middle_done" = 0 ]; then
                    set_shared "$d1" 1 || return 1
                    middle_done=1
                fi
                share_repo "$d2" || return 1
            done
        done
    fi
    if [ -z "$REPOS_REAL" ] && [ -z "$BIN_REAL" ]; then
        NOTE="neither repos nor bin exists under ${DATA_DIR}"
    fi
    [ "$CHANGED" = 1 ] || RESULT=skipped
    return 0
}

sudoers_rule() {
    printf '%s ALL=(%s) NOPASSWD: SETENV: ALL' "$DAEMON_USER" "$AGENT_USER"
}

step_sudoers_rule() {
    local rule
    rule="$(sudoers_rule)"
    if file_has_content "$SUDOERS_FILE" "$rule"; then
        RESULT=skipped
        return 0
    fi
    if [ ! -d "$SUDOERS_DIR" ]; then
        echo "sudoers directory missing: ${SUDOERS_DIR}" >&2
        return 1
    fi
    # A leftover of an interrupted run; through run, so a dry run prints it and
    # leaves it in place.
    if [ -e "$SUDOERS_TMP" ]; then
        run rm -f "$SUDOERS_TMP" || return 1
    fi
    write_file "$SUDOERS_TMP" 0440 "$rule" || return 1
    # Validate the candidate BEFORE it takes the name sudo reads; a failed
    # validation leaves nothing behind.
    if ! run visudo -cf "$SUDOERS_TMP"; then
        rm -f "$SUDOERS_TMP"
        echo "sudoers validation failed; nothing installed" >&2
        return 1
    fi
    run chown root:root "$SUDOERS_TMP" || return 1
    run mv -f "$SUDOERS_TMP" "$SUDOERS_FILE" || return 1
}

# Lines that name the daemon user: not a comment, the user as a whole word (a
# preceding % means a group of that name and does not count).
daemon_line_re() {
    printf '(^|[^[:alnum:]_.%%-])%s([^[:alnum:]_.-]|$)' "$DAEMON_USER"
}

# The cloud-init plan, one pass over the file. For each active line that names
# the daemon user: when the user list is exactly the daemon user and the run-as
# part is ALL, root or absent, the line is commented out (DISABLE); every other
# one (several users, an alias, a host list, a Defaults line, a run-as part
# such as "(deploy)") is left as it is (LEFT). With out set, the new file is
# written there. Report lines are tab separated: DISABLE n text, LEFT n reason text.
# shellcheck disable=SC2016 # the awk program is single-quoted on purpose
CLOUD_AWK='
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
function emit(s) { if (out != "") print s > out }
BEGIN { prefix = "# disabled by setup-agent-user: "; userhost = "^" d "[ \t]+[^ \t,]+$" }
{
    line = $0
    if (cont) {
        emit(commenting ? prefix line : line)
        cont = (line ~ /\\$/)
        if (!cont) commenting = 0
        next
    }
    cont = (line ~ /\\$/)
    t = trim(line)
    if (t == "" || t ~ /^#/ || t !~ re) { emit(line); next }
    reason = ""
    if (t ~ /^Defaults/) {
        reason = "Defaults line"
    } else {
        eq = index(t, "=")
        if (eq == 0) {
            reason = "no = in the line"
        } else {
            left = trim(substr(t, 1, eq - 1))
            right = trim(substr(t, eq + 1))
            if (left !~ userhost) {
                reason = "several users, an alias or a host list"
            } else if (substr(right, 1, 1) == "(") {
                rp = index(right, ")")
                runas = trim(substr(right, 2, rp - 2))
                split(runas, parts, ":")
                ru = trim(parts[1])
                if (rp == 0 || (ru != "ALL" && ru != "root")) reason = "run-as part (" runas ") is not ALL or root"
            }
        }
    }
    if (reason != "") {
        emit(line)
        printf "LEFT\t%d\t%s\t%s\n", NR, reason, line
        cont = 0
    } else {
        emit(prefix line)
        commenting = cont
        printf "DISABLE\t%d\t%s\n", NR, line
    }
}'

# restore_cloud_init — put the newest backup back with the mode and owner the
# file has now; 0440 root:root only when it is gone.
restore_cloud_init() {
    local meta
    meta="$(stat -c '%a:%U:%G' "$CLOUD_INIT_SUDOERS" 2>/dev/null || echo 440:root:root)"
    run install -m "${meta%%:*}" -o "$(cut -d: -f2 <<<"$meta")" -g "$(cut -d: -f3 <<<"$meta")" \
        "$CLOUD_INIT_BACKUP" "$CLOUD_INIT_SUDOERS"
}

# remaining_rules — where a NOPASSWD rule for the daemon user (its name, ALL or
# one of its groups) other than the agent rule sits: file:line: text.
remaining_rules() {
    local groups f
    groups=" $(id -nG "$DAEMON_USER" | tr '\n' ' ') "
    for f in "$SUDOERS_MAIN" "$SUDOERS_DIR"/*; do
        # sudo skips file names with a dot or a tilde in sudoers.d
        case "${f##*/}" in *.* | *~) [ "$f" = "$SUDOERS_MAIN" ] || continue ;; esac
        [ -f "$f" ] || continue
        awk -v d="$DAEMON_USER" -v groups="$groups" -v rule="$(sudoers_rule)" '
            /^[ \t]*#/ || !/NOPASSWD/ { next }
            {
                line = $0; sub(/^[ \t]+/, "", line); sub(/[ \t]+$/, "", line)
                if (line == rule) next
                n = split($1, users, ",")
                for (i = 1; i <= n; i++) {
                    u = users[i]
                    if (u == d || u == "ALL" || (substr(u, 1, 1) == "%" && index(groups, " " substr(u, 2) " ") > 0)) {
                        printf "  %s:%d: %s\n", FILENAME, FNR, line
                        break
                    }
                }
            }' "$f"
    done
}

# prove_sudo_removed — sudo -n -l -U for the daemon user must succeed and list
# no NOPASSWD rule but the agent rule itself, which sudo prints as
# "(AGENT) SETENV: NOPASSWD: ALL"; any other rule with the agent user as its
# run-as part counts as a remaining rule.
prove_sudo_removed() {
    local out rc=0 left where
    if [ "$DRY_RUN" = 1 ]; then
        printf '+ sudo -n -l -U %q  (after the edit: no NOPASSWD rule but the agent rule)\n' "$DAEMON_USER"
        return 0
    fi
    out="$(sudo -n -l -U "$DAEMON_USER" 2>&1)" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "sudo -n -l -U ${DAEMON_USER} failed (exit ${rc}); the sudo removal is not proven:" >&2
        printf '%s\n' "$out" | sed 's/^/  /' >&2
        return 1
    fi
    left="$(printf '%s\n' "$out" | awk -v agent="(${AGENT_USER}) SETENV: NOPASSWD: ALL" '
        /NOPASSWD/ { l = $0; sub(/^[ \t]+/, "", l); sub(/[ \t]+$/, "", l); if (l != agent) print $0 }')"
    if [ -n "$left" ]; then
        echo "${DAEMON_USER} still has a NOPASSWD rule that is not the agent rule:" >&2
        printf '%s\n' "$left" | sed 's/^[[:space:]]*/  sudo -l: /' >&2
        where="$(remaining_rules)"
        if [ -n "$where" ]; then
            echo "found in:" >&2
            printf '%s\n' "$where" >&2
        else
            echo "no such line in ${SUDOERS_MAIN} or ${SUDOERS_DIR}/ (an include or another source)" >&2
        fi
        return 1
    fi
}

# cloud_init_plan — the report of CLOUD_AWK for the cloud-init file, no output file.
cloud_init_plan() {
    awk -v d="$DAEMON_USER" -v re="$(daemon_line_re)" -v out="" "$CLOUD_AWK" "$CLOUD_INIT_SUDOERS"
}

step_cloud_init_sudoers() {
    local plan kind n rest reason text ndis=0 tmp meta bak_n
    if [ "$KEEP_DAEMON_SUDO" = 1 ]; then
        RESULT=skipped
        NOTE="--keep-daemon-sudo"
        return 0
    fi
    if [ ! -f "$CLOUD_INIT_SUDOERS" ]; then
        RESULT=skipped
        NOTE="no file at ${CLOUD_INIT_SUDOERS}"
        return 0
    fi
    plan="$(cloud_init_plan)"
    while IFS=$'\t' read -r kind n rest; do
        case "$kind" in
            DISABLE) ndis=$((ndis + 1)); echo "  to comment out: ${CLOUD_INIT_SUDOERS}:${n}: ${rest}" ;;
            LEFT)
                reason="${rest%%$'\t'*}"
                text="${rest#*$'\t'}"
                echo "  left untouched (${reason}): ${CLOUD_INIT_SUDOERS}:${n}: ${text}"
                ;;
        esac
    done <<<"$plan"
    if [ "$ndis" -eq 0 ]; then
        prove_sudo_removed || return 1
        RESULT=skipped
        return 0
    fi
    # Checked before the first change too; this guards the step on its own.
    if [ "$DRY_RUN" = 0 ] && [ "$CONFIRM_ROOT_PATH" = 0 ]; then
        echo "refusing to edit ${CLOUD_INIT_SUDOERS} without --confirm-root-path" >&2
        return 1
    fi
    # Never drop the old rule unless the new one is in place.
    if [ "$DRY_RUN" = 0 ] && ! file_has_content "$SUDOERS_FILE" "$(sudoers_rule)"; then
        echo "refusing to edit ${CLOUD_INIT_SUDOERS}: the agent rule is not installed" >&2
        return 1
    fi
    # The file still has an active daemon line, so it is the one to back up: an
    # older backup is kept under a numbered suffix, the new one takes the name.
    if [ -e "$CLOUD_INIT_BACKUP" ]; then
        bak_n=1
        while [ -e "${CLOUD_INIT_BACKUP}.${bak_n}" ]; do bak_n=$((bak_n + 1)); done
        run mv "$CLOUD_INIT_BACKUP" "${CLOUD_INIT_BACKUP}.${bak_n}" || return 1
    fi
    run install -m 0600 -o root -g root "$CLOUD_INIT_SUDOERS" "$CLOUD_INIT_BACKUP" || return 1
    tmp="${CLOUD_INIT_SUDOERS%/*}/.${CLOUD_INIT_SUDOERS##*/}.pilot-agent.new"
    meta="$(stat -c '%a:%U:%G' "$CLOUD_INIT_SUDOERS")"
    printf '+ write %s (the cloud-init file with the lines above commented out)\n' "$tmp"
    if [ "$DRY_RUN" = 0 ]; then
        (umask 077 && awk -v d="$DAEMON_USER" -v re="$(daemon_line_re)" -v out="$tmp" "$CLOUD_AWK" \
            "$CLOUD_INIT_SUDOERS" >/dev/null) || { rm -f "$tmp"; return 1; }
    fi
    run chown "$(cut -d: -f2 <<<"$meta"):$(cut -d: -f3 <<<"$meta")" "$tmp" || return 1
    run chmod "${meta%%:*}" "$tmp" || return 1
    run mv -f "$tmp" "$CLOUD_INIT_SUDOERS" || return 1
    if ! run visudo -c; then
        echo "sudoers validation failed; restoring ${CLOUD_INIT_SUDOERS}" >&2
        restore_cloud_init || true
        return 1
    fi
    prove_sudo_removed
}

# firewall_rules FAMILY — one rule per line, as arguments for the chain. The
# gateway port is rejected on the loopback address and, on the loopback
# interface, for every address of the host: the gateway can listen on 0.0.0.0 or
# ::, and a packet to an own address leaves through lo.
firewall_rules() {
    local loopback=127.0.0.1 meta="$METADATA_V4"
    if [ "$1" = v6 ]; then
        loopback=::1
        meta="$METADATA_V6"
    fi
    echo "-d ${meta} -j REJECT"
    echo "-p tcp -d ${loopback} --dport ${GATEWAY_PORT} -j REJECT --reject-with tcp-reset"
    echo "-p tcp --dport ${GATEWAY_PORT} -m addrtype --dst-type LOCAL -o lo -j REJECT --reject-with tcp-reset"
}

# jump_rule CHAIN — the rule in OUTPUT that sends the agent user's traffic to CHAIN.
jump_rule() {
    echo "-m owner --uid-owner ${AGENT_USER} -j ${1}"
}

# chain_complete BIN FAMILY — the chain holds exactly the rules of firewall_rules.
chain_complete() {
    local bin="$1" rule want=0 have
    while IFS= read -r rule; do
        want=$((want + 1))
        # shellcheck disable=SC2086 # the rule is a list of words on purpose
        "$bin" -w -C "$CHAIN" $rule >/dev/null 2>&1 || return 1
    done < <(firewall_rules "$2")
    have="$("$bin" -w -S "$CHAIN" 2>/dev/null | grep -c '^-A ' || true)"
    [ "$have" -eq "$want" ]
}

# add_rules BIN CHAIN FAMILY
add_rules() {
    local bin="$1" chain="$2" rule
    while IFS= read -r rule; do
        # shellcheck disable=SC2086
        run "$bin" -w -A "$chain" $rule || return 1
    done < <(firewall_rules "$3")
}

# drop_jumps BIN CHAIN — delete every jump to CHAIN from OUTPUT.
drop_jumps() {
    local bin="$1" chain="$2"
    # shellcheck disable=SC2046,SC2086
    while "$bin" -w -C OUTPUT $(jump_rule "$chain") >/dev/null 2>&1; do
        # shellcheck disable=SC2046,SC2086
        run "$bin" -w -D OUTPUT $(jump_rule "$chain") || return 1
        # A dry run deletes nothing, so the check above would never end.
        [ "$DRY_RUN" = 0 ] || break
    done
}

# ensure_jump_first BIN — the jump to CHAIN is the first rule of OUTPUT and the
# only one; a late or a stale one is inserted at the top first, the others go after.
ensure_jump_first() {
    local bin="$1" positions p reverse="" first
    positions="$("$bin" -w -S OUTPUT 2>/dev/null | awk -v c="$CHAIN" '/^-A OUTPUT /{n++; if ($NF == c && $(NF-1) == "-j") print n}')"
    first="$(printf '%s\n' "$positions" | head -1)"
    # shellcheck disable=SC2046,SC2086
    if [ "$first" = 1 ] && [ "$(printf '%s\n' "$positions" | grep -c .)" = 1 ] \
        && "$bin" -w -C OUTPUT $(jump_rule "$CHAIN") >/dev/null 2>&1; then
        return 0
    fi
    # shellcheck disable=SC2046,SC2086
    run "$bin" -w -I OUTPUT 1 $(jump_rule "$CHAIN") || return 1
    for p in $positions; do reverse="${p} ${reverse}"; done
    for p in $reverse; do
        run "$bin" -w -D OUTPUT $((p + 1)) || return 1
    done
    CHANGED=1
}

# rebuild_chain BIN FAMILY — a new chain under a temporary name, the jump to it
# inserted first, then the old jumps and the old chains removed and the new
# chain renamed. The reject rules are in force at every moment. A temporary
# chain left over from an interrupted run may hold the only jump in force: the
# new chain takes a temporary name that no jump uses, and the leftover goes only
# after the jump to the new chain is in place.
rebuild_chain() {
    local bin="$1" tmp="" c
    for c in "$CHAIN_NEW" "$CHAIN_ALT"; do
        # shellcheck disable=SC2046,SC2086
        if ! "$bin" -w -C OUTPUT $(jump_rule "$c") >/dev/null 2>&1; then
            tmp="$c"
            break
        fi
    done
    if [ -z "$tmp" ]; then
        echo "both ${CHAIN_NEW} and ${CHAIN_ALT} have a jump in OUTPUT; remove one by hand" >&2
        return 1
    fi
    if "$bin" -w -S "$tmp" >/dev/null 2>&1; then
        # a leftover with no jump to it: nothing is in force through it
        run "$bin" -w -F "$tmp" || return 1
        run "$bin" -w -X "$tmp" || return 1
    fi
    run "$bin" -w -N "$tmp" || return 1
    add_rules "$bin" "$tmp" "$2" || return 1
    # shellcheck disable=SC2046,SC2086
    run "$bin" -w -I OUTPUT 1 $(jump_rule "$tmp") || return 1
    for c in "$CHAIN" "$CHAIN_NEW" "$CHAIN_ALT"; do
        [ "$c" != "$tmp" ] || continue
        "$bin" -w -S "$c" >/dev/null 2>&1 || continue
        drop_jumps "$bin" "$c" || return 1
        run "$bin" -w -F "$c" || return 1
        run "$bin" -w -X "$c" || return 1
    done
    run "$bin" -w -E "$tmp" "$CHAIN" || return 1
}

# firewall_family BIN FAMILY — sets CHANGED=1 when it acts.
firewall_family() {
    local bin="$1"
    CHANGED=0
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "${bin} not found" >&2
        return 1
    fi
    if ! "$bin" -w -S "$CHAIN" >/dev/null 2>&1; then
        # The chain is complete before the jump to it takes effect.
        run "$bin" -w -N "$CHAIN" || return 1
        add_rules "$bin" "$CHAIN" "$2" || return 1
        CHANGED=1
    elif ! chain_complete "$bin" "$2"; then
        rebuild_chain "$bin" "$2" || return 1
        CHANGED=1
    fi
    ensure_jump_first "$bin" || return 1
    [ "$CHANGED" = 1 ] || RESULT=skipped
    return 0
}

step_firewall_ipv4() { firewall_family "$IPT4" v4; }

step_firewall_ipv6() {
    if ! command -v "$IPT6" >/dev/null 2>&1; then
        RESULT=skipped
        NOTE="${IPT6} not found: the IPv6 part is skipped, the agent user is not blocked on IPv6"
        return 0
    fi
    firewall_family "$IPT6" v6
}

unit_content() {
    local bin family rule
    cat <<EOF
[Unit]
Description=Firewall rules for the agent user (${AGENT_USER}): metadata address and local gateway port
Wants=network-pre.target
Before=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
EOF
    for family in "v4:${IPT4}" "v6:${IPT6}"; do
        bin="$(command -v "${family#*:}" 2>/dev/null || true)"
        [ -n "$bin" ] || continue
        # Same order as the script: the new chain is switched in before the old one goes.
        echo "ExecStart=-${bin} -w -D OUTPUT $(jump_rule "$CHAIN_NEW")"
        echo "ExecStart=-${bin} -w -F ${CHAIN_NEW}"
        echo "ExecStart=-${bin} -w -X ${CHAIN_NEW}"
        echo "ExecStart=${bin} -w -N ${CHAIN_NEW}"
        while IFS= read -r rule; do
            echo "ExecStart=${bin} -w -A ${CHAIN_NEW} ${rule}"
        done < <(firewall_rules "${family%%:*}")
        echo "ExecStart=${bin} -w -I OUTPUT 1 $(jump_rule "$CHAIN_NEW")"
        echo "ExecStart=-${bin} -w -D OUTPUT $(jump_rule "$CHAIN")"
        echo "ExecStart=-${bin} -w -F ${CHAIN}"
        echo "ExecStart=-${bin} -w -X ${CHAIN}"
        echo "ExecStart=${bin} -w -E ${CHAIN_NEW} ${CHAIN}"
    done
    cat <<EOF

[Install]
WantedBy=multi-user.target
EOF
}

step_firewall_unit() {
    local content
    content="$(unit_content)"
    if file_has_content "$UNIT_FILE" "$content" && systemctl is-enabled --quiet "$UNIT_NAME" >/dev/null 2>&1; then
        RESULT=skipped
        return 0
    fi
    if [ ! -d "$SYSTEMD_DIR" ]; then
        echo "systemd directory missing: ${SYSTEMD_DIR}" >&2
        return 1
    fi
    write_file "$UNIT_FILE" 0644 "$content" || return 1
    run systemctl daemon-reload || return 1
    run systemctl enable "$UNIT_NAME" || return 1
    if ! command -v "$IPT6" >/dev/null 2>&1; then
        NOTE="IPv4 only: ${IPT6} not found"
    fi
}

# ---------------------------------------------------------------- undo steps

undo_cloud_init_sudoers() {
    if [ ! -f "$CLOUD_INIT_BACKUP" ]; then
        RESULT=skipped
        NOTE="no backup at ${CLOUD_INIT_BACKUP}"
        return 0
    fi
    echo "restoring ${CLOUD_INIT_SUDOERS} from the newest backup ${CLOUD_INIT_BACKUP}"
    restore_cloud_init || return 1
    # The backup stays until the restored file is validated; older numbered
    # backups stay in any case.
    if ! run visudo -c; then
        echo "sudoers validation failed after the restore of ${CLOUD_INIT_SUDOERS}; backup kept" >&2
        return 1
    fi
    run rm -f "$CLOUD_INIT_BACKUP" || return 1
}

undo_firewall_family() {
    local bin="$1" changed=0 chain
    if ! command -v "$bin" >/dev/null 2>&1; then
        RESULT=skipped
        NOTE="${bin} not found"
        return 0
    fi
    for chain in "$CHAIN" "$CHAIN_NEW" "$CHAIN_ALT"; do
        # shellcheck disable=SC2046,SC2086
        if "$bin" -w -C OUTPUT $(jump_rule "$chain") >/dev/null 2>&1; then
            drop_jumps "$bin" "$chain" || return 1
            changed=1
        fi
        if "$bin" -w -S "$chain" >/dev/null 2>&1; then
            run "$bin" -w -F "$chain" || return 1
            run "$bin" -w -X "$chain" || return 1
            changed=1
        fi
    done
    [ "$changed" = 1 ] || RESULT=skipped
    return 0
}

undo_firewall_ipv4() { undo_firewall_family "$IPT4"; }
undo_firewall_ipv6() { undo_firewall_family "$IPT6"; }

undo_firewall_unit() {
    if [ ! -e "$UNIT_FILE" ] && ! systemctl is-enabled --quiet "$UNIT_NAME" >/dev/null 2>&1; then
        RESULT=skipped
        return 0
    fi
    run systemctl stop "$UNIT_NAME" || true
    run systemctl disable "$UNIT_NAME" || true
    run rm -f "$UNIT_FILE" || return 1
    run systemctl daemon-reload || return 1
}

undo_sudoers_rule() {
    if [ ! -e "$SUDOERS_FILE" ] && [ ! -e "$SUDOERS_TMP" ]; then
        RESULT=skipped
        return 0
    fi
    run rm -f "$SUDOERS_FILE" "$SUDOERS_TMP" || return 1
}

# undo_shared_read — the group and mode of every recorded path, last record
# first (a path recorded twice ends with its first, original values). A record
# is refused, as in apply, when its path resolves outside the data dir or runs
# through a symbolic link (apply records resolved paths only); the step then
# fails and the state file is kept.
undo_shared_read() {
    local records=() grp mode path i failed=0
    if [ ! -e "$STATE_FILE" ] && [ ! -L "$STATE_FILE" ]; then
        RESULT=skipped
        NOTE="no state file at ${STATE_FILE}"
        return 0
    fi
    if ! state_file_ok; then
        echo "state file ${STATE_FILE} is not a root:root 0600 regular file; refusing to use it" >&2
        return 1
    fi
    while IFS= read -r line; do
        records+=("$line")
    done <"$STATE_FILE"
    for ((i = ${#records[@]} - 1; i >= 0; i--)); do
        IFS=$'\t' read -r grp mode path <<<"${records[$i]}"
        if [ -L "$path" ]; then
            echo "symbolic link now, not restored: ${path}"
            continue
        fi
        if [ ! -e "$path" ]; then
            echo "gone, not restored: ${path}"
            continue
        fi
        if ! contained "$path" || [ "$RESOLVED" != "$path" ]; then
            echo "refused, not restored: ${path} resolves to '${RESOLVED:-?}' (outside the data dir '${DATA_DIR}' or through a symbolic link)" >&2
            failed=1
            continue
        fi
        if [ "$DRY_RUN" = 1 ]; then
            run chgrp -h "$grp" "$path"
            run chmod "$mode" "$path"
            echo "would restore: ${path} group=${grp} mode=${mode}"
        elif run chgrp -h "$grp" "$path" && { [ -L "$path" ] || run chmod "$mode" "$path"; }; then
            echo "restored: ${path} group=${grp} mode=${mode}"
        else
            echo "could not restore: ${path}" >&2
            failed=1
        fi
    done
    if [ "$failed" = 1 ]; then
        echo "some paths were not restored; the state file is kept" >&2
        return 1
    fi
    run rm -f "$STATE_FILE" || return 1
}

# ------------------------------------------------------------------- driver

print_summary() {
    local line
    echo
    echo "== summary"
    for line in "${SUMMARY[@]}"; do
        echo "step ${line}"
    done
}

run_step() {
    local name="$1" fn="$2" state
    RESULT="done"
    NOTE=""
    echo "== ${name}"
    if "$fn"; then
        state="$RESULT"
        if [ "$DRY_RUN" = 1 ] && [ "$state" = "done" ]; then
            state="done (dry-run)"
        fi
    else
        state=failed
    fi
    if [ -n "$NOTE" ]; then
        state="${state} - ${NOTE}"
    fi
    SUMMARY+=("${name}: ${state}")
    [ "${state%% *}" != failed ]
}

# run_plan NAME:FUNCTION ... — runs the steps in order. After a failed step the
# rest are not run; each still gets a summary line, so the summary has one per
# step.
run_plan() {
    local entry failed=""
    for entry in "$@"; do
        if [ -n "$failed" ]; then
            SUMMARY+=("${entry%%:*}: skipped - not run, ${failed} failed")
        elif ! run_step "${entry%%:*}" "${entry#*:}"; then
            failed="${entry%%:*}"
        fi
    done
    if [ -n "$failed" ]; then
        print_summary
        exit 1
    fi
}

print_manual_cleanup() {
    echo
    echo "== left in place (not run)"
    echo "The user, the group, the group membership and the directories stay. To remove them:"
    printf '  gpasswd -d %q %q\n' "$DAEMON_USER" "$GROUP"
    printf '  userdel %q\n' "$AGENT_USER"
    printf '  groupdel %q\n' "$GROUP"
    printf '  rm -rf %q %q\n' "$AGENT_HOME" "$WORKSPACES"
}

if [ "$DRY_RUN" = 1 ]; then
    echo "setup-agent-user: dry run, nothing is changed"
fi

if [ "$UNDO" = 1 ]; then
    run_plan cloud-init-sudoers:undo_cloud_init_sudoers \
        firewall-unit:undo_firewall_unit \
        firewall-ipv4:undo_firewall_ipv4 \
        firewall-ipv6:undo_firewall_ipv6 \
        sudoers-rule:undo_sudoers_rule \
        shared-read:undo_shared_read
    print_summary
    print_manual_cleanup
    exit 0
fi

# The removal of the daemon user's general sudo needs --confirm-root-path. It is
# asked here, before the first change, and only when the cloud-init file still
# has a line to comment out: a host that is set up already needs no flag.
if [ "$DRY_RUN" = 0 ] && [ "$KEEP_DAEMON_SUDO" = 0 ] && [ "$CONFIRM_ROOT_PATH" = 0 ] \
    && [ -f "$CLOUD_INIT_SUDOERS" ] && cloud_init_plan | grep -q '^DISABLE'; then
    die "refusing to remove the general sudo of ${DAEMON_USER}: keep another root session open (a second root shell or the console) and run again with --confirm-root-path, or pass --keep-daemon-sudo"
fi

# The removal of the daemon user's general sudo is the last step, after the
# firewall and the boot unit.
run_plan group:step_group \
    agent-user:step_agent_user \
    daemon-membership:step_daemon_membership \
    directories:step_directories \
    shared-read:step_shared_read \
    sudoers-rule:step_sudoers_rule \
    firewall-ipv4:step_firewall_ipv4 \
    firewall-ipv6:step_firewall_ipv6 \
    firewall-unit:step_firewall_unit \
    cloud-init-sudoers:step_cloud_init_sudoers
print_summary
