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
#   daemon-private    a check, changes nothing: no entry of the data dir but
#                     agent-home and agent-workspaces (a link is followed) is open
#                     to the agent user, through the "other" class or through the
#                     shared group. The agent user gets no access to a repository,
#                     the config or the binary of the daemon (its workspace gets
#                     the base commit as a bundle from the daemon). The step prints
#                     a chmod for each entry that is open and fails.
#   group             system group (default: pilot)
#   agent-user        system user, no login shell, home DATA_DIR/agent-home
#   daemon-membership daemon user joins the group
#   directories       agent-home, agent-workspaces, go/cache, go/mod
#   sudoers-rule      daemon user may run any command as the agent user
#                     (NOPASSWD, SETENV); validated BEFORE it is moved into place
#   firewall-<family> dedicated chain, owner match on the agent user: reject the
#                     metadata address and TCP to the gateway port on loopback; the
#                     chain is rebuilt under a temporary name and switched in, so
#                     the reject rules are never missing; a temporary chain that a
#                     failed run left is removed. IPv6 is skipped when ip6tables is
#                     missing.
#   firewall-unit     oneshot systemd unit that applies the chain at boot
#   cloud-init-sudoers  LAST: comment out the lines that give the daemon user (and
#                     nobody else) sudo to root in the cloud-init sudoers file
#                     (numbered backups, validated), then prove by behaviour that
#                     the daemon user has no root left: `sudo -n true` as the
#                     daemon user must fail (and `sudo -n -u AGENT true` succeed).
#                     Needs --confirm-root-path.
#
# Usage: setup-agent-user.sh [--daemon-user U] [--agent-user U] [--group G]
#                            [--data-dir D] [--dry-run] [--undo]
#                            [--keep-daemon-sudo] [--confirm-root-path]
#
#   --confirm-root-path  the operator holds another root session; required when
#                        the cloud-init file still has a line to comment out (not
#                        for --dry-run, --undo, --keep-daemon-sudo or a host that is
#                        set up already); asked before the first change
#   --dry-run            print every command, change nothing
#   --undo               restore the cloud-init file from the newest backup in
#                        which the root line of the daemon user is active, stop and
#                        disable the boot unit, remove the firewall chain and the
#                        sudoers rule; the user, the group and the directories stay
#                        (the commands to delete them are printed, not run). Undo
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

# Directories the data dir must never be, nor contain (Linux and the macOS
# aliases of the test host).
SYSTEM_DIRS="/ /bin /boot /dev /etc /home /lib /lib32 /lib64 /libx32 /media /mnt /opt /proc /root /run /sbin /srv /sys /tmp /usr /usr/bin /usr/lib /usr/lib64 /usr/libexec /usr/local /usr/local/bin /usr/sbin /usr/share /var /var/cache /var/lib /var/log /var/mail /var/run /var/spool /var/tmp /private /private/etc /private/tmp /private/var /System /Library /Applications /Users"

# System groups with privileges: a member is root, or has a short way to it.
PRIVILEGED_GROUPS="root wheel sudo adm disk docker shadow"

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
        --daemon-user | --agent-user | --group | --data-dir)
            [ $# -ge 2 ] || die "$1 needs a value"
            case "$1" in
                --daemon-user) DAEMON_USER="$2" ;;
                --agent-user) AGENT_USER="$2" ;;
                --group) GROUP="$2" ;;
                --data-dir) DATA_DIR="$2" ;;
            esac
            shift 2
            ;;
        --daemon-user=*) DAEMON_USER="${1#*=}"; shift ;;
        --agent-user=*) AGENT_USER="${1#*=}"; shift ;;
        --group=*) GROUP="${1#*=}"; shift ;;
        --data-dir=*) DATA_DIR="${1#*=}"; shift ;;
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
[ "$DAEMON_USER" != root ] && [ "$DAEMON_UID" -ge 1000 ] || die "the daemon user must not be root or have a uid below 1000 (${DAEMON_USER} has ${DAEMON_UID})"
[ "$DAEMON_USER" != "$AGENT_USER" ] || die "the agent user must differ from the daemon user"
[ "$AGENT_USER" != root ] || die "the agent user must not be root"
! is_privileged_group "$AGENT_USER" || die "--agent-user must not be ${AGENT_USER}: that is the name of a system group with privileges (${PRIVILEGED_GROUPS})"
! is_privileged_group "$GROUP" || die "--group must not be ${GROUP}: a system group with privileges (${PRIVILEGED_GROUPS})"
if getent group "$GROUP" >/dev/null 2>&1; then
    [ "$(getent group "$GROUP" | cut -d: -f3)" != 0 ] || die "--group must not be ${GROUP}: it has gid 0, the gid of root"
