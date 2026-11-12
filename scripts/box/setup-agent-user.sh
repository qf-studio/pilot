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
#   shared-read       group read + traverse on DATA_DIR/repos and DATA_DIR/bin
#   sudoers-rule      daemon user may run any command as the agent user
#                     (NOPASSWD, SETENV); validated BEFORE it is moved into place
#   cloud-init-sudoers  comment out the lines that name the daemon user in the
#                     cloud-init sudoers file (backup next to it, validated)
#   firewall-<family> dedicated chain, owner match on the agent user: reject the
#                     metadata address and TCP to 127.0.0.1:9091
#   firewall-unit     oneshot systemd unit that applies the chain at boot
#
# Usage: setup-agent-user.sh [--daemon-user U] [--agent-user U] [--group G]
#                            [--data-dir D] [--dry-run] [--undo]
#                            [--keep-daemon-sudo]
#
#   --dry-run            print every command, change nothing
#   --undo               remove the firewall chain and unit and the sudoers rule,
#                        restore the cloud-init file from its backup; the user,
#                        the group and the directories stay (the commands to
#                        delete them are printed, not run)
#   --keep-daemon-sudo   leave the cloud-init sudoers file alone
#
# Paths (env, real paths are the defaults; the test points them at a temp root):
#   SUDOERS_DIR          default /etc/sudoers.d
#   SYSTEMD_DIR          default /etc/systemd/system
#   CLOUD_INIT_SUDOERS   default $SUDOERS_DIR/90-cloud-init-users
#   NOLOGIN_SHELL        default /usr/sbin/nologin
#
# Exit status: 0 when every step is done or skipped, 1 when a step failed (the
# run stops at the first failed step), 2 on bad arguments.

set -euo pipefail

DAEMON_USER="ec2-user"
AGENT_USER="pilot-agent"
GROUP="pilot"
DATA_DIR="/var/lib/pilot"
DRY_RUN=0
UNDO=0
KEEP_DAEMON_SUDO=0

SUDOERS_DIR="${SUDOERS_DIR:-/etc/sudoers.d}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
CLOUD_INIT_SUDOERS="${CLOUD_INIT_SUDOERS:-${SUDOERS_DIR}/90-cloud-init-users}"
NOLOGIN_SHELL="${NOLOGIN_SHELL:-/usr/sbin/nologin}"

SUDOERS_NAME="90-pilot-agent-runas"
UNIT_NAME="pilot-agent-firewall.service"
CHAIN="PILOT_AGENT_EGRESS"
METADATA_V4="169.254.169.254"
METADATA_V6="fd00:ec2::254"
GATEWAY_PORT="9091"

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
        -h | --help) usage; exit 0 ;;
        *) usage; die "unknown argument: $1" ;;
    esac
done

NAME_RE='^[a-z_][a-z0-9_-]*$'
for pair in "daemon-user:$DAEMON_USER" "agent-user:$AGENT_USER" "group:$GROUP"; do
    [[ "${pair#*:}" =~ $NAME_RE ]] || die "--${pair%%:*} must match ${NAME_RE}: '${pair#*:}'"
done
[[ "$DATA_DIR" =~ ^/[A-Za-z0-9._/-]+$ ]] || die "--data-dir must be an absolute path of [A-Za-z0-9._/-]: '${DATA_DIR}'"
DATA_DIR="${DATA_DIR%/}"
[ "$DAEMON_USER" != "$AGENT_USER" ] || die "the agent user must differ from the daemon user"

AGENT_HOME="${DATA_DIR}/agent-home"
WORKSPACES="${DATA_DIR}/agent-workspaces"
SUDOERS_FILE="${SUDOERS_DIR}/${SUDOERS_NAME}"
SUDOERS_TMP="${SUDOERS_DIR}/.${SUDOERS_NAME}.new"
CLOUD_INIT_BACKUP="${CLOUD_INIT_SUDOERS}.pilot-agent.bak"
UNIT_FILE="${SYSTEMD_DIR}/${UNIT_NAME}"

