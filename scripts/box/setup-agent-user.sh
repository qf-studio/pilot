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
#   daemon-private    a check, changes no mode: the data dir gives group and others
#                     traverse at most (0711 or stricter), and no entry of it but
#                     the agent root (a link is followed) is open to the agent
#                     user, through the "other" class or through the shared group.
#                     The agent user gets no access to a repository, the config or
#                     the binary of the daemon (its workspace gets the base commit
#                     as a bundle from the daemon). On a failure the step prints
#                     each process of another user (not root, not the daemon user)
#                     with a file open below the data dir, then one chmod for each
#                     path, records the modes it found in DAEMON_MODES_RECORD and
#                     fails; the apply passes once the operator ran the chmods.
#                     Each chmod line runs chmod as the daemon user (runuser), so
#                     that a link the daemon user puts in place of an entry after
#                     the check leads root to change nothing of another user.
#                     Every path it prints is shell-quoted. An entry whose name or
#                     resolved path is not a plain path in the data dir ([A-Za-z0-9
#                     ._/-], a link out of the data dir included), or that resolves
#                     to the data dir or to the agent root or a path in it, or that
#                     is open and does not belong to the daemon user, or that is a
#                     link that does not resolve (dangling, a loop: the daemon start
#                     refuses it), gets no chmod and no record: the step fails with
#                     one line that names it.
#   group             system group (default: pilot)
#   agent-user        system user, no login shell, home AGENT_ROOT/home
#   daemon-membership daemon user joins the group
#   firewall-<family> dedicated chain, owner match on the agent user: reject the
#                     metadata address and TCP to the gateway port on loopback; the
#                     chain is rebuilt under a temporary name and switched in, so
#                     the reject rules are never missing; a temporary chain that a
#                     failed run left is removed. A missing ip6tables fails the
#                     IPv6 step, unless IPv6 is off on the host (disable_ipv6 is 1
#                     for every entry of IPV6_CONF_DIR, "all" and "default" too);
#                     then the step is skipped.
#   firewall-unit     oneshot systemd unit that applies the chain at boot
#   directories       the agent root (root:root 0755) and, below it, home (agent
#                     user, 0750) and workspaces (agent user, 2770), made by root;
#                     go, go/cache and go/mod below the home, made by the agent user
#                     (no root step inside a directory of the agent user). A link,
#                     a file or a FIFO at one of these fails the step; the firewall
#                     steps ran before it, so the agent user cannot stop them.
#   sudoers-rule      daemon user may run any command as the agent user
#                     (NOPASSWD, SETENV); validated BEFORE it is moved into place.
#                     After the firewall: a failed firewall step leaves no rule,
#                     also on a second run: an apply that fails at a step before
#                     this one removes the rule file of an earlier apply
#                     (remove_sudo_rule_on_failure), says so and lists it in the
#                     summary; the exit status stays 1.
#   cloud-init-sudoers  LAST: comment out the lines that give the daemon user (and
#                     nobody else) sudo to root in the cloud-init sudoers file
#                     (numbered backups, validated), then prove by behaviour that
#                     the daemon user has no root left: `sudo -n true` as the
#                     daemon user must fail (and `sudo -n -u AGENT true` succeed);
#                     and by the rules: in `sudo -ll -U DAEMON` every entry has the
#                     agent user as the only run-as user and no run-as group but
#                     the shared group (a rule for one command as root fails, so
#                     does a line the check does not know). Needs
#                     --confirm-root-path.
#   agent-proof       LAST, a check: as the agent user, create and remove a file in
#                     its home and in the workspace root, and fail to list the data
#                     dir, each directory in it but the agent root and one directory
#                     below those (a repository). A closed directory above the data
#                     dir (a home at 0700) passes every root step and fails here.
#
# Before the first step, when a sudoers file would change (not with
# --keep-daemon-sudo): the check of the sudo rules (precheck_sudo_rules) reads
# `sudo -ll -U DAEMON` as the proof does and passes over only the cloud-init
# entries the apply comments out; any other entry the proof would refuse fails
# the apply there, with nothing changed. The output names the entry; the
# operator removes the rule or the group membership first. A dry run reads the
# list too: on a refusal (or a list it cannot read) it says so, runs the rest
# of the plan and ends with exit 1 after the summary.
#
# A daemon user in a system group with privileges (wheel, adm, docker, ...) gets
# a WARNING before the steps and in the summary: a path to root; not a failure
# by itself, but a sudo rule for such a group fails the check of the sudo rules.
#
# On success the summary ends with the values for the executor section of the
# daemon config: run_as_user, run_as_group, run_as_workspace_dir and
# run_as_data_dir.
#
# Usage: setup-agent-user.sh [--daemon-user U] [--agent-user U] [--group G]
#                            [--data-dir D] [--agent-root A] [--dry-run] [--undo]
#                            [--keep-daemon-sudo] [--confirm-root-path]
#
#   --agent-root         default DATA_DIR/agent; a directory of root (made when
#                        missing) whose parent is the data dir or a directory of
#                        root; it must not hold the data dir
#   --confirm-root-path  the operator holds another root session; required when
#                        the cloud-init file still has a line to comment out (not
#                        for --dry-run, --undo, --keep-daemon-sudo or a host that is
#                        set up already); asked before the first change
#   --dry-run            print every command, change nothing
#   --undo               stop the daemon first: a daemon that runs can start an
#                        agent process until the sudo rule is removed (the undo
#                        says so, it stops no daemon). Remove the sudoers rule
#                        first, stop every process of the agent user (TERM, then
#                        KILL after 5 seconds; a pgrep that is missing, fails or
#                        exits 0 with no process id fails the step), stop and
#                        disable the boot unit, remove the firewall chain, and
#                        last restore the cloud-init file from the newest backup in
#                        which the root line of the daemon user is active: with
#                        that line the daemon user can run a command as the agent
#                        user again, so it comes back only after the firewall
#                        steps (also when one of them failed or was not run). A
#                        daemon user that kept its root line (--keep-daemon-sudo)
#                        has that path during the whole undo. Every step runs, also
#                        after a failed one, but the firewall steps do not run
#                        while the sudo rule file is still in place (its removal
#                        failed; a dangling link counts): they are listed as not
#                        run, and one line says to remove the file by hand and run
#                        --undo again. So do they while a process of the agent user
#                        may still run (the agent-processes step failed): one line
#                        says to stop the processes and run --undo again (when both
#                        failed, the line of the sudo rule). The failed ones are
#                        named at the end and the exit status is 1. When the rule
#                        file is gone and the cloud-init file still has the root
#                        line commented out (no backup, a failed restore), the
#                        summary says that the daemon user has no sudo rule now.
#                        The user, the group and the
#                        directories stay (the commands to delete them are
#                        printed, not run), and so
#                        do the modes of the daemon's entries (the chmods that put
#                        back the recorded modes are printed, as the daemon user,
#                        not run; a record line with a special bit or a path
#                        that is not a plain path in --data-dir is skipped, by
#                        its number). Undo
#                        reads no path that the agent user or the daemon user can
#                        write: each undo step checks the root files it changes.
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
#   IPV6_CONF_DIR        default /proc/sys/net/ipv6/conf (the disable_ipv6 settings)
#   DAEMON_MODES_RECORD  default /root/.setup-agent-user.modes (root's file outside
#                        the data dir: the modes the daemon-private step found)
#
# Exit status: 0 when every step is done or skipped, 1 when the check of the sudo
# rules or a step failed (an apply stops at the first failed step, the steps
# after it are listed in the summary as skipped; an undo runs every step, the
# firewall steps not while the sudo rule file is in place or a process of the
# agent user may still run; a dry run that the check of the sudo rules refuses
# ends so after the summary), 2 on bad arguments (nothing is changed).

set -euo pipefail

# The character classes of the path checks (plain_data_path) and of shq mean
# ASCII only in the C locale; set before any path is checked.
export LC_ALL=C

DAEMON_USER="ec2-user"
AGENT_USER="pilot-agent"
GROUP="pilot"
DATA_DIR="/var/lib/pilot"
AGENT_ROOT=""
DRY_RUN=0
UNDO=0
KEEP_DAEMON_SUDO=0
CONFIRM_ROOT_PATH=0

SUDOERS_DIR="${SUDOERS_DIR:-/etc/sudoers.d}"
SUDOERS_MAIN="${SUDOERS_MAIN:-/etc/sudoers}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
CLOUD_INIT_SUDOERS="${CLOUD_INIT_SUDOERS:-${SUDOERS_DIR}/90-cloud-init-users}"
NOLOGIN_SHELL="${NOLOGIN_SHELL:-/usr/sbin/nologin}"
IPT4="${IPTABLES_CMD:-iptables}"
IPT6="${IP6TABLES_CMD:-ip6tables}"
IPV6_CONF_DIR="${IPV6_CONF_DIR:-/proc/sys/net/ipv6/conf}"
MODES_RECORD="${DAEMON_MODES_RECORD:-/root/.setup-agent-user.modes}"

