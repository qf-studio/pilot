#!/bin/bash
# Plain-bash test suite for setup-agent-user.sh — no bats dependency, no root,
# no real host, and no GNU-only flag of find or stat (it runs on macOS too).
# The script runs against a temp root with a PATH of mock commands
# (scripts/box/testdata/mock-host-cmds.sh, linked under the names useradd,
# groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown,
# chgrp, chmod, id, getent, stat and sudo) that record their arguments; the
# sudoers and systemd directories are pointed at the temp root through
# SUDOERS_DIR / SYSTEMD_DIR. The real-host counterpart is
# setup-agent-user_container_test.sh.
#
# Usage: ./scripts/box/setup-agent-user_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT="${SCRIPT_DIR}/setup-agent-user.sh"
MOCK_CMDS="${SCRIPT_DIR}/testdata/mock-host-cmds.sh"
MOCKED="useradd groupadd usermod visudo iptables ip6tables systemctl install chown chgrp chmod id getent stat sudo"

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

# mode_of PATH — octal mode, GNU or BSD stat.
mode_of() {
    stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"
}

# set_meta PATH OWNER:GROUP — the owner and group the mocks report for PATH.
set_meta() {
    echo "$2" >"${STATE_DIR}/meta/$(echo "$1" | tr '/' '_')"
}

# group_of PATH — the group the mocks report (a chgrp record, else the real one).
group_of() {
    local mf
    mf="${STATE_DIR}/meta/$(echo "$1" | tr '/' '_')"
    if [ -e "$mf" ]; then
        cut -d: -f2 "$mf"
    else
        stat -c %G "$1" 2>/dev/null || stat -f %Sg "$1"
    fi
}

# build_repos — repos/alpha (a repository one level down) and repos/org/beta
# (two levels down, the middle directory and the repository at mode 0700), each
# with private files in the working tree, a hooks directory and git files at 0600.
build_repos() {
    local r="${DATA_DIR}/repos" repo
    for repo in "$r/alpha" "$r/org/beta"; do
        mkdir -p "$repo/.git/objects/ab" "$repo/.git/refs/heads" "$repo/.git/hooks" "$repo/src"
        printf 'ref: refs/heads/main\n' >"$repo/.git/HEAD"
        printf '[core]\n' >"$repo/.git/config"
        printf 'x\n' >"$repo/.git/packed-refs"
        printf 'x\n' >"$repo/.git/index"
        printf 'x\n' >"$repo/.git/objects/ab/obj"
        printf 'x\n' >"$repo/.git/refs/heads/main"
        printf '#!/bin/sh\n' >"$repo/.git/hooks/pre-commit"
        printf 'TOKEN=x\n' >"$repo/secret.env"
        printf 'package x\n' >"$repo/src/code.go"
        chmod 600 "$repo/.git/HEAD" "$repo/.git/config" "$repo/.git/packed-refs" "$repo/.git/index" \
            "$repo/.git/refs/heads/main" "$repo/secret.env" "$repo/src/code.go"
        chmod 444 "$repo/.git/objects/ab/obj"
        chmod 700 "$repo/.git/hooks/pre-commit"
        chmod 700 "$repo/.git/hooks" "$repo/.git/refs/heads" "$repo/.git/refs" "$repo/.git/objects/ab" \
            "$repo/.git/objects" "$repo/.git" "$repo/src" "$repo"
    done
    chmod 700 "$r/org"
}