if [ "$(id -u)" -ne 0 ]; then
    die "must run as root"
fi

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
    if ! id -u "$DAEMON_USER" >/dev/null 2>&1; then
        echo "daemon user '${DAEMON_USER}' does not exist" >&2
        return 1
    fi
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

step_shared_read() {
    local path cur gbits changed=0 present=0
    for path in "${DATA_DIR}/repos" "${DATA_DIR}/bin"; do
        if [ ! -d "$path" ]; then
            echo "absent, left alone: ${path}"
            continue
        fi
        present=1
        cur="$(stat -c '%G:%a' "$path")"
        gbits=$(((8#${cur#*:} >> 3) & 7))
        # r and x set, w clear for the group, and the group is the shared one.
        if [ "${cur%%:*}" = "$GROUP" ] && [ $((gbits & 5)) -eq 5 ] && [ $((gbits & 2)) -eq 0 ]; then
            continue
        fi
        # ":GROUP" changes the group only; the owner stays.
        run chown -R ":${GROUP}" "$path" || return 1
        run chmod -R g+rX,g-w "$path" || return 1
        changed=1
    done
    if [ "$present" = 0 ]; then
        NOTE="neither repos nor bin exists under ${DATA_DIR}"
    fi
    [ "$changed" = 1 ] || RESULT=skipped
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
    rm -f "$SUDOERS_TMP"
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

step_cloud_init_sudoers() {
    local re
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
    re="$(daemon_line_re)"
    if ! grep -v '^[[:space:]]*#' "$CLOUD_INIT_SUDOERS" | grep -Eq "$re"; then
        RESULT=skipped
        return 0
    fi
    # Never drop the old rule unless the new one is in place.
    if [ "$DRY_RUN" = 0 ] && ! file_has_content "$SUDOERS_FILE" "$(sudoers_rule)"; then
        echo "refusing to edit ${CLOUD_INIT_SUDOERS}: the agent rule is not installed" >&2
        return 1
    fi
    if [ ! -e "$CLOUD_INIT_BACKUP" ]; then
        run install -m 0600 -o root -g root "$CLOUD_INIT_SUDOERS" "$CLOUD_INIT_BACKUP" || return 1
    fi
    run sed -i -E "/^[[:space:]]*#/! { /${re}/ s/^/# disabled by setup-agent-user: / }" \
        "$CLOUD_INIT_SUDOERS" || return 1
    if ! run visudo -c; then
        echo "sudoers validation failed; restoring ${CLOUD_INIT_SUDOERS}" >&2
        run install -m 0440 -o root -g root "$CLOUD_INIT_BACKUP" "$CLOUD_INIT_SUDOERS" || true
        return 1
    fi
}

# firewall_rules FAMILY — one rule per line, as arguments for the chain.
firewall_rules() {
    if [ "$1" = ip6tables ]; then
        echo "-d ${METADATA_V6} -j REJECT"
    else
        echo "-d ${METADATA_V4} -j REJECT"
        echo "-p tcp -d 127.0.0.1 --dport ${GATEWAY_PORT} -j REJECT --reject-with tcp-reset"
    fi
}

jump_rule() {
    echo "-m owner --uid-owner ${AGENT_USER} -j ${CHAIN}"
}

firewall_family() {
    local bin="$1" rule changed=0
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "${bin} not found" >&2
        return 1
    fi
    if ! "$bin" -w -n -L "$CHAIN" >/dev/null 2>&1; then
        run "$bin" -w -N "$CHAIN" || return 1
        changed=1
    fi
    while IFS= read -r rule; do
        # shellcheck disable=SC2086 # the rule is a list of words on purpose
        if ! "$bin" -w -C "$CHAIN" $rule >/dev/null 2>&1; then
            # shellcheck disable=SC2086
            run "$bin" -w -A "$CHAIN" $rule || return 1
            changed=1
        fi
    done < <(firewall_rules "$bin")
    # The jump comes last: the chain is complete before it takes effect.
    # shellcheck disable=SC2046,SC2086
    if ! "$bin" -w -C OUTPUT $(jump_rule) >/dev/null 2>&1; then
        # shellcheck disable=SC2046,SC2086
        run "$bin" -w -I OUTPUT $(jump_rule) || return 1
        changed=1
    fi
    [ "$changed" = 1 ] || RESULT=skipped
    return 0
}

step_firewall_ipv4() { firewall_family iptables; }
step_firewall_ipv6() { firewall_family ip6tables; }

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
    for family in iptables ip6tables; do
        bin="$(command -v "$family")"
        echo "ExecStart=-${bin} -w -N ${CHAIN}"
        echo "ExecStart=${bin} -w -F ${CHAIN}"
        while IFS= read -r rule; do
            echo "ExecStart=${bin} -w -A ${CHAIN} ${rule}"
        done < <(firewall_rules "$family")
        echo "ExecStart=-${bin} -w -D OUTPUT $(jump_rule)"
        echo "ExecStart=${bin} -w -I OUTPUT $(jump_rule)"
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
}

# ---------------------------------------------------------------- undo steps

undo_cloud_init_sudoers() {
    if [ ! -f "$CLOUD_INIT_BACKUP" ]; then
        RESULT=skipped
        NOTE="no backup at ${CLOUD_INIT_BACKUP}"
        return 0
    fi
    run install -m 0440 -o root -g root "$CLOUD_INIT_BACKUP" "$CLOUD_INIT_SUDOERS" || return 1
    run rm -f "$CLOUD_INIT_BACKUP" || return 1
}

undo_firewall_family() {
    local bin="$1" changed=0
    if ! command -v "$bin" >/dev/null 2>&1; then
        RESULT=skipped
        NOTE="${bin} not found"
        return 0
    fi
    # shellcheck disable=SC2046,SC2086
    while "$bin" -w -C OUTPUT $(jump_rule) >/dev/null 2>&1; do
        # shellcheck disable=SC2046,SC2086
        run "$bin" -w -D OUTPUT $(jump_rule) || return 1
        changed=1
        # A dry run deletes nothing, so the check above would never end.
        [ "$DRY_RUN" = 0 ] || break
    done
    if "$bin" -w -n -L "$CHAIN" >/dev/null 2>&1; then
        run "$bin" -w -F "$CHAIN" || return 1
        run "$bin" -w -X "$CHAIN" || return 1
        changed=1
    fi
    [ "$changed" = 1 ] || RESULT=skipped
    return 0
}

undo_firewall_ipv4() { undo_firewall_family iptables; }
undo_firewall_ipv6() { undo_firewall_family ip6tables; }

undo_firewall_unit() {
    if [ ! -e "$UNIT_FILE" ] && ! systemctl is-enabled --quiet "$UNIT_NAME" >/dev/null 2>&1; then
        RESULT=skipped
        return 0
    fi
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
    if [ "${state%% *}" = failed ]; then
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
    run_step cloud-init-sudoers undo_cloud_init_sudoers
    run_step firewall-ipv4 undo_firewall_ipv4
    run_step firewall-ipv6 undo_firewall_ipv6
    run_step firewall-unit undo_firewall_unit
    run_step sudoers-rule undo_sudoers_rule
    print_summary
    print_manual_cleanup
    exit 0
fi

run_step group step_group
run_step agent-user step_agent_user
run_step daemon-membership step_daemon_membership
run_step directories step_directories
run_step shared-read step_shared_read
run_step sudoers-rule step_sudoers_rule
run_step cloud-init-sudoers step_cloud_init_sudoers
run_step firewall-ipv4 step_firewall_ipv4
run_step firewall-ipv6 step_firewall_ipv6
run_step firewall-unit step_firewall_unit
print_summary