SUDOERS_NAME="90-pilot-agent-runas"
UNIT_NAME="pilot-agent-firewall.service"
CHAIN="PILOT_AGENT_EGRESS"
CHAIN_NEW="${CHAIN}_NEW"
CHAIN_ALT="${CHAIN}_ALT"
METADATA_V4="169.254.169.254"
METADATA_V6="fd00:ec2::254"
GATEWAY_PORT="9091"
# Seconds between the TERM and the KILL of undo for the processes of the agent user.
KILL_WAIT=5

# Directories the data dir must never be, nor contain (Linux and the macOS
# aliases of the test host).
SYSTEM_DIRS="/ /bin /boot /dev /etc /home /lib /lib32 /lib64 /libx32 /media /mnt /opt /proc /root /run /sbin /srv /sys /tmp /usr /usr/bin /usr/lib /usr/lib64 /usr/libexec /usr/local /usr/local/bin /usr/sbin /usr/share /var /var/cache /var/lib /var/log /var/mail /var/run /var/spool /var/tmp /private /private/etc /private/tmp /private/var /System /Library /Applications /Users"

# System groups with privileges: a member is root, or has a short way to it.
PRIVILEGED_GROUPS="root wheel sudo adm disk docker shadow"

SUMMARY=()
RESULT=""
NOTE=""
DAEMON_GROUP_WARNING=""
SUDO_RULE_REMOVAL=""
NO_SUDO_LEFT=""

die() {
    echo "setup-agent-user: $*" >&2
    exit 2
}

usage() {
    sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --daemon-user | --agent-user | --group | --data-dir | --agent-root)
            [ $# -ge 2 ] || die "$1 needs a value"
            case "$1" in
                --daemon-user) DAEMON_USER="$2" ;;
                --agent-user) AGENT_USER="$2" ;;
                --group) GROUP="$2" ;;
                --data-dir) DATA_DIR="$2" ;;
                --agent-root) AGENT_ROOT="$2" ;;
            esac
            shift 2
            ;;
        --daemon-user=*) DAEMON_USER="${1#*=}"; shift ;;
        --agent-user=*) AGENT_USER="${1#*=}"; shift ;;
        --group=*) GROUP="${1#*=}"; shift ;;
        --data-dir=*) DATA_DIR="${1#*=}"; shift ;;
        --agent-root=*) AGENT_ROOT="${1#*=}"; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --undo) UNDO=1; shift ;;
        --keep-daemon-sudo) KEEP_DAEMON_SUDO=1; shift ;;
        --confirm-root-path) CONFIRM_ROOT_PATH=1; shift ;;
        -h | --help) usage; exit 0 ;;
        *) usage; die "unknown argument: $1" ;;
    esac
done

# ------------------------------------------------------- argument validation
# Everything below runs before the first change; a refusal exits 2. This part
# checks the arguments and root's own files only (passwd, group), so that it
# cannot stop an undo; the checks on paths the agent user or the daemon user can
# write run after the undo branch, for apply only.

if [ "$(id -u)" -ne 0 ]; then
    die "must run as root"
fi

NAME_RE='^[a-z][a-z0-9_-]*$'
for pair in "daemon-user:$DAEMON_USER" "agent-user:$AGENT_USER" "group:$GROUP"; do
    pair_value="${pair#*:}"
    [[ "$pair_value" =~ $NAME_RE ]] || die "--${pair%%:*} must match ${NAME_RE}: '${pair_value}'"
    [ "${#pair_value}" -le 32 ] || die "--${pair%%:*} is longer than 32 characters"
done

is_privileged_group() {
    case " ${PRIVILEGED_GROUPS} " in *" $1 "*) return 0 ;; esac
    return 1
}

