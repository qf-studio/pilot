#!/bin/bash
# Plain-bash test suite for setup-agent-user.sh — no bats dependency, no root,
# no real host, and no GNU-only flag of find or stat (it runs on macOS too, with
# the same count of checks).
# The script runs against a temp root with a PATH of mock commands
# (scripts/box/testdata/mock-host-cmds.sh, linked under the names useradd,
# groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown,
# chmod, id, getent, stat, sudo, runuser, pgrep, pkill and sleep) that record
# their arguments; the sudoers and systemd directories and the IPv6 settings are
# pointed at the temp root through SUDOERS_DIR / SYSTEMD_DIR / IPV6_CONF_DIR. The real-host counterpart, run in CI as root, is
# setup-agent-user_real_test.sh.
#
# Usage: ./scripts/box/setup-agent-user_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT="${SCRIPT_DIR}/setup-agent-user.sh"
MOCK_CMDS="${SCRIPT_DIR}/testdata/mock-host-cmds.sh"
MOCKED="useradd groupadd usermod visudo iptables ip6tables systemctl install chown chmod id getent stat sudo runuser pgrep pkill sleep"

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

CLOUD_INIT_ORIGINAL='# Created by cloud-init
# User rules for ec2-user
ec2-user ALL=(ALL) NOPASSWD:ALL
%ec2-user-admins ALL=(ALL) NOPASSWD:ALL
admin ALL=(ALL) NOPASSWD:ALL'

CLOUD_INIT_APPLIED='# Created by cloud-init
# User rules for ec2-user
# disabled by setup-agent-user: ec2-user ALL=(ALL) NOPASSWD:ALL
%ec2-user-admins ALL=(ALL) NOPASSWD:ALL
admin ALL=(ALL) NOPASSWD:ALL'

EXPECTED_RULE='ec2-user ALL=(pilot-agent) NOPASSWD: SETENV: ALL'

# mode_of PATH — octal mode as GNU stat %a prints it ("2770", "755"). BSD stat:
# %Lp alone drops the setgid digit, so %Mp is put in front and a leading zero
# ("0755") goes.
mode_of() {
    local m
    if m="$(stat -c %a "$1" 2>/dev/null)"; then
        echo "$m"
        return 0
    fi
    m="$(stat -f '%Mp%Lp' "$1")"
    echo "${m#0}"
}

# set_ipv6 VALUE [ENTRY VALUE ...] — the disable_ipv6 settings of the host:
# VALUE for all, default, lo and eth0, then one value per ENTRY.
set_ipv6() {
    local e
    rm -rf "$IPV6_CONF"
    for e in all default lo eth0; do
        mkdir -p "${IPV6_CONF}/${e}"
        echo "$1" >"${IPV6_CONF}/${e}/disable_ipv6"
    done
    shift
    while [ $# -gt 1 ]; do
        echo "$2" >"${IPV6_CONF}/$1/disable_ipv6"
        shift 2
    done
}

# set_meta PATH OWNER:GROUP — the owner and group the mocks report for PATH.
set_meta() {
    echo "$2" >"${STATE_DIR}/meta/$(echo "$1" | tr '/' '_')"
}

# build_repos — repos/alpha, a repository of the daemon as git leaves it: the
# working tree with a file at 0644 that git does not track, .git/config with a
# remote URL. The repos directory is closed (0700), as the check wants it.
build_repos() {
    local repo="${DATA_DIR}/repos/alpha"
    mkdir -p "$repo/.git/objects" "$repo/.git/refs/heads" "$repo/src"
    printf 'ref: refs/heads/main\n' >"$repo/.git/HEAD"
    printf '[remote "origin"]\n\turl = https://x:fake-test-token@example.invalid/r.git\n' >"$repo/.git/config"
    printf 'TOKEN=fake-api-key\n' >"$repo/.env"
    printf 'package x\n' >"$repo/src/code.go"
    chmod 644 "$repo/.git/HEAD" "$repo/.git/config" "$repo/.env" "$repo/src/code.go"
    chmod 755 "$repo/.git" "$repo/src" "$repo"
    chmod 700 "${DATA_DIR}/repos"
}

setup_sandbox() {
    SANDBOX="$(cd "$(mktemp -d)" && pwd -P)"
    chmod 711 "$SANDBOX"
    DATA_DIR="${SANDBOX}/data"
    SUDOERS_DIR="${SANDBOX}/sudoers.d"
    SYSTEMD_DIR="${SANDBOX}/systemd"
    STATE_DIR="${SANDBOX}/state"
    STUB_BIN="${SANDBOX}/bin"
    CALL_LOG="${SANDBOX}/calls.log"
    MUT_LOG="${SANDBOX}/mutations.log"
    OUT="${SANDBOX}/stdout.log"
    R="${DATA_DIR}/repos"
    BAK="${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak"
    AROOT="${DATA_DIR}/agent"
    AHOME="${AROOT}/home"
    AWS="${AROOT}/workspaces"
    RECORD="${SANDBOX}/root/modes"
    IPV6_CONF="${SANDBOX}/ipv6conf"

    mkdir -p "${DATA_DIR}/repos" "${DATA_DIR}/bin" "$SUDOERS_DIR" "$SYSTEMD_DIR" "$STUB_BIN" "${STATE_DIR}/users" "${STATE_DIR}/meta" "${SANDBOX}/root"
    chmod 711 "$DATA_DIR"
    chmod 700 "${DATA_DIR}/repos"
    chmod 750 "${DATA_DIR}/bin"
    printf '#!/bin/sh\n' >"${DATA_DIR}/bin/pilot"
    chmod 755 "${DATA_DIR}/bin/pilot"
    : >"$CALL_LOG"
    : >"$MUT_LOG"
    touch "${STATE_DIR}/users/ec2-user"
    set_meta "$DATA_DIR" ec2-user:ec2-user
    # the record of the modes is a file of root (the script writes it as root)
    set_meta "$RECORD" root:root
    printf '%s\n' "$CLOUD_INIT_ORIGINAL" >"${SUDOERS_DIR}/90-cloud-init-users"
    # IPv6 is on, as on most hosts
    set_ipv6 0

    local c
    for c in $MOCKED; do
        ln -s "$MOCK_CMDS" "${STUB_BIN}/${c}"
    done
}

teardown_sandbox() {
    rm -rf "$SANDBOX"
}

# run_setup [VAR=value ...] -- [script args ...]  (stdout+stderr go to $OUT)
# --confirm-root-path is added unless NO_CONFIRM=1 is among the variables.
run_setup() {
    local envs=() confirm="--confirm-root-path"
    while [ $# -gt 0 ] && [ "$1" != "--" ]; do
        case "$1" in
            NO_CONFIRM=1) confirm="" ;;
            *) envs+=("$1") ;;
        esac
        shift
    done
    shift
    env \
        PATH="${STUB_BIN}:${PATH}" \
        MOCK_REAL_PATH="$PATH" \
        MOCK_STATE_DIR="$STATE_DIR" \
        MOCK_SANDBOX="$SANDBOX" \
        MOCK_CALL_LOG="$CALL_LOG" \
        MOCK_MUT_LOG="$MUT_LOG" \
        SUDOERS_DIR="$SUDOERS_DIR" \
        SYSTEMD_DIR="$SYSTEMD_DIR" \
        DAEMON_MODES_RECORD="$RECORD" \
        IPV6_CONF_DIR="$IPV6_CONF" \
        ${envs[@]+"${envs[@]}"} \
        "$SETUP_SCRIPT" --data-dir "$DATA_DIR" ${confirm:+"$confirm"} "$@" >"$OUT" 2>&1
}

# mock_run COMMAND ARGS... — one mocked command, to prepare the host state.
mock_run() {
    env \
        PATH="${STUB_BIN}:${PATH}" \
        MOCK_REAL_PATH="$PATH" \
        MOCK_STATE_DIR="$STATE_DIR" \
        MOCK_SANDBOX="$SANDBOX" \
        MOCK_CALL_LOG="$CALL_LOG" \
        MOCK_MUT_LOG="$MUT_LOG" \
        SUDOERS_DIR="$SUDOERS_DIR" \
        "$@"
}

# Every file the script may write, as one comparable listing. The mocks' own
# state directory is left out: it is bookkeeping, and the mutation log covers it.
tree_listing() {
    local p
    find "$DATA_DIR" "$SUDOERS_DIR" "$SYSTEMD_DIR" | sort | while IFS= read -r p; do
        echo "${p} $(mode_of "$p")"
    done
    cat "${SUDOERS_DIR}/90-cloud-init-users"
}

# dir_listing DIR — path, mode and content of everything under DIR.
dir_listing() {
    local p
    find "$1" | sort | while IFS= read -r p; do
        if [ -f "$p" ]; then
            echo "${p} $(mode_of "$p") $(cat "$p")"
        else
            echo "${p} $(mode_of "$p")"
        fi
    done
}

# first_line PATTERN — line number of the first call that matches.
first_line() {
    grep -n -m1 -E -- "$1" "$CALL_LOG" | cut -d: -f1
}

# first_line_of FILE PATTERN — line number of the first line of FILE that matches.
first_line_of() {
    grep -n -m1 -E -- "$2" "$1" | cut -d: -f1
}

step_state() {
    { grep -E "^step $1: " "$OUT" || true; } | head -1 | sed -E "s/^step $1: //"
}

expect_step() {
    local name="$1" want="$2" got
    got="$(step_state "$name")"
    if [ "${got%% *}" = "$want" ]; then
        pass "step ${name}: ${want}"
    else
        fail_test "step ${name}: want ${want}, got '${got}'"
    fi
}

# has_line FILE LINE — a whole line equal to LINE. A substring match would take
# "+ visudo -cf ..." for "+ visudo -c".
has_line() {
    grep -qxF -- "$2" "$1"
}

# chmods_as_daemon FILE — FILE prints a chmod, and every chmod of a mode in it
# is in the runuser form of the daemon user: on an apply line after ": ", on an
# undo line at the start (after the indent). Called through check only (SC2317
# in ShellCheck 0.10, SC2329 from 0.11).
# shellcheck disable=SC2317,SC2329
chmods_as_daemon() {
    local all ok
    all="$(grep -cE "chmod ([go]+-rwx?|0?[0-7]{3,4}) '" "$1" || true)"
    ok="$(grep -cE "(: |^  )runuser -u ec2-user -- chmod ([go]+-rwx?|0?[0-7]{3,4}) '[^']*'\$" "$1" || true)"
    [ "${all:-0}" -gt 0 ] && [ "$all" = "$ok" ]
}

# expect_refused LABEL [VAR=value ...] -- ARGS... — exit 2, nothing mutated, no
# chmod or chown called.
expect_refused() {
    local label="$1" rc=0
    shift
    : >"$MUT_LOG"
    : >"$CALL_LOG"
    run_setup "$@" || rc=$?
    if [ "$rc" = 2 ] && [ ! -s "$MUT_LOG" ] && ! grep -qE '^(chmod|chown) ' "$CALL_LOG"; then
        pass "refused with exit 2, nothing changed: ${label}"
    else
        cat "$OUT"
        fail_test "${label}: exit ${rc}, mutations: $(wc -l <"$MUT_LOG" | tr -d ' ')"
    fi
}

ALL_STEPS="daemon-private group agent-user daemon-membership firewall-ipv4 firewall-ipv6 firewall-unit directories sudoers-rule cloud-init-sudoers agent-proof"
# daemon-private and agent-proof are checks: they change nothing, so they report skipped.
CHANGE_STEPS="group agent-user daemon-membership firewall-ipv4 firewall-ipv6 firewall-unit directories sudoers-rule cloud-init-sudoers"
UNDO_STEPS="sudoers-rule cloud-init-sudoers agent-processes firewall-unit firewall-ipv4 firewall-ipv6"

echo "=== test: dry run — prints every command, changes nothing ==="
setup_sandbox
build_repos
before="$(tree_listing)"
if run_setup -- --dry-run; then
    pass "dry run exits 0"
else
    cat "$OUT"
    fail_test "dry run exited non-zero"
fi
if [ ! -s "$MUT_LOG" ]; then
    pass "no mutating command was run"
else
    cat "$MUT_LOG"
    fail_test "dry run ran mutating commands"
fi
if [ "$before" = "$(tree_listing)" ]; then
    pass "files, modes and the cloud-init file are unchanged"
else
    fail_test "dry run changed the temp root"
fi
for want in "+ groupadd --system pilot" "+ useradd --system" "+ usermod -aG pilot ec2-user" \
    "+ visudo -cf" "+ iptables -w -N PILOT_AGENT_EGRESS" "+ ip6tables -w -N PILOT_AGENT_EGRESS" \
    "+ systemctl enable pilot-agent-firewall.service" "$EXPECTED_RULE" \
    "+ runuser -u ec2-user -- sudo -n true" \
    "+ sudo -n -ll -U ec2-user  (each entry: run-as user pilot-agent only, run-as group pilot at most)" \
    "+ runuser -u pilot-agent -- mkdir -p -m 750 ${AHOME}/go ${AHOME}/go/cache ${AHOME}/go/mod" \
    "+ runuser -u pilot-agent -- touch ${AHOME}/.setup-agent-user-proof" \
    "+ runuser -u pilot-agent -- touch ${AWS}/.setup-agent-user-proof" \
    "+ runuser -u pilot-agent -- ls '${DATA_DIR}'  (must fail)" \
    "+ runuser -u pilot-agent -- ls '${R}/alpha'  (must fail)"; do
    if grep -qF -- "$want" "$OUT"; then
        pass "prints: ${want}"
    else
        fail_test "dry run output lacks: ${want}"
    fi
done
if has_line "$OUT" "+ visudo -c"; then
    pass "prints: + visudo -c (whole line)"
else
    fail_test "dry run output lacks the line: + visudo -c"
fi
check_not "the dry run ran no sudo" grep -qE '^(sudo|runuser) ' "$CALL_LOG"
teardown_sandbox

# mutation pin: has_line goes back to a substring match, so "+ visudo -cf" counts as "+ visudo -c" -> this test fails
echo "=== test: has_line does not take a longer line for the line asked for ==="
SELF="$(mktemp)"
printf '%s\n' "+ visudo -cf /etc/sudoers.d/.x.new" >"$SELF"
if has_line "$SELF" "+ visudo -c"; then
    fail_test "has_line matched '+ visudo -cf' for '+ visudo -c'"
else
    pass "has_line needs the whole line"
fi
rm -f "$SELF"

echo "=== test: mode_of keeps the setgid digit (2770 stays 2770, 0755 reads 755) ==="
SELF="$(mktemp -d)"
mkdir "$SELF/ws" "$SELF/plain"
chmod 2770 "$SELF/ws"
chmod 755 "$SELF/plain"
check "a directory at 2770 reads 2770" test "$(mode_of "$SELF/ws")" = 2770
check "a directory at 0755 reads 755" test "$(mode_of "$SELF/plain")" = 755
rm -rf "$SELF"

