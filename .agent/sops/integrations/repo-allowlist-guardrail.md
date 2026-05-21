# SOP: Repo allowlist guardrail (TASK-286 / GH-3027)

> **When to read this:** you see `sub-issue guardrail rejected target repo`
> in Pilot logs, or `ErrRepoNotInConfig` surfaced in a task error.

## What it does

Before Pilot's epic decomposer shells out to `gh issue create` (or even
`gh issue list` for the dedup check), it resolves the worktree's `origin`
remote to `owner/repo` and refuses to proceed unless that pair matches a
project in the user's `~/.pilot/config.yaml`.

Filed after the 2026-05-20 incident on the upstream `qf-studio/pilot`,
where an external user's misconfigured Pilot fired 6 duplicate sub-issues
(#3021–#3026) before the decomposer ran out of subtasks.

Chokepoint: `internal/executor/repo_guardrail.go::ValidateTargetRepo`,
invoked from `internal/executor/epic.go::CreateSubIssues` before any `gh`
call. Wired onto the Runner via `cmd/pilot/repo_allowlist.go`.

## Symptom

Pilot logs one of:

```
ERROR sub-issue guardrail rejected target repo
  owner=qf-studio repo=pilot execution_path=...
  error="target repo is not in user's configured project list: qf-studio/pilot not in configured projects [alice/site]"
```

or:

```
ERROR sub-issue guardrail: could not resolve origin remote
  execution_path=... error="no origin remote found: ..."
```

The task fails with an error wrapping `executor.ErrRepoNotInConfig`
(or `ErrNoOriginRemote`).

## Diagnosis checklist

1. **Confirm the target repo is what you expect.**
   ```sh
   git -C <executionPath> remote get-url origin
   ```
   The output's `owner/repo` is what Pilot resolved. If it surprises you
   (e.g. it's the upstream rather than your fork), the bug is in your
   `~/.pilot/config.yaml` or local clone, not in Pilot.

2. **List configured projects.**
   ```sh
   yq '.projects[] | "\(.github.owner)/\(.github.repo) -> \(.path)"' \
     ~/.pilot/config.yaml
   ```
   Each `owner/repo` listed here is allowed. If the rejected pair isn't
   here, that's the immediate cause.

3. **Check for projectPath drift.**
   The guardrail also rejects "right repo, wrong working tree" — i.e.
   the configured project's `path` does not match the directory Pilot
   is executing in. This usually means you have a fork in a different
   path than the project entry expects.

## Fix paths

**Most common — user pointed Pilot at the wrong repo:**

```yaml
# ~/.pilot/config.yaml
projects:
  - name: my-fork
    path: /Users/me/projects/my-fork
    github:
      owner: my-username     # ← was previously upstream owner
      repo:  pilot-fork      # ← was previously upstream repo
```

Re-run; the guardrail will accept.

**Ad-hoc one-off (testing, recovery, debugging):**

```sh
PILOT_ALLOW_UNMANAGED_REPO=1 pilot run ...
```

The bypass always logs a WARN with the resolved owner/repo so the action
remains visible in dashboards and the daemon log:

```
WARN PILOT_ALLOW_UNMANAGED_REPO=1 bypassed repo allowlist
  component=executor.repo_guardrail
  owner=... repo=... project_path=... configured_repos=...
```

Never set this in `~/.pilot/config.yaml` or a long-lived shell env —
it disables the safety net.

**Library/SDK use (no allowlist wired):**

If you're calling `executor.NewRunnerWithConfig` directly and didn't
plumb a `RepoAllowlist`, you'll see:

```
WARN sub-issue guardrail skipped: no RepoAllowlist configured on Runner;
  production callers must invoke Runner.SetRepoAllowlist
```

In production this means cmd/pilot's wiring regressed — fix the
construction site. In a one-off script, either wire one or accept the
WARN (the call falls through to the existing `gh` error path, same as
pre-guardrail).

## What was wrong before this guardrail

`internal/executor/epic.go::createSubIssuesViaGitHub` ran
`exec.CommandContext(ctx, "gh", "issue", "create", ...)` with
`cmd.Dir = executionPath` and **no `owner/repo` validation**. `gh`
infers the target from the directory's `origin` remote, with no
cross-check against the user's configured projects. A misconfigured
worktree silently became a write-target for sub-issues.

A partial guard at `internal/executor/runner.go::ValidateRepoProjectMatch`
existed for the parent task but did not apply to the sub-issue creation
path.

## Related

- Incident comment: `qf-studio/pilot#3021#issuecomment-4508477616`
- Task plan: `.agent/tasks/TASK-286-guardrail-external-repo-issue-create.md`
- Pattern memory: `.agent/knowledge/memories/patterns/pattern_target_repo_validation.md` *(write after merge)*
- Pitfall memory: `.agent/knowledge/memories/pitfalls/pitfall_external_repo_issue_create.md` *(write after merge)*
