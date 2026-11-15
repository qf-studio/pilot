#!/bin/bash
# Real test for setup-agent-user.sh: the script runs for real (useradd, visudo,
# sudo, iptables, ip6tables, systemctl) as root on a disposable host, the runner
# of the CI job box-scripts-real (ubuntu-latest: a real iptables, a real systemd).
# It uses test names for the daemon user, the agent user and the group, a new
# data dir and its own cloud-init sudoers file, but it changes the users, the
# sudoers files and the firewall of the host it runs on: it refuses to start
# unless CI=true.
#
# Cases: the data dir check; apply (chains, jumps, unit, systemd-analyze verify);
# connections as the agent user and as the daemon user (metadata address, port
# 9091, another local port); the boot unit rebuilds a deleted chain; the agent
# user has no sudo and no access to a repository of the daemon; the sudo proof
# fails on a Defaults !authenticate line in a second file; a second apply skips
# every step; undo with go/cache a link outside the data dir, a second undo,
# then the printed cleanup; invalid arguments.
#
# Usage (CI): sudo --preserve-env=CI ./scripts/box/setup-agent-user_real_test.sh

# The single-quoted scripts run in a shell of the test user, not in this one;
# the helpers are called through check, check_not and the trap (SC2317 in
# ShellCheck 0.10, SC2329 from 0.11).
# shellcheck disable=SC2016,SC2317,SC2329
set -euo pipefail

if [ "${CI:-}" != true ]; then
    echo "setup-agent-user_real_test: refusing to start: CI is not true (this test changes the users, the sudoers files and the firewall of the host)" >&2
    exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
    echo "setup-agent-user_real_test: must run as root" >&2
    exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP="${SCRIPT_DIR}/setup-agent-user.sh"
DAEMON="pilotrt-daemon"
AGENT="pilotrt-agent"
GROUP="pilotrt"
CHAIN="PILOT_AGENT_EGRESS"
UNIT="pilot-agent-firewall.service"
UNIT_FILE="/etc/systemd/system/${UNIT}"
RUNAS_FILE="/etc/sudoers.d/90-pilot-agent-runas"
CLOUD_INIT="/etc/sudoers.d/91-pilotrt-cloud-init"
DEFAULTS_FILE="/etc/sudoers.d/92-pilotrt-defaults"
METADATA="169.254.169.254"
OTHER_PORT="18080"

for name in "$DAEMON" "$AGENT"; do
    if id -u "$name" >/dev/null 2>&1; then
        echo "setup-agent-user_real_test: user ${name} exists already; refusing to start" >&2
        exit 2
    fi
done
if getent group "$GROUP" >/dev/null 2>&1 || [ -e "$RUNAS_FILE" ] || [ -e "$UNIT_FILE" ] || iptables -w -S "$CHAIN" >/dev/null 2>&1; then
    echo "setup-agent-user_real_test: the group, the sudoers file, the unit or the chain exists already; refusing to start" >&2
    exit 2
fi

DATA_DIR="$(mktemp -d /var/lib/pilotrt-data.XXXXXX)"
OUTSIDE="$(mktemp -d /var/lib/pilotrt-outside.XXXXXX)"
WEB="$(mktemp -d)"
LOG="$(mktemp)"
LISTENERS=()
METADATA_ADDED=0

PASS=0
FAIL=0
pass() {
    echo "  PASS: $1"
    PASS=$((PASS + 1))
}
fail_test() {
    echo "  FAIL: $1"
    FAIL=$((FAIL + 1))
}

# check LABEL COMMAND... — passes when the command succeeds.
check() {
    local label="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        pass "$label"
    else
        fail_test "$label"
    fi
}

# check_not LABEL COMMAND... — passes when the command fails.
check_not() {
    local label="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        fail_test "$label"
    else
        pass "$label"
    fi
}

# as USER COMMAND... — COMMAND as USER, with USER's home as HOME.
as() {
    local user="$1" home
    shift
    home="$(getent passwd "$user" | cut -d: -f6)"
    runuser -u "$user" -- env HOME="$home" "$@"
}

# run_setup ARGS... — the script with the test names; output in $LOG.
run_setup() {
    CLOUD_INIT_SUDOERS="$CLOUD_INIT" "$SETUP" --daemon-user "$DAEMON" --agent-user "$AGENT" \
        --group "$GROUP" --data-dir "$DATA_DIR" "$@" >"$LOG" 2>&1
}

step_state() {
    { grep -E "^step $1: " "$LOG" || true; } | head -1 | sed -E "s/^step $1: //"
}

