#!/bin/bash
# Plain-bash test suite for setup-agent-user.sh — no bats dependency, no root,
# no real host. The script runs against a temp root with a PATH of mock
# commands (scripts/box/testdata/mock-host-cmds.sh, linked under the names
# useradd, groupadd, usermod, visudo, iptables, ip6tables, systemctl, install,
# chown, chmod, id, getent and stat) that record their arguments; the sudoers
# and systemd directories are pointed at the temp root through
# SUDOERS_DIR / SYSTEMD_DIR.
#
# Usage: ./scripts/box/setup-agent-user_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT="${SCRIPT_DIR}/setup-agent-user.sh"
MOCK_CMDS="${SCRIPT_DIR}/testdata/mock-host-cmds.sh"
MOCKED="useradd groupadd usermod visudo iptables ip6tables systemctl install chown chmod id getent stat"

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

setup_sandbox() {
    SANDBOX="$(mktemp -d)"
    DATA_DIR="${SANDBOX}/data"
    SUDOERS_DIR="${SANDBOX}/sudoers.d"
    SYSTEMD_DIR="${SANDBOX}/systemd"
    STATE_DIR="${SANDBOX}/state"
    STUB_BIN="${SANDBOX}/bin"
    CALL_LOG="${SANDBOX}/calls.log"
    MUT_LOG="${SANDBOX}/mutations.log"
    OUT="${SANDBOX}/stdout.log"

    mkdir -p "${DATA_DIR}/repos" "${DATA_DIR}/bin" "$SUDOERS_DIR" "$SYSTEMD_DIR" "$STUB_BIN" "${STATE_DIR}/users"
    chmod 775 "${DATA_DIR}/repos" "${DATA_DIR}/bin"
    : >"$CALL_LOG"
    : >"$MUT_LOG"
    touch "${STATE_DIR}/users/ec2-user"
    printf '%s\n' "$CLOUD_INIT_ORIGINAL" >"${SUDOERS_DIR}/90-cloud-init-users"

    local c
    for c in $MOCKED; do
        ln -s "$MOCK_CMDS" "${STUB_BIN}/${c}"
    done
}

teardown_sandbox() {
    rm -rf "$SANDBOX"
}

# run_setup [VAR=value ...] -- [script args ...]  (stdout+stderr go to $OUT)
run_setup() {
    local envs=()
    while [ $# -gt 0 ] && [ "$1" != "--" ]; do
        envs+=("$1")
        shift
    done
    shift
    env \
        PATH="${STUB_BIN}:${PATH}" \
        MOCK_REAL_PATH="$PATH" \
        MOCK_STATE_DIR="$STATE_DIR" \
        MOCK_CALL_LOG="$CALL_LOG" \
        MOCK_MUT_LOG="$MUT_LOG" \
        SUDOERS_DIR="$SUDOERS_DIR" \
        SYSTEMD_DIR="$SYSTEMD_DIR" \
        "${envs[@]}" \
        "$SETUP_SCRIPT" --data-dir "$DATA_DIR" "$@" >"$OUT" 2>&1
}

# Every file the script may write, as one comparable listing. The mocks' own
# state directory is left out: it is bookkeeping, and the mutation log covers it.
tree_listing() {
    find "$DATA_DIR" "$SUDOERS_DIR" "$SYSTEMD_DIR" -printf '%p %m\n' | sort
    cat "${SUDOERS_DIR}/90-cloud-init-users"
}

# first_line PATTERN — line number of the first call that matches.
first_line() {
    grep -n -m1 -E -- "$1" "$CALL_LOG" | cut -d: -f1
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

ALL_STEPS="group agent-user daemon-membership directories shared-read sudoers-rule cloud-init-sudoers firewall-ipv4 firewall-ipv6 firewall-unit"

echo "=== test: dry run — prints every command, changes nothing ==="
setup_sandbox
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
    "+ systemctl enable pilot-agent-firewall.service" "$EXPECTED_RULE"; do
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

echo "=== test: apply — steps run in order and leave the host as designed ==="
setup_sandbox
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply exited non-zero"
fi
prev=0
for pattern in '^groupadd ' '^useradd ' '^usermod ' '^install -d ' '^chown -R :pilot ' '^visudo -cf ' '^visudo -c$' '^iptables -w -N ' '^ip6tables -w -N ' '^systemctl enable '; do
    n="$(first_line "$pattern" || true)"
    if [ -n "$n" ] && [ "$n" -gt "$prev" ]; then
        pass "in order: ${pattern}"
        prev="$n"
    else
        fail_test "out of order or missing: ${pattern} (line '${n}', after ${prev})"
    fi
done
if [ "$(cat "${SUDOERS_DIR}/90-pilot-agent-runas")" = "$EXPECTED_RULE" ]; then
    pass "sudoers file holds exactly the run-as rule"
else
    fail_test "sudoers file content: $(cat "${SUDOERS_DIR}/90-pilot-agent-runas" 2>&1)"
fi
if [ "$(stat -c %a "${SUDOERS_DIR}/90-pilot-agent-runas")" = "440" ]; then
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
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak")" = "$CLOUD_INIT_ORIGINAL" ]; then
    pass "backup holds the original cloud-init file"
else
    fail_test "backup missing or different"
fi
if [ "$(stat -c %a "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak")" = "600" ]; then
    pass "backup is root-only (0600)"
else
    fail_test "backup mode is not 0600"
fi
for want in "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home" \
    "install -d -o pilot-agent -g pilot -m 2770 ${DATA_DIR}/agent-workspaces" \
    "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home/go/cache" \
    "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home/go/mod" \
    "useradd --system --gid pilot --home-dir ${DATA_DIR}/agent-home --no-create-home --shell /usr/sbin/nologin pilot-agent" \
    "chown -R :pilot ${DATA_DIR}/repos" "chown -R :pilot ${DATA_DIR}/bin" \
    "chmod -R g+rX,g-w ${DATA_DIR}/repos"; do
    if grep -qF -- "$want" "$CALL_LOG"; then
        pass "ran: ${want}"
    else
        fail_test "missing call: ${want}"
    fi
done
if [ "$(stat -c %a "${DATA_DIR}/agent-workspaces")" = "2770" ] && [ "$(stat -c %a "${DATA_DIR}/agent-home")" = "750" ]; then
    pass "workspace mode 2770, home mode 750"
else
    fail_test "directory modes wrong"
fi
if [ "$(stat -c %a "${DATA_DIR}/repos")" = "755" ] && [ "$(stat -c %a "${DATA_DIR}/bin")" = "755" ]; then
    pass "repos and bin: group read and traverse, no group write"
else
    fail_test "repos/bin modes: $(stat -c %a "${DATA_DIR}/repos") $(stat -c %a "${DATA_DIR}/bin")"
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
for s in $ALL_STEPS; do
    expect_step "$s" "done"
done

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
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
if [ ! -e "${SUDOERS_DIR}/90-pilot-agent-runas" ]; then pass "sudoers file removed"; else fail_test "sudoers file still there"; fi
if [ ! -e "$UNIT" ] && [ ! -e "${STATE_DIR}/enabled/pilot-agent-firewall.service" ]; then pass "unit removed and disabled"; else fail_test "unit still there"; fi
if ! grep -q PILOT_AGENT_EGRESS "${STATE_DIR}/iptables.rules" "${STATE_DIR}/ip6tables.rules"; then
    pass "firewall chain and jump removed (v4 and v6)"
else
    fail_test "firewall rules left"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ] && [ ! -e "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak" ]; then
    pass "cloud-init file restored from the backup"