fi

DAEMON_PRIMARY_GROUP="$(id -gn "$DAEMON_USER")"
[ "$GROUP" != "$DAEMON_PRIMARY_GROUP" ] || die "--group must not be the primary group of the daemon user (${DAEMON_PRIMARY_GROUP})"

# refuse_system_dir PATH — PATH must not be a system directory nor an ancestor
# of one. Checked on the value as given and, for apply, on its canonical form.
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

AGENT_HOME="${DATA_DIR}/agent-home"
WORKSPACES="${DATA_DIR}/agent-workspaces"
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
    refuse_system_dir "$DATA_DIR"
    [ -d "$DATA_DIR" ] || die "--data-dir is not a directory: '${DATA_DIR}'"
    [ "$(stat -c %U "$DATA_DIR")" = "$DAEMON_USER" ] || die "--data-dir '${DATA_DIR}' is not owned by the daemon user (${DAEMON_USER})"
    AGENT_HOME="${DATA_DIR}/agent-home"
    WORKSPACES="${DATA_DIR}/agent-workspaces"
}

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

# check_managed_dirs — the directories the script creates must be real
# directories inside the data dir. The agent user owns some of them, so this
# runs for apply only.
check_managed_dirs() {
    local managed
    for managed in "$AGENT_HOME" "$WORKSPACES" "${AGENT_HOME}/go" "${AGENT_HOME}/go/cache" "${AGENT_HOME}/go/mod"; do
        [ ! -L "$managed" ] || die "'${managed}' is a symbolic link; refused"
        resolve_inside "the directory" "$managed" d
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

# ensure_dir PATH OWNER GROUP MODE — sets CHANGED=1 when it had to act. A path
# that became a symbolic link after the checks is refused, not followed.
CHANGED=0
ensure_dir() {
    local path="$1" owner="$2" group="$3" mode="$4" cur
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

# step_daemon_private — every entry of the data dir but the two of the agent
# user is closed to it: no bit for "other" and, when its group is the shared
# group, no bit for the group. A link is followed (the agent user follows it
# too). The top level is enough: a closed directory closes all below it.
step_daemon_private() {
    local e meta grp mode fix bad=0
    for e in "$DATA_DIR"/* "$DATA_DIR"/.[!.]* "$DATA_DIR"/..?*; do
        case "$e" in "$AGENT_HOME" | "$WORKSPACES") continue ;; esac
        # an unmatched glob, or a dangling link: nothing to reach
        [ -e "$e" ] || continue
        meta="$(stat -L -c '%G:%a' "$e")" || return 1
        grp="${meta%%:*}"
        mode=$((8#${meta#*:}))
        fix=""
        if [ "$grp" = "$GROUP" ] && [ $(((mode >> 3) & 7)) -ne 0 ]; then
            fix="go-rwx"
        elif [ $((mode & 7)) -ne 0 ]; then
            fix="o-rwx"
        fi
        [ -n "$fix" ] || continue
        printf 'the agent user can reach %s (group %s, mode %s): chmod %s %q\n' \
            "$e" "$grp" "${meta#*:}" "$fix" "$(realpath "$e")" >&2
        bad=1
    done
    if [ "$bad" = 1 ]; then
        echo "nothing was changed; close each path above and run again" >&2
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

# prove_sudo_removed — by behaviour, whatever the text of the rules is: as the
# daemon user, `sudo -n -u AGENT true` must succeed (the same path reaches sudo
# and the agent rule works, so a failure below is not a broken sudo) and
# `sudo -n true` must fail (no command as root without a password).
prove_sudo_removed() {
    local out rc=0 where
    if [ "$DRY_RUN" = 1 ]; then
        printf '+ runuser -u %q -- sudo -n true  (after the edit: must fail; sudo -n -u %q true must succeed)\n' \
            "$DAEMON_USER" "$AGENT_USER"
        return 0
    fi
    out="$(runuser -u "$DAEMON_USER" -- sudo -n -u "$AGENT_USER" true 2>&1)" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "as ${DAEMON_USER}, sudo -n -u ${AGENT_USER} true failed (exit ${rc}); the sudo removal is not proven:" >&2
        printf '%s\n' "$out" | sed 's/^/  /' >&2
        return 1
    fi
    if ! out="$(runuser -u "$DAEMON_USER" -- sudo -n true 2>&1)"; then
        return 0
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
    echo "The user, the group, the group membership and the directories stay. To remove them"
    echo "(pkill first: a sudo of the daemon user to the agent user leaves a systemd --user"
    echo "of the agent user behind for a while, and userdel refuses a user with a process):"
    printf '  pkill -u %q\n' "$AGENT_USER"
    printf '  gpasswd -d %q %q\n' "$DAEMON_USER" "$GROUP"
    printf '  userdel %q\n' "$AGENT_USER"
    printf '  groupdel %q\n' "$GROUP"
    printf '  rm -rf %q %q\n' "$AGENT_HOME" "$WORKSPACES"
}

if [ "$DRY_RUN" = 1 ]; then
    echo "setup-agent-user: dry run, nothing is changed"
fi

# Undo first: nothing above reads a path that the agent user or the daemon user
# can write, so neither of them can stop the restore of root's files.
if [ "$UNDO" = 1 ]; then
    run_plan cloud-init-sudoers:undo_cloud_init_sudoers \
        firewall-unit:undo_firewall_unit \
        firewall-ipv4:undo_firewall_ipv4 \
        firewall-ipv6:undo_firewall_ipv6 \
        sudoers-rule:undo_sudoers_rule
    print_summary
    print_manual_cleanup
    exit 0
fi

check_agent_user
check_data_dir
check_managed_dirs

# The removal of the daemon user's general sudo needs --confirm-root-path. It is
# asked here, before the first change, and only when the cloud-init file still
# has a line to comment out: a host that is set up already needs no flag.
if [ "$DRY_RUN" = 0 ] && [ "$KEEP_DAEMON_SUDO" = 0 ] && [ "$CONFIRM_ROOT_PATH" = 0 ] \
    && [ -f "$CLOUD_INIT_SUDOERS" ] && has_active_root_line "$CLOUD_INIT_SUDOERS"; then
    die "refusing to remove the general sudo of ${DAEMON_USER}: keep another root session open (a second root shell or the console) and run again with --confirm-root-path, or pass --keep-daemon-sudo"
fi

# The check of the data dir comes first, before any change; the removal of the
# daemon user's general sudo is the last step, after the firewall and the boot unit.
run_plan daemon-private:step_daemon_private \
    group:step_group \
    agent-user:step_agent_user \
    daemon-membership:step_daemon_membership \
    directories:step_directories \
    sudoers-rule:step_sudoers_rule \
    firewall-ipv4:step_firewall_ipv4 \
    firewall-ipv6:step_firewall_ipv6 \
    firewall-unit:step_firewall_unit \
    cloud-init-sudoers:step_cloud_init_sudoers
print_summary
