#!/bin/bash
# Real test for setup-agent-user.sh: the script runs for real (useradd, visudo,
# sudo, iptables, ip6tables, systemctl) as root on a disposable host, the runner
# of the CI job box-scripts-real (ubuntu-24.04: a real iptables, a real systemd,
# the classic sudo).
# It uses test names for the daemon user, the agent user and the group, a new
# data dir and its own cloud-init sudoers file, but it changes the users, the
# sudoers files and the firewall of the host it runs on: it refuses to start
# unless CI=true.
#
# Cases: the data dir check (the data dir itself and its entries, a process of
# another user with a file open below it, the record of the modes, a link of
# the daemon user whose target path holds a newline and a forged line); apply
# (agent root, chains, jumps, unit, systemd-analyze verify, the config lines);
# connections as the agent user and as the daemon user (metadata address, port
# 9091 on 127.0.0.1 and on ::1, another local port); the boot unit rebuilds a deleted chain; the agent
# user has no sudo and no access to a repository of the daemon, also to a file
# the daemon user makes with umask 022 after the apply; 20 applies while the
# agent user swaps a directory of its home for a link to a directory of root;
# a data dir below a 0700 directory fails at the proof of the agent user; the
# sudo proof fails on a Defaults !authenticate line in a second file; the rule
# list of the sudo proof, in a second sudoers file: a NOPASSWD rule for one
# command as root fails, a rule through a group of the daemon user fails (and,
# with the cloud-init line active again, stops the apply at the check before the
# first change: the cloud-init file stays the same byte for byte), only the
# agent user passes; a second apply skips every step; undo with go/cache a link outside the data dir (it
# prints the recorded modes; an entry of the daemon user at 0777 before the
# apply, replaced by a link to a file of root before the undo: the printed undo
# line, run as root, leaves the file of root as it is; a process of the agent
# user is stopped), a second undo; with ip6tables hidden from PATH the apply
# fails and no sudoers rule file exists; a full apply, then an apply with
# ip6tables hidden: the sudoers rule file of the full apply is removed; then the
# printed cleanup; invalid arguments.
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
# The box has the classic sudo (sudo.ws). sudo-rs (the default of Ubuntu 26.04)
# rejects settings the cases below need (Defaults !authenticate), so a run
# against it proves nothing about the box: stop instead of a partial result.
SUDO_VERSION="$(sudo --version 2>&1 | head -n 1)"
echo "sudo: ${SUDO_VERSION}"
case "$SUDO_VERSION" in
    *sudo-rs*)
        echo "setup-agent-user_real_test: refusing to start: sudo is sudo-rs; run on a host with the classic sudo (the CI job pins ubuntu-24.04)" >&2
        exit 2
        ;;
esac

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
RULES_FILE="/etc/sudoers.d/93-pilotrt-rules"
OPS_GROUP="pilotrt-ops"
METADATA="169.254.169.254"
OTHER_PORT="18080"

for name in "$DAEMON" "$AGENT"; do
    if id -u "$name" >/dev/null 2>&1; then
        echo "setup-agent-user_real_test: user ${name} exists already; refusing to start" >&2
        exit 2
    fi
done
if getent group "$GROUP" >/dev/null 2>&1 || getent group "$OPS_GROUP" >/dev/null 2>&1 || [ -e "$RUNAS_FILE" ] || [ -e "$UNIT_FILE" ] || iptables -w -S "$CHAIN" >/dev/null 2>&1; then
    echo "setup-agent-user_real_test: the group, the sudoers file, the unit or the chain exists already; refusing to start" >&2
    exit 2
fi