expect_step() {
    local got
    got="$(step_state "$1")"
    if [ "${got%% *}" = "$2" ]; then
        pass "step $1: $2"
    else
        fail_test "step $1: want $2, got '${got}'"
    fi
}

# show_log — the output of the last run, for a failed check.
show_log() {
    sed 's/^/    | /' "$LOG"
}

cleanup() {
    local pid
    for pid in ${LISTENERS[@]+"${LISTENERS[@]}"}; do kill "$pid" 2>/dev/null || true; done
    [ "$METADATA_ADDED" = 0 ] || ip addr del "${METADATA}/32" dev lo 2>/dev/null || true
    rm -f "$DEFAULTS_FILE"
    run_setup --undo || true
    userdel "$AGENT" 2>/dev/null || true
    userdel -r "$DAEMON" 2>/dev/null || true
    groupdel "$GROUP" 2>/dev/null || true
    rm -f "$CLOUD_INIT" "${CLOUD_INIT}".pilot-agent.bak*
    rm -rf "$DATA_DIR" "$OUTSIDE" "$WEB" "$LOG"
}
trap cleanup EXIT

# The rules the script must put in the chain, per family, as arguments for -C.
rules_of() {
    local loopback=127.0.0.1 meta="$METADATA"
    if [ "$1" = ip6tables ]; then
        loopback=::1
        meta="fd00:ec2::254"
    fi
    echo "-d ${meta} -j REJECT"
    echo "-p tcp -d ${loopback} --dport 9091 -j REJECT --reject-with tcp-reset"
    echo "-p tcp --dport 9091 -m addrtype --dst-type LOCAL -o lo -j REJECT --reject-with tcp-reset"
}

# firewall_in_place BIN — the chain with exactly the three rules, the jump first
# in OUTPUT and the only one, no temporary chain. iptables -S prints the uid.
firewall_in_place() {
    local bin="$1" rule
    "$bin" -w -C OUTPUT -m owner --uid-owner "$AGENT" -j "$CHAIN" || return 1
    [ "$("$bin" -w -S OUTPUT | grep '^-A OUTPUT' | head -1)" = "-A OUTPUT -m owner --uid-owner $(id -u "$AGENT") -j ${CHAIN}" ] || return 1
    [ "$("$bin" -w -S OUTPUT | grep -c -- "-j ${CHAIN}\$")" = 1 ] || return 1
    while IFS= read -r rule; do
        # shellcheck disable=SC2086 # the rule is a list of words on purpose
        "$bin" -w -C "$CHAIN" $rule || return 1
    done < <(rules_of "$bin")
    [ "$("$bin" -w -S "$CHAIN" | grep -c '^-A ')" = 3 ] || return 1
    ! "$bin" -w -S | grep -qE "^-N ${CHAIN}_(NEW|ALT)\$"
}

# no_firewall BIN — no chain of the script and no jump to one.
no_firewall() {
    ! "$1" -w -S | grep -q "$CHAIN"
}

# curl_ok USER URL — USER gets an answer from URL.
curl_ok() {
    as "$1" curl -sS -o /dev/null --max-time 5 "$2"
}

# check_connections LABEL — the agent user reaches neither the metadata address
# nor port 9091, the daemon user reaches both, the agent user reaches another
# local port.
check_connections() {
    check_not "$1: the agent user cannot reach the metadata address" curl_ok "$AGENT" "http://${METADATA}/"
    check_not "$1: the agent user cannot reach 127.0.0.1:9091" curl_ok "$AGENT" "http://127.0.0.1:9091/"
    check "$1: the daemon user reaches the metadata address" curl_ok "$DAEMON" "http://${METADATA}/"
    check "$1: the daemon user reaches 127.0.0.1:9091" curl_ok "$DAEMON" "http://127.0.0.1:9091/"
    check "$1: the agent user reaches another local port (127.0.0.1:${OTHER_PORT})" curl_ok "$AGENT" "http://127.0.0.1:${OTHER_PORT}/"
}

# listen ADDRESS PORT — an HTTP server as root, up before it returns.
listen() {
    local i
    python3 -m http.server --bind "$1" --directory "$WEB" "$2" >/dev/null 2>&1 &
    LISTENERS+=("$!")
    for i in $(seq 1 50); do
        if curl -sS -o /dev/null --max-time 1 "http://$1:$2/" 2>/dev/null; then
            return 0
        fi
        sleep 0.2
    done
    echo "listener on $1:$2 did not come up (${i} tries)" >&2
    return 1
}

