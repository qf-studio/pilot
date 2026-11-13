#!/bin/bash
# Container test for setup-agent-user.sh: the script runs for real (real useradd,
# visudo, sudo, iptables, systemctl) as root in an amazonlinux:2023 container with
# systemd as PID 1. Cases: apply, a second apply, undo, and three invalid-argument
# calls. It also checks the real output of sudo -l -U and of iptables -S against
# what the script expects. Needs docker; without it the script prints a "skipped"
# line and exits 0. Not part of CI (the container needs --privileged and the
# network for dnf); run it by hand before the script goes to a host.
#
# Usage: ./scripts/box/setup-agent-user_container_test.sh
#
# The image is built once (tag pilot-setup-agent-user-test:al2023) and reused.

# The single-quoted scripts below run in the container's shell, not in this one.
# shellcheck disable=SC2016
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE_BASE="amazonlinux:2023"
IMAGE="pilot-setup-agent-user-test:al2023"
NAME="pilot-setup-agent-user-test-$$"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
    echo "skipped: docker is not available (container test of setup-agent-user.sh not run)"
    exit 0
fi

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

LOG="$(mktemp)"
# shellcheck disable=SC2329 # runs through the trap
cleanup() {
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    rm -f "$LOG"
}
trap cleanup EXIT

# cx CMD... — a command in the container, as root.
cx() {
    docker exec -i "$NAME" "$@"
}

# sh_ok LABEL SCRIPT — SCRIPT (run by bash in the container) must succeed.
sh_ok() {
    if cx bash -c "$2" >/dev/null 2>&1; then
        pass "$1"
    else
        fail_test "$1"
    fi
}

echo "=== container test: build the image (${IMAGE}) ==="
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    if ! docker build -q -t "$IMAGE" - >/dev/null 2>&1 <<EOF
FROM ${IMAGE_BASE}
RUN dnf install -y systemd iptables-nft sudo shadow-utils findutils util-linux procps-ng git && dnf clean all
EOF
    then
        echo "skipped: the image could not be built (no network for dnf?)"
        exit 0
    fi
fi

echo "=== container test: start ${IMAGE_BASE} with systemd ==="
if ! docker run -d --name "$NAME" --privileged --cgroupns=host \
    -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
    -v "${SCRIPT_DIR}:/opt/box:ro" \
    --tmpfs /run --tmpfs /run/lock \
    "$IMAGE" /usr/lib/systemd/systemd >/dev/null 2>&1; then
    echo "skipped: a privileged container with systemd could not be started"
    exit 0
fi
booted=0
state=""
for _ in $(seq 1 60); do
    state="$(cx systemctl is-system-running 2>/dev/null || true)"
    case "$state" in
        running | degraded)
            booted=1
            break
            ;;
    esac
    sleep 1
done
if [ "$booted" != 1 ]; then
    echo "skipped: systemd did not come up in the container (state: ${state:-none})"
    exit 0
fi
pass "systemd is up (${state})"

# The host as the script expects it: a daemon user with uid 1000, its data dir
# (reachable by others), a repository with private files, the binary, the
# cloud-init sudoers file.
cx bash -s <<'EOF'
set -e
useradd --uid 1000 --create-home ec2-user
install -d -o ec2-user -g ec2-user -m 0711 /var/lib/pilot
install -d -o ec2-user -g ec2-user -m 0700 /var/lib/pilot/repos /var/lib/pilot/bin
su ec2-user -c '
  set -e
  mkdir -p /var/lib/pilot/repos/alpha /var/lib/pilot/repos/org/beta
  for r in /var/lib/pilot/repos/alpha /var/lib/pilot/repos/org/beta; do
    git init -q "$r"
    echo secret > "$r/secret.env"
    chmod 600 "$r/secret.env" "$r/.git/HEAD" "$r/.git/config"
    chmod 700 "$r"
  done
  chmod 700 /var/lib/pilot/repos/org
  cp /usr/bin/true /var/lib/pilot/bin/pilot
  chmod 700 /var/lib/pilot/bin/pilot
'
echo 'ec2-user ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/90-cloud-init-users
chmod 0440 /etc/sudoers.d/90-cloud-init-users
EOF

SETUP="/opt/box/setup-agent-user.sh"
USR_BIN_GROUP="$(cx stat -c %G /usr/bin)"
SUDO_MODE="$(cx stat -c %a /usr/bin/sudo)"
BEFORE_GIT="$(cx bash -c 'stat -c "%n %G %a" /var/lib/pilot/repos/alpha/.git/HEAD /var/lib/pilot/repos/org /var/lib/pilot/bin/pilot')"