DATA_DIR="$(mktemp -d /var/lib/pilotrt-data.XXXXXX)"
OUTSIDE="$(mktemp -d /var/lib/pilotrt-outside.XXXXXX)"
ROOTDIR="$(mktemp -d /var/lib/pilotrt-rootdir.XXXXXX)"
CLOSED="$(mktemp -d /var/lib/pilotrt-closed.XXXXXX)"
WEB="$(mktemp -d)"
LOG="$(mktemp)"
STOP="$(mktemp)"
PIDFILE="$(mktemp)"
APPLIED_CLOUD_INIT="$(mktemp)"
ACTIVE_CLOUD_INIT="$(mktemp)"
RECORD="$(mktemp -u /root/.pilotrt-modes.XXXXXX)"
AROOT="${DATA_DIR}/agent"
AHOME="${AROOT}/home"
AWS="${AROOT}/workspaces"
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
    CLOUD_INIT_SUDOERS="$CLOUD_INIT" DAEMON_MODES_RECORD="$RECORD" "$SETUP" --daemon-user "$DAEMON" --agent-user "$AGENT" \
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
    rm -f "$DEFAULTS_FILE" "$RULES_FILE"
    run_setup --undo || true
    userdel "$AGENT" 2>/dev/null || true
    userdel -r "$DAEMON" 2>/dev/null || true
    groupdel "$GROUP" 2>/dev/null || true
    groupdel "$OPS_GROUP" 2>/dev/null || true
    rm -f "$CLOUD_INIT" "${CLOUD_INIT}".pilot-agent.bak* "$RECORD" "$STOP" "$PIDFILE" "$APPLIED_CLOUD_INIT" "$ACTIVE_CLOUD_INIT"
    rm -rf "$DATA_DIR" "$OUTSIDE" "$ROOTDIR" "$CLOSED" "$WEB" "$LOG"
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

# curl_ok USER URL — USER gets an answer from URL (-g: [::1] is an address, not a glob).
curl_ok() {
    as "$1" curl -g -sS -o /dev/null --max-time 5 "$2"
}

# check_connections LABEL — the agent user reaches neither the metadata address
# nor port 9091, the daemon user reaches both, the agent user reaches another
# local port.
check_connections() {
    check_not "$1: the agent user cannot reach the metadata address" curl_ok "$AGENT" "http://${METADATA}/"
    check_not "$1: the agent user cannot reach 127.0.0.1:9091" curl_ok "$AGENT" "http://127.0.0.1:9091/"
    check "$1: the daemon user reaches the metadata address" curl_ok "$DAEMON" "http://${METADATA}/"
    check "$1: the daemon user reaches 127.0.0.1:9091" curl_ok "$DAEMON" "http://127.0.0.1:9091/"
    check_not "$1: the agent user cannot reach [::1]:9091" curl_ok "$AGENT" "http://[::1]:9091/"
    check "$1: the daemon user reaches [::1]:9091" curl_ok "$DAEMON" "http://[::1]:9091/"
    check "$1: the agent user reaches another local port (127.0.0.1:${OTHER_PORT})" curl_ok "$AGENT" "http://127.0.0.1:${OTHER_PORT}/"
}