echo "=== real test: the test refuses to start when CI is not true ==="
rc=0
CI=false "${BASH_SOURCE[0]}" >"$LOG" 2>&1 || rc=$?
if [ "$rc" = 2 ] && grep -qF "refusing to start: CI is not true" "$LOG"; then
    pass "exit 2 and the reason"
else
    show_log
    fail_test "CI=false: exit ${rc}"
fi

echo "=== real test: the host of the daemon user ==="
# A daemon user with a data dir as on a host: open to others (0755), a
# repository that git made with the default umask (0755 directories, 0644
# files), an untracked file at 0644, a remote URL in .git/config, the binary.
useradd --create-home --shell /bin/bash "$DAEMON"
chown "${DAEMON}:${DAEMON}" "$DATA_DIR"
chmod 0755 "$DATA_DIR"
as "$DAEMON" bash -c '
    set -e
    umask 022
    mkdir -p "$1/repos/alpha" "$1/bin"
    git init -q "$1/repos/alpha"
    git -C "$1/repos/alpha" remote add origin "https://x:fake-test-token@example.invalid/r.git"
    echo "TOKEN=fake-api-key" >"$1/repos/alpha/.env"
    cp /usr/bin/true "$1/bin/pilot"
' _ "$DATA_DIR"
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$DAEMON" >"$CLOUD_INIT"
chmod 0440 "$CLOUD_INIT"
printf 'metadata\n' >"${OUTSIDE}/file"
chmod 0640 "${OUTSIDE}/file"
check "visudo accepts the host sudoers" visudo -c
check "the daemon user has root without a password before the apply" as "$DAEMON" sudo -n true
check "the untracked file has mode 0644 and the daemon user reads it" test "$(stat -c %a "${DATA_DIR}/repos/alpha/.env")" = 644
USR_BIN_GROUP="$(stat -c %G /usr/bin)"
SUDO_MODE="$(stat -c %a "$(command -v sudo)")"

echo "=== real test: the data dir check — open entries fail the apply, nothing changed ==="
rc=0
run_setup --confirm-root-path || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
expect_step daemon-private failed
check_not "no group was created" getent group "$GROUP"
check_not "no agent user was created" id -u "$AGENT"
for entry in repos bin; do
    check "the line for ${entry} with its chmod" grep -qxF "the agent user can reach ${DATA_DIR}/${entry} (group ${DAEMON}, mode 755): chmod o-rwx ${DATA_DIR}/${entry}" "$LOG"
done
# The operator's fix, as printed.
while IFS= read -r fix; do
    # shellcheck disable=SC2086 # "chmod o-rwx PATH" as printed, a list of words
    $fix
done < <(sed -nE 's/^the agent user can reach .*: (chmod [go]+-rwx .*)$/\1/p' "$LOG")

echo "=== real test: apply ==="
if run_setup --confirm-root-path; then
    pass "apply exits 0"
else
    show_log
    fail_test "apply exited non-zero"
fi
for s in group agent-user daemon-membership directories sudoers-rule firewall-ipv4 firewall-ipv6 firewall-unit cloud-init-sudoers; do
    expect_step "$s" "done"
done
check "the agent user exists with a no-login shell" bash -c "getent passwd ${AGENT} | grep -q 'nologin\$'"
check "the daemon user is in the group" bash -c "id -nG ${DAEMON} | tr ' ' '\n' | grep -qx ${GROUP}"
check "the workspace root is ${AGENT}:${GROUP} 2770" test "$(stat -c %U:%G:%a "${DATA_DIR}/agent-workspaces")" = "${AGENT}:${GROUP}:2770"
check "the cloud-init line is commented out" grep -qxF "# disabled by setup-agent-user: ${DAEMON} ALL=(ALL) NOPASSWD:ALL" "$CLOUD_INIT"
check "visudo accepts the sudoers files" visudo -c
for bin in iptables ip6tables; do
    check "${bin}: the chain with its three rules, the jump first in OUTPUT (-C)" firewall_in_place "$bin"
done
IPT_CHAIN="$(iptables -w -S "$CHAIN" 2>&1 || true)"
if printf '%s\n' "$IPT_CHAIN" | grep -qE -- "^-A ${CHAIN} -d 169\.254\.169\.254(/32)? -j REJECT --reject-with icmp-port-unreachable\$"; then
    pass "iptables -S: the metadata address is rejected"
else
    printf '%s\n' "$IPT_CHAIN"
    fail_test "iptables -S: no reject rule for the metadata address"