echo "=== container test: apply ==="
if cx "$SETUP" --confirm-root-path >"$LOG" 2>&1; then
    pass "apply exits 0"
else
    cat "$LOG"
    fail_test "apply exited non-zero"
fi
sh_ok "agent user exists with a no-login shell" 'getent passwd pilot-agent | grep -q "nologin$"'
sh_ok "the daemon user is in the shared group" 'id -nG ec2-user | tr " " "\n" | grep -qx pilot'
sh_ok "git directory readable by the group (HEAD 0640 pilot)" 'test "$(stat -c "%G:%a" /var/lib/pilot/repos/alpha/.git/HEAD)" = pilot:640'
sh_ok "the middle directory has traverse only (0710)" 'test "$(stat -c "%G:%a" /var/lib/pilot/repos/org)" = pilot:710'
sh_ok "the private file in the working tree stays private" 'test "$(stat -c "%a" /var/lib/pilot/repos/alpha/secret.env)" = 600 && test "$(stat -c "%G" /var/lib/pilot/repos/alpha/secret.env)" != pilot'
sh_ok "the agent user cannot read the private file" '! su -s /bin/bash pilot-agent -c "cat /var/lib/pilot/repos/alpha/secret.env" 2>/dev/null'
sh_ok "the agent user cannot list the working tree" '! su -s /bin/bash pilot-agent -c "ls /var/lib/pilot/repos/alpha" 2>/dev/null'
sh_ok "the agent user reads HEAD" 'su -s /bin/bash pilot-agent -c "cat /var/lib/pilot/repos/alpha/.git/HEAD"'
sh_ok "the agent user reads the repository two levels down" 'su -s /bin/bash pilot-agent -c "cat /var/lib/pilot/repos/org/beta/.git/HEAD"'
sh_ok "the agent user runs the binary" 'su -s /bin/bash pilot-agent -c "/var/lib/pilot/bin/pilot"'
sh_ok "the state file is root-only" 'test "$(stat -c %U:%a /var/lib/pilot/.agent-setup-state)" = root:600'

echo "=== container test: the real output of sudo -l -U ==="
SUDO_L="$(cx sudo -n -l -U ec2-user 2>&1 || true)"
if printf '%s\n' "$SUDO_L" | grep -qE '^[[:space:]]*\(pilot-agent\) SETENV: NOPASSWD: ALL$'; then
    pass "sudo -l -U lists the agent rule (sudo prints the tags as: (pilot-agent) SETENV: NOPASSWD: ALL)"
else
    printf '%s\n' "$SUDO_L"
    fail_test "sudo -l -U does not list the agent rule"
fi
if printf '%s\n' "$SUDO_L" | grep NOPASSWD | grep -qvE '^[[:space:]]*\(pilot-agent\) '; then
    printf '%s\n' "$SUDO_L"
    fail_test "sudo -l -U still lists another NOPASSWD rule"
else
    pass "sudo -l -U lists no other NOPASSWD rule"
fi
sh_ok "the daemon user can run a command as the agent user" 'su ec2-user -c "sudo -n -u pilot-agent id -un" | grep -qx pilot-agent'
sh_ok "the daemon user has no sudo to root left" '! su ec2-user -c "sudo -n true" 2>/dev/null'
sh_ok "sudoers is valid" 'visudo -c'

echo "=== container test: the real output of iptables -S ==="
IPT_CHAIN="$(cx iptables -S PILOT_AGENT_EGRESS 2>&1 || true)"
IPT_OUT="$(cx iptables -S OUTPUT 2>&1 || true)"
if printf '%s\n' "$IPT_OUT" | grep '^-A OUTPUT' | head -1 | grep -qE -- '-m owner --uid-owner [^ ]+ -j PILOT_AGENT_EGRESS$'; then
    pass "iptables -S OUTPUT: the jump is the first rule"
else
    printf '%s\n' "$IPT_OUT"
    fail_test "the jump is not the first rule of OUTPUT"
fi
if printf '%s\n' "$IPT_CHAIN" | grep -qE -- '^-A PILOT_AGENT_EGRESS -d 169\.254\.169\.254(/32)? -j REJECT --reject-with icmp-port-unreachable$'; then
    pass "iptables -S chain: the metadata address is rejected"
else
    printf '%s\n' "$IPT_CHAIN"
    fail_test "no reject rule for the metadata address"
fi
if printf '%s\n' "$IPT_CHAIN" | grep -qE -- '^-A PILOT_AGENT_EGRESS -d 127\.0\.0\.1(/32)? -p tcp -m tcp --dport 9091 -j REJECT --reject-with tcp-reset$'; then
    pass "iptables -S chain: tcp to 127.0.0.1:9091 is reset"