# repo_snapshot — path, mode and group of everything under repos and bin.
repo_snapshot() {
    local p
    find "${DATA_DIR}/repos" "${DATA_DIR}/bin" | sort | while IFS= read -r p; do
        if [ -L "$p" ]; then
            echo "${p} link"
        else
            echo "${p} $(mode_of "$p") $(group_of "$p")"
        fi
    done
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
    STATE_FILE="${DATA_DIR}/.agent-setup-state"
    R="${DATA_DIR}/repos"
    BAK="${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak"

    mkdir -p "${DATA_DIR}/repos" "${DATA_DIR}/bin" "$SUDOERS_DIR" "$SYSTEMD_DIR" "$STUB_BIN" "${STATE_DIR}/users" "${STATE_DIR}/meta"
    chmod 755 "$DATA_DIR"
    chmod 775 "${DATA_DIR}/repos" "${DATA_DIR}/bin"
    printf '#!/bin/sh\n' >"${DATA_DIR}/bin/pilot"
    chmod 755 "${DATA_DIR}/bin/pilot"
    : >"$CALL_LOG"
    : >"$MUT_LOG"
    touch "${STATE_DIR}/users/ec2-user"
    set_meta "$DATA_DIR" ec2-user:ec2-user
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
        MOCK_STATEFILE="$STATE_FILE" \
        SUDOERS_DIR="$SUDOERS_DIR" \
        SYSTEMD_DIR="$SYSTEMD_DIR" \
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

# expect_refused LABEL [VAR=value ...] -- ARGS... — exit 2, nothing mutated, no
# chgrp or chmod called.
expect_refused() {
    local label="$1" rc=0
    shift
    : >"$MUT_LOG"
    : >"$CALL_LOG"
    run_setup "$@" || rc=$?
    if [ "$rc" = 2 ] && [ ! -s "$MUT_LOG" ] && ! grep -qE '^(chgrp|chmod|chown) ' "$CALL_LOG"; then
        pass "refused with exit 2, nothing changed: ${label}"
    else
        cat "$OUT"
        fail_test "${label}: exit ${rc}, mutations: $(wc -l <"$MUT_LOG" | tr -d ' ')"
    fi
}

ALL_STEPS="group agent-user daemon-membership directories shared-read sudoers-rule firewall-ipv4 firewall-ipv6 firewall-unit cloud-init-sudoers"

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
check_not "no state file is written by a dry run" test -e "$STATE_FILE"
for want in "+ groupadd --system pilot" "+ useradd --system" "+ usermod -aG pilot ec2-user" \
    "+ visudo -cf" "+ iptables -w -N PILOT_AGENT_EGRESS" "+ ip6tables -w -N PILOT_AGENT_EGRESS" \
    "+ systemctl enable pilot-agent-firewall.service" "$EXPECTED_RULE" \
    "+ chgrp -h pilot ${DATA_DIR}/repos/alpha/.git/HEAD" "+ chmod 640 ${DATA_DIR}/repos/alpha/.git/HEAD" \
    "+ record ${DATA_DIR}/repos/alpha/.git/HEAD group="; do
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
build_repos
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply exited non-zero"
fi
prev=0
for pattern in '^groupadd ' '^useradd ' '^usermod ' '^install -d ' '^chgrp -h pilot ' '^visudo -cf ' '^iptables -w -N ' '^ip6tables -w -N ' '^systemctl enable ' '^install -m 0600 -o root -g root .*\.pilot-agent\.bak$' '^visudo -c$'; do
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
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak")" = "$CLOUD_INIT_ORIGINAL" ]; then
    pass "backup holds the original cloud-init file"
else
    fail_test "backup missing or different"
fi
if [ "$(mode_of "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak")" = "600" ]; then
    pass "backup is root-only (0600)"
else
    fail_test "backup mode is not 0600"
fi
for want in "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home" \
    "install -d -o pilot-agent -g pilot -m 2770 ${DATA_DIR}/agent-workspaces" \
    "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home/go/cache" \
    "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home/go/mod" \
    "useradd --system --gid pilot --home-dir ${DATA_DIR}/agent-home --no-create-home --shell /usr/sbin/nologin pilot-agent"; do
    if grep -qF -- "$want" "$CALL_LOG"; then
        pass "ran: ${want}"
    else
        fail_test "missing call: ${want}"
    fi
done
if [ "$(mode_of "${DATA_DIR}/agent-workspaces")" = "2770" ] && [ "$(mode_of "${DATA_DIR}/agent-home")" = "750" ]; then
    pass "workspace mode 2770, home mode 750"
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
for s in $ALL_STEPS; do
    expect_step "$s" "done"
done
check_not "chgrp and chmod found every path in the state file first" test -s "${STATE_DIR}/unrecorded.log"
check_not "the firewall never left a moment without reject rules" test -e "${STATE_DIR}/gap.log"

echo "=== test: second apply skips every step ==="
: >"$MUT_LOG"
state_size="$(wc -c <"$STATE_FILE" | tr -d ' ')"
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
check "second apply added nothing to the state file" test "$(wc -c <"$STATE_FILE" | tr -d ' ')" = "$state_size"

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
if [ "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL" ] && [ ! -e "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak" ]; then
    pass "cloud-init file restored from the backup"
else
    fail_test "cloud-init file not restored"
fi
if grep -qF "from the newest backup ${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak" "$OUT"; then
    pass "undo says which backup it restored"