echo "=== test: apply — steps run in order and leave the host as designed ==="
setup_sandbox
build_repos
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply exited non-zero"
fi
prev=0
for pattern in '^sudo -n -ll -U ec2-user$' '^groupadd ' '^useradd ' '^usermod ' '^iptables -w -N ' '^ip6tables -w -N ' '^systemctl enable ' '^install -d ' '^runuser -u pilot-agent -- mkdir ' '^visudo -cf ' '^install -m 0600 -o root -g root .*\.pilot-agent\.bak$' '^visudo -c$' '^runuser -u ec2-user -- sudo -n true$' '^runuser -u pilot-agent -- touch ' '^runuser -u pilot-agent -- ls '; do
    n="$(first_line "$pattern" || true)"
    if [ -n "$n" ] && [ "$n" -gt "$prev" ]; then
        pass "in order: ${pattern}"
        prev="$n"
    else
        fail_test "out of order or missing: ${pattern} (line '${n}', after ${prev})"
    fi
done
# The rule list is read twice: by the check before the first change (first in
# the list above) and by the proof, between sudo -n true and the agent proof.
n="$(awk -v a="$(first_line '^runuser -u ec2-user -- sudo -n true$')" 'NR > a && /^sudo -n -ll -U ec2-user$/ { print NR; exit }' "$CALL_LOG")"
check "the proof reads the rule list after sudo -n true, before the agent proof" test "${n:-0}" -gt 0 -a "${n:-0}" -lt "$(first_line '^runuser -u pilot-agent -- touch ')"
if [ "$(cat "${SUDOERS_DIR}/90-pilot-agent-runas")" = "$EXPECTED_RULE" ]; then
    pass "sudoers file holds exactly the run-as rule"
else
    fail_test "sudoers file content: $(cat "${SUDOERS_DIR}/90-pilot-agent-runas" 2>&1)"
fi
if [ "$(mode_of "${SUDOERS_DIR}/90-pilot-agent-runas")" = "440" ]; then
    pass "sudoers file mode is 0440"
else
    fail_test "sudoers file mode is not 0440"
fi
if [ -z "$(find "$SUDOERS_DIR" -name '.*' -type f)" ]; then
    pass "no temporary sudoers file is left"
else
    fail_test "temporary sudoers file left behind"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED" ]; then
    pass "cloud-init file: only the daemon-user line is commented out"
else
    fail_test "cloud-init file content: $(cat "${SUDOERS_DIR}/90-cloud-init-users")"
fi
if [ "$(cat "$BAK")" = "$CLOUD_INIT_ORIGINAL" ]; then
    pass "backup holds the original cloud-init file"
else
    fail_test "backup missing or different"
fi
if [ "$(mode_of "$BAK")" = "600" ]; then
    pass "backup is root-only (0600)"
else
    fail_test "backup mode is not 0600"
fi
for want in "install -d -o root -g root -m 755 ${AROOT}" \
    "install -d -o pilot-agent -g pilot -m 750 ${AHOME}" \
    "install -d -o pilot-agent -g pilot -m 2770 ${AWS}" \
    "runuser -u pilot-agent -- mkdir -p -m 750 ${AHOME}/go ${AHOME}/go/cache ${AHOME}/go/mod" \
    "useradd --system --gid pilot --home-dir ${AHOME} --no-create-home --shell /usr/sbin/nologin pilot-agent"; do
    if grep -qF -- "$want" "$CALL_LOG"; then
        pass "ran: ${want}"
    else
        fail_test "missing call: ${want}"
    fi
done
if [ "$(mode_of "$AWS")" = "2770" ] && [ "$(mode_of "$AHOME")" = "750" ] && [ "$(mode_of "$AROOT")" = "755" ]; then
    pass "workspace mode 2770, home mode 750, agent root 755"
else
    fail_test "directory modes wrong"
fi
if grep -q -- "-m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS" "${STATE_DIR}/iptables.rules" \
    && grep -q -- "-d 169.254.169.254 -j REJECT" "${STATE_DIR}/iptables.rules" \
    && grep -q -- "-p tcp -d 127.0.0.1 --dport 9091 -j REJECT" "${STATE_DIR}/iptables.rules" \
    && grep -q -- "-d fd00:ec2::254 -j REJECT" "${STATE_DIR}/ip6tables.rules"; then
    pass "firewall: chain, jump on the agent user, metadata v4/v6 and port 9091"
else
    fail_test "firewall rules missing"
fi
UNIT="${SYSTEMD_DIR}/pilot-agent-firewall.service"
if grep -q '^Type=oneshot' "$UNIT" && grep -q -- "--uid-owner pilot-agent -j PILOT_AGENT_EGRESS" "$UNIT" \
    && [ -e "${STATE_DIR}/enabled/pilot-agent-firewall.service" ]; then
    pass "oneshot unit written and enabled"
else
    fail_test "unit missing, not oneshot or not enabled"
fi
expect_step daemon-private skipped
for s in $CHANGE_STEPS; do
    expect_step "$s" "done"
done
check_not "no chgrp was run (no shared read)" grep -q '^chgrp ' "$CALL_LOG"
check_not "no state file is written" test -e "${DATA_DIR}/.agent-setup-state"
check "the repository of the daemon keeps its modes" test "$(mode_of "$R")" = 700 -a "$(mode_of "$R/alpha/.env")" = 644 -a "$(mode_of "$R/alpha/.git/config")" = 644
check_not "the firewall never left a moment without reject rules" test -e "${STATE_DIR}/gap.log"
check_not "the probe files of the proof are gone" test -e "${AHOME}/.setup-agent-user-proof" -o -e "${AWS}/.setup-agent-user-proof"
for want in "  run_as_user: pilot-agent" "  run_as_group: pilot" "  run_as_workspace_dir: ${AWS}"; do
    check "the summary prints the config line: ${want# }" has_line "$OUT" "$want"
done
# mutation pin: the apply summary prints no run_as_data_dir line, so a custom --data-dir leaves the Go check on /var/lib/pilot -> this test fails
check "the summary prints the config line for the custom --data-dir" has_line "$OUT" "  run_as_data_dir: ${DATA_DIR}"

echo "=== test: second apply skips every step ==="
: >"$MUT_LOG"
if run_setup -- ; then
    pass "second apply exits 0"
else
    cat "$OUT"
    fail_test "second apply exited non-zero"
fi
for s in $ALL_STEPS; do
    expect_step "$s" skipped
done
if [ ! -s "$MUT_LOG" ]; then
    pass "second apply ran no mutating command"
else
    cat "$MUT_LOG"
    fail_test "second apply ran mutating commands"
fi

echo "=== test: undo removes what apply added, keeps user/group/directories ==="
: >"$MUT_LOG"
: >"$CALL_LOG"
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
if [ ! -e "${SUDOERS_DIR}/90-pilot-agent-runas" ]; then pass "sudoers file removed"; else fail_test "sudoers file still there"; fi
if [ ! -e "$UNIT" ] && [ ! -e "${STATE_DIR}/enabled/pilot-agent-firewall.service" ]; then pass "unit removed and disabled"; else fail_test "unit still there"; fi
stop_n="$(grep -n -m1 '^systemctl stop pilot-agent-firewall.service' "$MUT_LOG" | cut -d: -f1 || true)"
disable_n="$(grep -n -m1 '^systemctl disable pilot-agent-firewall.service' "$MUT_LOG" | cut -d: -f1 || true)"
if [ -n "$stop_n" ] && [ -n "$disable_n" ] && [ "$stop_n" -lt "$disable_n" ]; then
    pass "undo stops, then disables, the boot unit"
else
    fail_test "undo did not stop and disable the boot unit (stop line '${stop_n}', disable line '${disable_n}')"
fi
if ! grep -q PILOT_AGENT_EGRESS "${STATE_DIR}/iptables.rules" "${STATE_DIR}/ip6tables.rules"; then
    pass "firewall chain and jump removed (v4 and v6)"
else
    fail_test "firewall rules left"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ] && [ ! -e "$BAK" ]; then
    pass "cloud-init file restored from the backup"
else
    fail_test "cloud-init file not restored"
fi
if grep -qF "restoring ${SUDOERS_DIR}/90-cloud-init-users from ${BAK}, the newest backup with an active root line of ec2-user" "$OUT"; then
    pass "undo says which backup it restored"
else
    fail_test "undo does not name the backup"
fi
if ! grep -qE '^(userdel|groupdel|gpasswd|rm -rf)' "$CALL_LOG" && [ -d "${AHOME}/go/mod" ] && [ -e "${STATE_DIR}/users/pilot-agent" ]; then
    pass "user, group and directories are left in place"
else
    fail_test "undo removed the user, the group or a directory"
fi
for want in "pkill -u pilot-agent" "userdel pilot-agent" "groupdel pilot" "rm -rf ${AROOT}"; do
    if grep -qF -- "  ${want}" "$OUT"; then
        pass "prints the manual command: ${want}"
    else
        fail_test "no manual command printed: ${want}"
    fi
done
: >"$MUT_LOG"
if run_setup -- --undo && [ ! -s "$MUT_LOG" ]; then
    pass "a second undo changes nothing"
else
    fail_test "second undo failed or changed something"
fi
expect_step cloud-init-sudoers skipped
teardown_sandbox

# ------------------------------------------- item 1: undo cannot be blocked

# mutation pin: the containment checks run before the undo branch again -> this test fails
echo "=== test: undo with go/cache a link to a directory outside the data dir — exit 0, root's files restored, nothing outside changed ==="
setup_sandbox
run_setup -- >/dev/null || true
mkdir "${SANDBOX}/outside"
printf 'keep\n' >"${SANDBOX}/outside/file"
chmod 640 "${SANDBOX}/outside/file"
chmod 700 "${SANDBOX}/outside"
rmdir "${AHOME}/go/cache"
ln -s "${SANDBOX}/outside" "${AHOME}/go/cache"
ln -s "${SANDBOX}/nowhere" "${AHOME}/go/mod.link"
outside_before="$(dir_listing "${SANDBOX}/outside")"
: >"$MUT_LOG"
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero with a link at go/cache"
fi
check "the cloud-init file (the sudo of the daemon user) is restored" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check "the sudoers file of the script is removed" test ! -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check_not "the firewall chain is removed" grep -q PILOT_AGENT_EGRESS "${STATE_DIR}/iptables.rules"
check "nothing outside the data dir changed" test "$outside_before" = "$(dir_listing "${SANDBOX}/outside")"
check_not "no command named the directory outside" grep -qF "${SANDBOX}/outside" "$MUT_LOG"
for s in cloud-init-sudoers firewall-unit firewall-ipv4 firewall-ipv6 sudoers-rule; do
    expect_step "$s" "done"
done
rc=0
run_setup -- || rc=$?
check "apply with go/cache a link outside the data dir exits 1" test "$rc" = 1
expect_step firewall-ipv4 "done"
expect_step directories failed
expect_step sudoers-rule skipped
check "the failure names the link" grep -qF "${AHOME}/go/cache' is a symbolic link; refused" "$OUT"
check_not "no sudoers rule file" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check "nothing outside the data dir changed by the apply" test "$outside_before" = "$(dir_listing "${SANDBOX}/outside")"
teardown_sandbox

echo "=== test: undo needs no data dir of the daemon user (gone, or another owner) ==="
setup_sandbox
run_setup -- >/dev/null || true
set_meta "$DATA_DIR" somebody:somebody
if run_setup -- --undo; then
    pass "undo exits 0 with a data dir of another owner"
else
    cat "$OUT"
    fail_test "undo failed on the owner of the data dir"
fi
check "the cloud-init file is restored" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
set_meta "$DATA_DIR" ec2-user:ec2-user
run_setup -- >/dev/null || true
if run_setup -- --undo --data-dir "${SANDBOX}/gone"; then
    pass "undo exits 0 with a data dir that does not exist"
else
    cat "$OUT"
    fail_test "undo failed on a missing data dir"
fi
check "the sudoers file of the script is removed" test ! -e "${SUDOERS_DIR}/90-pilot-agent-runas"
expect_refused "undo still checks its arguments (--group wheel)" -- --undo --group wheel
expect_refused "undo still checks its arguments (--data-dir /usr)" -- --undo --data-dir /usr
teardown_sandbox

echo "=== test: undo refuses a backup that is a link or not root's, and a cloud-init file that is a link ==="
setup_sandbox
run_setup -- >/dev/null || true
cp "$BAK" "${SANDBOX}/evil"
rm "$BAK"
ln -s "${SANDBOX}/evil" "$BAK"
if run_setup -- --undo; then
    fail_test "undo must fail when the only backup is a link"
else
    pass "undo exits non-zero when the only backup is a link"
fi
check "the link is reported as passed over" grep -qF "not a regular file of root, passed over: ${BAK}" "$OUT"
check "the cloud-init file stays as it is" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED"
expect_step cloud-init-sudoers failed
teardown_sandbox
setup_sandbox
run_setup -- >/dev/null || true
mv "${SUDOERS_DIR}/90-cloud-init-users" "${SANDBOX}/ci-target"
ln -s "${SANDBOX}/ci-target" "${SUDOERS_DIR}/90-cloud-init-users"
if run_setup -- --undo; then
    fail_test "undo must refuse a cloud-init file that is a link"
else
    pass "undo refuses a cloud-init file that is a link"
fi
check "the target of the link is unchanged" test "$(cat "${SANDBOX}/ci-target")" = "$CLOUD_INIT_APPLIED"
teardown_sandbox

# ------------------------------------- item 5: the backup with the root line

echo "=== test: undo restores the newest backup in which the root line of the daemon user is active ==="
setup_sandbox
run_setup -- >/dev/null || true
cp "$BAK" "${BAK}.1"
set_meta "${BAK}.1" root:root
printf '%s\n' "$CLOUD_INIT_APPLIED" >"$BAK"
: >"$MUT_LOG"
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
check "the cloud-init file holds the active root line again" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check "undo names the backup it used" grep -qF "restoring ${SUDOERS_DIR}/90-cloud-init-users from ${BAK}.1, the newest backup with an active root line of ec2-user" "$OUT"
check "undo names the newer backup it passed over" grep -qF "no active root line of ec2-user, passed over: ${BAK}" "$OUT"
check "both backups are kept" test -e "$BAK" -a -e "${BAK}.1"
: >"$MUT_LOG"
run_setup -- --undo >/dev/null || true
check "a second undo changes nothing (the root line is active)" test ! -s "$MUT_LOG"
teardown_sandbox

echo "=== test: a backup not owned by root is passed over for an older one of root ==="
setup_sandbox
run_setup -- >/dev/null || true
cp "$BAK" "${BAK}.1"
set_meta "${BAK}.1" root:root
printf 'ec2-user ALL=(ALL) NOPASSWD:ALL\n# planted\n' >"$BAK"
set_meta "$BAK" ec2-user:ec2-user
run_setup -- --undo >/dev/null || true
check "the root backup is restored, not the planted one" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check "the planted backup is reported" grep -qF "not a regular file of root, passed over: ${BAK}" "$OUT"
teardown_sandbox

