# TASK-500: Client-owned repo onboarding — `master`-default base, per-project branch template, CLI dispatch flags, per-project autopilot off

**Status**: 📋 Planned 2026-09-24 (research done; nothing dispatched; first consumer = `client-org/client-repo` for the client engagement, see `<client-workspace>/.agent/system/pilot-setup-client-repo.md`)
**Created**: 2026-09-24
**Assignee**: Aleks (plan) → Pilot (legs, via `nav-pilot`, one issue per leg)
**Priority**: blocks the first Pilot dry run on a client repo (TASK-06 in the client Navigator)

---

## Context

**Problem**: Pilot assumes every project is ours: default branch `main`, branches `pilot/GH-<n>`, issues in the project's own GitHub repo, autopilot on daemon-wide (`--env stage` = auto-merge on green), fix issues filed into the project's repo, one global `env_passthrough`. A client-owned repo with `master` as default, its own branch/PR conventions, issues in a Linear workspace we do not administer, and a hard "never merge, never file issues, never push to master" rule breaks on the first worktree.

**Evidence (2026-09-24 research, `internal/`)**:
- Worktree base is hardcoded to `origin/main`: `internal/executor/worktree.go:338` (`baseRef := "origin/main"`), `:344` (`git fetch origin main`), `:559`, `:568`, `:586` (`fetchOriginMainForWorktree`). `task.BaseBranch` (resolved correctly by `GetDefaultBranch`, `git.go:1044-1063`, and `ResolveBaseBranch`, `config.go:333-341`) only reaches the PR `--base` (`git.go:868,887`), never the worktree. A repo without a `main` ref fails every run (fetch fails closed since PR#5457).
- Branch names: `pilot/GH-<n>` hardcoded (`prompt_builder.go:85`); CLI `pilot task` → `pilot/TASK-<epoch%100000>` (`cmd/pilot/commands.go:512-513`); `workflow.BranchPrefix` declared, never read (`workflow.go:41-42`). No per-project template.
- CLI `pilot task` flags (`commands.go:1054-1061`): `--project` (a **path**), `--dry-run`, `--verbose`, `--alerts`, `--budget`, `--local`, `--result-json`, `--team`, `--team-member`. No `--branch`, `--task-id`, `--body-file`, `--base-branch`, `--title`. Body = one positional string.
- Autopilot: `orchestrator.autopilot.enabled`, `auto_merge`, `merge_method` are global (`autopilot/types.go:117-118`); per-project overlays exist only for `approval`, `release`, `ci_checks` (`types.go:393,417,838`). `approval.require_approval: true` gates `handleCIPassed` (`controller.go:919-923`). Adoption/tracking keys on `pilot/GH-` prefix or `Closes #N` / `Parent: GH-N` markers (`controller.go:1031-1065, 8642, 8757`; `scope_guard.go:97`), so a CLI PR on another prefix is invisible to autopilot — by accident, not by contract. Feedback loop constructor is `(owner, repo)` of the project (`feedback_loop.go:216`): fix issues would land in the client repo if a tracked PR ever failed CI.
- Env: `NPM_TOKEN` is scrubbed by the `_TOKEN` suffix rule (`model_env.go:23`) unless in the **global** `executor.claude_code.env_passthrough` (`pilot.example.yaml:716-717`).
- Commit identity: not set by Pilot (`grep user.name|GIT_AUTHOR` = none); box-wide git config.
- PR body: `gh pr create --body <literal>` (`git.go:862-911`); repo `.github/pull_request_template.md` is never loaded.
- PR title: `validatePRTitle` (`internal/executor/title.go:65`) rejects non-conventional titles; a client convention `KEY-<n>: <summary>` (no type) must be checked against it.
- No clone step anywhere (`grep 'git clone'` = none): the operator clones.
- `sync_main_after_task` (`backend.go:445`) → `syncMainBranch(task.ProjectPath)` (`runner.go:6871`) touches the **root** clone's default branch; must be off for client repos (box: not set → default; verify default is false).
- Base-presence gate: backticked `path/with.ext` in a body = "must exist on default branch" (`dependency_detector.go:169-219`) → holds on bodies that name files to create.

**Goal**: a `projects[]` entry can describe a client repo fully — base branch honoured by the worktree, branch and PR-title templates, autopilot/feedback/sync/ci-fix off per project, per-project env, a bootstrap command — and a task can be dispatched from a Linear/text body with an explicit id and branch, producing exactly one branch + one PR and nothing else.

---

## Acceptance Criteria

- [ ] A project with `default_branch: master` and no `main` ref runs end to end: worktree created from `origin/master`, PR opened against `master`. Unit test with a bare repo whose HEAD is `master`.
- [ ] `projects[].branch_template` (e.g. `KEY-{id}-{slug}`) and `pr_title_template` (e.g. `{id}: {summary}`) honoured by CLI and poller paths; `validatePRTitle` accepts the configured template; default templates unchanged for existing projects.
- [ ] `pilot task` gains `--task-id`, `--branch`, `--base-branch`, `--body-file`, `--title`; `--project` also accepts a config project **name**.
- [ ] `projects[].autopilot: {enabled: false}` (or `mode: pr_only`) makes the controller ignore the project's PRs entirely (no CI wait, no merge, no branch delete, no ci-fix, no fix issues, no labels/comments), independent of the global flag and `--env`. Test: tracked PR for such a project passes CI → nothing happens.
- [ ] `projects[].env` map injected into the model subprocess and gate commands for that project only; not visible to other projects' subprocesses. Test: two projects, one with `NPM_TOKEN`.
- [ ] `projects[].bootstrap` (command list run in the worktree before the model starts; failure = task fails before any model call); `sync_main_after_task` overridable per project and forced off when `autopilot.enabled: false`.
- [ ] PR body: when the repo has `.github/pull_request_template.md`, the executor prompt instructs the model to fill it and the PR body check requires its H2 headings (config `pr_body_template: repo`).
- [ ] Docs: `configs/pilot.example.yaml` client-repo example; `docs/` page "Client-owned repositories".

---

## Implementation

### Leg 1: worktree base ref honours the project's base branch (bug)
- [ ] Thread `task.BaseBranch` (or `ResolveBaseBranch`) into `preparePooledWorktree`, `fetchOriginMainForWorktree` (rename), `CreateWorktreeWithBranch`; fall back to `GetDefaultBranch` when empty; keep the fail-closed fetch semantics of PR#5457.
- [ ] Rename helpers away from "Main"; table test main/master/custom.
**Files**: `internal/executor/worktree.go`, `runner.go:3456-3513`, `runner_git.go`, tests.

### Leg 2: branch + PR-title templates per project
- [ ] `ProjectConfig.BranchTemplate`, `PRTitleTemplate`; placeholders `{id}`, `{number}`, `{slug}`, `{summary}`, `{type}`; slug from the title (kebab, ≤40 chars).
- [ ] `prompt_builder.go:85`, `commands.go:512`, orchestrator `pilot/%s` sites use the template; autopilot adoption learns the template for that project (not only `pilot/GH-`).
- [ ] `validatePRTitle` takes the template into account.
**Files**: `internal/config/config.go`, `internal/executor/{prompt_builder,title}.go`, `cmd/pilot/commands.go`, `internal/autopilot/{controller,scope_guard}.go`.

### Leg 3: CLI dispatch flags
- [ ] `--task-id`, `--branch`, `--base-branch`, `--body-file`, `--title`; `--project <name|path>`.
- [ ] Body from file becomes the task description; base-presence gate runs on it exactly as for issues (document the backtick rule in `--help`).
**Files**: `cmd/pilot/commands.go`.

### Leg 4: per-project autopilot off + feedback/sync guards
- [ ] `ProjectConfig.Autopilot *ProjectAutopilotOverride{Enabled *bool}`; controller skips tracking/adoption/CI/merge/ci-fix/labels for disabled projects; feedback loop never constructed for them; `sync_main_after_task` forced off.
**Files**: `internal/config/config.go`, `internal/autopilot/{controller,feedback_loop,reconciler}.go`, `internal/executor/runner.go:6871`.

### Leg 5: per-project env + bootstrap + PR template
- [ ] `ProjectConfig.Env map[string]string` merged only for that project's subprocess/gates; `Bootstrap []string`; `PRBodyTemplate string` (`repo|none`).
**Files**: `internal/executor/{model_env,backend_claudecode,git}.go`, `internal/config/config.go`, `configs/pilot.example.yaml`, `docs/`.

Order: Leg 1 (bug, smallest) → Leg 4 (safety) → Leg 3 → Leg 2 → Leg 5. Legs 1, 3, 4 are enough for the first dry run on `client-repo` if the branch name is accepted as `pilot/KEY-<n>` for that run.

---

## Out of Scope
- Linear webhook ingestion for a foreign workspace (needs their admin); Linear polling adapter may cover it later.
- Cloning repos onto the box (operator step, documented in the client SOP).
- Toolchain managers (nvm/mise) inside Pilot; the box PATH stays the contract.

## Technical Decisions
| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| How a client project opts out of autopilot | rely on `require_approval` · explicit per-project `autopilot.enabled` | explicit flag | `require_approval` still tracks, waits on CI, may ci-fix and file issues in their repo |
| Branch naming | keep `pilot/…` everywhere · template | template with unchanged default | client conventions are theirs; our repos keep `pilot/GH-` |
| Env | global `env_passthrough` · per-project `env` | per-project | one client's token must not reach another project's subprocess |

## Verify
```bash
make test && make lint
go test ./internal/executor -run 'Worktree|BaseBranch|Template' -v
go test ./internal/autopilot -run 'ProjectDisabled|Adoption' -v
pilot task --project client-repo --task-id KEY-1 --branch KEY-1-dry-run --body-file /tmp/body.md --dry-run
```

## Done
- [ ] All five legs merged; example config + docs page; a dry run on `client-repo` produced one branch from `origin/master` and one draft PR against `master`, no other remote writes (verified with `gh api` event list).

## Refs
- client SOP: `<client-workspace>/.agent/system/pilot-setup-client-repo.md`; client decisions D-010, D-022, `pilot-execution-rules.md` P5–P7.
- PR#5457 (fetch fail-closed), GH-4001 (release opt-in), GH-2290 (`branch_from`), GH-3716 (per-project quality gates), pitfalls `runselfreview-runs-in-repo-root-phantom-reimplementation`, `poller-branch-lookup-marks-issue-done-from-another-issues-pr`.

---

**Last Updated**: 2026-09-24