else
    fail_test "undo does not name the backup"
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
if run_setup NO_CONFIRM=1 -- --keep-daemon-sudo; then
    pass "apply --keep-daemon-sudo exits 0 without --confirm-root-path"
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
for s in firewall-ipv4 firewall-ipv6 firewall-unit cloud-init-sudoers; do
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
if grep -qF "install -m 640 -o root -g wheel ${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak ${SUDOERS_DIR}/90-cloud-init-users" "$CALL_LOG" \
    && [ "$(mode_of "${SUDOERS_DIR}/90-cloud-init-users")" = "640" ]; then
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
if grep -qF "from the newest backup ${BAK}" "$OUT" && [ -e "${BAK}.1" ] && [ -e "${BAK}.2" ]; then
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

# ----------------------------------------------------- item 1: validation

# mutation pin: the system-directory check is removed from the --data-dir validation -> this test fails
echo "=== test: --data-dir that is a system directory, or one above it, is refused before any change ==="
setup_sandbox
for sysdir in /usr /etc /; do
    set_meta "$sysdir" ec2-user:ec2-user
    expect_refused "--data-dir ${sysdir}" -- --data-dir "$sysdir"
done
set_meta /var ec2-user:ec2-user
expect_refused "--data-dir /var (an ancestor of /var/lib)" -- --data-dir /var
if grep -qF "system directory" "$OUT" || grep -qF "contains the system directory" "$OUT"; then
    pass "the message names the system directory"
else
    fail_test "no system directory message: $(cat "$OUT")"
fi
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
check "the canonical path is used" grep -qF "chgrp -h pilot ${DATA_DIR}/bin/pilot" "$OUT"
teardown_sandbox

echo "=== test: user and group arguments ==="
setup_sandbox
printf '999:/bin/bash:lowuser\n' >"${STATE_DIR}/users/lowuser"
printf '0:/usr/sbin/nologin:rootish\n' >"${STATE_DIR}/users/rootish"
printf '1001:/bin/bash:logger\n' >"${STATE_DIR}/users/logger"
printf '1002:/usr/sbin/nologin:adminish\n' >"${STATE_DIR}/users/adminish"
mkdir -p "${STATE_DIR}/members"
echo adminish >"${STATE_DIR}/members/wheel"
expect_refused "--daemon-user root" -- --daemon-user root
expect_refused "--daemon-user with uid below 1000" -- --daemon-user lowuser
expect_refused "--daemon-user that does not exist" -- --daemon-user nobody-here
expect_refused "--agent-user root" -- --agent-user root
expect_refused "--agent-user with uid 0" -- --agent-user rootish
expect_refused "--agent-user with a login shell" -- --agent-user logger
expect_refused "--agent-user in wheel" -- --agent-user adminish
expect_refused "--agent-user equal to the daemon user" -- --agent-user ec2-user
for g in root wheel sudo adm ec2-user; do
    expect_refused "--group ${g}" -- --group "$g"
done
for bad in "Pilot" "1pilot" "_pilot" "pilot agent" "pilot;id" "-pilot" "pilot.x" ""; do
    expect_refused "--agent-user '${bad}'" -- --agent-user "$bad"
    expect_refused "--group '${bad}'" -- --group "$bad"
done
teardown_sandbox

echo "=== test: an existing agent user with a no-login shell outside wheel and sudo is accepted ==="
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

# ------------------------------------------------- item 1: containment

# mutation pin: the containment check for the resolved binary path is removed -> this test fails
echo "=== test: a binary that is a symbolic link outside the data dir is refused ==="
setup_sandbox
mkdir "${SANDBOX}/outside"
printf '#!/bin/sh\n' >"${SANDBOX}/outside/evil"
chmod 600 "${SANDBOX}/outside/evil"
rm "${DATA_DIR}/bin/pilot"
ln -s "${SANDBOX}/outside/evil" "${DATA_DIR}/bin/pilot"
expect_refused "binary -> outside the data dir" --
check "the file outside keeps mode 600" test "$(mode_of "${SANDBOX}/outside/evil")" = 600
check "the message names the resolved path" grep -qF "resolves to '${SANDBOX}/outside/evil', outside the data dir" "$OUT"
check_not "no state file was written" test -e "$STATE_FILE"
teardown_sandbox