echo "=== test: no backup with an active root line — undo fails and restores nothing ==="
setup_sandbox
run_setup -- >/dev/null || true
printf '%s\n' "$CLOUD_INIT_APPLIED" >"$BAK"
if run_setup -- --undo; then
    fail_test "undo must fail when no backup has the root line"
else
    pass "undo exits non-zero"
fi
expect_step cloud-init-sudoers failed
check "the message says so" grep -qF "no backup of ${SUDOERS_DIR}/90-cloud-init-users has an active root line of ec2-user" "$OUT"
check "the cloud-init file is unchanged" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED"
teardown_sandbox

# ------------------------------------- undo goes on, the agent processes stop

# mutation pin: undo stops at the first failed step -> this test fails
echo "=== test: undo goes on after a failed step — every later step runs, the failed one is named, exit non-zero ==="
setup_sandbox
run_setup -- >/dev/null || true
cp "$BAK" "${SANDBOX}/evil"
rm "$BAK"
ln -s "${SANDBOX}/evil" "$BAK"
rc=0
run_setup -- --undo || rc=$?
check "undo exits non-zero" test "$rc" != 0
expect_step cloud-init-sudoers failed
for s in firewall-unit firewall-ipv4 firewall-ipv6 sudoers-rule; do
    expect_step "$s" "done"
done
check_not "the firewall chains are removed" grep -q PILOT_AGENT_EGRESS "${STATE_DIR}/iptables.rules" "${STATE_DIR}/ip6tables.rules"
check_not "the sudoers rule file is removed" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check_not "the unit file is removed" test -e "${SYSTEMD_DIR}/pilot-agent-firewall.service"
check "the failed step is named at the end" has_line "$OUT" "undo: failed steps: cloud-init-sudoers (every other step ran; see the summary)"
for s in $UNDO_STEPS; do
    check "summary line for undo step ${s}" test -n "$(step_state "$s")"
done
teardown_sandbox

echo "=== test: undo stops the processes of the agent user before the firewall goes, and says how many ==="
setup_sandbox
run_setup -- >/dev/null || true
mkdir -p "${STATE_DIR}/procs"
printf '4101\n4102\n' >"${STATE_DIR}/procs/pilot-agent"
: >"$MUT_LOG"
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
check "undo says how many processes it stops" has_line "$OUT" "stopping 2 process(es) of pilot-agent: TERM, then KILL after 5 seconds"
expect_step agent-processes "done"
check "the summary says how many stopped" grep -qF "step agent-processes: done - 2 process(es) of pilot-agent stopped" "$OUT"
check_not "no process of the agent user is left" test -s "${STATE_DIR}/procs/pilot-agent"
check_not "no KILL after a TERM that worked" grep -q '^pkill -KILL ' "$MUT_LOG"
term_n="$(first_line_of "$MUT_LOG" '^pkill -TERM -u pilot-agent$')"
fw_n="$(first_line_of "$MUT_LOG" '^ip6?tables ')"
unit_n="$(first_line_of "$MUT_LOG" '^systemctl ')"
if [ -n "$term_n" ] && [ -n "$fw_n" ] && [ -n "$unit_n" ] && [ "$term_n" -lt "$fw_n" ] && [ "$term_n" -lt "$unit_n" ]; then
    pass "TERM before the first firewall and unit change"
else
    fail_test "TERM line '${term_n}', first firewall line '${fw_n}', first unit line '${unit_n}'"
fi
: >"$MUT_LOG"
run_setup -- --undo >/dev/null || true
expect_step agent-processes skipped
check "a second undo changes nothing" test ! -s "$MUT_LOG"
teardown_sandbox

echo "=== test: processes that outlive TERM get KILL after 5 seconds ==="
setup_sandbox
run_setup -- >/dev/null || true
mkdir -p "${STATE_DIR}/procs"
printf '4101\n' >"${STATE_DIR}/procs/pilot-agent"
: >"$CALL_LOG"
if run_setup MOCK_PROC_TERM_IGNORED=1 -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
check "TERM, then KILL" test -n "$(first_line_of "$CALL_LOG" '^pkill -TERM ')" -a "$(first_line_of "$CALL_LOG" '^pkill -TERM ')" -lt "$(first_line_of "$CALL_LOG" '^pkill -KILL -u pilot-agent$' || echo 0)"
check "five seconds of waiting before the KILL" test "$(sed -n "1,$(first_line_of "$CALL_LOG" '^pkill -KILL ')p" "$CALL_LOG" | grep -c '^sleep 1$' | tr -d ' ')" = 5
check_not "no process of the agent user is left" test -s "${STATE_DIR}/procs/pilot-agent"
expect_step agent-processes "done"
teardown_sandbox

echo "=== test: a process that outlives KILL fails the step; the firewall still goes, exit non-zero ==="
setup_sandbox
run_setup -- >/dev/null || true
mkdir -p "${STATE_DIR}/procs"
printf '4101\n' >"${STATE_DIR}/procs/pilot-agent"
rc=0
run_setup MOCK_PROC_UNKILLABLE=1 -- --undo || rc=$?
check "undo exits non-zero" test "$rc" != 0
expect_step agent-processes failed
expect_step firewall-ipv4 "done"
expect_step sudoers-rule "done"
check "the failed step is named at the end" has_line "$OUT" "undo: failed steps: agent-processes (every other step ran; see the summary)"
teardown_sandbox

# mutation pin: the sudoers-rule step runs last in undo -> this test fails
echo "=== test: undo removes the sudo rule first, before the stop of the agent processes ==="
setup_sandbox
run_setup -- >/dev/null || true
mkdir -p "${STATE_DIR}/procs"
printf '4101\n' >"${STATE_DIR}/procs/pilot-agent"
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
rm_n="$(first_line_of "$OUT" "^\+ rm -f ${SUDOERS_DIR}/90-pilot-agent-runas ")"
pkill_n="$(first_line_of "$OUT" '^\+ pkill ')"
if [ -n "$rm_n" ] && [ -n "$pkill_n" ] && [ "$rm_n" -lt "$pkill_n" ]; then
    pass "the removal of the sudo rule comes before the first pkill"
else
    fail_test "sudo rule removal line '${rm_n}', first pkill line '${pkill_n}'"
fi
check_not "the sudoers rule file is removed" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
teardown_sandbox

# mutation pin: a failed pgrep reads as "no process" again -> this test fails
for pgrep_rc in 127 2; do
    echo "=== test: undo with a pgrep that exits ${pgrep_rc} — the step fails, the firewall still goes, exit non-zero ==="
    setup_sandbox
    run_setup -- >/dev/null || true
    mkdir -p "${STATE_DIR}/procs"
    printf '4101\n' >"${STATE_DIR}/procs/pilot-agent"
    rc=0
    run_setup MOCK_PGREP_RC="$pgrep_rc" -- --undo || rc=$?
    check "undo exits non-zero" test "$rc" != 0
    expect_step agent-processes failed
    if [ "$pgrep_rc" = 127 ]; then
        check "the line says pgrep is not found" grep -qF "pgrep not found (exit 127): the processes of pilot-agent are not known; none is stopped" "$OUT"
    else
        check "the line says pgrep failed" grep -qF "pgrep -u pilot-agent failed (exit 2): the processes of pilot-agent are not known; none is stopped" "$OUT"
    fi
    check_not "no pkill ran" grep -q '^pkill ' "$CALL_LOG"
    for s in firewall-unit firewall-ipv4 firewall-ipv6; do
        expect_step "$s" "done"
    done
    check_not "the firewall chains are removed" grep -q PILOT_AGENT_EGRESS "${STATE_DIR}/iptables.rules" "${STATE_DIR}/ip6tables.rules"
    check "the failed step is named at the end" has_line "$OUT" "undo: failed steps: agent-processes (every other step ran; see the summary)"
    teardown_sandbox
done

echo "=== test: undo with a pgrep that exits 1 (no process) — the step is skipped, exit 0 ==="
setup_sandbox
run_setup -- >/dev/null || true
if run_setup MOCK_PGREP_RC=1 -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
expect_step agent-processes skipped
check "the summary says there is no process" grep -qF "step agent-processes: skipped - no process of pilot-agent" "$OUT"
teardown_sandbox

echo "=== test: failing visudo — no sudoers file, non-zero exit, nothing after it ==="
setup_sandbox
if run_setup MOCK_VISUDO_FAIL=1 -- ; then
    fail_test "apply should exit non-zero when visudo rejects the rule"
else
    pass "apply exits non-zero when visudo rejects the rule"
fi
if [ ! -e "${SUDOERS_DIR}/90-pilot-agent-runas" ] && [ -z "$(find "$SUDOERS_DIR" -name '*pilot-agent*' ! -name '*.bak')" ]; then
    pass "no sudoers file and no temporary file"
else
    find "$SUDOERS_DIR"
    fail_test "a sudoers file exists after a failed validation"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ]; then
    pass "the cloud-init file is untouched"
else
    fail_test "cloud-init file changed after a failed validation"
fi
expect_step firewall-ipv4 "done"
expect_step cloud-init-sudoers skipped
expect_step agent-proof skipped
check_not "the run stopped: no proof as the agent user" grep -q '^runuser -u pilot-agent -- touch ' "$CALL_LOG"
expect_step sudoers-rule failed
teardown_sandbox

echo "=== test: cloud-init validation fails — backup is restored, non-zero exit ==="
setup_sandbox
if run_setup MOCK_VISUDO_C_FAIL=1 -- ; then
    fail_test "apply should exit non-zero when visudo -c fails"
else
    pass "apply exits non-zero when visudo -c fails"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ]; then
    pass "cloud-init file restored from the backup"
else
    fail_test "cloud-init file left edited: $(cat "${SUDOERS_DIR}/90-cloud-init-users")"
fi
expect_step cloud-init-sudoers failed
teardown_sandbox

echo "=== test: --keep-daemon-sudo leaves the cloud-init file untouched ==="
setup_sandbox
if run_setup NO_CONFIRM=1 -- --keep-daemon-sudo; then
    pass "apply --keep-daemon-sudo exits 0 without --confirm-root-path"
else
    cat "$OUT"
    fail_test "apply --keep-daemon-sudo exited non-zero"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ] && [ ! -e "$BAK" ]; then
    pass "cloud-init file unchanged, no backup made"
else
    fail_test "cloud-init file touched despite --keep-daemon-sudo"
fi
if [ -e "${SUDOERS_DIR}/90-pilot-agent-runas" ]; then
    pass "the run-as rule is still installed"
else
    fail_test "run-as rule missing"
fi
expect_step cloud-init-sudoers skipped
teardown_sandbox

# mutation pin: the stale-temp-file rm -f runs outside run in dry-run (deletes a leftover, prints nothing) -> this test fails
echo "=== test: dry run leaves a temporary sudoers file of an interrupted run and prints its removal ==="
setup_sandbox
printf 'stale\n' >"${SUDOERS_DIR}/.90-pilot-agent-runas.new"
if run_setup -- --dry-run; then
    pass "dry run exits 0 with a leftover temporary file"
else
    cat "$OUT"
    fail_test "dry run exited non-zero with a leftover temporary file"
fi
if [ -e "${SUDOERS_DIR}/.90-pilot-agent-runas.new" ]; then
    pass "the leftover temporary file is still there"
else
    fail_test "dry run deleted the leftover temporary file"
fi
if grep -qF -- "+ rm -f ${SUDOERS_DIR}/.90-pilot-agent-runas.new" "$OUT"; then
    pass "dry run prints the removal it would do"
else
    fail_test "removal of the leftover temporary file is not printed"
fi
teardown_sandbox

# mutation pin: a failed step exits at once, the later steps get no summary line -> this test fails
echo "=== test: a failed step still leaves one summary line per step ==="
setup_sandbox
run_setup MOCK_VISUDO_FAIL=1 -- || true
for s in $ALL_STEPS; do
    if [ -n "$(step_state "$s")" ]; then
        pass "summary line for step ${s}"
    else
        fail_test "no summary line for step ${s}"
    fi
done
for s in cloud-init-sudoers agent-proof; do
    expect_step "$s" skipped
done
expect_step sudoers-rule failed
teardown_sandbox

# mutation pin: the cloud-init restore uses 0440 root:root, not the mode and owner of the file -> this test fails
echo "=== test: the cloud-init restore keeps the mode and owner of the file ==="
setup_sandbox
chmod 640 "${SUDOERS_DIR}/90-cloud-init-users"
set_meta "${SUDOERS_DIR}/90-cloud-init-users" root:wheel
run_setup MOCK_VISUDO_C_FAIL=1 -- || true
if grep -qF "install -m 640 -o root -g wheel ${BAK} ${SUDOERS_DIR}/90-cloud-init-users" "$CALL_LOG" \
    && [ "$(mode_of "${SUDOERS_DIR}/90-cloud-init-users")" = "640" ]; then
    pass "restore after a failed validation keeps mode 640 and group wheel"
else
    grep -F "90-cloud-init-users" "$CALL_LOG" || true
    fail_test "restore after a failed validation lost the mode or the owner"
fi
run_setup -- || true
: >"$CALL_LOG"
run_setup -- --undo || true
if grep -qF "install -m 640 -o root -g wheel ${BAK} ${SUDOERS_DIR}/90-cloud-init-users" "$CALL_LOG"; then
    pass "undo keeps mode 640 and group wheel"
else
    grep -F "90-cloud-init-users" "$CALL_LOG" || true
    fail_test "undo restored the cloud-init file with another mode or owner"
fi
teardown_sandbox

# mutation pin: undo restores the cloud-init file without a visudo -c afterwards -> this test fails
echo "=== test: undo validates the restored cloud-init file ==="
setup_sandbox
run_setup -- >/dev/null || true
: >"$CALL_LOG"
if run_setup -- --undo; then
    pass "undo exits 0 when the validation passes"
else
    cat "$OUT"
    fail_test "undo failed with a valid sudoers setup"
fi
inst="$(first_line '^install .*90-cloud-init-users.pilot-agent.bak ' || true)"
val="$(first_line '^visudo -c$' || true)"
if [ -n "$inst" ] && [ -n "$val" ] && [ "$val" -gt "$inst" ]; then
    pass "visudo -c runs after the restore"
else
    fail_test "no visudo -c after the restore (install line '${inst}', visudo line '${val}')"
fi
teardown_sandbox
setup_sandbox
run_setup -- >/dev/null || true
if run_setup MOCK_VISUDO_C_FAIL=1 -- --undo; then
    fail_test "undo must fail when visudo -c rejects the restored file"
else
    pass "undo exits non-zero when visudo -c rejects the restored file"
fi
expect_step cloud-init-sudoers failed
if [ -e "$BAK" ]; then
    pass "the backup is kept after a failed validation"
else
    fail_test "the backup was removed after a failed validation"
fi
teardown_sandbox