# listen ADDRESS PORT — an HTTP server as root, up before it returns.
listen() {
    local i host="$1"
    case "$host" in *:*) host="[${host}]" ;; esac
    python3 -m http.server --bind "$1" --directory "$WEB" "$2" >/dev/null 2>&1 &
    LISTENERS+=("$!")
    for i in $(seq 1 50); do
        if curl -g -sS -o /dev/null --max-time 1 "http://${host}:$2/" 2>/dev/null; then
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
# The ubuntu-24.04 runner image has /etc/sudoers.d/runner with a mode that the
# classic visudo -c rejects ("bad permissions, should be mode 0440"). The script
# is right to stop on a host whose sudoers files do not validate, so give the
# files of the image the mode visudo wants before the first check.
for f in /etc/sudoers.d/*; do
    [ -f "$f" ] && [ ! -L "$f" ] && chmod 0440 "$f"
done
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
    echo open >"$1/open"
    chmod 0777 "$1/open"
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

echo "=== real test: the data dir check — an open data dir and open entries fail the apply, no mode changed ==="
# A process of another user that reads a file below the data dir: the chmods
# take its access away, so the check names it before the chmods.
chmod 0666 "$PIDFILE"
runuser -u nobody -- bash -c 'exec 3<"$1"; echo "$$" >"$2"; exec sleep 300' _ "${DATA_DIR}/repos/alpha/.env" "$PIDFILE" &
LISTENERS+=("$!")
for try in $(seq 1 50); do
    if [ -s "$PIDFILE" ] && [ -e "/proc/$(cat "$PIDFILE")/fd/3" ]; then break; fi
    sleep 0.2
done
NOBODY_PID="$(cat "$PIDFILE")"
LISTENERS+=("$NOBODY_PID")
# The daemon user names the entries and the targets of the links: a link whose
# target path holds a newline must forge no second line in the record of root
# ("4755<TAB>/usr/bin/sudo"), nor in the output of undo below.
FORGED_TOP="${DATA_DIR}/x"$'\n'"4755"$'\t'
mkdir -p "${FORGED_TOP}/usr/bin/sudo"
ln -s "${FORGED_TOP}/usr/bin/sudo" "${DATA_DIR}/evil"
chown -hR "${DAEMON}:${DAEMON}" "$FORGED_TOP" "${DATA_DIR}/evil"
rc=0
run_setup --confirm-root-path || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
expect_step daemon-private failed
check "the link with a newline in its target is named, quoted, for a look by hand" grep -qxF "the entry '${DATA_DIR}/evil' is not a plain path in the data dir (a name or a link target outside [A-Za-z0-9._/-], or a link out of the data dir): no chmod is printed, look at it by hand" "$LOG"
check_not "no line of the apply output starts with the forged mode" grep -qE '^4755' "$LOG"
check_not "the record of root has no forged line" grep -qF "/usr/bin/sudo" "$RECORD"
check_not "every line of the record is a mode and a plain path in the data dir" grep -qvE "^[0-7]+	${DATA_DIR}(/[A-Za-z0-9._-]+)*\$" "$RECORD"
# The operator's look by hand.
rm -rf "$FORGED_TOP" "${DATA_DIR}/evil"
check_not "no group was created" getent group "$GROUP"
check_not "no agent user was created" id -u "$AGENT"
check "the line for the data dir with its chmod" grep -qxF "the data dir '${DATA_DIR}' gives more than traverse (group ${DAEMON}, mode 755): runuser -u ${DAEMON} -- chmod go-rw '${DATA_DIR}'" "$LOG"
for entry in repos bin; do
    check "the line for ${entry} with its chmod" grep -qxF "the agent user can reach '${DATA_DIR}/${entry}' (group ${DAEMON}, mode 755): runuser -u ${DAEMON} -- chmod o-rwx '${DATA_DIR}/${entry}'" "$LOG"
done
check "the line for the entry at 0777 with its chmod" grep -qxF "the agent user can reach '${DATA_DIR}/open' (group ${DAEMON}, mode 777): runuser -u ${DAEMON} -- chmod o-rwx '${DATA_DIR}/open'" "$LOG"
check_not "no chmod line without the runuser form" grep -qE ": chmod " "$LOG"
proc_n="$(grep -n -m1 -F "a process of nobody (pid ${NOBODY_PID}, 'sleep') has '${DATA_DIR}/repos/alpha/.env' open" "$LOG" | cut -d: -f1 || true)"
chmod_n="$(grep -n -m1 -F ": runuser -u ${DAEMON} -- chmod " "$LOG" | cut -d: -f1 || true)"
if [ -n "$proc_n" ] && [ -n "$chmod_n" ] && [ "$proc_n" -lt "$chmod_n" ]; then
    pass "the process of nobody with a file open below the data dir is named before the chmods"
else
    show_log
    fail_test "no line for the process of nobody before the chmods (process line '${proc_n}', chmod line '${chmod_n}')"
fi
kill "$NOBODY_PID" 2>/dev/null || true
check "the data dir keeps its mode (the check changes no mode)" test "$(stat -c %a "$DATA_DIR")" = 755
check "the record of root holds the mode of the data dir" grep -qxF "755	${DATA_DIR}" "$RECORD"
check "the record of root holds the mode of the repos directory" grep -qxF "755	${DATA_DIR}/repos" "$RECORD"
check "the record of root holds the mode 777 of the open entry" grep -qxF "777	${DATA_DIR}/open" "$RECORD"
check "the record is root's, mode 0600" test "$(stat -c %U:%a "$RECORD")" = root:600
# The operator's fix, as printed, run as root: a shell reads the quoted path.
while IFS= read -r fix; do
    bash -c "$fix"
done < <(sed -nE "s/^(the agent user can reach|the data dir) .*: (runuser -u ${DAEMON} -- chmod [go]+-rwx? '\/.*)\$/\2/p" "$LOG")
check "the data dir is 0711 after the printed chmod" test "$(stat -c %a "$DATA_DIR")" = 711
check "the open entry is 0770 after the printed chmod" test "$(stat -c %a "${DATA_DIR}/open")" = 770

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
expect_step agent-proof skipped
for want in "  run_as_user: ${AGENT}" "  run_as_group: ${GROUP}" "  run_as_workspace_dir: ${AWS}"; do
    check "the summary prints the config line:${want#' '}" grep -qxF "$want" "$LOG"
done
check "the agent user exists with a no-login shell" bash -c "getent passwd ${AGENT} | grep -q 'nologin\$'"
check "the home of the agent user is ${AHOME}" test "$(getent passwd "$AGENT" | cut -d: -f6)" = "$AHOME"
check "the agent root is root:root 755" test "$(stat -c %U:%G:%a "$AROOT")" = "root:root:755"
check "the agent home is ${AGENT}:${GROUP} 750" test "$(stat -c %U:%G:%a "$AHOME")" = "${AGENT}:${GROUP}:750"
check "go/cache belongs to the agent user (it made it)" test "$(stat -c %U:%G "${AHOME}/go/cache")" = "${AGENT}:${GROUP}"
check "the daemon user is in the group" bash -c "id -nG ${DAEMON} | tr ' ' '\n' | grep -qx ${GROUP}"
check "the workspace root is ${AGENT}:${GROUP} 2770" test "$(stat -c %U:%G:%a "$AWS")" = "${AGENT}:${GROUP}:2770"
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
listen ::1 9091
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
check "the agent user reaches [::1]:9091 without the chain (the test is not vacuous)" curl_ok "$AGENT" "http://[::1]:9091/"
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
check "the agent user writes in its workspace root" as "$AGENT" touch "${AWS}/probe"
check_not "the agent user cannot list the data dir" as "$AGENT" ls "$DATA_DIR"
as "$DAEMON" bash -c 'umask 022 && echo "secret" >"$1/repos/alpha/after-apply.txt"' _ "$DATA_DIR"
check "the daemon user made a file with mode 0644 after the apply" test "$(stat -c %a "${DATA_DIR}/repos/alpha/after-apply.txt")" = 644
check_not "the agent user cannot read that file" as "$AGENT" cat "${DATA_DIR}/repos/alpha/after-apply.txt"
check "the daemon user still reads its own file" as "$DAEMON" cat "${DATA_DIR}/repos/alpha/.env"
if [ "$(as "$DAEMON" sudo -n -u "$AGENT" id -un 2>/dev/null)" = "$AGENT" ]; then
    pass "the daemon user runs a command as the agent user through the installed rule"
else
    fail_test "the daemon user cannot run a command as the agent user"
fi
check_not "the daemon user: sudo -n true fails (no root left)" as "$DAEMON" sudo -n true

echo "=== real test: no root step in a directory of the agent user — 20 applies while it swaps go/cache for a link ==="
chmod 0755 "$ROOTDIR"
printf 'root file\n' >"${ROOTDIR}/file"
# every file outside the agent root that an apply could reach through the link
owners() {
    find "$ROOTDIR" "$OUTSIDE" "$DATA_DIR" -path "$AROOT" -prune -o -printf '%p %U:%G %m\n' | sort
}
OWNERS_BEFORE="$(owners)"
chmod 0644 "$STOP"
as "$AGENT" bash -c '
    while [ -e "$3" ]; do
        rm -rf "$1/go/cache"
        ln -s "$2" "$1/go/cache"
        rm -f "$1/go/cache"
        mkdir "$1/go/cache"
    done' _ "$AHOME" "$ROOTDIR" "$STOP" &
SWAP_PID=$!
LISTENERS+=("$SWAP_PID")
outcomes=""
for i in $(seq 1 20); do
    rc=0
    run_setup || rc=$?
    outcomes="${outcomes} ${rc}"
done
rm -f "$STOP"
wait "$SWAP_PID" 2>/dev/null || true
echo "    exit codes of the 20 applies:${outcomes}"
as "$AGENT" bash -c 'if [ -L "$1" ] || [ ! -d "$1" ]; then rm -rf "$1" && mkdir "$1"; fi' _ "${AHOME}/go/cache"
check "no file outside the agent root has a new owner or mode" test "$OWNERS_BEFORE" = "$(owners)"
check_not "no file of the agent user in the directory of root" bash -c "find '${ROOTDIR}' -user '${AGENT}' | grep -q ."
if run_setup; then
    pass "an apply passes after the loop"
else
    show_log
    fail_test "an apply failed after the loop"
fi

echo "=== real test: a data dir below a 0700 directory fails the apply at the proof of the agent user ==="
mkdir "${CLOSED}/data"
chown "${DAEMON}:${DAEMON}" "$CLOSED" "${CLOSED}/data"
chmod 0700 "$CLOSED"
chmod 0711 "${CLOSED}/data"
rc=0
run_setup --data-dir "${CLOSED}/data" || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
expect_step directories "done"
expect_step agent-proof failed
check "the message names the agent home" grep -qF "the agent user cannot create a file in ${CLOSED}/data/agent/home;" "$LOG"
check "the agent root was made, root:root 755" test "$(stat -c %U:%G:%a "${CLOSED}/data/agent")" = "root:root:755"

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

echo "=== real test: the sudo proof by the rules — a NOPASSWD rule for one command as root in a second file ==="
printf '%s ALL=(root) NOPASSWD: /usr/bin/id\n' "$DAEMON" >"$RULES_FILE"
chmod 0440 "$RULES_FILE"
check "visudo accepts the second file" visudo -c
check_not "sudo -n true still fails for the daemon user" as "$DAEMON" sudo -n true
check "the daemon user runs /usr/bin/id as root" as "$DAEMON" sudo -n /usr/bin/id -u
rc=0
run_setup || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
expect_step cloud-init-sudoers failed
check "the rule list is named" grep -qF "the sudo rules of ${DAEMON} (sudo -ll -U ${DAEMON}) hold more than the agent rule" "$LOG"
check "the entry and its reason are printed" grep -qE "^  entry [0-9]+ \(run-as user root, not ${AGENT} alone\):\$" "$LOG"
check "the file of the entry is printed" grep -qxF "      | Sudoers entry: ${RULES_FILE}" "$LOG"
check "the command of the entry is printed" grep -qxF "      | "$'\t'"/usr/bin/id" "$LOG"

echo "=== real test: the sudo proof by the rules — a rule through a group of the daemon user ==="
groupadd "$OPS_GROUP"
usermod -aG "$OPS_GROUP" "$DAEMON"
# Run-as root, not ALL: sudo takes the last rule that matches, and a later
# (ALL) rule that needs a password would cover the agent user too.
printf '%%%s ALL=(root) ALL\n' "$OPS_GROUP" >"$RULES_FILE"
check "visudo accepts the group rule" visudo -c
check_not "sudo -n true fails (the rule needs a password)" as "$DAEMON" sudo -n true
check "sudo -n -u the agent user still works" as "$DAEMON" sudo -n -u "$AGENT" true
rc=0
run_setup || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
expect_step cloud-init-sudoers failed
check "the entry and its reason are printed" grep -qE "^  entry [0-9]+ \(run-as user root, not ${AGENT} alone\):\$" "$LOG"
check "the file of the entry is printed" grep -qxF "      | Sudoers entry: ${RULES_FILE}" "$LOG"

echo "=== real test: the check before the first change — the group rule stops the apply, the cloud-init file stays as it is ==="
# The cloud-init line active again, as on a host that is not set up yet; the
# group rule of the case above is still in the second file.
cp -p "$CLOUD_INIT" "$APPLIED_CLOUD_INIT"
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$DAEMON" >"$CLOUD_INIT"
cp -p "$CLOUD_INIT" "$ACTIVE_CLOUD_INIT"
check "visudo accepts the active cloud-init line" visudo -c
rc=0
run_setup --confirm-root-path || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply: exit ${rc}, want 1"; fi
check "the cloud-init file is the same byte for byte" cmp -s "$CLOUD_INIT" "$ACTIVE_CLOUD_INIT"
check_not "no step ran" grep -qx '== daemon-private' "$LOG"
check "the entry and its reason are printed" grep -qE "^  entry [0-9]+ \(run-as user root, not ${AGENT} alone\):\$" "$LOG"
check "the file of the entry is printed" grep -qxF "      | Sudoers entry: ${RULES_FILE}" "$LOG"
check_not "the cloud-init entry is not named" grep -qxF "      | Sudoers entry: ${CLOUD_INIT}" "$LOG"
check "the operator is told to remove the rule or the membership first" grep -qF "remove each rule, or the membership of ${DAEMON} in the group it names, first" "$LOG"
cp -p "$APPLIED_CLOUD_INIT" "$CLOUD_INIT"

echo "=== real test: the sudo proof by the rules — a second rule to the agent user alone passes ==="
printf '%s ALL=(%s) NOPASSWD: /usr/bin/true\n' "$DAEMON" "$AGENT" >"$RULES_FILE"
check "visudo accepts the agent rule" visudo -c
if run_setup; then pass "apply exits 0"; else show_log; fail_test "apply failed with only rules to the agent user"; fi
expect_step cloud-init-sudoers skipped
check_not "no entry is named" grep -qE '^  entry [0-9]+ ' "$LOG"
rm -f "$RULES_FILE"
gpasswd -d "$DAEMON" "$OPS_GROUP" >/dev/null
groupdel "$OPS_GROUP"

echo "=== real test: second apply (no --confirm-root-path: nothing is left to remove) ==="
if run_setup; then
    pass "second apply exits 0"
else
    show_log
    fail_test "second apply exited non-zero"
fi
skipped="$(grep -c '^step .*: skipped' "$LOG" || true)"
if [ "$skipped" = 11 ]; then
    pass "second apply skips all 11 steps"
else
    show_log
    fail_test "second apply: ${skipped} of 11 steps skipped"
fi

echo "=== real test: undo with go/cache a link to a directory outside the data dir ==="
as "$AGENT" bash -c 'rmdir "$1/go/cache" && ln -s "$2" "$1/go/cache"' _ "$AHOME" "$OUTSIDE"
check "go/cache is a link outside the data dir" test -L "${AHOME}/go/cache"
# The daemon user puts a link to a file of root in place of the entry that the
# record holds at 777; the printed undo line must not make that file writable.
check "the file of root is root's, mode 644" test "$(stat -c %U:%a "${ROOTDIR}/file")" = root:644
as "$DAEMON" bash -c 'rm -f "$1/open" && ln -s "$2" "$1/open"' _ "$DATA_DIR" "${ROOTDIR}/file"
check "the open entry is a link to the file of root" test "$(readlink "${DATA_DIR}/open")" = "${ROOTDIR}/file"
OUTSIDE_BEFORE="$(find "$OUTSIDE" -printf '%p %m %U:%G %s\n' | sort)"
# A process of the agent user that would outlive the firewall: undo stops it.
as "$AGENT" sleep 300 &
AGENT_SLEEP=$!
LISTENERS+=("$AGENT_SLEEP")
for try in $(seq 1 50); do
    if pgrep -u "$AGENT" -x sleep >/dev/null; then break; fi
    sleep 0.2
done
check "a process of the agent user runs before the undo" pgrep -u "$AGENT" -x sleep
if run_setup --undo; then
    pass "undo exits 0"
else
    show_log
    fail_test "undo exited non-zero"
fi
check "undo says how many processes of the agent user it stops" grep -qE "^stopping [1-9][0-9]* process\(es\) of ${AGENT}: TERM, then KILL after 5 seconds\$" "$LOG"
expect_step agent-processes "done"
check_not "no process of the agent user is left after the undo" pgrep -u "$AGENT"
wait "$AGENT_SLEEP" 2>/dev/null || true
check "the sudoers file of the daemon user is restored (active root line)" grep -qxF "${DAEMON} ALL=(ALL) NOPASSWD:ALL" "$CLOUD_INIT"
check "undo prints the chmod that puts back the mode of the data dir" grep -qxF "  runuser -u ${DAEMON} -- chmod 755 '${DATA_DIR}'" "$LOG"
check "undo prints the chmod that puts back the mode of the repos directory" grep -qxF "  runuser -u ${DAEMON} -- chmod 755 '${DATA_DIR}/repos'" "$LOG"
check_not "undo prints no chmod line without the runuser form" grep -qE "^  chmod " "$LOG"
UNDO_OPEN="$(grep -xF "  runuser -u ${DAEMON} -- chmod 777 '${DATA_DIR}/open'" "$LOG" || true)"
if [ -n "$UNDO_OPEN" ]; then
    pass "undo prints the chmod 777 for the open entry, as the daemon user"
else
    show_log
    fail_test "undo printed no chmod 777 line for the open entry"
fi
# The operator runs the printed line as root, as it is.
rc=0
bash -c "$UNDO_OPEN" 2>/dev/null || rc=$?
if [ "$rc" != 0 ]; then pass "the printed undo line fails on the file of root"; else fail_test "the printed undo line succeeded on the file of root"; fi
check "the file of root keeps its owner and mode 644" test "$(stat -c %U:%a "${ROOTDIR}/file")" = root:644
check_not "undo prints no chmod 4755" grep -qF "chmod 4755" "$LOG"
check_not "undo prints nothing of the forged path" grep -qF "/usr/bin/sudo" "$LOG"
check "the data dir keeps 0711 (undo prints the chmod, it does not run it)" test "$(stat -c %a "$DATA_DIR")" = 711
check "the daemon user has root without a password again" as "$DAEMON" sudo -n true
check "nothing outside the data dir changed" test "$OUTSIDE_BEFORE" = "$(find "$OUTSIDE" -printf '%p %m %U:%G %s\n' | sort)"
check "visudo accepts the sudoers files" visudo -c
if run_setup --undo; then
    pass "a second undo exits 0"
else
    show_log
    fail_test "a second undo exited non-zero"
fi
for s in cloud-init-sudoers agent-processes firewall-unit firewall-ipv4 firewall-ipv6 sudoers-rule; do
    expect_step "$s" skipped
done
for bin in iptables ip6tables; do
    check "${bin}: no chain and no jump" no_firewall "$bin"
done
check_not "no unit file" test -e "$UNIT_FILE"
check_not "the unit is not enabled" systemctl is-enabled --quiet "$UNIT"
check_not "the unit is not active" systemctl is-active --quiet "$UNIT"
check_not "no sudoers file of the script" test -e "$RUNAS_FILE"

echo "=== real test: with ip6tables hidden from PATH the apply fails before the sudoers rule ==="
check "IPv6 is on on this host (disable_ipv6 of all is 0)" test "$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6)" = 0
# A PATH of links to every command of PATH but ip6tables*.
HIDDEN="$(mktemp -d)"
IFS=: read -ra path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
    for f in "$d"/*; do
        case "${f##*/}" in ip6tables*) continue ;; esac
        if [ -x "$f" ] && [ ! -e "${HIDDEN}/${f##*/}" ]; then ln -s "$f" "${HIDDEN}/${f##*/}"; fi
    done