echo "=== test: repos, bin and the agent directories that leave the data dir are refused ==="
setup_sandbox
mkdir "${SANDBOX}/outside"
rmdir "${DATA_DIR}/repos"
ln -s "${SANDBOX}/outside" "${DATA_DIR}/repos"
expect_refused "repos -> outside the data dir" --
rm "${DATA_DIR}/repos"
mkdir "${DATA_DIR}/repos"
rm -r "${DATA_DIR:?}/bin"
ln -s "${SANDBOX}/outside" "${DATA_DIR}/bin"
expect_refused "bin -> outside the data dir" --
rm "${DATA_DIR}/bin"
mkdir "${DATA_DIR}/bin"
ln -s "${SANDBOX}/outside" "${DATA_DIR}/agent-home"
expect_refused "agent-home is a symbolic link" --
check "the directory outside is untouched" test "$(mode_of "${SANDBOX}/outside")" = "$(mode_of "${SANDBOX}/outside")"
teardown_sandbox

echo "=== test: a binary that is a symbolic link inside the data dir is resolved and the link left alone ==="
setup_sandbox
mkdir -p "${DATA_DIR}/releases/v1"
printf '#!/bin/sh\n' >"${DATA_DIR}/releases/v1/pilot"
chmod 700 "${DATA_DIR}/releases/v1/pilot"
rm "${DATA_DIR}/bin/pilot"
ln -s "${DATA_DIR}/releases/v1/pilot" "${DATA_DIR}/bin/pilot"
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply with a binary link inside the data dir failed"
fi
check "the target gets group r-x (0750)" test "$(mode_of "${DATA_DIR}/releases/v1/pilot")" = 750
check "the target is in the shared group" test "$(group_of "${DATA_DIR}/releases/v1/pilot")" = pilot
check_not "the link itself was not passed to chgrp or chmod" grep -qE "^(chgrp|chmod) .* ${DATA_DIR}/bin/pilot\$" "$CALL_LOG"
run_setup -- --undo >/dev/null || true
check "undo restores the target's mode 700" test "$(mode_of "${DATA_DIR}/releases/v1/pilot")" = 700
teardown_sandbox

# --------------------------------------------------- item 2: shared read

echo "=== test: shared read gives the group exactly the git directories, never the working tree ==="
setup_sandbox
build_repos
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply exited non-zero"
fi
check "bin directory: group r-x (mode 755, no group write)" test "$(mode_of "${DATA_DIR}/bin")" = 755
check "repos top directory: group r-x, group write removed (775 -> 755)" test "$(mode_of "$R")" = 755
check "the binary and the bin directory are in the shared group" test "$(group_of "${DATA_DIR}/bin/pilot")" = pilot -a "$(group_of "${DATA_DIR}/bin")" = pilot
check "middle directory repos/org (0700): group traverse only (0710)" test "$(mode_of "$R/org")" = 710
check "repository directory repos/org/beta (0700): group traverse only (0710)" test "$(mode_of "$R/org/beta")" = 710
check "repository directory repos/alpha (0700): group traverse only (0710)" test "$(mode_of "$R/alpha")" = 710
check "the middle directory is in the shared group" test "$(group_of "$R/org")" = pilot
for repo in "$R/alpha" "$R/org/beta"; do
    g="$repo/.git"
    check "${g#"$R"/}: git directory, objects, refs: group r-x (0750)" test "$(mode_of "$g")" = 750 -a "$(mode_of "$g/objects")" = 750 -a "$(mode_of "$g/objects/ab")" = 750 -a "$(mode_of "$g/refs")" = 750 -a "$(mode_of "$g/refs/heads")" = 750
    check "${g#"$R"/}: HEAD, config, packed-refs, a ref: group r (0640)" test "$(mode_of "$g/HEAD")" = 640 -a "$(mode_of "$g/config")" = 640 -a "$(mode_of "$g/packed-refs")" = 640 -a "$(mode_of "$g/refs/heads/main")" = 640
    check "${g#"$R"/}: an object with 0444 keeps its mode and gets the group" test "$(mode_of "$g/objects/ab/obj")" = 444 -a "$(group_of "$g/objects/ab/obj")" = pilot
    check "${g#"$R"/}: hooks, index are untouched" test "$(mode_of "$g/hooks")" = 700 -a "$(mode_of "$g/hooks/pre-commit")" = 700 -a "$(mode_of "$g/index")" = 600 -a "$(group_of "$g/index")" != pilot
    # mutation pin: the repos step is recursive over the working tree again (chmod -R / chgrp -R on the repository) -> this check fails
    check "${repo#"$R"/}: a private file in the working tree keeps mode 0600 and its group" test "$(mode_of "$repo/secret.env")" = 600 -a "$(group_of "$repo/secret.env")" != pilot
    check "${repo#"$R"/}: the working tree's source directory and files are untouched" test "$(mode_of "$repo/src")" = 700 -a "$(mode_of "$repo/src/code.go")" = 600 -a "$(group_of "$repo/src/code.go")" != pilot
