#!/bin/bash
# Plain-bash test suite for setup-agent-user.sh — no bats dependency, no root,
# no real host, and no GNU-only flag of find or stat (it runs on macOS too, with
# the same count of checks).
# The script runs against a temp root with a PATH of mock commands
# (scripts/box/testdata/mock-host-cmds.sh, linked under the names useradd,
# groupadd, usermod, visudo, iptables, ip6tables, systemctl, install, chown,
# chmod, id, getent, stat, sudo and runuser) that record their arguments; the
# sudoers and systemd directories are pointed at the temp root through
# SUDOERS_DIR / SYSTEMD_DIR. The real-host counterpart, run in CI as root, is
# setup-agent-user_real_test.sh.
#
# Usage: ./scripts/box/setup-agent-user_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT="${SCRIPT_DIR}/setup-agent-user.sh"
MOCK_CMDS="${SCRIPT_DIR}/testdata/mock-host-cmds.sh"
MOCKED="useradd groupadd usermod visudo iptables ip6tables systemctl install chown chmod id getent stat sudo runuser"

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

    mkdir -p "${DATA_DIR}/repos" "${DATA_DIR}/bin" "$SUDOERS_DIR" "$SYSTEMD_DIR" "$STUB_BIN" "${STATE_DIR}/users" "${STATE_DIR}/meta"
    chmod 755 "$DATA_DIR"
    chmod 700 "${DATA_DIR}/repos"
    chmod 750 "${DATA_DIR}/bin"
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

ALL_STEPS="daemon-private group agent-user daemon-membership directories sudoers-rule firewall-ipv4 firewall-ipv6 firewall-unit cloud-init-sudoers"
# daemon-private is a check: it changes nothing, so it reports skipped.
CHANGE_STEPS="group agent-user daemon-membership directories sudoers-rule firewall-ipv4 firewall-ipv6 firewall-unit cloud-init-sudoers"

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
    "+ runuser -u ec2-user -- sudo -n true"; do
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
for pattern in '^groupadd ' '^useradd ' '^usermod ' '^install -d ' '^visudo -cf ' '^iptables -w -N ' '^ip6tables -w -N ' '^systemctl enable ' '^install -m 0600 -o root -g root .*\.pilot-agent\.bak$' '^visudo -c$' '^runuser -u ec2-user -- sudo -n true$'; do
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
expect_step daemon-private skipped
for s in $CHANGE_STEPS; do
    expect_step "$s" "done"
done
check_not "no chgrp was run (no shared read)" grep -q '^chgrp ' "$CALL_LOG"
check_not "no state file is written" test -e "${DATA_DIR}/.agent-setup-state"
check "the repository of the daemon keeps its modes" test "$(mode_of "$R")" = 700 -a "$(mode_of "$R/alpha/.env")" = 644 -a "$(mode_of "$R/alpha/.git/config")" = 644
check_not "the firewall never left a moment without reject rules" test -e "${STATE_DIR}/gap.log"

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
if ! grep -qE '^(userdel|groupdel|gpasswd|rm -rf)' "$CALL_LOG" && [ -d "${DATA_DIR}/agent-home/go/mod" ] && [ -e "${STATE_DIR}/users/pilot-agent" ]; then
    pass "user, group and directories are left in place"
else
    fail_test "undo removed the user, the group or a directory"
fi
for want in "pkill -u pilot-agent" "userdel pilot-agent" "groupdel pilot" "rm -rf ${DATA_DIR}/agent-home ${DATA_DIR}/agent-workspaces"; do
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
rmdir "${DATA_DIR}/agent-home/go/cache"
ln -s "${SANDBOX}/outside" "${DATA_DIR}/agent-home/go/cache"
ln -s "${SANDBOX}/nowhere" "${DATA_DIR}/agent-home/go/mod.link"
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
expect_refused "apply with go/cache a link outside the data dir" --
check "the refusal names the link" grep -qF "${DATA_DIR}/agent-home/go/cache' is a symbolic link; refused" "$OUT"
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
check "the canonical path is used" grep -qF "install -d -o pilot-agent -g pilot -m 750 ${DATA_DIR}/agent-home" "$OUT"
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

