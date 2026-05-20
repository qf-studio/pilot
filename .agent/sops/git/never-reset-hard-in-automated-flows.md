# SOP: Never use `git reset --hard` in automated sync flows

**Source incident**: GH-3018 / TASK-283 — `syncMainBranch()` used `git reset --hard origin/main` after every task. GitHub push-propagation lagged behind the immediately-following fetch+reset, silently wiping local commits. Reflog evidence from 2026-05-20 workshop demo.

## Rule

In any **automated** code path that syncs a local branch to its remote (post-task hygiene, post-merge cleanup, between-step sync in a loop), use:

```go
exec.CommandContext(ctx, "git", "merge", "--ff-only", remoteRef)
```

**Never** use `git reset --hard <remoteRef>` for this purpose.

## Why

`reset --hard` is unconditional. It does not care whether local is ahead, behind, or diverged. If `<remoteRef>` lags behind local state for any reason — push-propagation latency, network blip, race with an in-flight push — the reset will rewind local to that stale ref and the unique local commits become reflog-only (90-day recovery window).

The race vector that burned us:

1. Code pushes a commit to `main` (HTTP succeeds at github.com).
2. Code immediately runs `git fetch origin main` + `git reset --hard origin/main`.
3. GitHub's `origin/main` ref hasn't propagated yet — the fetch returns the *pre-push* SHA.
4. Reset rewinds local to that pre-push SHA. **The just-pushed commit is gone from local.**

`merge --ff-only` is a safe drop-in:

- **Local behind origin** → fast-forwards (the desired sync).
- **Local ahead of origin** → fails cleanly, local state preserved.
- **Diverged** → fails cleanly, local state preserved.
- **Dirty working tree** → fails cleanly (`Your local changes would be overwritten`), uncommitted changes preserved.

All failure modes are safe to log-and-skip. The raw git output should be included in the warning log so operators can distinguish the cases.

## Pattern

```go
// 1. Detect branch first; do NOT hardcode main.
branchOutput, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
if err != nil { return err }
currentBranch := strings.TrimSpace(string(branchOutput))

remoteRef := "origin/" + currentBranch

// 2. Fetch matching ref.
if _, err := runGit(ctx, repoPath, "fetch", "origin", currentBranch); err != nil {
    return err
}

// 3. ff-only merge — safe on local-ahead, divergence, dirty tree.
out, err := runGit(ctx, repoPath, "merge", "--ff-only", remoteRef)
if err != nil {
    log.Warn("Skipped sync — fast-forward not possible (non-fatal)",
        slog.String("branch", currentBranch),
        slog.String("git_output", strings.TrimSpace(string(out))),
        slog.String("hint", "Sync manually: git fetch && git merge --ff-only " + remoteRef),
    )
    return nil // non-fatal — sync is hygiene, not correctness
}
```

## When `reset --hard` IS acceptable

Isolated, throwaway state where there is no possibility of unique local commits worth preserving:

- **Pooled worktrees** (`worktree.go:349`) — tmp dirs reset between tasks by design.
- **Test fixture setup** — reset a tmpdir to a known state at the start of a test.
- **Recovery commands explicitly invoked by a user** — e.g., `pilot reset-state --hard` where the user accepts the consequences.

In each case, add a comment explaining *why* the destructive operation is safe in context, referencing this SOP.

## Detection / lint

There is no automated lint for this today. When reviewing PRs that touch `internal/executor/*git*.go` or any post-task hook, scan for `reset --hard` and challenge it.

## Refs

- GH-3018 (the fix PR)
- `.agent/knowledge/memories/pitfalls/pitfall_git_reset_hard_in_automated_sync.md`
- `internal/executor/runner_git.go:syncMainBranch` (canonical safe pattern)
- `internal/executor/worktree.go:349` (canonical accepted exception, with safety comment)