done
check_not "no recursive chmod or chown was run" grep -qE '^(chmod|chown|chgrp) .*-R' "$CALL_LOG"
check_not "chgrp and chmod found every path in the state file first" test -s "${STATE_DIR}/unrecorded.log"
check "the state file is root-only (root:root 0600)" test "$(cat "${STATE_DIR}/meta/$(echo "$STATE_FILE" | tr '/' '_')")" = root:root -a "$(mode_of "$STATE_FILE")" = 600
check "the state file holds the earlier mode of repos/org (700)" grep -qF "$(printf '\t700\t%s' "$R/org")" "$STATE_FILE"
check "the state file holds the earlier mode and group of the binary" grep -qF "$(printf '755\t%s' "${DATA_DIR}/bin/pilot")" "$STATE_FILE"
check_not "the state file does not hold the working tree" grep -qF "secret.env" "$STATE_FILE"
teardown_sandbox

echo "=== test: shared read is idempotent and notices a file made later in a git directory only ==="
setup_sandbox
build_repos
run_setup -- >/dev/null || true
: >"$MUT_LOG"
run_setup -- || true
expect_step shared-read skipped
check "a second apply changes nothing" test ! -s "$MUT_LOG"
printf 'x\n' >"$R/alpha/.git/refs/heads/late"
chmod 600 "$R/alpha/.git/refs/heads/late"
printf 'x\n' >"$R/alpha/src/late.go"
chmod 600 "$R/alpha/src/late.go"
: >"$MUT_LOG"
run_setup -- || true
expect_step shared-read "done"
check "the new ref in the git directory is fixed (0640)" test "$(mode_of "$R/alpha/.git/refs/heads/late")" = 640
check "the new file in the working tree is not" test "$(mode_of "$R/alpha/src/late.go")" = 600
teardown_sandbox

echo "=== test: a symbolic link in a git directory is not followed ==="
setup_sandbox
build_repos
mkdir "${SANDBOX}/outside"
printf 'secret\n' >"${SANDBOX}/outside/key"
chmod 600 "${SANDBOX}/outside/key"
ln -s "${SANDBOX}/outside/key" "$R/alpha/.git/refs/heads/link"
ln -s "${SANDBOX}/outside" "$R/alpha/.git/objects/pack"
ln -s "${SANDBOX}/outside" "$R/org/beta/.git/refs.link"
if run_setup -- ; then
    pass "apply exits 0 with links in the git directory"
else
    cat "$OUT"
    fail_test "apply failed with links in the git directory"
fi
check "the file behind a link keeps mode 600" test "$(mode_of "${SANDBOX}/outside/key")" = 600
check "the directory behind a link keeps its mode" test "$(mode_of "${SANDBOX}/outside")" = "$(mode_of "${SANDBOX}/outside")"
check_not "no chgrp or chmod named a link or what is behind it" grep -qE "^(chgrp|chmod) .*(refs/heads/link|objects/pack|refs\.link|outside)" "$CALL_LOG"
teardown_sandbox

echo "=== test: a repository whose working tree is a worktree link (.git is a file) is left alone ==="
setup_sandbox
mkdir -p "$R/wt"
printf 'gitdir: /elsewhere\n' >"$R/wt/.git"
chmod 600 "$R/wt/.git"
chmod 700 "$R/wt"
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed with a .git file"
fi
check "the directory and the .git file are untouched" test "$(mode_of "$R/wt")" = 700 -a "$(mode_of "$R/wt/.git")" = 600
teardown_sandbox

echo "=== test: undo restores the group and mode of every recorded path and prints each one ==="
setup_sandbox
build_repos
before_snap="$(repo_snapshot)"
run_setup -- >/dev/null || true
check "apply changed the tree" test "$before_snap" != "$(repo_snapshot)"
: >"$MUT_LOG"
if run_setup -- --undo; then
    pass "undo exits 0"
else
    cat "$OUT"
    fail_test "undo exited non-zero"
fi
if [ "$before_snap" = "$(repo_snapshot)" ]; then
    pass "repos and bin are back to the earlier groups and modes"
else
    diff <(echo "$before_snap") <(repo_snapshot) | head -20 || true
    fail_test "undo did not restore every path"