else
    printf '%s\n' "$IPT_CHAIN"
    fail_test "no reject rule for 127.0.0.1:9091"
fi
if printf '%s\n' "$IPT_CHAIN" | grep -qE -- '^-A PILOT_AGENT_EGRESS -o lo -p tcp -m tcp --dport 9091 -m addrtype --dst-type LOCAL -j REJECT --reject-with tcp-reset$'; then
    pass "iptables -S chain: tcp to port 9091 on the loopback interface for a local address is reset"
else
    printf '%s\n' "$IPT_CHAIN"
    fail_test "no reject rule for port 9091 on lo"
fi
sh_ok "the chain holds exactly three rules and no temporary chain is left" 'test "$(iptables -S PILOT_AGENT_EGRESS | grep -c "^-A ")" = 3 && ! iptables -S | grep -q PILOT_AGENT_EGRESS_NEW'
sh_ok "ip6tables: the jump is the first rule and the loopback rule is there" 'ip6tables -S OUTPUT | grep "^-A OUTPUT" | head -1 | grep -q PILOT_AGENT_EGRESS && ip6tables -S PILOT_AGENT_EGRESS | grep -q -- "-o lo .*--dport 9091"'
sh_ok "boot unit is enabled" 'systemctl is-enabled --quiet pilot-agent-firewall.service'
sh_ok "the boot unit runs and keeps the rules (restart)" 'systemctl restart pilot-agent-firewall.service && iptables -S OUTPUT | grep "^-A OUTPUT" | head -1 | grep -q PILOT_AGENT_EGRESS && test "$(iptables -S PILOT_AGENT_EGRESS | grep -c "^-A ")" = 3'

echo "=== container test: second apply ==="
out="$(cx "$SETUP" --confirm-root-path 2>&1 || true)"
skipped="$(printf '%s\n' "$out" | grep -c '^step .*: skipped' || true)"
if [ "$skipped" = 10 ]; then
    pass "second apply skips all 10 steps"
else
    printf '%s\n' "$out"
    fail_test "second apply: ${skipped} of 10 steps skipped"
fi

echo "=== container test: undo ==="
if cx "$SETUP" --undo >"$LOG" 2>&1; then
    pass "undo exits 0"
else
    cat "$LOG"
    fail_test "undo exited non-zero"
fi
sh_ok "the sudoers rule and the cloud-init file are back" 'test ! -e /etc/sudoers.d/90-pilot-agent-runas && grep -q "^ec2-user ALL=(ALL) NOPASSWD:ALL" /etc/sudoers.d/90-cloud-init-users'
sh_ok "the firewall chain is gone" '! iptables -S | grep -q PILOT_AGENT_EGRESS && ! ip6tables -S | grep -q PILOT_AGENT_EGRESS'
sh_ok "the boot unit is stopped, disabled and gone" '! systemctl is-enabled --quiet pilot-agent-firewall.service 2>/dev/null && ! systemctl is-active --quiet pilot-agent-firewall.service && test ! -e /etc/systemd/system/pilot-agent-firewall.service'
AFTER_GIT="$(cx bash -c 'stat -c "%n %G %a" /var/lib/pilot/repos/alpha/.git/HEAD /var/lib/pilot/repos/org /var/lib/pilot/bin/pilot')"
if [ "$BEFORE_GIT" = "$AFTER_GIT" ]; then
    pass "group and mode of HEAD, the middle directory and the binary are restored"
else
    printf 'before:\n%s\nafter:\n%s\n' "$BEFORE_GIT" "$AFTER_GIT"
    fail_test "undo did not restore the group and mode"
fi
sh_ok "the state file is removed" 'test ! -e /var/lib/pilot/.agent-setup-state'

echo "=== container test: invalid arguments change nothing ==="
cx chown ec2-user /usr # so that the owner check passes and the system-directory check is the one that refuses
for args in "--data-dir /usr" "--group wheel" "--agent-user root"; do
    rc=0
    # shellcheck disable=SC2086 # the arguments are a word list on purpose
    cx "$SETUP" $args --confirm-root-path >/dev/null 2>&1 || rc=$?
    if [ "$rc" = 2 ]; then
        pass "refused with exit 2: ${args}"
    else
        fail_test "${args}: exit ${rc}, want 2"
    fi
done
cx chown root /usr
if [ "$(cx stat -c %G /usr/bin)" = "$USR_BIN_GROUP" ] && [ "$(cx stat -c %a /usr/bin/sudo)" = "$SUDO_MODE" ]; then
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