# mutation pin: an existing backup is reused, so undo restores content that is older than the last apply -> this test fails
echo "=== test: a second apply refreshes a stale backup and keeps the older one numbered ==="
setup_sandbox
run_setup -- >/dev/null || true
REWRITTEN="${CLOUD_INIT_ORIGINAL}
# rewritten by cloud-init after the first apply"
printf '%s\n' "$REWRITTEN" >"${SUDOERS_DIR}/90-cloud-init-users"
if run_setup -- ; then
    pass "second apply on a changed cloud-init file exits 0"
else
    cat "$OUT"
    fail_test "second apply on a changed cloud-init file failed"
fi
if [ "$(cat "$BAK")" = "$REWRITTEN" ]; then
    pass "backup holds the file as it was before the second apply"
else
    fail_test "backup is stale"
fi
if [ "$(cat "${BAK}.1")" = "$CLOUD_INIT_ORIGINAL" ]; then
    pass "the older backup is kept as .1"
else
    fail_test "the older backup is gone or different"
fi
REWRITTEN2="${REWRITTEN}
# rewritten again"
printf '%s\n' "$REWRITTEN2" >"${SUDOERS_DIR}/90-cloud-init-users"
run_setup -- >/dev/null || true
if [ "$(cat "${BAK}.1")" = "$CLOUD_INIT_ORIGINAL" ] && [ "$(cat "${BAK}.2")" = "$REWRITTEN" ] && [ "$(cat "$BAK")" = "$REWRITTEN2" ]; then
    pass "a third apply numbers the second backup as .2"
else
    fail_test "numbered backups wrong after the third apply"
fi
run_setup -- --undo >/dev/null || true
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$REWRITTEN2" ]; then
    pass "undo restores the newest backup"
else
    fail_test "undo restored stale content: $(cat "${SUDOERS_DIR}/90-cloud-init-users")"
fi
if grep -qF "from ${BAK}, the newest backup with an active root line" "$OUT" && [ -e "${BAK}.1" ] && [ -e "${BAK}.2" ]; then
    pass "undo names the backup and keeps the numbered ones"
else
    fail_test "undo output or the numbered backups are wrong"
fi
teardown_sandbox

# mutation pin: the port 9091 rule covers only IPv4 127.0.0.1, not ::1 or the loopback interface -> this test fails
echo "=== test: the gateway port is rejected on ::1 and, on the loopback interface, on every local address ==="
setup_sandbox
run_setup -- >/dev/null || true
LOCAL_RULE="-p tcp --dport 9091 -m addrtype --dst-type LOCAL -o lo -j REJECT --reject-with tcp-reset"
if grep -qF -- "-p tcp -d ::1 --dport 9091 -j REJECT" "${STATE_DIR}/ip6tables.rules"; then
    pass "ip6tables rejects tcp to ::1:9091"
else
    fail_test "no ip6tables rule for ::1:9091"
fi
for f in iptables ip6tables; do
    if grep -qF -- "$LOCAL_RULE" "${STATE_DIR}/${f}.rules" \
        && grep -qF -- "-A PILOT_AGENT_EGRESS_NEW ${LOCAL_RULE}" "${SYSTEMD_DIR}/pilot-agent-firewall.service"; then
        pass "${f}: port 9091 rejected on the loopback interface for any local address (chain and unit)"
    else
        fail_test "${f}: no rule for port 9091 on the loopback interface"
    fi
done
teardown_sandbox

# ----------------------------------------------------------- validation

# mutation pin: the system-directory check is removed from the --data-dir validation -> this test fails
echo "=== test: --data-dir that is a system directory, or one above it, is refused before any change ==="
# Each value must be refused by this check, not by another one (the mocks report
# / as owned by root, and /var/lib does not exist on macOS): the message is
# checked per value.
setup_sandbox
for sysdir in / /usr /var/lib /etc /var /usr/; do
    set_meta "$sysdir" ec2-user:ec2-user
    expect_refused "--data-dir ${sysdir}" -- --data-dir "$sysdir"
    if grep -qE "^setup-agent-user: --data-dir '[^']*' (is a system directory|contains the system directory )" "$OUT"; then
        pass "--data-dir ${sysdir}: refused as a system directory"
    else
        fail_test "--data-dir ${sysdir}: not refused as a system directory: $(cat "$OUT")"
    fi
done
teardown_sandbox

echo "=== test: --data-dir must exist, be a directory owned by the daemon user, have no '..' and no leading dash ==="
setup_sandbox
mkdir "${SANDBOX}/foreign"
set_meta "${SANDBOX}/foreign" somebody:somebody
touch "${SANDBOX}/plainfile"
set_meta "${SANDBOX}/plainfile" ec2-user:ec2-user
expect_refused "missing directory" -- --data-dir "${SANDBOX}/nope"
expect_refused "a file, not a directory" -- --data-dir "${SANDBOX}/plainfile"
expect_refused "owned by another user" -- --data-dir "${SANDBOX}/foreign"
expect_refused "a '..' part" -- --data-dir "${DATA_DIR}/../data"
expect_refused "a leading dash" -- --data-dir "-${DATA_DIR}"
expect_refused "a relative path" -- --data-dir "data"
ln -s "$DATA_DIR" "${SANDBOX}/datalink"
if run_setup -- --data-dir "${SANDBOX}/datalink" --dry-run; then
    pass "a symbolic link to a good data dir is canonicalised and accepted"
else
    cat "$OUT"
    fail_test "a symlink to a good data dir was refused"
fi
check "the canonical path is used" grep -qF "install -d -o pilot-agent -g pilot -m 750 ${AHOME}" "$OUT"
teardown_sandbox

echo "=== test: user and group arguments ==="
setup_sandbox
printf '999:/bin/bash:lowuser\n' >"${STATE_DIR}/users/lowuser"
printf '0:/usr/sbin/nologin:rootish\n' >"${STATE_DIR}/users/rootish"
printf '1001:/bin/bash:logger\n' >"${STATE_DIR}/users/logger"
printf '1002:/usr/sbin/nologin:adminish\n' >"${STATE_DIR}/users/adminish"
printf '1003:/usr/sbin/nologin:dockerish\n' >"${STATE_DIR}/users/dockerish"
printf '1004:/usr/sbin/nologin:disk\n' >"${STATE_DIR}/users/diskish"
mkdir -p "${STATE_DIR}/members"
echo adminish >"${STATE_DIR}/members/wheel"
echo dockerish >"${STATE_DIR}/members/docker"
expect_refused "--daemon-user root" -- --daemon-user root
expect_refused "--daemon-user with uid below 1000" -- --daemon-user lowuser
expect_refused "--daemon-user that does not exist" -- --daemon-user nobody-here
expect_refused "--agent-user root" -- --agent-user root
expect_refused "--agent-user with uid 0" -- --agent-user rootish
expect_refused "--agent-user with a login shell" -- --agent-user logger
expect_refused "--agent-user in wheel" -- --agent-user adminish
expect_refused "--agent-user in docker" -- --agent-user dockerish
check "one line names the reason (docker)" has_line "$OUT" "setup-agent-user: the agent user 'dockerish' is a member of docker, a system group with privileges"
expect_refused "--agent-user with the primary group disk" -- --agent-user diskish
check "one line names the reason (disk)" has_line "$OUT" "setup-agent-user: the agent user 'diskish' is a member of disk, a system group with privileges"
expect_refused "--agent-user equal to the daemon user" -- --agent-user ec2-user
for g in root wheel sudo adm disk docker shadow; do
    expect_refused "--group ${g}" -- --group "$g"
    check "one line names the reason (--group ${g})" grep -qxE "setup-agent-user: --group must not be ${g}: a system group with privileges \(.*\)" "$OUT"
    expect_refused "--agent-user ${g}" -- --agent-user "$g"
done
expect_refused "--group ec2-user (primary group of the daemon user)" -- --group ec2-user
for bad in "Pilot" "1pilot" "_pilot" "pilot agent" "pilot;id" "-pilot" "pilot.x" ""; do
    expect_refused "--agent-user '${bad}'" -- --agent-user "$bad"
    expect_refused "--group '${bad}'" -- --group "$bad"
done
teardown_sandbox

echo "=== test: an existing agent user with a no-login shell outside the privileged groups is accepted ==="
setup_sandbox
printf '1003:/sbin/nologin:pilot\n' >"${STATE_DIR}/users/pilot-agent"
mkdir -p "${STATE_DIR}/groups" && touch "${STATE_DIR}/groups/pilot"
if run_setup -- --dry-run; then
    pass "accepted"
else
    cat "$OUT"
    fail_test "a good existing agent user was refused"
fi
teardown_sandbox

# fw_in_place STEP — the firewall step ran: done, or skipped as already in place
# (not "skipped - not run").
fw_in_place() {
    case "$(step_state "$1")" in done | skipped) return 0 ;; esac
    return 1
}

# expect_dirs_failed LABEL — the apply exits 1 at the directories step, after the
# firewall steps, and installs no sudoers rule.
expect_dirs_failed() {
    local rc=0
    run_setup -- || rc=$?
    if [ "$rc" = 1 ] && [ "$(step_state directories)" = failed ] && fw_in_place firewall-ipv4 \
        && fw_in_place firewall-ipv6 && fw_in_place firewall-unit && [ ! -e "${SUDOERS_DIR}/90-pilot-agent-runas" ]; then
        pass "the directories step fails after the firewall steps, no sudoers rule: $1"
    else
        cat "$OUT"
        fail_test "$1: exit ${rc}, directories '$(step_state directories)', firewall-ipv4 '$(step_state firewall-ipv4)'"
    fi
}

echo "=== test: the agent root as a link is refused; a link, a file or a FIFO at a directory of the agent user fails the directories step, not the firewall ==="
setup_sandbox
mkdir "${SANDBOX}/outside"
outside_before="$(mode_of "${SANDBOX}/outside")"
ln -s "${SANDBOX}/outside" "$AROOT"
expect_refused "the agent root is a symbolic link" --
rm "$AROOT"
mkdir "$AROOT"
set_meta "$AROOT" root:root
ln -s "${SANDBOX}/outside" "$AHOME"
expect_dirs_failed "the agent home is a symbolic link"
check "the step names the link" grep -qF "'${AHOME}' is a symbolic link; refused" "$OUT"
rm "$AHOME"
ln -s "${SANDBOX}/outside" "$AWS"
expect_dirs_failed "the workspace root is a symbolic link"
rm "$AWS"
printf 'x\n' >"$AWS"
expect_dirs_failed "the workspace root is a file"
check "the step names the file" grep -qF "'${AWS}' is not a directory (a file or a FIFO); refused" "$OUT"
rm "$AWS"
mkdir -p "${AHOME}/go"
mkfifo "${AHOME}/go/cache"
expect_dirs_failed "go/cache is a FIFO"
check "the step names the FIFO" grep -qF "'${AHOME}/go/cache' is not a directory (a file or a FIFO); refused" "$OUT"
check "the directory outside is untouched" test "$(mode_of "${SANDBOX}/outside")" = "$outside_before"
teardown_sandbox

# ------------------------------------------- item 2: no access to the daemon

echo "=== test: a data dir entry open to others fails the check — one line per entry, nothing changed ==="
setup_sandbox
build_repos
chmod 755 "$R" "${DATA_DIR}/bin"
mkdir "${DATA_DIR}/pilot-home"
chmod 711 "${DATA_DIR}/pilot-home"
printf 'x\n' >"${DATA_DIR}/.hidden"
chmod 604 "${DATA_DIR}/.hidden"
mkdir "${DATA_DIR}/target"
chmod 755 "${DATA_DIR}/target"
ln -s "${DATA_DIR}/target" "${DATA_DIR}/linked"
ln -s "${SANDBOX}/gone" "${DATA_DIR}/dangling"
before="$(tree_listing)"
: >"$MUT_LOG"
if run_setup -- ; then
    fail_test "apply must fail while the daemon's files are open to the agent user"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
expect_step group skipped
check "nothing was mutated" test ! -s "$MUT_LOG"
check "nothing changed in the temp root" test "$before" = "$(tree_listing)"
# mutation pin: a chmod path is printed without shell quoting -> this test fails
for want in "the agent user can reach '${R}' (group $(stat -c %G "$R" 2>/dev/null || stat -f %Sg "$R"), mode 755): runuser -u ec2-user -- chmod o-rwx '${R}'" \
    "the agent user can reach '${DATA_DIR}/bin' (group $(stat -c %G "$R" 2>/dev/null || stat -f %Sg "$R"), mode 755): runuser -u ec2-user -- chmod o-rwx '${DATA_DIR}/bin'"; do
    check "prints: ${want#the agent user can reach }" has_line "$OUT" "$want"
done
check "the pilot home at 0711 is reported (traverse is enough)" grep -qF "can reach '${DATA_DIR}/pilot-home' " "$OUT"
check "a hidden file is reported" grep -qF "can reach '${DATA_DIR}/.hidden' " "$OUT"
check "a link is followed and its target named" grep -qF "can reach '${DATA_DIR}/linked' (group $(stat -c %G "$R" 2>/dev/null || stat -f %Sg "$R"), mode 755): runuser -u ec2-user -- chmod o-rwx '${DATA_DIR}/target'" "$OUT"
check_not "a dangling link is not reported" grep -qF "${DATA_DIR}/dangling" "$OUT"
check "six lines, one per open entry" test "$(grep -c 'can reach ' "$OUT" | tr -d ' ')" = 6
check "every chmod line of the apply runs chmod as the daemon user" chmods_as_daemon "$OUT"
check "the text before the lines says to run them as root, as they are" grep -qF "run each chmod line below as root, as it is: runuser runs chmod as ec2-user" "$OUT"
chmod o-rwx "$R" "${DATA_DIR}/bin" "${DATA_DIR}/pilot-home" "${DATA_DIR}/.hidden" "${DATA_DIR}/target"
if run_setup -- ; then
    pass "apply passes once each entry is closed"
else
    cat "$OUT"
    fail_test "apply failed after the chmods"
fi
check "the file of the working tree keeps its mode" test "$(mode_of "$R/alpha/.env")" = 644
teardown_sandbox

echo "=== test: an entry in the shared group with a group bit fails the check ==="
setup_sandbox
mkdir "${DATA_DIR}/shared"
chmod 750 "${DATA_DIR}/shared"
set_meta "${DATA_DIR}/shared" ec2-user:pilot
if run_setup -- ; then
    fail_test "apply must fail on an entry of the shared group"
else
    pass "apply exits non-zero"
fi
check "prints the chmod for group and others" has_line "$OUT" "the agent user can reach '${DATA_DIR}/shared' (group pilot, mode 750): runuser -u ec2-user -- chmod go-rwx '${DATA_DIR}/shared'"
teardown_sandbox

# ------------------------------------------- the record and the chmod lines take plain paths only

# mutation pin: the result of realpath is recorded without plain_data_path -> this test fails
echo "=== test: a link whose target path holds a newline forges no line in the record nor in the undo output ==="
setup_sandbox
build_repos
chmod 755 "$R"
FORGED_TOP="${DATA_DIR}/x"$'\n'"4755"$'\t'
mkdir -p "${FORGED_TOP}/usr/bin/su"
chmod 700 "$FORGED_TOP"
chmod 755 "${FORGED_TOP}/usr/bin/su"
ln -s "${FORGED_TOP}/usr/bin/su" "${DATA_DIR}/evil"
if run_setup -- ; then
    fail_test "apply must fail on a link with a newline in its target"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
