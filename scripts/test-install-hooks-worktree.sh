#!/bin/bash
# Test harness for scripts/install-hooks.sh from a git worktree (GH-6218).
#
# Worktrees share one .git/hooks dir, so the installed hooks must resolve the
# project root from the working tree that runs them (git rev-parse
# --show-toplevel), not from the hooks dir's location (the main checkout).
# The installer must also work from a worktree, where .git is a file.
#
# Builds a disposable fixture repo with stub gate/classifier scripts that
# record their own location — never touches the real repo's hooks.
#
# Usage: ./scripts/test-install-hooks-worktree.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER="$SCRIPT_DIR/install-hooks.sh"

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

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
TMP="$(cd "$TMP" && pwd -P)"

MAIN="$TMP/main"
WT="$TMP/wt"
REMOTE="$TMP/remote.git"
export GATE_MARKER="$TMP/gate-marker"

git init -q --bare "$REMOTE"
git init -q -b main "$MAIN"
git -C "$MAIN" config user.email "test@example.com"
git -C "$MAIN" config user.name "Test"

mkdir -p "$MAIN/scripts"
cp "$INSTALLER" "$MAIN/scripts/install-hooks.sh"
# Stub gate: records the directory of the gate script that actually ran.
cat > "$MAIN/scripts/pre-push-gate.sh" << 'STUB'
#!/bin/bash
echo "$(cd "$(dirname "$0")" && pwd -P)" > "$GATE_MARKER"
STUB
# Stub classifier: never docs-only, so the gate always runs.
cat > "$MAIN/scripts/pre-push-classify.sh" << 'STUB'
#!/bin/bash
cat > /dev/null
echo code
STUB
chmod +x "$MAIN"/scripts/*.sh

git -C "$MAIN" add -A
git -C "$MAIN" commit -q -m "init"
git -C "$MAIN" remote add origin "$REMOTE"

echo "=== install-hooks from a worktree ==="

git -C "$MAIN" worktree add -q -b wt-branch "$WT"

if (cd "$MAIN" && ./scripts/install-hooks.sh > "$TMP/install-main.log" 2>&1); then
    pass "installer succeeds from the main checkout"
else
    fail_test "installer fails from the main checkout"
    cat "$TMP/install-main.log"
fi

rm -f "$MAIN/.git/hooks/pre-push" "$MAIN/.git/hooks/pre-commit"

if (cd "$WT" && ./scripts/install-hooks.sh > "$TMP/install-wt.log" 2>&1); then
    pass "installer succeeds from the worktree"
else
    fail_test "installer fails from the worktree"
    cat "$TMP/install-wt.log"
fi

if [ -x "$MAIN/.git/hooks/pre-push" ] && [ -x "$MAIN/.git/hooks/pre-commit" ]; then
    pass "worktree install writes the shared hooks dir"
else
    fail_test "worktree install did not write the shared hooks dir"
fi

echo "=== pre-push gate resolution ==="

rm -f "$GATE_MARKER"
echo "worktree change" > "$WT/change.txt"
git -C "$WT" add change.txt
git -C "$WT" commit -q -m "worktree change"
git -C "$WT" push -q origin wt-branch > "$TMP/push-wt.log" 2>&1 || {
    fail_test "push from the worktree failed"
    cat "$TMP/push-wt.log"
}
# mutation pin: worktree push runs the worktree gate -> this test fails
if [ "$(cat "$GATE_MARKER" 2>/dev/null)" = "$WT/scripts" ]; then
    pass "worktree push runs the worktree gate script"
else
    fail_test "worktree push ran gate from '$(cat "$GATE_MARKER" 2>/dev/null)', want '$WT/scripts'"
fi

rm -f "$GATE_MARKER"
echo "main change" > "$MAIN/change.txt"
git -C "$MAIN" add change.txt
git -C "$MAIN" commit -q -m "main change"
git -C "$MAIN" push -q origin main > "$TMP/push-main.log" 2>&1 || {
    fail_test "push from the main checkout failed"
    cat "$TMP/push-main.log"
}
if [ "$(cat "$GATE_MARKER" 2>/dev/null)" = "$MAIN/scripts" ]; then
    pass "main checkout push runs the main checkout gate script"
else
    fail_test "main push ran gate from '$(cat "$GATE_MARKER" 2>/dev/null)', want '$MAIN/scripts'"
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