fi
if printf '%s\n' "$IPT_CHAIN" | grep -qE -- "^-A ${CHAIN} -d 127\.0\.0\.1(/32)? -p tcp -m tcp --dport 9091 -j REJECT --reject-with tcp-reset\$"; then
    pass "iptables -S: tcp to 127.0.0.1:9091 is reset"
else
    printf '%s\n' "$IPT_CHAIN"
    fail_test "iptables -S: no reject rule for 127.0.0.1:9091"
fi
if printf '%s\n' "$IPT_CHAIN" | grep -qE -- "^-A ${CHAIN} -o lo -p tcp -m tcp --dport 9091 -m addrtype --dst-type LOCAL -j REJECT --reject-with tcp-reset\$"; then
    pass "iptables -S: tcp to port 9091 on lo for a local address is reset"
else
    printf '%s\n' "$IPT_CHAIN"
    fail_test "iptables -S: no reject rule for port 9091 on lo"
fi
check "the unit is enabled" systemctl is-enabled --quiet "$UNIT"
if systemd-analyze verify "$UNIT_FILE" >"$LOG" 2>&1; then
    pass "systemd-analyze verify accepts the unit file"
else
    show_log
    fail_test "systemd-analyze verify rejects the unit file"
fi

echo "=== real test: connections ==="
ip addr add "${METADATA}/32" dev lo
METADATA_ADDED=1
listen "$METADATA" 80
listen 127.0.0.1 9091
listen 127.0.0.1 "$OTHER_PORT"
check_connections "after the apply"

echo "=== real test: the boot unit puts back a deleted chain ==="
for bin in iptables ip6tables; do
    "$bin" -w -D OUTPUT -m owner --uid-owner "$AGENT" -j "$CHAIN"
    "$bin" -w -F "$CHAIN"
    "$bin" -w -X "$CHAIN"
    check "${bin}: the jump and the chain are gone" no_firewall "$bin"
done
check "the agent user reaches port 9091 without the chain (the test is not vacuous)" curl_ok "$AGENT" "http://127.0.0.1:9091/"
if systemctl start "$UNIT"; then
    pass "systemctl start ${UNIT}"
else
    journalctl -u "$UNIT" --no-pager | tail -20 || true
    fail_test "the unit failed to start"
fi
check "the unit is active" systemctl is-active --quiet "$UNIT"
for bin in iptables ip6tables; do
    check "${bin}: the chain and the first jump are back" firewall_in_place "$bin"
done
check_connections "after the boot unit"

echo "=== real test: the agent user ==="
check_not "the agent user: sudo -n true fails" as "$AGENT" sudo -n true
check_not "the agent user cannot read a 0644 file in the working tree of the daemon" as "$AGENT" cat "${DATA_DIR}/repos/alpha/.env"
check_not "the agent user cannot read .git/config of that repository" as "$AGENT" cat "${DATA_DIR}/repos/alpha/.git/config"
check_not "the agent user cannot list the repository directory" as "$AGENT" ls "${DATA_DIR}/repos/alpha"
check_not "the agent user cannot run the binary of the daemon" as "$AGENT" "${DATA_DIR}/bin/pilot"
check "the agent user writes in its workspace root" as "$AGENT" touch "${DATA_DIR}/agent-workspaces/probe"
check "the daemon user still reads its own file" as "$DAEMON" cat "${DATA_DIR}/repos/alpha/.env"
if [ "$(as "$DAEMON" sudo -n -u "$AGENT" id -un 2>/dev/null)" = "$AGENT" ]; then
    pass "the daemon user runs a command as the agent user through the installed rule"
else
    fail_test "the daemon user cannot run a command as the agent user"
fi
check_not "the daemon user: sudo -n true fails (no root left)" as "$DAEMON" sudo -n true

echo "=== real test: the sudo proof — a Defaults line in a second file ==="
printf 'Defaults:%s !authenticate\n%s ALL=(ALL) ALL\n' "$DAEMON" "$DAEMON" >"$DEFAULTS_FILE"
chmod 0440 "$DEFAULTS_FILE"
check "visudo accepts the second file" visudo -c
check "with it the daemon user gets root without a password" as "$DAEMON" sudo -n true
rc=0
run_setup || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
expect_step cloud-init-sudoers failed
check "the proof says sudo -n true succeeded" grep -qF "${DAEMON} still runs a command as root without a password: sudo -n true succeeded" "$LOG"
check "the second file is named" grep -qF "  ${DEFAULTS_FILE}:1: Defaults:${DAEMON} !authenticate" "$LOG"
rm -f "$DEFAULTS_FILE"

