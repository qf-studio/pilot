# Git auto-gc races t.TempDir() cleanup in bare-repo tests

## Problem

`go test ./internal/executor/...` intermittently fails with:

```
testing.go:1369: TempDir RemoveAll cleanup: unlinkat /tmp/<Test>.../001: directory not empty
```

on tests that `git init --bare` a `t.TempDir()` and push into it (e.g.
`setupFreshnessRepo` in `git_freshness_test.go`, used by
`TestRunner_DirtyWorktreeAfterNoCommitRetry_AutoPreserved`). It's flaky, not
deterministic — reproduces far more reliably under CI load than on an idle
laptop. Observed: PR #5383 post-merge CI (GH-5386), unrelated to that PR's
actual diff (autopilot label logic, never touches `internal/executor`).

## Root Cause

`git push` to a local bare repo forks `git-receive-pack` as a child of the
`git push` process. When receive-pack's auto-gc heuristics trip, it forks
`git gc --auto`, redirects that child's stdout/stderr away from the
inherited pipes, and does not wait for it — the message is "Auto packing
the repository in background for optimum performance." Because Go's
`exec.Cmd.Wait()` only blocks until the pipes it's reading are closed, and
the backgrounded gc process closed/redirected its copies immediately,
`cmd.CombinedOutput()` for `git push` returns as soon as the immediate
child exits — while the detached gc process keeps repacking/renaming files
under the bare repo's `objects/` directory.

`t.Cleanup`'s `os.RemoveAll` on the enclosing `t.TempDir()` then races that
detached process: if RemoveAll lists a subdirectory's entries, and the gc
process creates a new pack/tmp file in it before RemoveAll gets to rmdir
it, `unlinkat` returns `ENOTEMPTY`.

## Solution

Disable auto-gc on the test-only repos, scoped via local git config (not a
production code change — production `GitOperations.Push`/`Commit` are
untouched):

```go
runGit(t, bareDir, "init", "--bare", "-b", "main")
runGit(t, bareDir, "config", "gc.auto", "0")
runGit(t, bareDir, "config", "receive.autogc", "false")

runGit(t, repoDir, "init", "-b", "main")
runGit(t, repoDir, "config", "gc.auto", "0")
```

Applied in `setupFreshnessRepo` (`internal/executor/git_freshness_test.go`).

## Prevention

Any new test helper that does `git init --bare` (or `git init`) on a
`t.TempDir()` and then pushes/commits into it should set `gc.auto 0` (and
`receive.autogc false` on the bare side) right after init — before the
first commit/push that could trip the loose-object threshold. Existing
call sites to audit if this recurs elsewhere:
`runner_git_test.go` (`setupTestRepoWithRemote`), `worktree_test.go`,
`git_test.go`, `git_gh5223_env_leak_test.go`.