# check_path_arg FLAG VALUE — absolute, no leading dash, no ".." part.
check_path_arg() {
    case "$2" in -*) die "$1 must not start with a dash: '$2'" ;; esac
    [[ "/$2/" != */../* ]] || die "$1 must not have a '..' part: '$2'"
    [[ "$2" =~ ^/[A-Za-z0-9._/-]*$ ]] || die "$1 must be an absolute path of [A-Za-z0-9._/-]: '$2'"
}

id -u "$DAEMON_USER" >/dev/null 2>&1 || die "daemon user '${DAEMON_USER}' does not exist"
DAEMON_UID="$(id -u "$DAEMON_USER")"
if [ "$DAEMON_USER" = root ] || [ "$DAEMON_UID" -lt 1000 ]; then
    die "the daemon user must not be root or have a uid below 1000 (${DAEMON_USER} has ${DAEMON_UID})"
fi
[ "$DAEMON_USER" != "$AGENT_USER" ] || die "the agent user must differ from the daemon user"
[ "$AGENT_USER" != root ] || die "the agent user must not be root"
! is_privileged_group "$AGENT_USER" || die "--agent-user must not be ${AGENT_USER}: that is the name of a system group with privileges (${PRIVILEGED_GROUPS})"
! is_privileged_group "$GROUP" || die "--group must not be ${GROUP}: a system group with privileges (${PRIVILEGED_GROUPS})"
if getent group "$GROUP" >/dev/null 2>&1; then
    [ "$(getent group "$GROUP" | cut -d: -f3)" != 0 ] || die "--group must not be ${GROUP}: it has gid 0, the gid of root"
fi

DAEMON_PRIMARY_GROUP="$(id -gn "$DAEMON_USER")"
[ "$GROUP" != "$DAEMON_PRIMARY_GROUP" ] || die "--group must not be the primary group of the daemon user (${DAEMON_PRIMARY_GROUP})"

# refuse_system_dir PATH [FLAG] — PATH must not be a system directory nor an
# ancestor of one. Checked on the value as given and, for apply, on its
# canonical form.
refuse_system_dir() {
    local p="$1" flag="${2:---data-dir}" s
    while [ "$p" != / ] && [ "${p%/}" != "$p" ]; do p="${p%/}"; done
    for s in $SYSTEM_DIRS; do
        [ "$p" != "$s" ] || die "${flag} '${p}' is a system directory"
        case "${s}/" in "${p}"/*) die "${flag} '${p}' contains the system directory ${s}" ;; esac
    done
}

check_path_arg --data-dir "$DATA_DIR"
refuse_system_dir "$DATA_DIR"
check_path_arg DAEMON_MODES_RECORD "$MODES_RECORD"

# set_agent_paths — the agent root (default DATA_DIR/agent) and the two
# directories below it.
AGENT_ROOT_ARG="$AGENT_ROOT"
set_agent_paths() {
    AGENT_ROOT="${AGENT_ROOT_ARG:-${DATA_DIR}/agent}"
    while [ "$AGENT_ROOT" != / ] && [ "${AGENT_ROOT%/}" != "$AGENT_ROOT" ]; do AGENT_ROOT="${AGENT_ROOT%/}"; done
    AGENT_HOME="${AGENT_ROOT}/home"
    WORKSPACES="${AGENT_ROOT}/workspaces"
}
if [ -n "$AGENT_ROOT_ARG" ]; then
    check_path_arg --agent-root "$AGENT_ROOT_ARG"
    refuse_system_dir "$AGENT_ROOT_ARG" --agent-root
fi
set_agent_paths
SUDOERS_FILE="${SUDOERS_DIR}/${SUDOERS_NAME}"
SUDOERS_TMP="${SUDOERS_DIR}/.${SUDOERS_NAME}.new"
CLOUD_INIT_BACKUP="${CLOUD_INIT_SUDOERS}.pilot-agent.bak"
UNIT_FILE="${SYSTEMD_DIR}/${UNIT_NAME}"

# check_agent_user — an agent user that exists already: not uid 0, a no-login
# shell, no member of a system group with privileges (its primary group counts).
check_agent_user() {
    local agent_shell g
    id -u "$AGENT_USER" >/dev/null 2>&1 || return 0
    [ "$(id -u "$AGENT_USER")" -ne 0 ] || die "the agent user '${AGENT_USER}' exists with uid 0"
    agent_shell="$(getent passwd "$AGENT_USER" | cut -d: -f7 || true)"
    case "$agent_shell" in
        /sbin/nologin | /usr/sbin/nologin | /usr/bin/nologin | /bin/false | /usr/bin/false | "$NOLOGIN_SHELL") ;;
        *) die "the agent user '${AGENT_USER}' exists with a login shell ('${agent_shell}'); it needs a no-login shell" ;;
    esac
    for g in $(id -nG "$AGENT_USER"); do
        ! is_privileged_group "$g" || die "the agent user '${AGENT_USER}' is a member of ${g}, a system group with privileges"
    done
}

# check_data_dir — the data dir exists, is a directory of the daemon user and
# is no system directory in its canonical form; DATA_DIR becomes that form.
check_data_dir() {
    [ -e "$DATA_DIR" ] || die "--data-dir does not exist: '${DATA_DIR}'"
    DATA_DIR="$(realpath "$DATA_DIR")" || die "cannot resolve --data-dir"
    plain_data_path "$DATA_DIR" || die "--data-dir resolves to $(shq "$DATA_DIR"), not an absolute path of [A-Za-z0-9._/-]"
    refuse_system_dir "$DATA_DIR"
    [ -d "$DATA_DIR" ] || die "--data-dir is not a directory: '${DATA_DIR}'"
    [ "$(stat -c %U "$DATA_DIR")" = "$DAEMON_USER" ] || die "--data-dir '${DATA_DIR}' is not owned by the daemon user (${DAEMON_USER})"
    case "$MODES_RECORD" in
        "$DATA_DIR"/*) die "DAEMON_MODES_RECORD '${MODES_RECORD}' is inside the data dir; it must be a file of root outside it" ;;
    esac
    set_agent_paths
}

# parents_safe PATH — no part of the path above PATH is a link, and every
# directory above PATH, up to the data dir or /, belongs to root. The data dir
# and the directories above it belong to the daemon user, who has root during
# the apply anyway; the agent user owns none of them. A dry run made no
# directory, so it checks from the nearest one that exists.
parents_safe() {
    local d="${1%/*}"
    [ -n "$d" ] || d=/
    while [ "$DRY_RUN" = 1 ] && [ ! -e "$d" ] && [ ! -L "$d" ]; do
        d="${d%/*}"
        [ -n "$d" ] || d=/
    done
    [ "$(realpath "$d" 2>/dev/null)" = "$d" ] || return 1
    while [ "$d" != "$DATA_DIR" ] && [ "$d" != / ]; do
        [ "$(stat -c %U "$d" 2>/dev/null)" = root ] || return 1
        d="${d%/*}"
        [ -n "$d" ] || d=/
    done
}

# check_agent_root — the agent root: its parent is the data dir or a directory
# of root (parents_safe), it holds neither the data dir nor the record of the
# modes, and when it exists it is a directory of root, not a link. Root makes
# it and the two directories below it; nothing of the agent user is above them.
check_agent_root() {
    [ "$AGENT_ROOT" != "$DATA_DIR" ] || die "--agent-root must not be the data dir"
    case "${DATA_DIR}/" in "${AGENT_ROOT}"/*) die "--agent-root '${AGENT_ROOT}' holds the data dir '${DATA_DIR}'" ;; esac
    case "$MODES_RECORD" in "$AGENT_ROOT"/*) die "DAEMON_MODES_RECORD '${MODES_RECORD}' is inside the agent root" ;; esac
    [ -d "${AGENT_ROOT%/*}" ] || die "the parent of --agent-root '${AGENT_ROOT}' does not exist"
    parents_safe "$AGENT_ROOT" || die "--agent-root '${AGENT_ROOT}': a directory above it is a link, or neither the data dir nor a directory of root"
    [ ! -L "$AGENT_ROOT" ] || die "'${AGENT_ROOT}' is a symbolic link; refused"
    if [ -e "$AGENT_ROOT" ]; then
        [ -d "$AGENT_ROOT" ] || die "--agent-root '${AGENT_ROOT}' is not a directory"
        [ "$(stat -c %U "$AGENT_ROOT")" = root ] || die "--agent-root '${AGENT_ROOT}' exists and is not owned by root; choose another path"
    fi
}

# shq S — S quoted for a shell, on one line: in single quotes, or in the form of
# printf %q ($'...') when S has a quote or a byte outside space to tilde (a
# newline, or a byte of 0x80 to 0x9f that a terminal reads as a C1 control: in
# the C locale [[:cntrl:]] does not match those).
shq() {
    case "$1" in
        *[!\ -~]* | *\'*) printf '%q' "$1" ;;
        *) printf "'%s'" "$1" ;;
    esac
}

# plain_data_path PATH — true when PATH, a resolved path, is the data dir or is
# below it and holds only letters, digits, dot, underscore, hyphen and slash,
# with no empty, "." or ".." part. Only such a path is recorded (the record has
# one entry on each line) or printed in a chmod line or in a line about an
# entry: the daemon user names the entries and the targets of the links there.
plain_data_path() {
    local base="$DATA_DIR"
    while [ "$base" != / ] && [ "${base%/}" != "$base" ]; do base="${base%/}"; done
    [[ "$1" =~ ^/[A-Za-z0-9._/-]*$ ]] || return 1
    case "${1}/" in */./* | */../* | *//*) return 1 ;; esac
    case "$1" in "$base" | "$base"/*) return 0 ;; esac
    return 1
}

# chmod_as_daemon_line MODE PATH — the one form of every printed chmod line,
# for apply and for undo: chmod runs as the daemon user. The operator runs the
# line as root, as it is; the daemon user owns the entries and can put a link in
# place of one after the line was printed, and through runuser the chmod can
# change only what the daemon user owns, not the target of such a link.
chmod_as_daemon_line() {
    printf 'runuser -u %s -- chmod %s %s' "$DAEMON_USER" "$1" "$(shq "$2")"
}

# contained PATH — true when the resolved form of PATH is strictly inside the
# agent root. Sets RESOLVED (empty when PATH cannot be resolved).
RESOLVED=""
contained() {
    RESOLVED="$(realpath "$1" 2>/dev/null)" || RESOLVED=""
    case "$RESOLVED" in
        "$AGENT_ROOT"/*) return 0 ;;
    esac
    return 1
}

# check_managed_dirs — the directories below the agent root must be real
# directories inside it. The agent user owns them, so this is the first check of
# the directories step (after the firewall steps), not a refusal before the
# first change: a link, a file or a FIFO there fails that step only.
check_managed_dirs() {
    local managed
    for managed in "$AGENT_HOME" "$WORKSPACES" "${AGENT_HOME}/go" "${AGENT_HOME}/go/cache" "${AGENT_HOME}/go/mod"; do
        if [ -L "$managed" ]; then
            echo "'${managed}' is a symbolic link; refused" >&2
            return 1
        fi
        [ -e "$managed" ] || continue
        if ! contained "$managed"; then
            echo "the directory '${managed}' resolves to '${RESOLVED:-?}', outside the agent root '${AGENT_ROOT}'" >&2
            return 1
        fi
        if [ ! -d "$RESOLVED" ]; then
            echo "'${managed}' is not a directory (a file or a FIFO); refused" >&2
            return 1
        fi
    done
}

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

# ensure_dir PATH OWNER GROUP MODE — a root step: only where every directory
# above PATH is root's (or the data dir, see parents_safe), so that no other
# user can swap a part of the path between the check and the call. Sets
# CHANGED=1 when it had to act. A path that became a symbolic link after the
# checks is refused, not followed.
CHANGED=0
ensure_dir() {
    local path="$1" owner="$2" group="$3" mode="$4" cur
    if ! parents_safe "$path"; then
        echo "'${path}': a directory above it is a link or not root's; refused" >&2
        return 1
    fi
    if [ -L "$path" ]; then
        echo "'${path}' is a symbolic link; refused" >&2
        return 1
    fi
    cur="$(stat -c '%U:%G:%a' "$path" 2>/dev/null || true)"
    if [ "$cur" = "${owner}:${group}:${mode}" ]; then
        return 0
    fi
    run install -d -o "$owner" -g "$group" -m "$mode" "$path" || return 1
    CHANGED=1
}

# ---------------------------------------------------------------- apply steps

# open_below_data_dir — each process of a user other than root and the daemon
# user with a file (or its working directory) open below the data dir, outside
# the agent root, from /proc: the chmods of daemon-private take its access away.
open_below_data_dir() {
    local p uid f t name
    for p in /proc/[0-9]*; do
        uid="$(awk '/^Uid:/ { print $2; exit }' "$p/status" 2>/dev/null)" || continue
        case "$uid" in "" | 0 | "$DAEMON_UID") continue ;; esac
        for f in "$p/cwd" "$p"/fd/*; do
            t="$(readlink "$f" 2>/dev/null)" || continue
            case "$t" in
                "$AGENT_ROOT" | "$AGENT_ROOT"/*) continue ;;
                "$DATA_DIR" | "$DATA_DIR"/*) ;;
                *) continue ;;
            esac
            name="$(getent passwd "$uid" 2>/dev/null | cut -d: -f1 || true)"
            printf 'a process of %s (pid %s, %s) has %s open; it loses its access with the chmods below\n' \
                "${name:-uid ${uid}}" "${p##*/}" "$(shq "$(cat "$p/comm" 2>/dev/null || true)")" "$(shq "$t")" >&2
            break
        done
    done
}