fi
for p in "${DATA_DIR}/bin/pilot" "${DATA_DIR}/bin" "$R" "$R/org" "$R/org/beta" "$R/alpha/.git/HEAD" "$R/alpha/.git/objects/ab/obj"; do
    check "undo prints: restored ${p#"$SANDBOX"/}" grep -qE "^restored: ${p} group=[^ ]+ mode=[0-7]+\$" "$OUT"
done
check_not "undo prints nothing for the working tree" grep -qF "secret.env" "$OUT"
check "the state file is removed after the restore" test ! -e "$STATE_FILE"
check_not "undo ran no recursive command" grep -qE '^(chmod|chgrp) .*-R' "$MUT_LOG"
: >"$MUT_LOG"
check "a second undo changes nothing" run_setup -- --undo
check "a second undo ran no mutating command" test ! -s "$MUT_LOG"
teardown_sandbox

echo "=== test: undo refuses a state file that is not root-only ==="
setup_sandbox
build_repos
run_setup -- >/dev/null || true
set_meta "$STATE_FILE" ec2-user:ec2-user
before_snap="$(repo_snapshot)"
if run_setup -- --undo; then
    fail_test "undo must fail on a state file owned by another user"
else
    pass "undo exits non-zero on a state file owned by another user"
fi
expect_step shared-read failed
check "nothing was restored" test "$before_snap" = "$(repo_snapshot)"
teardown_sandbox

# ----------------------------------------------------- item 3: path chain

echo "=== test: repos reached through a symlink chain the agent user cannot traverse — one line per directory, step fails ==="
setup_sandbox
build_repos
mkdir "${DATA_DIR}/store"
mv "${DATA_DIR}/repos" "${DATA_DIR}/store/repos"
ln -s "${DATA_DIR}/store/repos" "${DATA_DIR}/repos"
chmod 700 "${DATA_DIR}/store"
if run_setup -- ; then
    fail_test "apply must fail when the agent cannot traverse the chain"
else
    pass "apply exits non-zero"
fi
expect_step shared-read failed
want="agent user pilot-agent cannot traverse ${DATA_DIR}/store: setfacl -m u:pilot-agent:x ${DATA_DIR}/store  (or: chmod o+x ${DATA_DIR}/store)"
if has_line "$OUT" "$want"; then
    pass "prints the command for the directory without traverse"
else
    grep -F 'cannot traverse' "$OUT" || true
    fail_test "no line for ${DATA_DIR}/store"
fi
check "one line only: the other directories of the chain are fine" test "$(grep -c 'cannot traverse' "$OUT" | tr -d ' ')" = 1
expect_step firewall-ipv4 skipped
chmod 711 "${DATA_DIR}/store"
if run_setup -- ; then
    pass "apply passes once the directory is traversable"
else
    cat "$OUT"
    fail_test "apply failed after the grant"
fi
check "the symlink itself was not changed, the target directory was" test -L "${DATA_DIR}/repos" -a "$(mode_of "${DATA_DIR}/store/repos")" = 755
teardown_sandbox

echo "=== test: --repos-link (repeatable) checks every directory of each chain ==="
setup_sandbox
mkdir -p "${SANDBOX}/home/user" "${SANDBOX}/home2/dev"
ln -s "${DATA_DIR}/repos" "${SANDBOX}/home/user/repos"
ln -s "${DATA_DIR}/repos" "${SANDBOX}/home2/dev/work"
chmod 755 "${SANDBOX}/home" "${SANDBOX}/home2"
chmod 700 "${SANDBOX}/home/user" "${SANDBOX}/home2/dev"
if run_setup -- --repos-link "${SANDBOX}/home/user/repos" --repos-link "${SANDBOX}/home2/dev/work"; then
    fail_test "apply must fail with two chains the agent cannot traverse"
else
    pass "apply exits non-zero"
fi
check "a line for the first chain" has_line "$OUT" "agent user pilot-agent cannot traverse ${SANDBOX}/home/user: setfacl -m u:pilot-agent:x ${SANDBOX}/home/user  (or: chmod o+x ${SANDBOX}/home/user)"
check "a line for the second chain" has_line "$OUT" "agent user pilot-agent cannot traverse ${SANDBOX}/home2/dev: setfacl -m u:pilot-agent:x ${SANDBOX}/home2/dev  (or: chmod o+x ${SANDBOX}/home2/dev)"
check "two lines, one per directory" test "$(grep -c 'cannot traverse' "$OUT" | tr -d ' ')" = 2
expect_step shared-read failed
chmod 711 "${SANDBOX}/home/user" "${SANDBOX}/home2/dev"
check "apply passes once both are traversable" run_setup -- --repos-link "${SANDBOX}/home/user/repos" --repos-link "${SANDBOX}/home2/dev/work"
ln -s "${SANDBOX}" "${SANDBOX}/home/elsewhere"
if run_setup -- --repos-link "${SANDBOX}/home/elsewhere"; then
    fail_test "a link that does not lead to the repos directory must fail"