echo "=== real test: second apply (no --confirm-root-path: nothing is left to remove) ==="
if run_setup; then
    pass "second apply exits 0"
else
    show_log
    fail_test "second apply exited non-zero"
fi
skipped="$(grep -c '^step .*: skipped' "$LOG" || true)"
if [ "$skipped" = 10 ]; then
    pass "second apply skips all 10 steps"
else
    show_log
    fail_test "second apply: ${skipped} of 10 steps skipped"
fi

echo "=== real test: undo with go/cache a link to a directory outside the data dir ==="
as "$AGENT" bash -c 'rmdir "$1/agent-home/go/cache" && ln -s "$2" "$1/agent-home/go/cache"' _ "$DATA_DIR" "$OUTSIDE"
check "go/cache is a link outside the data dir" test -L "${DATA_DIR}/agent-home/go/cache"
OUTSIDE_BEFORE="$(find "$OUTSIDE" -printf '%p %m %U:%G %s\n' | sort)"
if run_setup --undo; then
    pass "undo exits 0"
else
    show_log
    fail_test "undo exited non-zero"
fi
check "the sudoers file of the daemon user is restored (active root line)" grep -qxF "${DAEMON} ALL=(ALL) NOPASSWD:ALL" "$CLOUD_INIT"
check "the daemon user has root without a password again" as "$DAEMON" sudo -n true
check "nothing outside the data dir changed" test "$OUTSIDE_BEFORE" = "$(find "$OUTSIDE" -printf '%p %m %U:%G %s\n' | sort)"
check "visudo accepts the sudoers files" visudo -c
if run_setup --undo; then
    pass "a second undo exits 0"
else
    show_log
    fail_test "a second undo exited non-zero"
fi
for s in cloud-init-sudoers firewall-unit firewall-ipv4 firewall-ipv6 sudoers-rule; do
    expect_step "$s" skipped
done
for bin in iptables ip6tables; do
    check "${bin}: no chain and no jump" no_firewall "$bin"
done
check_not "no unit file" test -e "$UNIT_FILE"
check_not "the unit is not enabled" systemctl is-enabled --quiet "$UNIT"
check_not "the unit is not active" systemctl is-active --quiet "$UNIT"
check_not "no sudoers file of the script" test -e "$RUNAS_FILE"
# The user, the group and the directories stay after undo; the cleanup it
# prints removes them.
# A process that pkill signalled can take a moment to exit, so each command
# gets ten tries; pkill itself exits 1 when no process is left.
while IFS= read -r cmd; do
    for try in $(seq 1 10); do
        # shellcheck disable=SC2086 # the printed command, a list of words
        if out="$($cmd 2>&1)"; then
            break
        fi
        case "$cmd" in pkill\ *) break ;; esac
        if [ "$try" = 10 ]; then
            echo "    printed cleanup failed: ${cmd}: ${out}"
            ps -o user,pid,args -u "$AGENT" 2>/dev/null || true
        fi
        sleep 1
    done
done < <(sed -n '/^== left in place/,$ s/^  //p' "$LOG")
check_not "after the printed cleanup: no agent user" id -u "$AGENT"
check_not "after the printed cleanup: no group" getent group "$GROUP"
check_not "after the printed cleanup: no agent home" test -e "${DATA_DIR}/agent-home"
check "nothing outside the data dir changed by the cleanup" test "$OUTSIDE_BEFORE" = "$(find "$OUTSIDE" -printf '%p %m %U:%G %s\n' | sort)"

echo "=== real test: invalid arguments change nothing ==="
for args in "--data-dir /usr" "--group wheel" "--group docker" "--agent-user root"; do
    rc=0
    # shellcheck disable=SC2086 # the arguments are a word list on purpose
    CLOUD_INIT_SUDOERS="$CLOUD_INIT" "$SETUP" --daemon-user "$DAEMON" --data-dir "$DATA_DIR" $args --confirm-root-path >"$LOG" 2>&1 || rc=$?
    if [ "$rc" = 2 ]; then
        pass "refused with exit 2: ${args}"
    else
        show_log
        fail_test "${args}: exit ${rc}, want 2"
    fi
done
check_not "no group was created by a refused run" getent group "$GROUP"
if [ "$(stat -c %G /usr/bin)" = "$USR_BIN_GROUP" ] && [ "$(stat -c %a "$(command -v sudo)")" = "$SUDO_MODE" ]; then
    pass "/usr/bin keeps its group and sudo keeps its setuid bit"
else
    fail_test "/usr/bin or sudo changed"
fi

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