check "the record holds the plain line only" test "$(cat "$RECORD" 2>/dev/null)" = "755	${R}"
check_not "the record has no forged line" grep -qF "/usr/bin/su" "$RECORD"
check "the link is named, quoted, for a look by hand" has_line "$OUT" "the entry '${DATA_DIR}/evil' is not a plain path in the data dir (a name or a link target outside [A-Za-z0-9._/-], or a link out of the data dir): no chmod is printed, look at it by hand"
check_not "no line of the apply output starts with the forged mode" grep -qE '^4755' "$OUT"
check_not "no chmod for the target of the link" grep -qE 'chmod [go]+-rwx? .*usr/bin/su' "$OUT"
run_setup -- --undo >/dev/null || true
check "undo prints the plain line" has_line "$OUT" "  runuser -u ec2-user -- chmod 755 '${R}'"
check_not "undo prints no chmod 4755" grep -qF "chmod 4755" "$OUT"
check_not "undo prints nothing of the forged path" grep -qF "/usr/bin/su" "$OUT"
teardown_sandbox

echo "=== test: an entry name with a newline forges no line in the apply output ==="
setup_sandbox
build_repos
# a name holds no slash: the forged line names a relative path
FORGED_LINE="the agent user can reach etc (group root, mode 755): chmod o-rwx etc"
mkdir "${DATA_DIR}/a"$'\n'"${FORGED_LINE}"
chmod 755 "${DATA_DIR}/a"$'\n'"${FORGED_LINE}"
if run_setup -- ; then
    fail_test "apply must fail on an entry name with a newline"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
check_not "no forged line in the apply output" grep -qE "^the agent user can reach etc " "$OUT"
check "the name is printed on one line, quoted" grep -qE "^the entry [\$]'${DATA_DIR}/a.*the agent user can reach etc .*' is not a plain path in the data dir" "$OUT"
check "one line names the entry" test "$(grep -c '^the entry .* is not a plain path in the data dir' "$OUT" | tr -d ' ')" = 1
check "no record is written for it" test ! -e "$RECORD"
teardown_sandbox

echo "=== test: a link to a directory outside the data dir fails the apply and names the entry ==="
setup_sandbox
build_repos
mkdir "${SANDBOX}/elsewhere"
chmod 755 "${SANDBOX}/elsewhere"
ln -s "${SANDBOX}/elsewhere" "${DATA_DIR}/linked"
: >"$MUT_LOG"
if run_setup -- ; then
    fail_test "apply must fail on a link out of the data dir"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
check "nothing was mutated" test ! -s "$MUT_LOG"
check "the entry is named" grep -qF "the entry '${DATA_DIR}/linked' is not a plain path in the data dir" "$OUT"
check_not "no chmod on the target outside" grep -qF "${SANDBOX}/elsewhere" "$OUT"
check "no record is written for it" test ! -e "$RECORD"
# mutation pin: the closing sentence names the chmod lines with none printed -> this test fails
check_not "only a refused entry: the closing sentence names no chmod line" grep -qF "run each chmod" "$OUT"
check "only a refused entry: the closing sentence says to look by hand" has_line "$OUT" "no mode was changed; look at each entry above by hand and run again"
chmod 700 "${SANDBOX}/elsewhere"
check_not "a closed target outside fails the apply too" run_setup --
rm "${DATA_DIR}/linked"
if run_setup -- ; then
    pass "apply passes once the link is gone"
else
    cat "$OUT"
    fail_test "apply failed after the link was removed"
fi
teardown_sandbox

echo "=== test: a path with a space is printed quoted ==="
setup_sandbox
build_repos
mkdir "${DATA_DIR}/with space"
chmod 755 "${DATA_DIR}/with space"
if run_setup -- ; then
    fail_test "apply must fail on an entry with a space"
else
    pass "apply exits non-zero"
fi
check "the entry is printed in quotes" grep -qF "the entry '${DATA_DIR}/with space' is not a plain path in the data dir" "$OUT"
check_not "it is printed nowhere without quotes" grep -qE "(^|[^'])${DATA_DIR}/with space" "$OUT"
check "no record is written for it" test ! -e "$RECORD"
teardown_sandbox

# shq_c1_case — shq as the script defines it, in the C locale the script sets: a
# name with U+009B (0xC2 0x9B) and a name with the byte 0x9B alone, a C1 control
# for a terminal, come out in printable ASCII only, and a shell reads each back
# as it was. Called once, below.
# mutation pin: shq takes [[:cntrl:]] for the escaped form again -> this test fails
shq_c1_case() {
    local def name out back
    def="$(sed -n '/^shq() {$/,/^}$/p' "$SETUP_SCRIPT")"
    for name in "a"$'\xc2\x9b'"b" "a"$'\x9b'"b"; do
        out="$(LC_ALL=C /bin/bash -c "${def}"$'\n''shq "$1"' _ "$name")"
        back=""
        eval "back=${out}"
        if [ -n "$out" ] && [ -z "$(printf '%s' "$out" | LC_ALL=C tr -d ' -~')" ] && [ "$back" = "$name" ]; then
            pass "shq prints $(printf '%s' "$name" | od -An -tx1 | tr -s ' ') as ${out}"
        else
            fail_test "shq prints a byte outside printable ASCII for $(printf '%s' "$name" | od -An -tx1 | tr -s ' '): $(printf '%s' "$out" | od -An -tx1 | tr -s ' ')"
        fi
    done
}

echo "=== test: shq escapes a name with a C1 control byte ==="
shq_c1_case

# mutation pin: the line export LC_ALL=C is removed -> this test fails
echo "=== test: the commands of the script run in the C locale, whatever locale it starts in ==="
setup_sandbox
build_repos
LOCALE_LOG="${SANDBOX}/locale.log"
run_setup LC_ALL=en_US.UTF-8 MOCK_LOCALE_LOG="$LOCALE_LOG" -- --dry-run || true
check "a mocked command ran" test -s "$LOCALE_LOG"
check_not "every mocked command sees LC_ALL=C" grep -qvx '[a-z0-9]* LC_ALL=C' "$LOCALE_LOG"
teardown_sandbox

echo "=== test: an open entry of root (the parent of a nested agent root) gets no chmod and no record; the apply fails and names its owner ==="
setup_sandbox
build_repos
mkdir "${DATA_DIR}/outer"
chmod 755 "${DATA_DIR}/outer"
set_meta "${DATA_DIR}/outer" root:root
: >"$MUT_LOG"
if run_setup -- --agent-root "${DATA_DIR}/outer/agent"; then
    fail_test "apply must fail on an open entry of root"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
check "nothing was mutated" test ! -s "$MUT_LOG"
check "one line names the entry and its owner, for a look by hand" has_line "$OUT" "the entry '${DATA_DIR}/outer' belongs to root, not to ec2-user (group root, mode 755): no chmod is printed, look at it by hand"
check "one line names an entry" test "$(grep -c '^the entry ' "$OUT" | tr -d ' ')" = 1
check_not "no chmod line is printed" grep -qE "chmod ([go]+-rwx?|[0-7]{3,4}) " "$OUT"
check "no record is written" test ! -e "$RECORD"
check_not "the closing sentence names no chmod line" grep -qF "run each chmod" "$OUT"
check "the closing sentence says to look by hand" has_line "$OUT" "no mode was changed; look at each entry above by hand and run again"
teardown_sandbox

# mutation pin: daemon-private accepts a link to the data dir -> this test fails
echo "=== test: a link to the data dir, to the agent root or into it gets no chmod; the apply fails and names it ==="
for target in data-dir agent-root agent-home; do
    setup_sandbox
    build_repos
    mkdir -p "$AHOME"
    chmod 755 "$AROOT"
    chmod 750 "$AHOME"
    set_meta "$AROOT" root:root
    set_meta "$AHOME" pilot-agent:pilot
    case "$target" in
        data-dir) ln -s "$DATA_DIR" "${DATA_DIR}/self" ;;
        agent-root) ln -s "$AROOT" "${DATA_DIR}/self" ;;
        agent-home) ln -s "$AHOME" "${DATA_DIR}/self" ;;
    esac
    : >"$MUT_LOG"
    if run_setup -- ; then
        fail_test "${target}: apply must fail on a link to the ${target}"
    else
        pass "${target}: apply exits non-zero"
    fi
    expect_step daemon-private failed
    check "${target}: nothing was mutated" test ! -s "$MUT_LOG"
    check "${target}: one line names the entry, quoted" has_line "$OUT" "the entry '${DATA_DIR}/self' resolves to the data dir or to the agent root or a path in it: no chmod is printed, look at it by hand"
    check_not "${target}: no chmod line is printed" grep -qE "chmod ([go]+-rwx?|[0-7]{3,4}) " "$OUT"
    check "${target}: no record is written" test ! -e "$RECORD"
    rm "${DATA_DIR}/self"
    if run_setup -- ; then
        pass "${target}: apply passes once the link is gone"
    else
        cat "$OUT"
        fail_test "${target}: apply failed after the link was removed"
    fi
    teardown_sandbox
done

echo "=== test: a link to a sibling directory whose name starts with the name of the data dir is a link out of it ==="
setup_sandbox
build_repos
mkdir "${DATA_DIR}-sibling"
chmod 755 "${DATA_DIR}-sibling"
ln -s "${DATA_DIR}-sibling" "${DATA_DIR}/sib"
if run_setup -- ; then
    fail_test "apply must fail on a link to ${DATA_DIR}-sibling"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
check "the entry is named" has_line "$OUT" "the entry '${DATA_DIR}/sib' is not a plain path in the data dir (a name or a link target outside [A-Za-z0-9._/-], or a link out of the data dir): no chmod is printed, look at it by hand"
check_not "no chmod on the sibling" grep -qF "${DATA_DIR}-sibling" "$OUT"
check "no record is written for it" test ! -e "$RECORD"
teardown_sandbox

# mutation pin: the line filter of undo is removed -> this test fails
echo "=== test: undo skips a record line with a special bit or a path outside the data dir, by its number ==="
setup_sandbox
build_repos
printf '755\t%s\n4755\t%s\n755\t/usr/bin\n0750\t%s\n' "$R" "${DATA_DIR}/bin" "${DATA_DIR}/bin" >"$RECORD"
chmod 600 "$RECORD"
run_setup -- --undo >/dev/null || true
check "line 1 is printed" has_line "$OUT" "  runuser -u ec2-user -- chmod 755 '${R}'"
check "line 4 is printed (a leading zero is no special bit)" has_line "$OUT" "  runuser -u ec2-user -- chmod 0750 '${DATA_DIR}/bin'"
check_not "the mode 4755 is not printed" grep -qF "4755" "$OUT"
check_not "the path outside the data dir is not printed" grep -qE "chmod [0-7]+ '?/usr/bin" "$OUT"
check "line 2 is named as skipped" has_line "$OUT" "  # line 2 of the record is skipped: not a mode without a special bit and a plain path in the data dir '${DATA_DIR}'"
check "line 3 is named as skipped" has_line "$OUT" "  # line 3 of the record is skipped: not a mode without a special bit and a plain path in the data dir '${DATA_DIR}'"
check "two lines are skipped" test "$(grep -c '^  # line [0-9]* of the record is skipped' "$OUT" | tr -d ' ')" = 2
check "every chmod line of the undo runs chmod as the daemon user" chmods_as_daemon "$OUT"
teardown_sandbox

echo "=== test: undo skips a record line whose path has a .. part ==="
setup_sandbox
build_repos
printf '755\t%s\n755\t%s\n755\t%s\n' "$R" "${DATA_DIR}/../etc" "${R}/../bin" >"$RECORD"
chmod 600 "$RECORD"
run_setup -- --undo >/dev/null || true
check "line 1 is printed" has_line "$OUT" "  runuser -u ec2-user -- chmod 755 '${R}'"
check "line 2 is named as skipped" has_line "$OUT" "  # line 2 of the record is skipped: not a mode without a special bit and a plain path in the data dir '${DATA_DIR}'"
check "line 3 is named as skipped" has_line "$OUT" "  # line 3 of the record is skipped: not a mode without a special bit and a plain path in the data dir '${DATA_DIR}'"
check_not "no path with a .. part is printed" grep -qF "/../" "$OUT"
check "one chmod line is printed" test "$(grep -c 'chmod 755 ' "$OUT" | tr -d ' ')" = 1
teardown_sandbox

echo "=== test: undo with another data dir than the apply skips every line of the record ==="
setup_sandbox
build_repos
chmod 755 "$R"
run_setup -- >/dev/null || true
check "the apply recorded the repos directory" grep -qxF "755	${R}" "$RECORD"
mkdir "${SANDBOX}/other"
set_meta "${SANDBOX}/other" ec2-user:ec2-user
run_setup -- --undo --data-dir "${SANDBOX}/other" >/dev/null || true
check "the line is named as skipped, with the data dir of the undo" has_line "$OUT" "  # line 1 of the record is skipped: not a mode without a special bit and a plain path in the data dir '${SANDBOX}/other'"
check_not "no chmod line is printed" grep -qE "chmod [0-7]{3,4} " "$OUT"
teardown_sandbox

# ------------------------------------------- item 3: the sudo proof by behaviour

echo "=== test: only exact daemon-user lines with run-as ALL, root or none are commented out ==="
setup_sandbox
cat >"${SUDOERS_DIR}/90-cloud-init-users" <<'EOF'
# Created by cloud-init
Defaults:ec2-user !requiretty
User_Alias OPS = ec2-user, bob
ec2-user ALL=(ALL) NOPASSWD:ALL
ec2-user ALL=(root) NOPASSWD: ALL
ec2-user ALL=NOPASSWD: ALL
ec2-user ALL = (ALL:ALL) ALL
%ec2-user-admins ALL=(ALL) NOPASSWD:ALL
EOF
if run_setup -- ; then
    pass "apply exits 0: sudo -n true fails for the daemon user"
else
    cat "$OUT"
    fail_test "apply failed"
fi
want='# Created by cloud-init
Defaults:ec2-user !requiretty
User_Alias OPS = ec2-user, bob
# disabled by setup-agent-user: ec2-user ALL=(ALL) NOPASSWD:ALL
# disabled by setup-agent-user: ec2-user ALL=(root) NOPASSWD: ALL
# disabled by setup-agent-user: ec2-user ALL=NOPASSWD: ALL
# disabled by setup-agent-user: ec2-user ALL = (ALL:ALL) ALL
%ec2-user-admins ALL=(ALL) NOPASSWD:ALL'
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$want" ]; then
    pass "four lines commented out; Defaults, alias and group lines untouched"
else
    fail_test "cloud-init file: $(cat "${SUDOERS_DIR}/90-cloud-init-users")"