else
    pass "a link that does not lead to the repos directory fails"
fi
check "the message says so" grep -qF "does not lead to the repos directory" "$OUT"
expect_refused "--repos-link with a '..' part" -- --repos-link "${SANDBOX}/home/../home/user/repos"
expect_refused "--repos-link that is relative" -- --repos-link "home/user/repos"
teardown_sandbox

# ------------------------------------------------ item 4: cloud-init proof

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
    pass "apply exits 0: no NOPASSWD rule is left but the agent rule"
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

echo "=== test: a rule with the run-as part (deploy) is left untouched and reported, and the proof still fails on it ==="
setup_sandbox
cat >"${SUDOERS_DIR}/90-cloud-init-users" <<'EOF'
# Created by cloud-init
ec2-user ALL=(ALL) NOPASSWD:ALL
ec2-user ALL=(deploy) NOPASSWD: ALL
EOF
if run_setup -- ; then
    fail_test "apply must fail while a NOPASSWD rule other than the agent rule is left"
else
    pass "apply exits non-zero"
fi
check "the (deploy) line is untouched" grep -qxF "ec2-user ALL=(deploy) NOPASSWD: ALL" "${SUDOERS_DIR}/90-cloud-init-users"
check "the root line is commented out" grep -qxF "# disabled by setup-agent-user: ec2-user ALL=(ALL) NOPASSWD:ALL" "${SUDOERS_DIR}/90-cloud-init-users"
check "the (deploy) line is reported as left untouched" grep -qF "left untouched (run-as part (deploy) is not ALL or root): ${SUDOERS_DIR}/90-cloud-init-users:3: ec2-user ALL=(deploy) NOPASSWD: ALL" "$OUT"
check "the remaining rule is printed with file and line" has_line "$OUT" "  ${SUDOERS_DIR}/90-cloud-init-users:3: ec2-user ALL=(deploy) NOPASSWD: ALL"
expect_step cloud-init-sudoers failed
teardown_sandbox

echo "=== test: a NOPASSWD rule left in another file fails the step with that file and line ==="
setup_sandbox
printf '# rules\nec2-user,admin ALL=(ALL) NOPASSWD:ALL\n' >"${SUDOERS_DIR}/10-other"
if run_setup -- ; then
    fail_test "apply must fail while another file gives the daemon user NOPASSWD"
else
    pass "apply exits non-zero"
fi
check "the file and line are printed" has_line "$OUT" "  ${SUDOERS_DIR}/10-other:2: ec2-user,admin ALL=(ALL) NOPASSWD:ALL"
check "the sudo -l line is printed" grep -qF "sudo -l: (ALL) NOPASSWD: ALL" "$OUT"
check "the cloud-init lines are still commented out" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_APPLIED"
expect_step cloud-init-sudoers failed
teardown_sandbox

# mutation pin: the error of sudo -l is swallowed (|| true) so an empty output counts as proof -> this test fails
echo "=== test: sudo -n -l -U that itself fails fails the step with its output ==="
setup_sandbox
if run_setup MOCK_SUDO_L_FAIL=1 -- ; then
    fail_test "apply must fail when sudo -l fails"
else
    pass "apply exits non-zero when sudo -l exits non-zero"
fi
expect_step cloud-init-sudoers failed
check "the output of sudo -l is printed" grep -qF "sudo: a password is required" "$OUT"
check "the message says the removal is not proven" grep -qF "the sudo removal is not proven" "$OUT"
teardown_sandbox

echo "=== test: the proof also runs when nothing is left to edit ==="
setup_sandbox
run_setup -- >/dev/null || true
if run_setup MOCK_SUDO_L_FAIL=1 -- ; then
    fail_test "second apply must fail when sudo -l fails"
else
    pass "second apply fails when sudo -l fails"
fi
if run_setup MOCK_SUDO_L_EXTRA='(ALL) NOPASSWD: ALL' -- ; then
    fail_test "second apply must fail on a remaining NOPASSWD rule"
else
    pass "second apply fails on a remaining NOPASSWD rule"
fi
check "no source line is found for an unknown source" grep -qF "no such line in" "$OUT"
teardown_sandbox

