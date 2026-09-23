# SOP: A failed `git fetch` must not fall back to the stale local tracking ref

**Source incident**: GH-5449 — `CreateWorktreeWithBranch` (`internal/executor/worktree.go`) fetched `origin main` before creating a task worktree, but on fetch failure only logged a warning and proceeded to branch from the local `origin/main` ref anyway. Observed 2026-09-23 on the hosted daemon (v2.276.1), task GH-10 in `aws-infrastructure-pilot`: the fetch lost a ref-update lock race (`error: cannot lock ref 'refs/remotes/origin/main': ... .lock: File exists`), but the code treated that as non-fatal. The clone's `origin/main` was ~7 weeks stale, so the worktree branched from an August commit. PR #11 came out 100 commits behind `main`, conflicted, and autopilot had to escalate to `needs-manual-rebase`.

## Rule

When a code path needs "the freshest `origin/main`" as a worktree/branch base, do not treat a fetch failure as safe-to-ignore-and-use-the-local-ref. Resolve in this order:

1. **Fetch succeeds** → use `origin/main`.
2. **Fetch fails, but its combined output contains `-> FETCH_HEAD`** → the remote-tracking ref update lost a race (e.g. a concurrent fetch holding the lock), but git still wrote `FETCH_HEAD` with the real answer. Use `FETCH_HEAD` as the base instead of the stale tracking ref.
3. **Fetch fails and `FETCH_HEAD` wasn't written** (e.g. unreachable remote, DNS failure) → retry once after a short delay (~500ms; lock races typically clear within a second).
4. **Still failing** → return an error. Fail the caller closed rather than guess at a base. Do not create the worktree.

See `fetchOriginMainForWorktree` in `internal/executor/worktree.go` for the reference implementation, and `logWorktreeBaseResolved` for logging the resolved base SHA/date at worktree-creation time so a stale base is visible in the daemon log without cross-referencing the intent judge's diff-base line.

## Why `FETCH_HEAD` is trustworthy here

`git fetch` always writes `FETCH_HEAD` with what it downloaded *before* attempting to update `refs/remotes/<remote>/<branch>`. A ref-lock failure (another process — e.g. a concurrent fetch on a shared clone — holding `.lock`) happens at the second step, after `FETCH_HEAD` is already correct. So `FETCH_HEAD` reflects this fetch's real result even when the tracking-ref update failed. This is different from a fetch that never reached the remote at all (network/DNS failure) — in that case `FETCH_HEAD` is untouched and still holds whatever the *previous* successful fetch left there, which is exactly the stale state we're trying to avoid. Detect the distinction by checking the fetch's combined output for the `-> FETCH_HEAD` line rather than just checking whether the file exists.

## Reproducing the ref-lock race in tests

Real network conditions are hard to simulate deterministically. Reproduce the exact failure shape locally with a bare remote + a manually-created lock file:

```go
// 1. Push a NEW commit to the remote from a second clone, so the fetch has
//    a real ref update to attempt (a no-op fetch won't touch the lock).
newSHA := pushNewCommitToRemote(t, remoteDir)

// 2. Pre-create the lock file git would create during a ref update.
lockPath := filepath.Join(repoDir, ".git", "refs", "remotes", "origin", "main.lock")
os.MkdirAll(filepath.Dir(lockPath), 0755)
os.WriteFile(lockPath, nil, 0644)
defer os.Remove(lockPath)

// 3. Fetch now fails with "cannot lock ref ... .lock: File exists" but
//    still writes FETCH_HEAD — same as the real incident.
```

Without step 1 (a real pending ref update), git has nothing to update and returns success even with the lock file present — it won't reproduce the bug.

## When plain non-fatal fetch-and-continue IS acceptable

- Fetches whose result doesn't determine the base ref (e.g. `ResolveFixContinuationBaseRef` fetches a specific branch/SHA and separately verifies it resolved — the base ref check itself is the gate, not "did fetch return exit 0").
- Best-effort freshness fetches where the caller doesn't derive a decision from success/failure (rare — audit before assuming this applies).

## Refs

- GH-5449 (this fix)
- `internal/executor/worktree.go` — `fetchOriginMainForWorktree`, `logWorktreeBaseResolved`, `CreateWorktreeWithBranch`, `createPooledWorktree`
- `internal/executor/worktree_test.go` — `TestFetchOriginMainForWorktree`, `TestCreateWorktreeWithBranch_RefLockFallsBackToFetchHead`, `TestCreateWorktreeWithBranch_HardFetchFailureFailsClosed`