fi
check "the Defaults line is reported" grep -qF "left untouched (Defaults line): ${SUDOERS_DIR}/90-cloud-init-users:2: Defaults:ec2-user !requiretty" "$OUT"
check "the alias line is reported" grep -qF "left untouched (several users, an alias or a host list): ${SUDOERS_DIR}/90-cloud-init-users:3: User_Alias OPS = ec2-user, bob" "$OUT"
expect_step cloud-init-sudoers "done"
teardown_sandbox

echo "=== test: a cloud-init rule with the run-as part (deploy) stops the apply at the check before the first change ==="
setup_sandbox
cat >"${SUDOERS_DIR}/90-cloud-init-users" <<'EOF'
# Created by cloud-init
ec2-user ALL=(ALL) NOPASSWD:ALL
ec2-user ALL=(deploy) NOPASSWD: ALL
EOF
before="$(cat "${SUDOERS_DIR}/90-cloud-init-users")"
if run_setup -- ; then
    fail_test "apply must fail: the (deploy) rule has a run-as user other than the agent user"
else
    pass "apply exits non-zero on a (deploy) rule"
fi
check "the (deploy) entry is named" grep -qE '^  entry [0-9]+ \(run-as user deploy, not pilot-agent alone\):$' "$OUT"
check "the cloud-init file has no change, the root line is still active" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$before"
check_not "no step ran" grep -qx '== daemon-private' "$OUT"
teardown_sandbox

echo "=== test: a NOPASSWD rule left in another file fails the step with that file and line ==="
setup_sandbox
printf '# rules\nec2-user,admin ALL=(ALL) NOPASSWD:ALL\n' >"${SUDOERS_DIR}/10-other"
# late: sudo reads the file only once the agent rule is in place (a rule that
# came after the check before the first change), so the proof meets it
if run_setup MOCK_SUDO_LATE=10-other -- ; then
    fail_test "apply must fail while another file gives the daemon user NOPASSWD"
else
    pass "apply exits non-zero"
fi
check "the proof says sudo -n true succeeded" grep -qF "ec2-user still runs a command as root without a password: sudo -n true succeeded" "$OUT"
check "the file and line are printed" has_line "$OUT" "  ${SUDOERS_DIR}/10-other:2: ec2-user,admin ALL=(ALL) NOPASSWD:ALL"
check "the cloud-init lines are still commented out" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED"
expect_step cloud-init-sudoers failed
teardown_sandbox

# mutation pin: prove_sudo_removed reads the rule text again (NOPASSWD lines of sudo -l) -> this test fails
echo "=== test: Defaults:USER !authenticate with a rule without NOPASSWD in another file fails the step ==="
setup_sandbox
printf 'Defaults:ec2-user !authenticate\n' >"${SUDOERS_DIR}/05-defaults"
printf 'ec2-user ALL=(ALL) ALL\n' >"${SUDOERS_DIR}/10-other"
check "the mocked sudo gives root without a password here" mock_run env MOCK_INVOKER=ec2-user sudo -n true
LATE="MOCK_SUDO_LATE=05-defaults 10-other"
if run_setup "$LATE" -- ; then
    fail_test "apply must fail: the daemon user gets root without a password"
else
    pass "apply exits non-zero"
fi
expect_step cloud-init-sudoers failed
check "the proof says sudo -n true succeeded" grep -qF "sudo -n true succeeded" "$OUT"
check "the Defaults line is named" has_line "$OUT" "  ${SUDOERS_DIR}/05-defaults:1: Defaults:ec2-user !authenticate"
check "the rule is named" has_line "$OUT" "  ${SUDOERS_DIR}/10-other:1: ec2-user ALL=(ALL) ALL"
rm "${SUDOERS_DIR}/05-defaults"
if run_setup "$LATE" -- ; then
    fail_test "apply must fail: the rule needs a password, but the rule list holds it"
else
    pass "without the Defaults line sudo -n true fails, the rule list fails the step"
fi
check_not "sudo -n true did not succeed" grep -qF "sudo -n true succeeded" "$OUT"
check "the rule list names the entry" grep -qE '^  entry [0-9]+ \(run-as user ALL, not pilot-agent alone\):$' "$OUT"
printf 'Defaults !authenticate\n' >"${SUDOERS_DIR}/05-defaults"
if run_setup "$LATE" -- ; then
    fail_test "apply must fail with a global Defaults !authenticate"
else
    pass "a global Defaults !authenticate fails the step too"
fi
teardown_sandbox

echo "=== test: the proof ignores the text of a rule that gives no root ==="
setup_sandbox
printf 'ec2-user ALL=(pilot-agent) NOPASSWD: ALL\n' >"${SUDOERS_DIR}/10-other"
if run_setup -- ; then
    pass "a second NOPASSWD rule to the agent user is no root: apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed on a rule that gives no root"
fi
teardown_sandbox

# rule_list FILE ENTRY... — a rule list as sudo -ll -U ec2-user prints it: the
# agent rule, then each ENTRY (printf %b: \n and \t in it are expanded).
rule_list() {
    local f="$1" e
    shift
    {
        printf 'Matching Defaults entries for ec2-user on host:\n    !visiblepw, always_set_home\n\n'
        printf 'User ec2-user may run the following commands on host:\n\n'
        printf 'Sudoers entry: %s/90-pilot-agent-runas\n    RunAsUsers: pilot-agent\n    Options: setenv, !authenticate\n    Commands:\n\tALL\n' "$SUDOERS_DIR"
        for e in "$@"; do printf '\n%b\n' "$e"; done
    } >"$f"
}

# mutation pin: prove_sudo_removed skips the rule list -> this test fails
echo "=== test: the rule list — a NOPASSWD rule for one command as root fails the step, the entry is printed ==="
setup_sandbox
rule_list "${SANDBOX}/list" 'Sudoers entry: /etc/sudoers.d/10-dnf\n    RunAsUsers: root\n    Options: !authenticate\n    Commands:\n\t/usr/bin/dnf'
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
    fail_test "apply must fail: a rule for one command as root is left"
else
    pass "apply exits non-zero"
fi
expect_step cloud-init-sudoers failed
check_not "sudo -n true failed: the behaviour part passed" grep -qF "sudo -n true succeeded" "$OUT"
check "the message names the rule list" grep -qF "the sudo rules of ec2-user (sudo -ll -U ec2-user) hold more than the agent rule" "$OUT"
check "the entry and its reason are printed" has_line "$OUT" "  entry 2 (run-as user root, not pilot-agent alone):"
check "the lines of the entry are printed" has_line "$OUT" "      | Sudoers entry: /etc/sudoers.d/10-dnf"
check "its command is printed" has_line "$OUT" "      | "$'\t'"/usr/bin/dnf"
check_not "the agent rule is not named" grep -qF "entry 1 (" "$OUT"
teardown_sandbox

echo "=== test: the rule list — an entry without a run-as line fails the step ==="
setup_sandbox
rule_list "${SANDBOX}/list" 'Sudoers entry: /etc/sudoers.d/10-x\n    Options: !authenticate\n    Commands:\n\tALL'
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
    fail_test "apply must fail on an entry without a RunAsUsers line"
else
    pass "apply exits non-zero"
fi
expect_step cloud-init-sudoers failed
check "the entry is named" has_line "$OUT" "  entry 2 (no RunAsUsers line):"
check "the counts are named" has_line "$OUT" "  2 entries, 1 RunAsUsers lines"
teardown_sandbox

echo "=== test: the rule list — a line the check does not know fails the step ==="
setup_sandbox
rule_list "${SANDBOX}/list" 'Sudoers entry: /etc/sudoers.d/10-x\n    RunAsUsers: pilot-agent\n    Chroot: /srv\n    Commands:\n\tALL'
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
    fail_test "apply must fail on an unknown line in an entry"
else
    pass "apply exits non-zero on an unknown line in an entry"
fi
check "the entry and the line are named" has_line "$OUT" "  entry 2 (a line the check does not know:     Chroot: /srv):"
rule_list "${SANDBOX}/list" 'Host ALL may run anything'
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
    fail_test "apply must fail on an unknown line outside an entry"
else
    pass "apply exits non-zero on an unknown line outside an entry"
fi
check "the line is named" has_line "$OUT" "  a line the check does not know: Host ALL may run anything"
teardown_sandbox

echo "=== test: the rule list — only the agent user (a run-as group pilot too) passes ==="
setup_sandbox
rule_list "${SANDBOX}/list" 'Sudoers entry: /etc/sudoers.d/10-x\n    RunAsUsers: pilot-agent\n    RunAsGroups: pilot\n    Commands:\n\t/usr/bin/true'
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed on a list with only the agent user"
fi
expect_step cloud-init-sudoers "done"
check "the rule list was read" grep -qE '^sudo -n -ll -U ec2-user$' "$CALL_LOG"
rule_list "${SANDBOX}/list" 'Sudoers entry: /etc/sudoers.d/10-x\n    RunAsUsers: pilot-agent\n    RunAsGroups: wheel\n    Commands:\n\tALL'
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
    fail_test "apply must fail on a run-as group other than pilot"
else
    pass "a run-as group other than pilot fails the step"
fi
check "the group is named" has_line "$OUT" "  entry 2 (run-as group wheel, not pilot alone):"
teardown_sandbox

# mutation pin: the parser accepts a run-as list that contains the agent user -> this test fails
echo "=== test: the rule list — a run-as list with the agent user and root, in both orders, fails the proof ==="
for runas in "pilot-agent, root" "root, pilot-agent"; do
    setup_sandbox
    rule_list "${SANDBOX}/list" "Sudoers entry: /etc/sudoers.d/10-x\n    RunAsUsers: ${runas}\n    Commands:\n\tALL"
    if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" -- ; then
        fail_test "apply must fail on the run-as list ${runas}"
    else
        pass "apply exits non-zero on the run-as list ${runas}"
    fi
    expect_step cloud-init-sudoers failed
    check "the list is named: ${runas}" has_line "$OUT" "  entry 2 (run-as user ${runas}, not pilot-agent alone):"
    teardown_sandbox
done

# mutation pin: check_rule_list ignores the exit status of sudo -ll -> this test fails
echo "=== test: the rule list command exits non-zero with a list that looks valid — the proof fails ==="
setup_sandbox
rule_list "${SANDBOX}/list"
if run_setup MOCK_SUDO_LIST="${SANDBOX}/list" MOCK_SUDO_LIST_RC=1 -- ; then
    fail_test "apply must fail when sudo -ll exits non-zero"
else
    pass "apply exits non-zero"
fi
expect_step cloud-init-sudoers failed
check "the exit status is named" grep -qF "sudo -ll -U ec2-user failed (exit 1); the sudo removal is not proven" "$OUT"
teardown_sandbox

# ------------------------------- the check of the sudo rules before the first change

# mutation pin: the call of precheck_sudo_rules is removed -> "the cloud-init file has no change" fails
echo "=== test: a sudo rule through a group of the daemon user stops the apply before the first change ==="
setup_sandbox
mock_run usermod -aG wheel ec2-user
printf '%%wheel ALL=(ALL) ALL\n' >"${SUDOERS_DIR}/10-wheel"
before="$(cat "${SUDOERS_DIR}/90-cloud-init-users")"
: >"$MUT_LOG"
rc=0
run_setup -- || rc=$?
if [ "$rc" = 1 ]; then pass "apply exits 1"; else cat "$OUT"; fail_test "apply: exit ${rc}, want 1"; fi
check "the cloud-init file has no change" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$before"
check "no backup of the cloud-init file" test ! -e "$BAK"
check "no mutating command" test ! -s "$MUT_LOG"
check_not "no step ran" grep -qx '== daemon-private' "$OUT"
check "the entry is named" has_line "$OUT" "  entry 1 (run-as user ALL, not pilot-agent alone):"
check "the file of the entry is printed" has_line "$OUT" "      | Sudoers entry: ${SUDOERS_DIR}/10-wheel"
check_not "the cloud-init entry is not named" grep -qF "| Sudoers entry: ${SUDOERS_DIR}/90-cloud-init-users" "$OUT"
check "the operator is told to remove the rule or the membership first" grep -qF "nothing is changed: the proof after the edit of ${SUDOERS_DIR}/90-cloud-init-users would refuse the entries above; remove each rule, or the membership of ec2-user in the group it names, first, then run again" "$OUT"
teardown_sandbox

echo "=== test: the check before the first change — only the cloud-init rule and the agent rule: the apply goes on ==="
setup_sandbox
printf '%s\n' "$EXPECTED_RULE" >"${SUDOERS_DIR}/90-pilot-agent-runas"
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed with only the cloud-init rule and the agent rule"
fi
check "the check ran before the first step" test "$(first_line_of "$OUT" '^== sudo-rules ')" -lt "$(first_line_of "$OUT" '^== daemon-private$')"
check "the check read the rule list before the first change" test "$(first_line '^sudo -n -ll -U ec2-user$')" -lt "$(first_line '^groupadd ')"
check "the cloud-init lines are commented out" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED"
expect_step cloud-init-sudoers "done"
teardown_sandbox

echo "=== test: the check before the first change passes over one entry per line it comments out ==="
setup_sandbox
printf '%s\n' 'ec2-user ALL=(ALL) NOPASSWD:ALL' 'ec2-user,bob ALL=(ALL) ALL' >"${SUDOERS_DIR}/90-cloud-init-users"
before="$(cat "${SUDOERS_DIR}/90-cloud-init-users")"
if run_setup -- ; then
    fail_test "apply must fail: the second line names two users and stays"
else
    pass "apply exits non-zero"
fi
check "the cloud-init file has no change with a line the apply leaves" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$before"
check "the second entry of the cloud-init file is named" has_line "$OUT" "  entry 2 (run-as user ALL, not pilot-agent alone):"
teardown_sandbox

# mutation pin: the error of the positive control is swallowed, so a broken sudo counts as proof -> this test fails
echo "=== test: a sudo that does not run fails the step with its output ==="
setup_sandbox
if run_setup MOCK_SUDO_BROKEN=1 -- ; then
    fail_test "apply must fail when sudo itself fails"
else
    pass "apply exits non-zero when sudo itself fails"
fi
expect_step cloud-init-sudoers failed
check "the output of sudo is printed" grep -qF "sudo: unable to initialize policy plugin" "$OUT"
check "the message says the removal is not proven" grep -qF "the sudo removal is not proven" "$OUT"
teardown_sandbox

echo "=== test: the proof also runs when nothing is left to edit, and without a cloud-init file ==="
setup_sandbox
run_setup -- >/dev/null || true
if run_setup MOCK_SUDO_BROKEN=1 -- ; then
    fail_test "second apply must fail when sudo fails"
else
    pass "second apply fails when sudo fails"
fi
if run_setup MOCK_SUDO_ROOT_OK=1 -- ; then
    fail_test "second apply must fail when sudo -n true succeeds"
else
    pass "second apply fails when sudo -n true succeeds (a rule from another source)"