done
check_not "ip6tables is not found with that PATH" env PATH="$HIDDEN" bash -c 'command -v ip6tables'
check "iptables is found with that PATH" env PATH="$HIDDEN" bash -c 'command -v iptables'
# The link to the file of root that the undo case left: the operator removes it.
rm -f "${DATA_DIR}/open"
rc=0
(export PATH="$HIDDEN" && run_setup --confirm-root-path) || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else show_log; fail_test "apply with ip6tables hidden: exit ${rc}, want 1"; fi
expect_step firewall-ipv4 "done"
expect_step firewall-ipv6 failed
expect_step sudoers-rule skipped
expect_step cloud-init-sudoers skipped
check "the failure says ip6tables is not found and IPv6 is on" grep -qF "ip6tables not found and IPv6 is on" "$LOG"
check_not "no sudoers rule file exists" test -e "$RUNAS_FILE"
check "the root line of the daemon user is still active" grep -qxF "${DAEMON} ALL=(ALL) NOPASSWD:ALL" "$CLOUD_INIT"
if run_setup --undo; then
    pass "undo after the failed apply exits 0"
else
    show_log
    fail_test "undo after the failed apply exited non-zero"
fi
expect_step firewall-ipv4 "done"
check "iptables: no chain and no jump" no_firewall iptables