# record_modes LINE... — each LINE is "MODE<TAB>PATH"; added to the record of
# root unless the path is in it already (the first mode found is the one undo
# puts back). Not in a dry run.
record_modes() {
    local tmp="${MODES_RECORD%/*}/.${MODES_RECORD##*/}.new"
    [ "$DRY_RUN" = 0 ] || return 0
    if [ -L "$MODES_RECORD" ] || { [ -e "$MODES_RECORD" ] && [ ! -f "$MODES_RECORD" ]; }; then
        echo "${MODES_RECORD} is not a regular file; the modes are not recorded" >&2
        return 1
    fi
    (
        umask 077
        { if [ -f "$MODES_RECORD" ]; then cat "$MODES_RECORD"; fi; printf '%s\n' "$@"; } \
            | awk -F '\t' '!seen[$2]++' >"$tmp"
    ) || { rm -f "$tmp"; return 1; }
    mv -f "$tmp" "$MODES_RECORD" || return 1
    echo "the modes found are recorded in ${MODES_RECORD}; --undo prints the chmods that put them back" >&2
}

# step_daemon_private — the data dir gives group and others traverse at most
# (0711 or stricter, never read or write), and every entry of it but the agent
# root is closed to the agent user: no bit for "other" and, when its group is
# the shared group, no bit for the group. A link is followed (the agent user
# follows it too). The top level is enough: nothing of the agent user is
# between the entries of the daemon, and a closed directory closes all below it.
# The step changes no mode itself. An entry whose name or resolved path is not
# a plain path in the data dir (plain_data_path; a link out of it is one), or
# that resolves to the data dir or to the agent root or a path in it (a chmod
# there takes the traverse of the agent user away), is neither recorded nor
# given a chmod: one line names it, for a look by hand. So is an open entry that
# does not belong to the daemon user (one of root, as the parent of a nested
# agent root): the chmod as the daemon user would fail on it.
step_daemon_private() {
    local e real meta owner grp mode fix lines=() records=()
    meta="$(stat -L -c '%G:%a' "$DATA_DIR")" || return 1
    if [ $((8#${meta#*:} & 8#066)) -ne 0 ]; then
        lines+=("$(printf 'the data dir %s gives more than traverse (group %s, mode %s): %s' \
            "$(shq "$DATA_DIR")" "${meta%%:*}" "${meta#*:}" "$(chmod_as_daemon_line go-rw "$DATA_DIR")")")
        records+=("${meta#*:}"$'\t'"${DATA_DIR}")
    fi
    for e in "$DATA_DIR"/* "$DATA_DIR"/.[!.]* "$DATA_DIR"/..?*; do
        [ "$e" != "$AGENT_ROOT" ] || continue
        if [ ! -e "$e" ]; then
            # an unmatched glob: nothing to reach
            [ -L "$e" ] || continue
            # a dangling link or a link loop: the preflight of the daemon
            # refuses it at the start, so the apply does not pass it
            lines+=("$(printf 'the entry %s is a link that does not resolve: remove it or correct it, look at it by hand' \
                "$(shq "$e")")")
            continue
        fi
        real="$(realpath "$e" 2>/dev/null)" || real=""
        if ! plain_data_path "$e" || ! plain_data_path "$real"; then
            lines+=("$(printf 'the entry %s is not a plain path in the data dir (a name or a link target outside [A-Za-z0-9._/-], or a link out of the data dir): no chmod is printed, look at it by hand' \
                "$(shq "$e")")")
            continue
        fi
        case "$real" in
            "$DATA_DIR" | "$AGENT_ROOT" | "$AGENT_ROOT"/*)
                lines+=("$(printf 'the entry %s resolves to the data dir or to the agent root or a path in it: no chmod is printed, look at it by hand' \
                    "$(shq "$e")")")
                continue
                ;;
        esac
        meta="$(stat -L -c '%U:%G:%a' "$e")" || return 1
        owner="${meta%%:*}"
        meta="${meta#*:}"
        grp="${meta%%:*}"
        mode=$((8#${meta#*:}))
        fix=""
        if [ "$grp" = "$GROUP" ] && [ $(((mode >> 3) & 7)) -ne 0 ]; then
            fix="go-rwx"
        elif [ $((mode & 7)) -ne 0 ]; then
            fix="o-rwx"
        fi
        [ -n "$fix" ] || continue
        if [ "$owner" != "$DAEMON_USER" ]; then
            lines+=("$(printf 'the entry %s belongs to %s, not to %s (group %s, mode %s): no chmod is printed, look at it by hand' \
                "$(shq "$e")" "$owner" "$DAEMON_USER" "$grp" "${meta#*:}")")
            continue
        fi
        lines+=("$(printf 'the agent user can reach %s (group %s, mode %s): %s' \
            "$(shq "$e")" "$grp" "${meta#*:}" "$(chmod_as_daemon_line "$fix" "$real")")")
        records+=("${meta#*:}"$'\t'"${real}")
    done
    if [ "${#lines[@]}" -gt 0 ]; then
        open_below_data_dir
        if [ "${#records[@]}" -gt 0 ]; then
            echo "run each chmod line below as root, as it is: runuser runs chmod as ${DAEMON_USER}, so it changes only what ${DAEMON_USER} owns" >&2
        fi
        printf '%s\n' "${lines[@]}" >&2
        if [ "${#records[@]}" -gt 0 ]; then
            record_modes "${records[@]}" || true
            echo "no mode was changed; run each chmod above and run again" >&2
        else
            echo "no mode was changed; look at each entry above by hand and run again" >&2
        fi
        return 1
    fi
    RESULT=skipped
    NOTE="a check: no daemon file under ${DATA_DIR} is open to the agent user"
}

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

# step_directories — root makes the agent root and the two directories below
# it (their parents are root's). The Go caches are below the agent home, a
# directory of the agent user, where the agent user could swap a part of the
# path for a link between a check and a root call: the agent user makes them.
# When it cannot (a closed directory above its home), the step goes on and the
# proof at the end fails with the path.
step_directories() {
    local d missing=0
    CHANGED=0
    check_managed_dirs || return 1
    ensure_dir "$AGENT_ROOT" root root 755 || return 1
    ensure_dir "$AGENT_HOME" "$AGENT_USER" "$GROUP" 750 || return 1
    ensure_dir "$WORKSPACES" "$AGENT_USER" "$GROUP" 2770 || return 1
    for d in "${AGENT_HOME}/go" "${AGENT_HOME}/go/cache" "${AGENT_HOME}/go/mod"; do
        if [ -L "$d" ] || [ ! -d "$d" ] || [ "$(stat -c %U "$d")" != "$AGENT_USER" ]; then
            missing=1
        fi
    done
    if [ "$missing" = 1 ]; then
        if ! run runuser -u "$AGENT_USER" -- mkdir -p -m 750 "${AGENT_HOME}/go" "${AGENT_HOME}/go/cache" "${AGENT_HOME}/go/mod"; then
            NOTE="the agent user could not make its Go caches; agent-proof names the cause"
        fi
        CHANGED=1
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

# cloud_init_plan [FILE] — the report of CLOUD_AWK for FILE (default: the
# cloud-init file), no output file.
cloud_init_plan() {
    awk -v d="$DAEMON_USER" -v re="$(daemon_line_re)" -v out="" "$CLOUD_AWK" "${1:-$CLOUD_INIT_SUDOERS}"
}

# has_active_root_line FILE — FILE has a line that gives the daemon user sudo
# to root, one the apply would comment out.
has_active_root_line() {
    cloud_init_plan "$1" | grep -q '^DISABLE'
}

# restore_cloud_init BACKUP — put BACKUP back with the mode and owner the file
# has now; 0440 root:root only when it is gone.
restore_cloud_init() {
    local meta
    meta="$(stat -c '%a:%U:%G' "$CLOUD_INIT_SUDOERS" 2>/dev/null || echo 440:root:root)"
    run install -m "${meta%%:*}" -o "$(cut -d: -f2 <<<"$meta")" -g "$(cut -d: -f3 <<<"$meta")" \
        "$1" "$CLOUD_INIT_SUDOERS"
}

# remaining_rules — a hint for a failed proof, not the proof: where a sudoers
# line names the daemon user (its name, ALL or one of its groups) as the user,
# other than the agent rule, or a Defaults line for it touches authenticate.
# Output: file:line: text.
remaining_rules() {
    local groups f
    groups=" $(id -nG "$DAEMON_USER" | tr '\n' ' ') "
    for f in "$SUDOERS_MAIN" "$SUDOERS_DIR"/*; do
        # sudo skips file names with a dot or a tilde in sudoers.d
        case "${f##*/}" in *.* | *~) [ "$f" = "$SUDOERS_MAIN" ] || continue ;; esac
        [ -f "$f" ] || continue
        awk -v d="$DAEMON_USER" -v groups="$groups" -v rule="$(sudoers_rule)" '
            /^[ \t]*#/ || /^[ \t]*$/ { next }
            {
                line = $0; sub(/^[ \t]+/, "", line); sub(/[ \t]+$/, "", line)
                if (line == rule) next
                if ($1 ~ /^Defaults/) {
                    if (($1 == "Defaults" || $1 == "Defaults:" d) && line ~ /authenticate/)
                        printf "  %s:%d: %s\n", FILENAME, FNR, line
                    next
                }
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

# The rule list of `sudo -ll -U DAEMON_USER` (the long form of sudo 1.9), one
# pass. Every entry ("Sudoers entry:" or "LDAP Role:") must have one RunAsUsers
# line that names the agent user alone and at most a RunAsGroups line that
# names the shared group alone; Options, Commands and the command lines (a tab
# in front) are passed over, so are the Defaults sections. Any other line, an
# entry without its RunAsUsers line, or a count of entries that differs from the
# count of RunAsUsers lines fails. With skipfile and skipmax set, at most
# skipmax entries of skipfile ("Sudoers entry: FILE") whose run-as user is ALL
# or root are passed over: the lines the apply comments out. Output: one line
# per failure, an entry with its lines below it; exit 1 on a failure.
# shellcheck disable=SC2016 # the awk program is single-quoted on purpose
RULES_AWK='
function fail(msg) { print msg; failed = 1 }
function flush(   i) {
    if (n == 0) return
    if (cand && runas == 1 && (ru == "ALL" || ru == "root") && skipped < skipmax) {
        skipped++; n = 0; bad = ""; runas = 0; cmds = 0; cand = 0
        return
    }
    if (bad == "" && runas != 1) bad = (runas == 0 ? "no RunAsUsers line" : "more than one RunAsUsers line")
    if (bad != "") {
        fail("entry " entries " (" bad "):")
        for (i = 1; i <= n; i++) print "    | " text[i]
    }
    n = 0; bad = ""; runas = 0; cmds = 0; cand = 0
}
/^Matching Defaults entries for / || /^Runas and Command-specific defaults for / { flush(); sec = "defaults"; next }
/^User .* may run the following commands on / { flush(); sec = "rules"; listed = 1; next }
/^User .* is not allowed to run sudo on / { flush(); sec = "none"; listed = 1; next }
/^$/ { flush(); next }
sec == "defaults" && /^    / { next }
sec == "rules" && (/^Sudoers entry:/ || /^LDAP Role:/) {
    flush(); entries++; text[++n] = $0
    cand = (skipfile != "" && $0 == "Sudoers entry: " skipfile)
    next
}
n > 0 {
    text[++n] = $0
    if (/^    RunAsUsers: /) {
        runas++; total++
        v = substr($0, 17)
        ru = v
        if (v != agent && bad == "") bad = "run-as user " v ", not " agent " alone"
    } else if (/^    RunAsGroups: /) {
        v = substr($0, 18)
        if (v != group && bad == "") bad = "run-as group " v ", not " group " alone"
    } else if (/^    Options: /) {
    } else if (/^    Commands:$/) {
        cmds = 1
    } else if (cmds && /^\t/) {
    } else if (bad == "") {
        bad = "a line the check does not know: " $0
    }
    next
}
{ fail("a line the check does not know: " $0) }
END {
    flush()
    if (!listed) fail("no \"User ... may run\" or \"is not allowed\" line in the list")
    if (entries != total) fail(entries " entries, " total " RunAsUsers lines")
    exit failed
}'

# check_rule_list [SKIPFILE SKIPMAX] — read the rules of the daemon user (sudo
# -ll -U, as root) and check them with RULES_AWK: the only path left from the
# daemon user is the agent user. A rule that `sudo -n true` does not show (one
# command as root, a rule that needs a password, a rule through a group) fails
# here. SKIPFILE and SKIPMAX: the entries RULES_AWK passes over (the precheck).
# Exit 1 on a refused entry, 2 when the rule list cannot be read.
check_rule_list() {
    local list rc=0 report
    list="$(sudo -n -ll -U "$DAEMON_USER")" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "sudo -ll -U ${DAEMON_USER} failed (exit ${rc}); the sudo removal is not proven" >&2
        return 2
    fi
    if report="$(printf '%s\n' "$list" | awk -v agent="$AGENT_USER" -v group="$GROUP" \
        -v skipfile="${1:-}" -v skipmax="${2:-0}" "$RULES_AWK")"; then
        return 0
    fi
    echo "the sudo rules of ${DAEMON_USER} (sudo -ll -U ${DAEMON_USER}) hold more than the agent rule: each entry must have ${AGENT_USER} as the only run-as user and no run-as group but ${GROUP}" >&2
    printf '%s\n' "$report" | sed 's/^/  /' >&2
    return 1
}

# prove_sudo_removed — by behaviour, whatever the text of the rules is: as the
# daemon user, `sudo -n -u AGENT true` must succeed (the same path reaches sudo
# and the agent rule works, so a failure below is not a broken sudo) and
# `sudo -n true` must fail (no command as root without a password). Then by the
# rules (check_rule_list): both parts are needed.
prove_sudo_removed() {
    local out rc=0 where
    if [ "$DRY_RUN" = 1 ]; then
        printf '+ runuser -u %q -- sudo -n true  (after the edit: must fail; sudo -n -u %q true must succeed)\n' \
            "$DAEMON_USER" "$AGENT_USER"
        printf '+ sudo -n -ll -U %q  (each entry: run-as user %q only, run-as group %q at most)\n' \
            "$DAEMON_USER" "$AGENT_USER" "$GROUP"
        return 0
    fi
    out="$(runuser -u "$DAEMON_USER" -- sudo -n -u "$AGENT_USER" true 2>&1)" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "as ${DAEMON_USER}, sudo -n -u ${AGENT_USER} true failed (exit ${rc}); the sudo removal is not proven:" >&2
        printf '%s\n' "$out" | sed 's/^/  /' >&2
        return 1
    fi
    if ! out="$(runuser -u "$DAEMON_USER" -- sudo -n true 2>&1)"; then
        check_rule_list
        return
    fi
    echo "${DAEMON_USER} still runs a command as root without a password: sudo -n true succeeded" >&2
    where="$(remaining_rules)"
    if [ -n "$where" ]; then
        echo "sudoers lines that may give it:" >&2
        printf '%s\n' "$where" >&2
    else
        echo "no such line in ${SUDOERS_MAIN} or ${SUDOERS_DIR}/ (an include or another source)" >&2
    fi
    return 1
}

# precheck_sudo_rules — before the first step, so before the first change of a
# sudoers file: the rule list as check_rule_list reads it, but for the entries
# the apply removes by itself (the cloud-init file, run-as user ALL or root, at
# most one per line it comments out). Any other entry the proof would refuse (a
# rule through a group of the daemon user, a rule for a command as root, a
# cloud-init line the apply leaves) fails the apply here, with the host as it
# was; after the edit the proof would fail with the cloud-init file changed. Not
# run with --keep-daemon-sudo (no proof follows) or when no sudoers file would
# change (the agent rule is in place, no line to comment out): then the proof
# alone decides. A dry run reads the rule list too (the command only reads): on
# a refusal, or a rule list that cannot be read, DRY_RUN_STOP says so, the dry
# run goes on and ends with exit 1 after the summary.
DRY_RUN_STOP=""
precheck_sudo_rules() {
    local ndis=0 rc=0
    [ "$KEEP_DAEMON_SUDO" = 0 ] || return 0
    if [ -f "$CLOUD_INIT_SUDOERS" ]; then
        ndis="$(cloud_init_plan | grep -c '^DISABLE' || true)"
    fi
    if [ "$ndis" -eq 0 ] && file_has_content "$SUDOERS_FILE" "$(sudoers_rule)"; then
        return 0
    fi
    echo "== sudo-rules (check before the first change)"
    if [ "$DRY_RUN" = 1 ]; then
        printf '+ sudo -n -ll -U %q  (each entry but %s of %q: run-as user %q only, run-as group %q at most)\n' \
            "$DAEMON_USER" "$ndis" "$CLOUD_INIT_SUDOERS" "$AGENT_USER" "$GROUP"
    fi
    check_rule_list "$CLOUD_INIT_SUDOERS" "$ndis" || rc=$?
    [ "$rc" -ne 0 ] || return 0
    if [ "$DRY_RUN" = 1 ]; then
        if [ "$rc" = 2 ]; then
            DRY_RUN_STOP="dry run: the rule list could not be read"
        else
            DRY_RUN_STOP="dry run: the apply would stop here, nothing would be changed"
        fi
        echo "$DRY_RUN_STOP" >&2
        return 0
    fi
    echo "nothing is changed: the proof after the edit of ${CLOUD_INIT_SUDOERS} would refuse the entries above; remove each rule, or the membership of ${DAEMON_USER} in the group it names, first, then run again" >&2
    return 1
}

step_cloud_init_sudoers() {
    local plan kind n rest reason text ndis=0 tmp meta bak_n
    if [ "$KEEP_DAEMON_SUDO" = 1 ]; then
        RESULT=skipped
        NOTE="--keep-daemon-sudo"
        return 0
    fi
    if [ ! -f "$CLOUD_INIT_SUDOERS" ]; then
        prove_sudo_removed || return 1
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
        restore_cloud_init "$CLOUD_INIT_BACKUP" || true
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

# drop_chain BIN CHAIN — its jumps, its rules, then the chain itself.
drop_chain() {
    drop_jumps "$1" "$2" || return 1
    run "$1" -w -F "$2" || return 1
    run "$1" -w -X "$2" || return 1
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
        drop_chain "$bin" "$c" || return 1
    done
    run "$bin" -w -E "$tmp" "$CHAIN" || return 1
}

# firewall_family BIN FAMILY — sets CHANGED=1 when it acts.
firewall_family() {
    local bin="$1" c
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
    # A temporary chain of an earlier failed run, with or without a jump to it
    # (also when the main chain was absent): it goes once the jump to the
    # complete main chain is first, so the reject rules stay in force.
    for c in "$CHAIN_NEW" "$CHAIN_ALT"; do
        "$bin" -w -S "$c" >/dev/null 2>&1 || continue
        drop_chain "$bin" "$c" || return 1
        CHANGED=1
    done
    [ "$CHANGED" = 1 ] || RESULT=skipped
    return 0
}

step_firewall_ipv4() { firewall_family "$IPT4" v4; }

# ipv6_off — IPv6 is off on the host: disable_ipv6 is 1 for every entry of
# IPV6_CONF_DIR ("all", "default" and each interface). A host without the
# settings counts as on.
ipv6_off() {
    local f
    [ -f "${IPV6_CONF_DIR}/all/disable_ipv6" ] || return 1
    for f in "$IPV6_CONF_DIR"/*/disable_ipv6; do
        [ "$(cat "$f" 2>/dev/null)" = 1 ] || return 1
    done
}

# step_firewall_ipv6 — without ip6tables the agent user would reach the metadata
# address and the gateway port over IPv6: a failure, unless IPv6 is off.
step_firewall_ipv6() {
    if ! command -v "$IPT6" >/dev/null 2>&1; then
        if ipv6_off; then
            RESULT=skipped
            NOTE="${IPT6} not found and IPv6 is off (disable_ipv6 is 1 in ${IPV6_CONF_DIR}): the IPv6 part is skipped"
            return 0
        fi
        echo "${IPT6} not found and IPv6 is on (a disable_ipv6 in ${IPV6_CONF_DIR} is not 1): the agent user would not be blocked on IPv6; install ${IPT6} or turn IPv6 off" >&2
        return 1
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
        NOTE="IPv4 only: ${IPT6} not found, IPv6 is off"
    fi
}

# step_agent_proof — the last step, a check as the agent user: it creates and
# removes a file in its home and in the workspace root, its Go caches exist,
# and it fails to list
# the data dir, each directory in it but the agent root and one directory below
# those (a repository of the daemon). Every root step passes when a directory
# above the data dir (a home at 0700) keeps the agent user out; this does not.
step_agent_proof() {
    local d probe e repo="" bad=0 targets=()
    for d in "$AGENT_HOME" "$WORKSPACES"; do
        probe="${d}/.setup-agent-user-proof"
        if ! run runuser -u "$AGENT_USER" -- touch "$probe"; then
            echo "the agent user cannot create a file in ${d}; a directory above it may be closed to the agent user (a home at 0700)" >&2
            bad=1
            continue
        fi
        if ! run runuser -u "$AGENT_USER" -- rm -f "$probe"; then
            echo "the agent user cannot remove ${probe}" >&2
            bad=1
        fi
    done
    for d in "${AGENT_HOME}/go/cache" "${AGENT_HOME}/go/mod"; do
        [ "$DRY_RUN" = 0 ] || continue
        if [ -L "$d" ] || [ ! -d "$d" ] || [ "$(stat -c %U "$d")" != "$AGENT_USER" ]; then
            echo "the Go cache directory ${d} is missing or not a directory of the agent user" >&2
            bad=1
        fi
    done
    targets=("$DATA_DIR")
    for e in "$DATA_DIR"/* "$DATA_DIR"/.[!.]* "$DATA_DIR"/..?*; do
        if [ "$e" = "$AGENT_ROOT" ] || [ ! -d "$e" ]; then continue; fi
        targets+=("$e")
        if [ -z "$repo" ]; then
            for d in "$e"/*/; do
                [ -d "$d" ] || continue
                repo="${d%/}"
                break
            done
        fi
    done
    [ -z "$repo" ] || targets+=("$repo")
    for e in "${targets[@]}"; do
        printf '+ runuser -u %q -- ls %s  (must fail)\n' "$AGENT_USER" "$(shq "$e")"
        [ "$DRY_RUN" = 0 ] || continue
        if runuser -u "$AGENT_USER" -- ls "$e" >/dev/null 2>&1; then
            echo "the agent user can list $(shq "$e")" >&2
            bad=1
        fi
    done
    [ "$bad" = 0 ] || return 1
    RESULT=skipped
    NOTE="a check: the agent user writes in its home and its workspace root and lists no directory of the daemon"
}

# ---------------------------------------------------------------- undo steps
# Each undo step checks the paths it changes, and only those; all of them are
# root's files outside the data dir.

# cloud_init_backups — the backups, newest first: the unnumbered one, then the
# numbered ones from the highest number down (a rotation moves the old one to
# the first free number).
cloud_init_backups() {
    local f
    if [ -e "$CLOUD_INIT_BACKUP" ] || [ -L "$CLOUD_INIT_BACKUP" ]; then
        echo "$CLOUD_INIT_BACKUP"
    fi
    for f in "$CLOUD_INIT_BACKUP".*; do
        [[ "${f##*.}" =~ ^[0-9]+$ ]] || continue
        printf '%s\t%s\n' "${f##*.}" "$f"
    done | sort -rn | cut -f2-
}

# backup_ok PATH — a regular file of root, not a link.
backup_ok() {
    [ ! -L "$1" ] && [ -f "$1" ] && [ "$(stat -c %U "$1")" = root ]
}

# undo_cloud_init_sudoers — nothing to do while the root line of the daemon
# user is active in the file. Else the newest backup in which it is active goes
# back (a newer backup without it is passed over and kept).
undo_cloud_init_sudoers() {
    local b chosen="" any=0
    if [ -L "$CLOUD_INIT_SUDOERS" ]; then
        echo "${CLOUD_INIT_SUDOERS} is a symbolic link; refusing to restore it" >&2
        return 1
    fi
    if [ -f "$CLOUD_INIT_SUDOERS" ] && has_active_root_line "$CLOUD_INIT_SUDOERS"; then
        RESULT=skipped
        NOTE="the root line of ${DAEMON_USER} is active in ${CLOUD_INIT_SUDOERS}"
        return 0
    fi
    while IFS= read -r b; do
        any=1
        if ! backup_ok "$b"; then
            echo "not a regular file of root, passed over: ${b}"
        elif has_active_root_line "$b"; then
            chosen="$b"
            break
        else
            echo "no active root line of ${DAEMON_USER}, passed over: ${b}"
        fi
    done < <(cloud_init_backups)
    if [ -z "$chosen" ]; then
        if [ "$any" = 0 ]; then
            RESULT=skipped
            NOTE="no backup at ${CLOUD_INIT_BACKUP}"
            return 0
        fi
        echo "no backup of ${CLOUD_INIT_SUDOERS} has an active root line of ${DAEMON_USER}; nothing restored" >&2
        return 1
    fi
    echo "restoring ${CLOUD_INIT_SUDOERS} from ${chosen}, the newest backup with an active root line of ${DAEMON_USER}"
    restore_cloud_init "$chosen" || return 1
    # The backup stays until the restored file is validated; the numbered
    # backups stay in any case.
    if ! run visudo -c; then
        echo "sudoers validation failed after the restore of ${CLOUD_INIT_SUDOERS}; backup kept" >&2
        return 1
    fi
    if [ "$chosen" = "$CLOUD_INIT_BACKUP" ]; then
        run rm -f "$CLOUD_INIT_BACKUP" || return 1
    fi
}

# count_agent_procs WHEN — sets PROC_COUNT to the count of processes of the
# agent user. pgrep exits 1 when there is none. A pgrep that is not found (127),
# that exits with another code, or that exits 0 with no process id gives no
# count: the reason (WHEN ends it: what the undo did so far) goes to stderr and
# to NOTE, the note of the summary line of the step, and the function fails, so
# that a failed pgrep never reads as "no process".
PROC_COUNT=0
count_agent_procs() {
    local pids rc=0 reason=""
    PROC_COUNT=0
    pids="$(pgrep -u "$AGENT_USER")" || rc=$?
    case "$rc" in
        0)
            PROC_COUNT="$(printf '%s\n' "$pids" | grep -c . || true)"
            [ "$PROC_COUNT" -gt 0 ] || reason="pgrep -u ${AGENT_USER} exited 0 with no process id"
            ;;
        1) ;;
        127) reason="pgrep not found (exit 127)" ;;
        *) reason="pgrep -u ${AGENT_USER} failed (exit ${rc})" ;;
    esac
    [ -n "$reason" ] || return 0
    NOTE="${reason}: the processes of ${AGENT_USER} are not known; $1"
    echo "$NOTE" >&2
    return 1
}

# undo_agent_processes — before the firewall goes, every process of the agent
# user stops: TERM, then KILL for those left after KILL_WAIT seconds.
undo_agent_processes() {
    local n left
    if ! id -u "$AGENT_USER" >/dev/null 2>&1; then
        RESULT=skipped
        NOTE="no user ${AGENT_USER}"
        return 0
    fi
    count_agent_procs "none is stopped" || return 1
    n="$PROC_COUNT"
    if [ "$n" -eq 0 ]; then
        RESULT=skipped
        NOTE="no process of ${AGENT_USER}"
        return 0
    fi
    echo "stopping ${n} process(es) of ${AGENT_USER}: TERM, then KILL after ${KILL_WAIT} seconds"
    run pkill -TERM -u "$AGENT_USER" || true
    if [ "$DRY_RUN" = 1 ]; then
        run pkill -KILL -u "$AGENT_USER"
        NOTE="${n} process(es) of ${AGENT_USER}"
        return 0
    fi
    for _ in $(seq 1 "$KILL_WAIT"); do
        pgrep -u "$AGENT_USER" >/dev/null || break
        sleep 1
    done
    count_agent_procs "after the TERM" || return 1
    left="$PROC_COUNT"
    if [ "$left" -gt 0 ]; then
        echo "${left} process(es) of ${AGENT_USER} still run after TERM and ${KILL_WAIT} seconds: KILL"
        run pkill -KILL -u "$AGENT_USER" || true
        sleep 1
        count_agent_procs "after the KILL" || return 1
        left="$PROC_COUNT"
        if [ "$left" -gt 0 ]; then
            echo "${left} process(es) of ${AGENT_USER} still run after KILL:" >&2
            pgrep -l -u "$AGENT_USER" >&2 || true
            return 1
        fi
    fi
    NOTE="${n} process(es) of ${AGENT_USER} stopped"
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
    if [ ! -e "$UNIT_FILE" ] && [ ! -L "$UNIT_FILE" ] && ! systemctl is-enabled --quiet "$UNIT_NAME" >/dev/null 2>&1; then
        RESULT=skipped
        return 0
    fi
    run systemctl stop "$UNIT_NAME" || true
    run systemctl disable "$UNIT_NAME" || true
    # rm -f on a link removes the link, never what it points to
    run rm -f "$UNIT_FILE" || return 1
    run systemctl daemon-reload || return 1
}

undo_sudoers_rule() {
    if [ ! -e "$SUDOERS_FILE" ] && [ ! -L "$SUDOERS_FILE" ] && [ ! -e "$SUDOERS_TMP" ] && [ ! -L "$SUDOERS_TMP" ]; then
        RESULT=skipped
        return 0
    fi
    run rm -f "$SUDOERS_FILE" "$SUDOERS_TMP" || return 1
}

# undo_firewall_allowed — false when the sudoers-rule step of the undo failed
# and the rule file is still there (a dangling link too: a file can take its
# place): the daemon user can then start the agent user, so the firewall stays.
# A dry run removes nothing and prints the whole plan.
undo_firewall_allowed() {
    [ "$DRY_RUN" = 0 ] || return 0
    case " ${FAILED_STEPS} " in *" sudoers-rule "*) ;; *) return 0 ;; esac
    [ ! -e "$SUDOERS_FILE" ] && [ ! -L "$SUDOERS_FILE" ]
}

# undo_procs_left — true when the agent-processes step of the undo failed: a
# process of the agent user may still run (the list failed, or one outlived
# KILL), and without the firewall it reaches the gateway port and the metadata
# address, so the firewall stays.
undo_procs_left() {
    case " ${FAILED_STEPS} " in *" agent-processes "*) return 0 ;; esac
    return 1
}