fi
check "no source line is found for an unknown source" grep -qF "no such line in" "$OUT"
rm "${SUDOERS_DIR}/90-cloud-init-users"
if run_setup MOCK_SUDO_ROOT_OK=1 -- ; then
    fail_test "apply without a cloud-init file must still prove the removal"
else
    pass "apply without a cloud-init file fails when sudo -n true succeeds"
fi
check "the proof ran" grep -qE '^runuser -u ec2-user -- sudo -n true$' "$CALL_LOG"
teardown_sandbox

# ------------------------------------- the daemon user in a privileged group

echo "=== test: the daemon user in wheel and docker — one WARNING block, repeated in the summary, no failure ==="
setup_sandbox
mock_run usermod -aG wheel ec2-user
mock_run usermod -aG docker ec2-user
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed for a daemon user in wheel"
fi
WANT_WARNING="WARNING: the daemon user ec2-user is a member of docker, wheel: each of these memberships is a path to root (this script removes none of them); a sudo rule for one of these groups stops the apply at the check of the sudo rules, before the first change"
check "the warning names each group" has_line "$OUT" "$WANT_WARNING"
check "one warning block" test "$(grep -c '^== warning$' "$OUT")" = 1
check "the line is printed twice: the block and the summary" test "$(grep -cxF "$WANT_WARNING" "$OUT")" = 2
check "the summary repeats it" has_line <(sed -n '/^== summary$/,$p' "$OUT") "$WANT_WARNING"
check "the block comes before the first step" test "$(grep -n -m1 '^== warning$' "$OUT" | cut -d: -f1)" -lt "$(grep -n -m1 '^== daemon-private$' "$OUT" | cut -d: -f1)"
teardown_sandbox

echo "=== test: the daemon user in no privileged group — no warning ==="
setup_sandbox
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed"
fi
check_not "no WARNING line" grep -q 'WARNING' "$OUT"
check_not "no warning block" grep -q '^== warning$' "$OUT"
teardown_sandbox

# ----------------------------------------------- order, firewall

echo "=== test: the removal of the daemon user's sudo needs --confirm-root-path ==="
setup_sandbox
expect_refused "no --confirm-root-path" NO_CONFIRM=1 --
check "the message tells the operator to hold another root session" grep -qF "keep another root session open" "$OUT"
check "the cloud-init file is untouched" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check "nothing was done before the refusal (no group, no user)" test ! -e "${STATE_DIR}/groups/pilot" -a ! -e "${STATE_DIR}/users/pilot-agent"
check "--dry-run does not need it" run_setup NO_CONFIRM=1 -- --dry-run
check "--undo does not need it" run_setup NO_CONFIRM=1 -- --undo
teardown_sandbox

# mutation pin: --confirm-root-path is demanded during argument checking even when nothing is left to remove -> this test fails
echo "=== test: a second apply on a host that is set up needs no --confirm-root-path ==="
setup_sandbox
run_setup -- >/dev/null || true
: >"$MUT_LOG"
if run_setup NO_CONFIRM=1 -- ; then
    pass "second apply without --confirm-root-path exits 0"
else
    cat "$OUT"
    fail_test "second apply without --confirm-root-path failed"
fi
for s in $ALL_STEPS; do
    expect_step "$s" skipped
done
check "it changes nothing" test ! -s "$MUT_LOG"
printf '%s\n' "$CLOUD_INIT_ORIGINAL" >"${SUDOERS_DIR}/90-cloud-init-users"
expect_refused "a line to comment out again, no --confirm-root-path" NO_CONFIRM=1 --
check "the message tells the operator to hold another root session" grep -qF "keep another root session open" "$OUT"
teardown_sandbox

# mutation pin: the sudoers-rule step runs before the firewall steps -> this test fails
echo "=== test: the firewall comes before the sudoers rule — a failed firewall step leaves no rule and the daemon user's sudo alone ==="
setup_sandbox
if run_setup IPTABLES_CMD=iptables-not-installed -- ; then
    fail_test "apply must fail without iptables"
else
    pass "apply exits non-zero without iptables"
fi
expect_step firewall-ipv4 failed
expect_step sudoers-rule skipped
expect_step cloud-init-sudoers skipped
check_not "no sudoers rule file exists" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check_not "visudo never ran" grep -q '^visudo ' "$CALL_LOG"
check "the cloud-init file is untouched" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check_not "no backup was made" test -e "$BAK"
teardown_sandbox

echo "=== test: a missing ip6tables with IPv6 on fails the apply before the sudoers rule ==="
setup_sandbox
rc=0
run_setup IP6TABLES_CMD=ip6tables-not-installed -- || rc=$?
check "apply exits 1 without ip6tables" test "$rc" = 1
expect_step firewall-ipv4 "done"
expect_step firewall-ipv6 failed
expect_step firewall-unit skipped
expect_step sudoers-rule skipped
check "the failure says IPv6 is on" grep -qF "ip6tables-not-installed not found and IPv6 is on" "$OUT"
check_not "no sudoers rule file exists" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check_not "no unit file exists" test -e "${SYSTEMD_DIR}/pilot-agent-firewall.service"
# IPv6 off for all and default, on for one interface: still on
set_ipv6 1 eth0 0
rc=0
run_setup IP6TABLES_CMD=ip6tables-not-installed -- || rc=$?
check "IPv6 on for one interface: apply exits 1" test "$rc" = 1
expect_step firewall-ipv6 failed
check_not "no sudoers rule file exists" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
# no IPv6 settings at all: counted as on
rm -rf "$IPV6_CONF"
rc=0
run_setup IP6TABLES_CMD=ip6tables-not-installed -- || rc=$?
check "no IPv6 settings: apply exits 1" test "$rc" = 1
expect_step firewall-ipv6 failed
teardown_sandbox

# mutation pin: the call of remove_sudo_rule_on_failure is removed -> this test fails
echo "=== test: a second apply that fails at the IPv4 firewall step removes the sudo rule of the first apply ==="
setup_sandbox
run_setup -- >/dev/null || true
check "the first apply installed the sudo rule" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
# the firewall rules are gone and iptables is missing
mock_run iptables -w -D OUTPUT -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS
mock_run iptables -w -F PILOT_AGENT_EGRESS
mock_run iptables -w -X PILOT_AGENT_EGRESS
rc=0
run_setup IPTABLES_CMD=iptables-not-installed -- || rc=$?
check "the second apply exits 1" test "$rc" = 1
expect_step firewall-ipv4 failed
expect_step sudoers-rule skipped
check_not "no sudoers rule file exists" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check "one line says the rule is removed" grep -qxF "firewall-ipv4 failed before the sudoers-rule step: removing the sudo rule of an earlier apply, ${SUDOERS_DIR}/90-pilot-agent-runas, so that the daemon cannot start the agent user without the complete firewall" "$OUT"
check "the removal is printed" has_line "$OUT" "+ rm -f ${SUDOERS_DIR}/90-pilot-agent-runas"
check "the summary lists the removal" has_line "$OUT" "sudo rule removed: ${SUDOERS_DIR}/90-pilot-agent-runas (firewall-ipv4 failed; the next apply that passes puts it back)"
# with iptables back, the next apply puts the rule back
if run_setup -- ; then
    pass "a third apply with iptables exits 0"
else
    cat "$OUT"
    fail_test "a third apply with iptables failed"
fi
check "the third apply puts the rule back" test "$(cat "${SUDOERS_DIR}/90-pilot-agent-runas")" = "$EXPECTED_RULE"
check_not "no removal line" grep -qF "sudo rule removed" "$OUT"
teardown_sandbox

echo "=== test: a second apply with IPv6 on and no ip6tables (the first ran with IPv6 off) removes the sudo rule ==="
setup_sandbox
set_ipv6 1
if run_setup IP6TABLES_CMD=ip6tables-not-installed -- ; then
    pass "the first apply with IPv6 off exits 0"
else
    cat "$OUT"
    fail_test "the first apply with IPv6 off failed"
fi
check "the first apply installed the sudo rule" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
set_ipv6 0
rc=0
run_setup IP6TABLES_CMD=ip6tables-not-installed -- || rc=$?
check "the second apply exits 1" test "$rc" = 1
expect_step firewall-ipv6 failed
check_not "no sudoers rule file exists" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check "one line says the rule is removed" grep -qF "firewall-ipv6 failed before the sudoers-rule step: removing the sudo rule of an earlier apply" "$OUT"
check "the summary lists the removal" has_line "$OUT" "sudo rule removed: ${SUDOERS_DIR}/90-pilot-agent-runas (firewall-ipv6 failed; the next apply that passes puts it back)"
teardown_sandbox

echo "=== test: a dry run that fails before the sudoers-rule step prints the removal and changes nothing ==="
setup_sandbox
run_setup -- >/dev/null || true
before="$(tree_listing)"
: >"$MUT_LOG"
rc=0
run_setup IPTABLES_CMD=iptables-not-installed -- --dry-run || rc=$?
check "the dry run exits 1" test "$rc" = 1
check "the sudoers rule file is still there" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check "files and modes unchanged" test "$before" = "$(tree_listing)"
check "no mutating command" test ! -s "$MUT_LOG"
check "the removal is printed" has_line "$OUT" "+ rm -f ${SUDOERS_DIR}/90-pilot-agent-runas"
check "the summary lists the removal as a dry run" has_line "$OUT" "sudo rule removed (dry-run): ${SUDOERS_DIR}/90-pilot-agent-runas (firewall-ipv4 failed; the next apply that passes puts it back)"
teardown_sandbox

echo "=== test: a failure at the sudoers-rule step or after it does not remove the rule file ==="
setup_sandbox
run_setup -- >/dev/null || true
rc=0
run_setup MOCK_SUDO_BROKEN=1 -- || rc=$?
check "the apply exits 1" test "$rc" = 1
expect_step cloud-init-sudoers failed
check "the sudoers rule file is still there" test -e "${SUDOERS_DIR}/90-pilot-agent-runas"
check_not "no removal line" grep -qF "sudo rule removed" "$OUT"
teardown_sandbox

echo "=== test: the jump to the chain is moved to the first place in OUTPUT ==="
setup_sandbox
run_setup -- >/dev/null || true
mock_run iptables -w -I OUTPUT 1 -j ACCEPT
mock_run ip6tables -w -I OUTPUT 1 -j ACCEPT
: >"$MUT_LOG"
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed"
fi
for f in iptables ip6tables; do
    first="$(mock_run "$f" -w -S OUTPUT | grep '^-A ' | head -1)"
    if [ "$first" = "-A OUTPUT -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS" ]; then
        pass "${f}: the jump is the first rule of OUTPUT"
    else
        fail_test "${f}: first rule of OUTPUT is '${first}'"
    fi
    check "${f}: exactly one jump, the ACCEPT rule is still there" test "$(mock_run "$f" -w -S OUTPUT | grep -c -- '-j PILOT_AGENT_EGRESS$' | tr -d ' ')" = 1 -a "$(mock_run "$f" -w -S OUTPUT | grep -c -- '-j ACCEPT' | tr -d ' ')" = 1
done
expect_step firewall-ipv4 "done"
check_not "no gap while the jump moved" test -e "${STATE_DIR}/gap.log"
run_setup -- >/dev/null || true
: >"$MUT_LOG"
run_setup -- || true
expect_step firewall-ipv4 skipped
check "the next apply changes nothing" test ! -s "$MUT_LOG"
teardown_sandbox

echo "=== test: a chain rebuild never leaves a moment without the reject rules ==="
setup_sandbox
mock_run iptables -w -N PILOT_AGENT_EGRESS
mock_run iptables -w -A PILOT_AGENT_EGRESS -d 169.254.169.254 -j REJECT
mock_run iptables -w -A OUTPUT -j ACCEPT
mock_run iptables -w -A OUTPUT -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS
rm -f "${STATE_DIR}/gap.log"
: >"$MUT_LOG"
if run_setup -- ; then
    pass "apply exits 0 on a host with an older, incomplete chain"
else
    cat "$OUT"
    fail_test "apply failed on an incomplete chain"
fi
check_not "no moment without a jump to a chain with a REJECT rule" test -e "${STATE_DIR}/gap.log"
check "the chain is complete (3 rules)" test "$(mock_run iptables -w -S PILOT_AGENT_EGRESS | grep -c '^-A ' | tr -d ' ')" = 3
check_not "the temporary chain is gone" grep -qFx "chain PILOT_AGENT_EGRESS_NEW" "${STATE_DIR}/iptables.rules"
check "one jump, first in OUTPUT, the ACCEPT rule after it" test "$(mock_run iptables -w -S OUTPUT | grep '^-A ' | head -1)" = "-A OUTPUT -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS" -a "$(mock_run iptables -w -S OUTPUT | grep -c '^-A ' | tr -d ' ')" = 2
new_n="$(grep -n -m1 '^iptables -w -N PILOT_AGENT_EGRESS_NEW' "$MUT_LOG" | cut -d: -f1 || true)"
ins_n="$(grep -n -m1 '^iptables -w -I OUTPUT 1 .*-j PILOT_AGENT_EGRESS_NEW' "$MUT_LOG" | cut -d: -f1 || true)"
del_n="$(grep -n -m1 '^iptables -w -X PILOT_AGENT_EGRESS$' "$MUT_LOG" | cut -d: -f1 || true)"
if [ -n "$new_n" ] && [ -n "$ins_n" ] && [ -n "$del_n" ] && [ "$new_n" -lt "$ins_n" ] && [ "$ins_n" -lt "$del_n" ]; then
    pass "new chain built, jump switched, then the old chain removed"
else
    fail_test "rebuild order wrong (new ${new_n}, switch ${ins_n}, remove ${del_n})"
fi
teardown_sandbox

# mutation pin: rebuild_chain drops the jump to a leftover temporary chain before the jump to the new chain is in place -> this test fails
echo "=== test: a leftover temporary chain whose jump is the only one in force is replaced without a gap ==="
setup_sandbox
# an interrupted rebuild: the old chain flushed, the temporary chain complete and switched in
mock_run iptables -w -N PILOT_AGENT_EGRESS
mock_run iptables -w -N PILOT_AGENT_EGRESS_NEW
mock_run iptables -w -A PILOT_AGENT_EGRESS_NEW -d 169.254.169.254 -j REJECT
mock_run iptables -w -A PILOT_AGENT_EGRESS_NEW -p tcp -d 127.0.0.1 --dport 9091 -j REJECT --reject-with tcp-reset
mock_run iptables -w -A PILOT_AGENT_EGRESS_NEW -p tcp --dport 9091 -m addrtype --dst-type LOCAL -o lo -j REJECT --reject-with tcp-reset
mock_run iptables -w -I OUTPUT 1 -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS_NEW
rm -f "${STATE_DIR}/gap.log"
if run_setup -- ; then
    pass "apply exits 0 after an interrupted rebuild"
else
    cat "$OUT"
    fail_test "apply failed after an interrupted rebuild"