echo "=== test: the agent directories that are links, or leave the data dir, are refused by apply ==="
setup_sandbox
mkdir "${SANDBOX}/outside"
ln -s "${SANDBOX}/outside" "${DATA_DIR}/agent-home"
expect_refused "agent-home is a symbolic link" --
rm "${DATA_DIR}/agent-home"
ln -s "${SANDBOX}/outside" "${DATA_DIR}/agent-workspaces"
expect_refused "agent-workspaces is a symbolic link" --
check "the directory outside is untouched" test "$(mode_of "${SANDBOX}/outside")" = "$(mode_of "${SANDBOX}/outside")"
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
mkdir "${SANDBOX}/elsewhere"
chmod 755 "${SANDBOX}/elsewhere"
ln -s "${SANDBOX}/elsewhere" "${DATA_DIR}/linked"
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
for want in "the agent user can reach ${R} (group $(stat -c %G "$R" 2>/dev/null || stat -f %Sg "$R"), mode 755): chmod o-rwx ${R}" \
    "the agent user can reach ${DATA_DIR}/bin (group $(stat -c %G "$R" 2>/dev/null || stat -f %Sg "$R"), mode 755): chmod o-rwx ${DATA_DIR}/bin"; do
    check "prints: ${want#the agent user can reach }" has_line "$OUT" "$want"
done
check "the pilot home at 0711 is reported (traverse is enough)" grep -qF "can reach ${DATA_DIR}/pilot-home " "$OUT"
check "a hidden file is reported" grep -qF "can reach ${DATA_DIR}/.hidden " "$OUT"
check "a link is followed and its target named" grep -qE "can reach ${DATA_DIR}/linked .*: chmod o-rwx ${SANDBOX}/elsewhere\$" "$OUT"
check_not "a dangling link is not reported" grep -qF "can reach ${DATA_DIR}/dangling" "$OUT"
check "five lines, one per open entry" test "$(grep -c 'can reach ' "$OUT" | tr -d ' ')" = 5
chmod o-rwx "$R" "${DATA_DIR}/bin" "${DATA_DIR}/pilot-home" "${DATA_DIR}/.hidden" "${SANDBOX}/elsewhere"
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
check "prints the chmod for group and others" has_line "$OUT" "the agent user can reach ${DATA_DIR}/shared (group pilot, mode 750): chmod go-rwx ${DATA_DIR}/shared"
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

echo "=== test: a rule with the run-as part (deploy) is left untouched and reported; it gives no root, so the proof passes ==="
setup_sandbox
cat >"${SUDOERS_DIR}/90-cloud-init-users" <<'EOF'
# Created by cloud-init
ec2-user ALL=(ALL) NOPASSWD:ALL
ec2-user ALL=(deploy) NOPASSWD: ALL
EOF
if run_setup -- ; then
    pass "apply exits 0"
else
    cat "$OUT"
    fail_test "apply failed on a (deploy) rule"
fi
check "the (deploy) line is untouched" grep -qxF "ec2-user ALL=(deploy) NOPASSWD: ALL" "${SUDOERS_DIR}/90-cloud-init-users"
check "the root line is commented out" grep -qxF "# disabled by setup-agent-user: ec2-user ALL=(ALL) NOPASSWD:ALL" "${SUDOERS_DIR}/90-cloud-init-users"
check "the (deploy) line is reported as left untouched" grep -qF "left untouched (run-as part (deploy) is not ALL or root): ${SUDOERS_DIR}/90-cloud-init-users:3: ec2-user ALL=(deploy) NOPASSWD: ALL" "$OUT"
teardown_sandbox

echo "=== test: a NOPASSWD rule left in another file fails the step with that file and line ==="
setup_sandbox
printf '# rules\nec2-user,admin ALL=(ALL) NOPASSWD:ALL\n' >"${SUDOERS_DIR}/10-other"
if run_setup -- ; then
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
if run_setup -- ; then
    fail_test "apply must fail: the daemon user gets root without a password"
else
    pass "apply exits non-zero"
fi
expect_step cloud-init-sudoers failed
check "the proof says sudo -n true succeeded" grep -qF "sudo -n true succeeded" "$OUT"
check "the Defaults line is named" has_line "$OUT" "  ${SUDOERS_DIR}/05-defaults:1: Defaults:ec2-user !authenticate"
check "the rule is named" has_line "$OUT" "  ${SUDOERS_DIR}/10-other:1: ec2-user ALL=(ALL) ALL"
rm "${SUDOERS_DIR}/05-defaults"
if run_setup -- ; then
    pass "without the Defaults line the rule needs a password and the proof passes"
else
    cat "$OUT"
    fail_test "apply failed without the Defaults line"
fi
printf 'Defaults !authenticate\n' >"${SUDOERS_DIR}/05-defaults"
if run_setup -- ; then
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
check_not "no backup was made" test -e "$BAK"
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