else
    fail_test "cloud-init file not restored"
fi
if ! grep -qE '^(userdel|groupdel|gpasswd|rm -rf)' "$CALL_LOG" && [ -d "${DATA_DIR}/agent-home/go/mod" ] && [ -e "${STATE_DIR}/users/pilot-agent" ]; then
    pass "user, group and directories are left in place"
else
    fail_test "undo removed the user, the group or a directory"
fi
for want in "userdel pilot-agent" "groupdel pilot" "rm -rf ${DATA_DIR}/agent-home ${DATA_DIR}/agent-workspaces"; do
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
if ! grep -qE '^(iptables|ip6tables|systemctl)' "$MUT_LOG"; then
    pass "the run stopped: no firewall or unit change"
else
    fail_test "steps after the failed validation ran"
fi
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
if run_setup -- --keep-daemon-sudo; then
    pass "apply --keep-daemon-sudo exits 0"
else
    cat "$OUT"
    fail_test "apply --keep-daemon-sudo exited non-zero"
fi
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ] && [ ! -e "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak" ]; then
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
for s in cloud-init-sudoers firewall-ipv4 firewall-ipv6 firewall-unit; do
    expect_step "$s" skipped
done
expect_step sudoers-rule failed
teardown_sandbox

# mutation pin: shared-read checks only the top-level group and mode, so a file with mode 600 or another group under repos/bin is skipped -> this test fails
echo "=== test: shared-read looks inside repos and bin ==="
setup_sandbox
run_setup -- >/dev/null || true
set_meta() { mkdir -p "${STATE_DIR}/meta" && echo "$2" >"${STATE_DIR}/meta/$(echo "$1" | tr '/' '_')"; }
mkdir -p "${DATA_DIR}/repos/p"
printf 'x' >"${DATA_DIR}/repos/p/ok"
chmod 640 "${DATA_DIR}/repos/p/ok"
chmod 755 "${DATA_DIR}/repos/p"
set_meta "${DATA_DIR}/repos/p" root:pilot
set_meta "${DATA_DIR}/repos/p/ok" root:pilot
: >"$MUT_LOG"
run_setup -- || true
if ! grep -qE '^chown -R :pilot ' "$MUT_LOG"; then
    pass "readable files in the shared group: skipped"
else
    fail_test "a tree that is fine was changed"