fi
check_not "no moment without a jump to a chain with a REJECT rule" test -e "${STATE_DIR}/gap.log"
check "the chain is complete (3 rules)" test "$(mock_run iptables -w -S PILOT_AGENT_EGRESS | grep -c '^-A ' | tr -d ' ')" = 3
check_not "no temporary chain is left" grep -qE "^chain PILOT_AGENT_EGRESS_(NEW|ALT)$" "${STATE_DIR}/iptables.rules"
check "one jump, to the chain, first in OUTPUT" test "$(mock_run iptables -w -S OUTPUT | grep '^-A ')" = "-A OUTPUT -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS"
: >"$MUT_LOG"
run_setup -- || true
expect_step firewall-ipv4 skipped
check "the next apply changes nothing" test ! -s "$MUT_LOG"
teardown_sandbox

# mutation pin: the leftover temporary chains are removed only on the rebuild path, not when the main chain is absent -> this test fails
echo "=== test: a temporary chain of a failed run is removed also when the main chain is absent ==="
setup_sandbox
# a boot unit that failed after it removed the main chain: the temporary chain holds the only jump
mock_run iptables -w -N PILOT_AGENT_EGRESS_NEW
mock_run iptables -w -A PILOT_AGENT_EGRESS_NEW -d 169.254.169.254 -j REJECT
mock_run iptables -w -I OUTPUT 1 -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS_NEW
# and a leftover without a jump
mock_run iptables -w -N PILOT_AGENT_EGRESS_ALT
mock_run ip6tables -w -N PILOT_AGENT_EGRESS_ALT
rm -f "${STATE_DIR}/gap.log"
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed with leftover temporary chains"
fi
for f in iptables ip6tables; do
    check_not "${f}: no temporary chain is left" grep -qE "^chain PILOT_AGENT_EGRESS_(NEW|ALT)$" "${STATE_DIR}/${f}.rules"
    check "${f}: one jump, to the complete chain, first in OUTPUT" test "$(mock_run "$f" -w -S OUTPUT | grep '^-A ')" = "-A OUTPUT -m owner --uid-owner pilot-agent -j PILOT_AGENT_EGRESS" -a "$(mock_run "$f" -w -S PILOT_AGENT_EGRESS | grep -c '^-A ' | tr -d ' ')" = 3
done
check_not "no moment without a jump to a chain with a REJECT rule" test -e "${STATE_DIR}/gap.log"
: >"$MUT_LOG"
run_setup -- || true
check "the next apply changes nothing" test ! -s "$MUT_LOG"
teardown_sandbox

echo "=== test: the gap check itself notices a flush of the chain behind a live jump ==="
setup_sandbox
run_setup -- >/dev/null || true
rm -f "${STATE_DIR}/gap.log"
mock_run iptables -w -F PILOT_AGENT_EGRESS
check "the flush is reported as a gap" test -s "${STATE_DIR}/gap.log"
teardown_sandbox

echo "=== test: without ip6tables on a host with IPv6 off the IPv4 rules and the unit are applied and IPv6 is skipped ==="
setup_sandbox
set_ipv6 1
if run_setup IP6TABLES_CMD=ip6tables-not-installed -- ; then
    pass "apply exits 0 without ip6tables"
else
    cat "$OUT"
    fail_test "apply failed without ip6tables"
fi
expect_step firewall-ipv4 "done"
expect_step firewall-ipv6 skipped
check "the summary says the IPv6 part is skipped" grep -qE '^step firewall-ipv6: skipped - .*IPv6 part is skipped' "$OUT"
check_not "nothing failed" grep -q '^step .*: failed' "$OUT"
check "the unit has the IPv4 rules" grep -qF -- "ExecStart=${STUB_BIN}/iptables -w -A PILOT_AGENT_EGRESS_NEW -d 169.254.169.254 -j REJECT" "${SYSTEMD_DIR}/pilot-agent-firewall.service"
check_not "the unit has no ip6tables line" grep -q ip6tables "${SYSTEMD_DIR}/pilot-agent-firewall.service"
check "the unit is enabled" test -e "${STATE_DIR}/enabled/pilot-agent-firewall.service"
check "the removal step still ran" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED"
: >"$MUT_LOG"
run_setup IP6TABLES_CMD=ip6tables-not-installed -- || true
expect_step firewall-unit skipped
check "a second apply changes nothing" test ! -s "$MUT_LOG"
run_setup IP6TABLES_CMD=ip6tables-not-installed -- --undo >/dev/null || true
check "undo skips IPv6 as well" grep -qE '^step firewall-ipv6: skipped' "$OUT"
teardown_sandbox

# ------------------------------------------------------------- dry run

echo "=== test: --dry-run deletes and changes nothing, also on a host that was set up before ==="
setup_sandbox
build_repos
run_setup -- >/dev/null || true
printf '%s\n' "$REWRITTEN" >"${SUDOERS_DIR}/90-cloud-init-users"
printf 'stale\n' >"${SUDOERS_DIR}/.90-pilot-agent-runas.new"
mock_run iptables -w -I OUTPUT 1 -j ACCEPT
before="$(tree_listing)"
before_rules="$(cat "${STATE_DIR}/iptables.rules")"
: >"$MUT_LOG"
: >"$CALL_LOG"
if run_setup -- --dry-run; then
    pass "dry run exits 0"
else
    cat "$OUT"
    fail_test "dry run failed"
fi
check "no mutating command" test ! -s "$MUT_LOG"
check "files and modes unchanged" test "$before" = "$(tree_listing)"
check "firewall rules unchanged" test "$before_rules" = "$(cat "${STATE_DIR}/iptables.rules")"
check "the rotation of the old backup is printed, not done" grep -qF "+ mv ${BAK} ${BAK}.1" "$OUT"
check "the lines to comment out are printed" grep -qF "to comment out: ${SUDOERS_DIR}/90-cloud-init-users:3:" "$OUT"
check "the proof is printed, not run" grep -qF "+ runuser -u ec2-user -- sudo -n true" "$OUT"
check "the check of the sudo rules is printed, not run" grep -qF "+ sudo -n -ll -U ec2-user  (each entry but " "$OUT"
check_not "the proof did not run" grep -qE '^(runuser|sudo) ' "$CALL_LOG"
run_setup -- >/dev/null || true
: >"$MUT_LOG"
before="$(tree_listing)"
if run_setup -- --dry-run --undo; then
    pass "dry-run undo exits 0"
else
    cat "$OUT"
    fail_test "dry-run undo failed"
fi
check "dry-run undo: no mutating command" test ! -s "$MUT_LOG"
check "dry-run undo: files and modes unchanged" test "$before" = "$(tree_listing)"
check "dry-run undo prints the restore it would do" grep -qE "^\+ install -m [0-7]+ -o [^ ]+ -g [^ ]+ ${BAK} ${SUDOERS_DIR}/90-cloud-init-users\$" "$OUT"
check "dry-run undo prints the removal of the rule" grep -qF "+ rm -f ${SUDOERS_DIR}/90-pilot-agent-runas" "$OUT"
teardown_sandbox

# ------------------------------------------- agent root, proof as the agent user

# mutation pin: a Go cache directory is made by root (install -d) again -> this test fails
echo "=== test: no root step inside the agent home — the agent user makes the Go caches ==="
setup_sandbox
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply exited non-zero"
fi
check_not "no install -d names a path below the agent home" grep -qE "^install -d .* ${AHOME}/" "$CALL_LOG"
check "the agent user made go, go/cache and go/mod" has_line "$CALL_LOG" "runuser -u pilot-agent -- mkdir -p -m 750 ${AHOME}/go ${AHOME}/go/cache ${AHOME}/go/mod"
for d in go go/cache go/mod; do
    check "${d} belongs to the agent user" test "$(mock_run stat -c %U:%G "${AHOME}/${d}")" = pilot-agent:pilot
done
check "root made the agent root root:root 0755" has_line "$CALL_LOG" "install -d -o root -g root -m 755 ${AROOT}"
check "the agent root belongs to root" test "$(mock_run stat -c %U:%G:%a "$AROOT")" = root:root:755
teardown_sandbox

echo "=== test: --agent-root below a directory of root; refused when it holds the data dir, is not root's or sits in a directory of another user ==="
setup_sandbox
mkdir "${SANDBOX}/srv"
set_meta "${SANDBOX}/srv" root:root
if run_setup -- --agent-root "${SANDBOX}/srv/agent"; then
    pass "apply exits 0 with the agent root outside the data dir"
else
    cat "$OUT"
    fail_test "apply failed with --agent-root ${SANDBOX}/srv/agent"
fi
check "the home of the agent user is below it" grep -qF -- "--home-dir ${SANDBOX}/srv/agent/home " "$CALL_LOG"
check "the summary names its workspace root" has_line "$OUT" "  run_as_workspace_dir: ${SANDBOX}/srv/agent/workspaces"
check_not "no agent root in the data dir" test -e "$AROOT"
expect_refused "--agent-root is the data dir" -- --agent-root "$DATA_DIR"
expect_refused "--agent-root holds the data dir" -- --agent-root "$SANDBOX"
expect_refused "--agent-root is an entry of the daemon user" -- --agent-root "$R"
check "the reason is printed" grep -qF "exists and is not owned by root" "$OUT"
expect_refused "--agent-root inside a directory of the daemon user" -- --agent-root "${DATA_DIR}/bin/agent"
expect_refused "--agent-root is a relative path" -- --agent-root agent
teardown_sandbox

# mutation pin: daemon-private skips the mode of the data dir itself -> this test fails
echo "=== test: a data dir that gives more than traverse fails the check; the modes are recorded and undo prints them ==="
setup_sandbox
build_repos
chmod 755 "$DATA_DIR" "$R"
: >"$MUT_LOG"
if run_setup -- --dry-run; then
    fail_test "a dry run must fail the check too"
else
    pass "a dry run fails the check"
fi
check_not "a dry run writes no record" test -e "$RECORD"
if run_setup -- ; then
    fail_test "apply must fail while the data dir is 0755"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private failed
check "prints the chmod for the data dir" has_line "$OUT" "the data dir '${DATA_DIR}' gives more than traverse (group ec2-user, mode 755): runuser -u ec2-user -- chmod go-rw '${DATA_DIR}'"
check "the data dir keeps its mode (the step changes no mode)" test "$(mode_of "$DATA_DIR")" = 755
check "nothing was mutated" test ! -s "$MUT_LOG"
check "the record holds the mode of the data dir" grep -qxF "755	${DATA_DIR}" "$RECORD"
check "the record holds the mode of the repos directory" grep -qxF "755	${R}" "$RECORD"
# mutation pin: one chmod line is printed without the runuser form -> this test fails
check "every chmod line of the apply runs chmod as the daemon user" chmods_as_daemon "$OUT"
chmod 711 "$DATA_DIR"
chmod 700 "$R"
if run_setup -- ; then
    pass "apply passes once the chmods ran"
else
    cat "$OUT"
    fail_test "apply failed after the chmods"
fi
run_setup -- --undo >/dev/null || true
check "undo prints the chmod that puts back the mode of the data dir" has_line "$OUT" "  runuser -u ec2-user -- chmod 755 '${DATA_DIR}'"
check "undo prints the chmod that puts back the mode of the repos directory" has_line "$OUT" "  runuser -u ec2-user -- chmod 755 '${R}'"
check "the data dir keeps its mode after undo (printed, not run)" test "$(mode_of "$DATA_DIR")" = 711
restore_n="$(grep -n -m1 '^== earlier modes' "$OUT" | cut -d: -f1 || true)"
left_n="$(grep -n -m1 '^== left in place' "$OUT" | cut -d: -f1 || true)"
check "the chmods come before the cleanup section" test -n "$restore_n" -a -n "$left_n" -a "${restore_n:-0}" -lt "${left_n:-0}"
check "every chmod line of the undo runs chmod as the daemon user" chmods_as_daemon "$OUT"
check "the text before the lines says to run them as root, as they are" grep -qF "line below as root, as it is (runuser runs chmod as ec2-user" "$OUT"
set_meta "$RECORD" ec2-user:ec2-user
run_setup -- --undo >/dev/null || true
check "a record that is not root's is passed over" grep -qF "${RECORD} is not a regular file of root; passed over" "$OUT"
check_not "and no chmod is printed from it" has_line "$OUT" "  runuser -u ec2-user -- chmod 755 '${DATA_DIR}'"
teardown_sandbox

# mutation pin: the proof as the agent user is removed -> this test fails
echo "=== test: a data dir below a 0700 directory fails the apply at the proof of the agent user, with the path ==="
setup_sandbox
CLOSED="${SANDBOX}/closed"
mkdir -p "${CLOSED}/data"
chmod 700 "$CLOSED"
chmod 711 "${CLOSED}/data"
set_meta "$CLOSED" ec2-user:ec2-user
set_meta "${CLOSED}/data" ec2-user:ec2-user
if run_setup -- --data-dir "${CLOSED}/data"; then
    fail_test "apply must fail: the agent user cannot enter its home"
else
    pass "apply exits non-zero"
fi
for s in group agent-user daemon-membership directories sudoers-rule firewall-ipv4 firewall-ipv6 firewall-unit cloud-init-sudoers; do
    expect_step "$s" "done"
done
expect_step agent-proof failed
check "the message names the home" grep -qF "the agent user cannot create a file in ${CLOSED}/data/agent/home;" "$OUT"
check "the message names the workspace root" grep -qF "the agent user cannot create a file in ${CLOSED}/data/agent/workspaces;" "$OUT"
check_not "no config lines after a failed apply" grep -qF "run_as_user:" "$OUT"
chmod 711 "$CLOSED"
if run_setup -- --data-dir "${CLOSED}/data"; then
    pass "apply passes once the directory above gives traverse"
else
    cat "$OUT"
    fail_test "apply failed with a 0711 directory above the data dir"
fi
expect_step agent-proof skipped
teardown_sandbox

echo "=== test: the proof fails when the agent user can list a directory of the daemon through another of its groups ==="
setup_sandbox
build_repos
# daemon-private looks at "other" and the shared group; an agent user that
# exists already can be in one more group, and the proof finds that
mkdir -p "${STATE_DIR}/members"
echo pilot-agent >"${STATE_DIR}/members/staff"
chmod 750 "$R"
set_meta "$R" ec2-user:staff
if run_setup -- ; then
    fail_test "apply must fail: the agent user can list the repos directory"
else
    pass "apply exits non-zero"
fi
expect_step daemon-private skipped
expect_step agent-proof failed
check "the message names the directory" has_line "$OUT" "the agent user can list '${R}'"
teardown_sandbox

echo "=== test: not root, bad arguments ==="
setup_sandbox
if run_setup MOCK_UID=1000 -- ; then
    fail_test "must refuse to run as a non-root user"
else
    pass "refuses to run as a non-root user"
fi
if [ ! -s "$MUT_LOG" ]; then pass "nothing mutated as non-root"; else fail_test "mutations as non-root"; fi
expect_refused "agent user equal to daemon user" -- --agent-user ec2-user
expect_refused "unknown argument" -- --bogus
expect_refused "--repos-link is gone" -- --repos-link /x
expect_refused "missing value" -- --group
teardown_sandbox

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