echo "=== real test: a full apply, then an apply with ip6tables hidden from PATH — the sudoers rule file of the full apply is removed ==="
# The link at go/cache that the undo case left: the agent user puts the directory back.
as "$AGENT" bash -c 'rm -f "$1/go/cache" && mkdir "$1/go/cache"' _ "$AHOME"
if run_setup --confirm-root-path; then
    pass "full apply exits 0"
else
    show_log
    fail_test "full apply exited non-zero"
fi
check "the full apply installed the sudoers rule file" test -e "$RUNAS_FILE"
check "the daemon user runs a command as the agent user" as "$DAEMON" sudo -n -u "$AGENT" true
rc=0
PATH="$HIDDEN" run_setup || rc=$?
if [ "$rc" = 1 ]; then pass "apply with ip6tables hidden exits 1"; else show_log; fail_test "apply with ip6tables hidden after a full apply: exit ${rc}, want 1"; fi
expect_step firewall-ipv6 failed
check_not "no sudoers rule file exists" test -e "$RUNAS_FILE"
check "one line says the rule is removed" grep -qF "firewall-ipv6 failed before the sudoers-rule step: removing the sudo rule of an earlier apply, ${RUNAS_FILE}" "$LOG"
check "the summary lists the removal" grep -qxF "sudo rule removed: ${RUNAS_FILE} (firewall-ipv6 failed; the next apply that passes puts it back)" "$LOG"
check_not "the daemon user cannot run a command as the agent user" as "$DAEMON" sudo -n -u "$AGENT" true
rm -rf "$HIDDEN"
if run_setup --undo; then
    pass "undo after the second, failed apply exits 0"
else
    show_log
    fail_test "undo after the second, failed apply exited non-zero"
fi
check "the sudoers file of the daemon user is restored (active root line)" grep -qxF "${DAEMON} ALL=(ALL) NOPASSWD:ALL" "$CLOUD_INIT"
for bin in iptables ip6tables; do
    check "${bin}: no chain and no jump" no_firewall "$bin"
done

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
check_not "after the printed cleanup: no agent root" test -e "$AROOT"
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