fi
printf 'x' >"${DATA_DIR}/repos/p/secret"
chmod 600 "${DATA_DIR}/repos/p/secret"
set_meta "${DATA_DIR}/repos/p/secret" root:pilot
: >"$MUT_LOG"
run_setup -- || true
if grep -qF "chmod -R g+rX,g-w ${DATA_DIR}/repos" "$MUT_LOG"; then
    pass "a file with mode 600 under repos is fixed"
else
    fail_test "a file with mode 600 under repos was not noticed"
fi
rm -f "${DATA_DIR}/repos/p/secret"
printf 'x' >"${DATA_DIR}/repos/p/other"
chmod 640 "${DATA_DIR}/repos/p/other"
set_meta "${DATA_DIR}/repos/p/other" root:other
: >"$MUT_LOG"
run_setup -- || true
if grep -qF "chown -R :pilot ${DATA_DIR}/repos" "$MUT_LOG"; then
    pass "a file in another group under repos is fixed"
else
    fail_test "a file in another group under repos was not noticed"
fi
teardown_sandbox

# mutation pin: the cloud-init file is restored as 0440 root:root, not with the mode and owner it had -> this test fails
echo "=== test: the cloud-init restore keeps the mode and owner of the file ==="
setup_sandbox
chmod 640 "${SUDOERS_DIR}/90-cloud-init-users"
mkdir -p "${STATE_DIR}/meta"
echo "root:wheel" >"${STATE_DIR}/meta/$(echo "${SUDOERS_DIR}/90-cloud-init-users" | tr '/' '_')"
run_setup MOCK_VISUDO_C_FAIL=1 -- || true
if grep -qF "install -m 640 -o root -g wheel ${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak ${SUDOERS_DIR}/90-cloud-init-users" "$CALL_LOG" \
    && [ "$(stat -c %a "${SUDOERS_DIR}/90-cloud-init-users")" = "640" ]; then
    pass "restore after a failed validation keeps mode 640 and group wheel"
else
    grep -F "90-cloud-init-users" "$CALL_LOG" || true
    fail_test "restore after a failed validation lost the mode or the owner"
fi
run_setup -- || true
: >"$CALL_LOG"
run_setup -- --undo || true
if grep -qF "install -m 640 -o root -g wheel ${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak ${SUDOERS_DIR}/90-cloud-init-users" "$CALL_LOG"; then
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
if [ -e "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak" ]; then
    pass "the backup is kept after a failed validation"
else
    fail_test "the backup was removed after a failed validation"
fi
teardown_sandbox

# mutation pin: an existing backup is reused, so undo restores content that is older than the last apply -> this test fails
echo "=== test: a second apply refreshes a stale backup ==="
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
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak")" = "$REWRITTEN" ]; then
    pass "backup holds the file as it was before the second apply"
else
    fail_test "backup is stale"
fi
run_setup -- --undo >/dev/null || true
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$REWRITTEN" ]; then
    pass "undo restores the content from before the last apply"
else
    fail_test "undo restored stale content: $(cat "${SUDOERS_DIR}/90-cloud-init-users")"
fi
teardown_sandbox

# mutation pin: the port 9091 rule covers only IPv4 127.0.0.1, not ::1 or the host's own addresses -> this test fails
echo "=== test: the gateway port is rejected on ::1 and on every local address ==="
setup_sandbox
run_setup -- >/dev/null || true
LOCAL_RULE="-p tcp --dport 9091 -m addrtype --dst-type LOCAL -j REJECT --reject-with tcp-reset"
if grep -qF -- "-p tcp -d ::1 --dport 9091 -j REJECT" "${STATE_DIR}/ip6tables.rules"; then
    pass "ip6tables rejects tcp to ::1:9091"
else
    fail_test "no ip6tables rule for ::1:9091"
fi
for f in iptables ip6tables; do
    if grep -qF -- "$LOCAL_RULE" "${STATE_DIR}/${f}.rules" \
        && grep -qF -- "-A PILOT_AGENT_EGRESS ${LOCAL_RULE}" "${SYSTEMD_DIR}/pilot-agent-firewall.service"; then
        pass "${f}: port 9091 rejected on any local address (chain and unit)"
    else
        fail_test "${f}: no rule for port 9091 on local addresses"
    fi
done
teardown_sandbox

echo "=== test: not root, bad arguments ==="
setup_sandbox
if run_setup MOCK_UID=1000 -- ; then
    fail_test "must refuse to run as a non-root user"
else
    pass "refuses to run as a non-root user"
fi
if [ ! -s "$MUT_LOG" ]; then pass "nothing mutated as non-root"; else fail_test "mutations as non-root"; fi
if run_setup -- --agent-user ec2-user; then
    fail_test "agent user equal to daemon user must be refused"
else
    pass "agent user equal to daemon user is refused"
fi
if run_setup -- --bogus; then
    fail_test "unknown argument must be refused"
else
    pass "unknown argument is refused"
fi
teardown_sandbox

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
