# SOP: Repo allowlist guardrail (TASK-286 / GH-3027)

> **When to read this:** you see `sub-issue guardrail rejected target repo`
> or `CreatePilotIssue repo guardrail` in Pilot logs, or `ErrRepoNotInConfig`
> surfaced in a task error.

## What it does

Pilot refuses to create a GitHub issue when the resolved `owner/repo` is
not in `~/.pilot/config.yaml`'s `projects[]` list. Two layers enforce this:

| Layer | Where | What it guards |
|---|---|---|
| **Primary** — sub-issue path | `internal/executor/repo_guardrail.go::ValidateTargetRepo`, called from `internal/executor/epic.go::CreateSubIssues` | The epic decomposer. Resolves `executionPath`'s git origin and rejects before any `gh issue list` or `gh issue create` fires. |
| **Defense in depth** — adapter | `internal/adapters/github/issue_create.go::validateIssueRepo`, called from `CreatePilotIssue` | Any direct caller of the GitHub adapter (autopilot feedback loop, future paths). |

The two layers use distinct interfaces (`executor.RepoAllowlist` and
`github.IssueAllowlist`) only to avoid the executor→github import cycle —
they have the same shape and the same concrete `configRepoAllowlist`
implementation in `cmd/pilot/repo_allowlist.go` satisfies both.

Filed after the 2026-05-20 incident on `qf-studio/pilot`, where
@tenlisboa's misconfigured Pilot fired 6 duplicate sub-issues
(#3021–#3026) before the decomposer ran out of subtasks. `gh issue
create` had been inferring the target from the directory's `origin`
remote with no allowlist cross-check.

## Symptoms

```
ERROR sub-issue guardrail rejected target repo
  owner=qf-studio repo=pilot execution_path=...
  error="target repo is not in user's configured project list: qf-studio/pilot not in configured projects [alice/site]"
```

```
ERROR sub-issue guardrail: could not resolve origin remote
  execution_path=... error="no origin remote found: ..."
```

```
ERROR CreatePilotIssue repo guardrail: owner/repo not in configured projects [...]
```

The task fails with an error wrapping `executor.ErrRepoNotInConfig`
(or `ErrNoOriginRemote`).

## Diagnosis checklist

1. **Confirm which repo Pilot resolved.**
   ```sh
   grep -E "guardrail" ~/.pilot/logs/pilot.log | tail -10
   # or for the executionPath in question:
   git -C <executionPath> remote get-url origin
   ```
   If the resolved `owner/repo` surprises you (e.g. upstream instead of
   your fork), the bug is in your config or local clone — not Pilot.

2. **List configured projects.**
   ```sh
   yq '.projects[] | "\(.github.owner)/\(.github.repo) -> \(.path)"' \
     ~/.pilot/config.yaml
   ```
   Each entry is an allowed pair. Missing from the list = rejection.

3. **Check for projectPath drift.**
   The primary guardrail also rejects "right repo, wrong working tree" —
   i.e. the configured project's `path` differs from the directory Pilot
   is executing in. Often means a fork in an unexpected path.

## Fix paths

**Most common — register the repo:**

```yaml
# ~/.pilot/config.yaml
projects:
  - name: my-fork
    path: /Users/me/projects/my-fork
    github:
      owner: my-username
      repo:  my-fork
```

Re-run; the guardrail accepts.

**Ad-hoc one-off (testing, recovery, debugging):**

```sh
PILOT_ALLOW_UNMANAGED_REPO=1 pilot run ...
```

The bypass always logs WARN with the resolved owner/repo. Never set
permanently in production or in a long-lived shell env.

```
WARN PILOT_ALLOW_UNMANAGED_REPO=1 bypassed repo allowlist
  component=executor.repo_guardrail
  owner=... repo=... project_path=... configured_repos=...
```

**Library/SDK use (no allowlist wired):**

Calling `executor.NewRunnerWithConfig` or `github.CreatePilotIssue`
directly without an allowlist logs:

```
WARN sub-issue guardrail skipped: no RepoAllowlist configured on Runner;
  production callers must invoke Runner.SetRepoAllowlist
```

```
WARN CreatePilotIssue: no IssueAllowlist configured; repo check skipped
```

The autopilot feedback loop intentionally passes `nil` because its
`f.owner` / `f.repo` come from explicit config at construction time —
already constrained. New non-feedback-loop callers must pass a non-nil
allowlist.

## Code locations

| Symbol | File |
|--------|------|
| `RepoAllowlist` interface | `internal/executor/repo_guardrail.go` |
| `ValidateTargetRepo` | `internal/executor/repo_guardrail.go` |
| `resolveGitRemote` / `parseGitHubRemoteURL` | `internal/executor/repo_guardrail.go` |
| `Runner.SetRepoAllowlist` | `internal/executor/runner.go` |
| Primary guardrail call site | `internal/executor/epic.go::CreateSubIssues` |
| `IssueAllowlist` interface | `internal/adapters/github/issue_create.go` |
| Adapter guardrail (`validateIssueRepo`) | `internal/adapters/github/issue_create.go` |
| `configRepoAllowlist` (satisfies both) | `cmd/pilot/repo_allowlist.go` |
| Wiring (production) | `cmd/pilot/main.go`, `cmd/pilot/commands.go`, `cmd/pilot/interactive.go` |
| Bypass env var | `PILOT_ALLOW_UNMANAGED_REPO=1` |

## What was wrong before

`internal/executor/epic.go::createSubIssuesViaGitHub` ran
`exec.CommandContext(ctx, "gh", "issue", "create", ...)` with
`cmd.Dir = executionPath` and no owner/repo validation. `gh` inferred
the target from the directory's `origin` remote with no cross-check. A
partial guard at `internal/executor/runner.go::ValidateRepoProjectMatch`
existed for the *parent* task but did not apply to the sub-issue path
or to the adapter.

## Related

- Incident: `qf-studio/pilot#3021`–`#3026` (all closed as dupes)
- Diagnosis comment: `qf-studio/pilot#3021#issuecomment-4508477616`
- Task plan (archived): `.agent/tasks/archive/TASK-286-guardrail-external-repo-issue-create.md`
- Primary PR (v2.147.0): `qf-studio/pilot#3033` — sub-issue path + executor-side guardrail
- Phase B PR (this change): adapter-level guardrail + `IssueAllowlist`