# no_sudo_left — after the undo: the rule file is gone and the cloud-init file
# still has a root line of the daemon user that the apply commented out (no
# active one): the daemon user has no sudo at all. Reads the file, not the
# result of the step (no backup, a failed restore).
no_sudo_left() {
    if [ -e "$SUDOERS_FILE" ] || [ -L "$SUDOERS_FILE" ] || [ -L "$CLOUD_INIT_SUDOERS" ] || [ ! -f "$CLOUD_INIT_SUDOERS" ]; then
        return 1
    fi
    grep -qE "^# disabled by setup-agent-user:.*$(daemon_line_re)" "$CLOUD_INIT_SUDOERS" || return 1
    ! has_active_root_line "$CLOUD_INIT_SUDOERS"
}

# ------------------------------------------------------------------- driver

print_summary() {
    local line
    echo
    echo "== summary"
    for line in "${SUMMARY[@]}"; do
        echo "step ${line}"
    done
    [ -z "$SUDO_RULE_REMOVAL" ] || echo "$SUDO_RULE_REMOVAL"
    [ -z "$NO_SUDO_LEFT" ] || echo "$NO_SUDO_LEFT"
    [ -z "$DRY_RUN_STOP" ] || echo "sudo-rules: ${DRY_RUN_STOP}"
    [ -z "$DAEMON_GROUP_WARNING" ] || echo "$DAEMON_GROUP_WARNING"
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

# run_every NAME:FUNCTION ... — undo: runs every step in order, also after a
# failed one; FAILED_STEPS names the failed ones.
FAILED_STEPS=""
run_every() {
    local entry
    for entry in "$@"; do
        run_step "${entry%%:*}" "${entry#*:}" || FAILED_STEPS="${FAILED_STEPS:+${FAILED_STEPS} }${entry%%:*}"
    done
}

# remove_sudo_rule_on_failure FAILED NOT_RUN... — the apply failed at FAILED;
# NOT_RUN are the plan entries it did not run. When sudoers-rule is one of them,
# a rule file of an earlier apply would let the daemon start the agent user
# while the firewall is not complete (its rules gone and iptables missing, or
# IPv6 on now with no ip6tables): the file goes, through run (a dry run prints
# the command and changes nothing). One line says so, and the summary lists it;
# the exit status of the apply stays 1. While the cloud-init file still has an
# active root line of the daemon user (--keep-daemon-sudo, or an apply that
# failed at cloud-init-sudoers), the daemon user keeps a way to the agent user
# through it: the line says that instead.
remove_sudo_rule_on_failure() {
    local failed="$1" entry ahead=0 dry=""
    shift
    for entry in "$@"; do
        [ "${entry%%:*}" != sudoers-rule ] || ahead=1
    done
    [ "$ahead" = 1 ] || return 0
    [ -e "$SUDOERS_FILE" ] || [ -L "$SUDOERS_FILE" ] || return 0
    [ "$DRY_RUN" = 0 ] || dry=" (dry-run)"
    if [ -f "$CLOUD_INIT_SUDOERS" ] && has_active_root_line "$CLOUD_INIT_SUDOERS"; then
        echo "${failed} failed before the sudoers-rule step: removing the sudo rule of an earlier apply, ${SUDOERS_FILE}; ${DAEMON_USER} still has sudo through ${CLOUD_INIT_SUDOERS} (an active root line), so the daemon can still start the agent user without the complete firewall"
    else
        echo "${failed} failed before the sudoers-rule step: removing the sudo rule of an earlier apply, ${SUDOERS_FILE}, so that the daemon cannot start the agent user without the complete firewall"
    fi
    if run rm -f "$SUDOERS_FILE"; then
        SUDO_RULE_REMOVAL="sudo rule removed${dry}: ${SUDOERS_FILE} (${failed} failed; the next apply that passes puts it back)"
    else
        SUDO_RULE_REMOVAL="sudo rule NOT removed: ${SUDOERS_FILE} (${failed} failed); remove it by hand"
    fi
}

# run_plan NAME:FUNCTION ... — runs the steps in order. After a failed step the
# rest are not run; each still gets a summary line, so the summary has one per
# step. A failure before the sudoers-rule step removes the rule file of an
# earlier apply (remove_sudo_rule_on_failure).
run_plan() {
    local entry failed=""
    while [ $# -gt 0 ]; do
        entry="$1"
        shift
        run_step "${entry%%:*}" "${entry#*:}" && continue
        failed="${entry%%:*}"
        for entry in "$@"; do
            SUMMARY+=("${entry%%:*}: skipped - not run, ${failed} failed")
        done
        remove_sudo_rule_on_failure "$failed" "$@"
        print_summary
        exit 1
    done
}

# print_config — the values for the executor section of the daemon config.
print_config() {
    echo
    echo "== daemon config (executor section)"
    printf '  run_as_user: %s\n' "$AGENT_USER"
    printf '  run_as_group: %s\n' "$GROUP"
    printf '  run_as_workspace_dir: %s\n' "$WORKSPACES"
    printf '  run_as_data_dir: %s\n' "$DATA_DIR"
}

# print_mode_restore — the chmods that put back the modes daemon-private found,
# from root's record; printed (chmod_as_daemon_line), not run. A record that is
# a link or not root's is passed over. A line is printed only when its path passes plain_data_path
# for the data dir of this run (as given) and its mode is three or four octal
# digits with no setuid, setgid or sticky bit; a line that fails is named by its
# number, its content is not printed.
print_mode_restore() {
    local mode path n=0
    [ -e "$MODES_RECORD" ] || [ -L "$MODES_RECORD" ] || return 0
    echo
    echo "== earlier modes of the daemon's entries (not run)"
    if ! backup_ok "$MODES_RECORD"; then
        echo "${MODES_RECORD} is not a regular file of root; passed over"
        return 0
    fi
    echo "The apply asked for chmods on these paths; to put the earlier modes back, run each"
    echo "line below as root, as it is (runuser runs chmod as ${DAEMON_USER}, so it changes only"
    echo "what ${DAEMON_USER} owns):"
    while IFS=$'\t' read -r mode path; do
        n=$((n + 1))
        if ! [[ "$mode" =~ ^0?[0-7]{3}$ ]] || ! plain_data_path "$path"; then
            printf '  # line %s of the record is skipped: not a mode without a special bit and a plain path in the data dir %s\n' \
                "$n" "$(shq "$DATA_DIR")"
            continue
        fi
        printf '  %s\n' "$(chmod_as_daemon_line "$mode" "$path")"
    done <"$MODES_RECORD"
}

print_manual_cleanup() {
    echo
    echo "== left in place (not run)"
    echo "The user, the group, the group membership and the directories stay. To remove them"
    echo "(pkill first: a sudo of the daemon user to the agent user leaves a systemd --user"
    echo "of the agent user behind for a while, and userdel refuses a user with a process):"
    printf '  pkill -u %q\n' "$AGENT_USER"
    printf '  gpasswd -d %q %q\n' "$DAEMON_USER" "$GROUP"
    printf '  userdel %q\n' "$AGENT_USER"
    printf '  groupdel %q\n' "$GROUP"
    printf '  rm -rf %q\n' "$AGENT_ROOT"
}

if [ "$DRY_RUN" = 1 ]; then
    echo "setup-agent-user: dry run, nothing is changed"
fi

# Undo first: nothing above reads a path that the agent user or the daemon user
# can write, so neither of them can stop the restore of root's files. A failed
# step does not stop the undo: each later step runs, but the firewall steps do
# not run while the rule file is still there (undo_firewall_allowed) or a
# process of the agent user may still run (undo_procs_left). The
# sudoers rule goes first, so that the daemon cannot start an agent process
# after the stop of the agent processes; those stop before the firewall goes.
# The cloud-init file comes back last, after the firewall steps: its root line
# gives the daemon user a way to the agent user again.
if [ "$UNDO" = 1 ]; then
    echo "stop the daemon before --undo: a daemon that runs can start an agent process until the sudo rule is removed"
    NOT_RUN_STEPS=""
    run_every sudoers-rule:undo_sudoers_rule \
        agent-processes:undo_agent_processes
    if ! undo_firewall_allowed; then
        for step in firewall-unit firewall-ipv4 firewall-ipv6; do
            SUMMARY+=("${step}: not run: the sudo rule is still in place")
            NOT_RUN_STEPS="${NOT_RUN_STEPS:+${NOT_RUN_STEPS} }${step}"
        done
        echo "the sudo rule ${SUDOERS_FILE} is still in place, so the firewall of ${AGENT_USER} stays: remove the rule file by hand (rm -f $(shq "$SUDOERS_FILE")) and run --undo again" >&2
    elif undo_procs_left; then
        for step in firewall-unit firewall-ipv4 firewall-ipv6; do
            SUMMARY+=("${step}: not run: processes of the agent user still run")
            NOT_RUN_STEPS="${NOT_RUN_STEPS:+${NOT_RUN_STEPS} }${step}"
        done
        echo "processes of ${AGENT_USER} may still run, so the firewall of ${AGENT_USER} stays: stop them by hand (pkill -KILL -u ${AGENT_USER}, then pgrep -u ${AGENT_USER} lists none) and run --undo again" >&2
    else
        run_every firewall-unit:undo_firewall_unit \
            firewall-ipv4:undo_firewall_ipv4 \
            firewall-ipv6:undo_firewall_ipv6
    fi
    run_every cloud-init-sudoers:undo_cloud_init_sudoers
    if no_sudo_left; then
        NO_SUDO_LEFT="the daemon user ${DAEMON_USER} has no sudo rule now: restore ${CLOUD_INIT_SUDOERS} by hand"
    fi
    print_summary
    print_mode_restore
    print_manual_cleanup
    if [ -n "$NOT_RUN_STEPS" ]; then
        echo
        echo "undo: failed steps: ${FAILED_STEPS}; not run: ${NOT_RUN_STEPS} (see the summary)" >&2
        exit 1
    fi
    if [ -n "$FAILED_STEPS" ]; then
        echo
        echo "undo: failed steps: ${FAILED_STEPS} (every other step ran; see the summary)" >&2
        exit 1
    fi
    exit 0
fi

check_agent_user
check_data_dir
check_agent_root

# warn_daemon_groups — the daemon user in a group of PRIVILEGED_GROUPS: a warning
# before the steps, repeated in the summary; no failure (the script removes no
# membership, and the daemon user has root during the apply anyway). A sudo rule
# for such a group does fail the apply, at precheck_sudo_rules.
warn_daemon_groups() {
    local g found=""
    for g in $(id -nG "$DAEMON_USER"); do
        if is_privileged_group "$g"; then found="${found:+${found}, }${g}"; fi
    done
    [ -n "$found" ] || return 0
    DAEMON_GROUP_WARNING="WARNING: the daemon user ${DAEMON_USER} is a member of ${found}: each of these memberships is a path to root (this script removes none of them); a sudo rule for one of these groups stops the apply at the check of the sudo rules, before the first change"
    echo "== warning"
    echo "$DAEMON_GROUP_WARNING"
}
warn_daemon_groups

# The removal of the daemon user's general sudo needs --confirm-root-path. It is
# asked here, before the first change, and only when the cloud-init file still
# has a line to comment out: a host that is set up already needs no flag.
if [ "$DRY_RUN" = 0 ] && [ "$KEEP_DAEMON_SUDO" = 0 ] && [ "$CONFIRM_ROOT_PATH" = 0 ] \
    && [ -f "$CLOUD_INIT_SUDOERS" ] && has_active_root_line "$CLOUD_INIT_SUDOERS"; then
    die "refusing to remove the general sudo of ${DAEMON_USER}: keep another root session open (a second root shell or the console) and run again with --confirm-root-path, or pass --keep-daemon-sudo"
fi

precheck_sudo_rules || exit 1

# The check of the sudo rules above and the check of the data dir come first,
# before any change. The firewall and the boot unit come before the directories
# (a path of the agent user there cannot stop them) and before the sudoers rule
# (a failed firewall step leaves the daemon user no way to the agent user); the
# removal of the daemon user's general sudo comes after them; the proof as the
# agent user is last.
run_plan daemon-private:step_daemon_private \
    group:step_group \
    agent-user:step_agent_user \
    daemon-membership:step_daemon_membership \
    firewall-ipv4:step_firewall_ipv4 \
    firewall-ipv6:step_firewall_ipv6 \
    firewall-unit:step_firewall_unit \
    directories:step_directories \
    sudoers-rule:step_sudoers_rule \
    cloud-init-sudoers:step_cloud_init_sudoers \
    agent-proof:step_agent_proof
print_summary
[ -z "$DRY_RUN_STOP" ] || exit 1
print_config