# ----------------------------------------------- items 6 and 7: order, firewall

echo "=== test: the removal of the daemon user's sudo needs --confirm-root-path ==="
setup_sandbox
expect_refused "no --confirm-root-path" NO_CONFIRM=1 --
check "the message tells the operator to hold another root session" grep -qF "keep another root session open" "$OUT"
check "the cloud-init file is untouched" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check "nothing was done before the refusal (no group, no user)" test ! -e "${STATE_DIR}/groups/pilot" -a ! -e "${STATE_DIR}/users/pilot-agent"
check "--dry-run does not need it" run_setup NO_CONFIRM=1 -- --dry-run
check "--undo does not need it" run_setup NO_CONFIRM=1 -- --undo
teardown_sandbox

echo "=== test: the removal comes last — a failed firewall step leaves the daemon user's sudo alone ==="
setup_sandbox
if run_setup IPTABLES_CMD=iptables-not-installed -- ; then
    fail_test "apply must fail without iptables"
else
    pass "apply exits non-zero without iptables"
fi
expect_step firewall-ipv4 failed
expect_step cloud-init-sudoers skipped
check "the cloud-init file is untouched" test "$(cat "${SUDOERS_DIR}/90-cloud-init-users")" = "$CLOUD_INIT_ORIGINAL"
check_not "no backup was made" test -e "${SUDOERS_DIR}/90-cloud-init-users.pilot-agent.bak"
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

echo "=== test: the gap check itself notices a flush of the chain behind a live jump ==="
setup_sandbox
run_setup -- >/dev/null || true
rm -f "${STATE_DIR}/gap.log"
mock_run iptables -w -F PILOT_AGENT_EGRESS
check "the flush is reported as a gap" test -s "${STATE_DIR}/gap.log"
teardown_sandbox

echo "=== test: without ip6tables the IPv4 rules and the unit are applied and IPv6 is skipped ==="
setup_sandbox
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

# -------------------------------------------------------- item 8: dry run

echo "=== test: --dry-run deletes and changes nothing, also on a host that was set up before ==="
setup_sandbox
build_repos
run_setup -- >/dev/null || true
printf '%s\n' "$REWRITTEN" >"${SUDOERS_DIR}/90-cloud-init-users"
printf 'stale\n' >"${SUDOERS_DIR}/.90-pilot-agent-runas.new"
printf 'x\n' >"$R/alpha/.git/refs/heads/late"
chmod 600 "$R/alpha/.git/refs/heads/late"
mock_run iptables -w -I OUTPUT 1 -j ACCEPT
before="$(tree_listing)"
before_snap="$(repo_snapshot)"
before_rules="$(cat "${STATE_DIR}/iptables.rules")"
state_before="$(cat "$STATE_FILE")"
: >"$MUT_LOG"
if run_setup -- --dry-run; then
    pass "dry run exits 0"
else
    cat "$OUT"
    fail_test "dry run failed"
fi
check "no mutating command" test ! -s "$MUT_LOG"
check "files and modes unchanged" test "$before" = "$(tree_listing)"
check "groups and modes under repos unchanged" test "$before_snap" = "$(repo_snapshot)"
check "firewall rules unchanged" test "$before_rules" = "$(cat "${STATE_DIR}/iptables.rules")"
check "state file unchanged" test "$state_before" = "$(cat "$STATE_FILE")"
check "the rotation of the old backup is printed, not done" grep -qF "+ mv ${BAK} ${BAK}.1" "$OUT"
check "the lines to comment out are printed" grep -qF "to comment out: ${SUDOERS_DIR}/90-cloud-init-users:3:" "$OUT"
check "the proof is printed, not run" grep -qF "+ sudo -n -l -U ec2-user" "$OUT"
: >"$MUT_LOG"
before="$(tree_listing)"
if run_setup -- --dry-run --undo; then
    pass "dry-run undo exits 0"
else
    cat "$OUT"
    fail_test "dry-run undo failed"
fi
check "dry-run undo: no mutating command" test ! -s "$MUT_LOG"
check "dry-run undo: files, modes and the state file unchanged" test "$before" = "$(tree_listing)" -a "$state_before" = "$(cat "$STATE_FILE")"
check "dry-run undo prints the restore it would do" grep -qE "^restored: ${DATA_DIR}/bin/pilot group=" "$OUT"
check "dry-run undo: the groups and modes under repos are unchanged" test "$before_snap" = "$(repo_snapshot)"
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
expect_refused "missing value" -- --repos-link
teardown_sandbox

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
