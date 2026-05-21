# TASK-286: Guardrail — refuse issue creation on repos outside the user's project list

**Status**: in-progress (handed off to Pilot as [#3027](https://github.com/qf-studio/pilot/issues/3027))
**Priority**: P1 (data-integrity / reputation)
**Estimated Effort**: S (3-4 person-hours)
**Risk Level**: Low (additive validation; opt-out via env var for power users)

## Problem

On 2026-05-20 an external Pilot user (@tenlisboa) accidentally pointed his
local `pilot start` instance at the **upstream** `qf-studio/pilot` repo
instead of his own fork. The epic decomposer fired and created 6 duplicate
sub-issues (#3021–#3026) titled `feat(auth): add OAuth provider integration`,
each carrying `<!--autopilot-meta parent: GH-201 inherited-spec: true-->`.
The user's PAT had default public-repo write scope, so GitHub accepted
the calls. We closed the issues and commented on #3021 with the
diagnosis.

**Root cause** (research via `Explore` agent, 2026-05-21):

- `internal/executor/epic.go:1129` — `createSubIssuesViaGitHub()` shells
  out `gh issue create` at `:1223` with `cmd.Dir = executionPath`.
- `gh issue create` silently uses the working directory's `origin` remote
  to infer `owner/repo`. **Zero validation** that this owner/repo
  corresponds to a project in the user's `~/.pilot/config.yaml`.
- A partial safeguard already exists for the **parent** task at
  `internal/executor/runner.go:1130-1131`
  (`ValidateRepoProjectMatch(task.SourceRepo, task.ProjectPath)`) — it is
  not invoked on the sub-issue path.
- The direct adapter entry point at
  `internal/adapters/github/issue_create.go:15`
  (`CreatePilotIssue(ctx, c, owner, repo, ...)`) also has no
  owner/repo check.

The blast radius today: any project pointed at a repo the user does not
own, but where their PAT has issue-write scope (almost every public
repo), will silently receive issues. This is a reputation risk for
Pilot and a confusion / noise risk for upstream maintainers.

## Approach (one paragraph)

Introduce a single chokepoint —
`internal/executor/repo_guardrail.go::ValidateTargetRepo(ctx, cfg, owner,
repo, projectPath) error` — that returns an error unless the
`(owner, repo)` pair resolves from a project in `cfg.Projects` whose
`Path` matches `projectPath` (or, when `projectPath == ""`, any
configured project). Invoke it (a) inside
`createSubIssuesViaGitHub()` before `gh issue create`, by first
resolving `executionPath`'s git remote to `owner/repo`, and (b) at the
top of `CreatePilotIssue()` in the adapter. The check is bypassable
with `PILOT_ALLOW_UNMANAGED_REPO=1` for power users running ad-hoc
commands, but **never** silently — the bypass logs a `WARN` line with
the resolved repo so it shows up in dashboards and audit. The
existing `ValidateRepoProjectMatch` at `runner.go:1130` continues to
guard the parent-task entry; this task adds the same guard on the
two remaining holes.

## Files Touched

| File | Change | Why |
|---|---|---|
| `internal/executor/repo_guardrail.go` *(new)* | `ValidateTargetRepo()` + git-remote resolver + tests-of-tests helper | Single chokepoint, reusable |
| `internal/executor/repo_guardrail_test.go` *(new)* | Table-driven tests | Cover allow / reject / bypass / missing-remote |
| `internal/executor/epic.go:1218–1223` | Call `ValidateTargetRepo` before `gh issue create` | Closes the incident's exact path |
| `internal/adapters/github/issue_create.go:15` | Call `ValidateTargetRepo` at top of `CreatePilotIssue` | Defense in depth — adapter is callable from non-executor paths |
| `internal/config/config.go` *(read-only)* | Reuse `Config.Projects` + `GetProject(path)` | No schema change |
| `docs/configuration.md` (or nearest live doc) | One paragraph: bypass via `PILOT_ALLOW_UNMANAGED_REPO=1` | Discoverability for power users |

## Implementation Steps

### Step 1 — Guardrail package + tests (S, 1.5h)
- Create `internal/executor/repo_guardrail.go` with:
  - `func resolveGitRemote(dir string) (owner, repo string, err error)` — `git -C <dir> remote get-url origin`, parse `git@github.com:o/r.git` and `https://github.com/o/r(.git)?` forms. Reuse existing parser if one exists under `internal/git/` — grep first.
  - `func ValidateTargetRepo(ctx, cfg *config.Config, owner, repo, projectPath string) error` — match against `cfg.Projects`; if `projectPath != ""`, require it to match the matched project's `Path`.
  - `PILOT_ALLOW_UNMANAGED_REPO=1` bypass that logs a `WARN` with `slog`.
- `repo_guardrail_test.go` table tests:
  - happy path (configured repo)
  - reject (unconfigured repo) → returns typed error `ErrRepoNotInConfig`
  - reject (configured repo but wrong projectPath)
  - bypass via env var → returns nil + WARN observable through a `slog.NewTextHandler` writing into a buffer
  - missing git remote → typed error `ErrNoOriginRemote`

### Step 2 — Wire into sub-issue path (S, 0.5h)
- In `internal/executor/epic.go`, in `createSubIssuesViaGitHub()`:
  - Before the loop that shells `gh issue create` (`:1218`-ish), call `resolveGitRemote(executionPath)`, then `ValidateTargetRepo(ctx, r.config, owner, repo, executionPath)`.
  - On error: return wrapped error; abort the whole sub-issue batch (do **not** create some-but-not-all).
- Add one integration-style test in `epic_test.go` that uses a temp dir with a fake `origin` pointing at `qf-studio/pilot` and an empty `Config.Projects`, asserting no `gh` call is made (use a mock `exec.Cmd` runner if available; otherwise gate the test behind a build tag).

### Step 3 — Wire into adapter (S, 0.5h)
- In `internal/adapters/github/issue_create.go`, top of `CreatePilotIssue`:
  - Accept `cfg *config.Config` + caller's `projectPath` (additive params). Update the 1–2 call sites — grep before editing.
  - Call `ValidateTargetRepo(ctx, cfg, owner, repo, projectPath)`.
- The adapter is lower-level, so projectPath may be `""`; still require `(owner, repo)` to map to **some** configured project.

### Step 4 — Docs + changelog (XS, 0.5h)
- Add a paragraph to `docs/` (whichever page documents `~/.pilot/config.yaml`) describing the guardrail and the bypass env var.
- Add a `.agent/sops/guardrail-target-repo.md` SOP — short, "if you see `ErrRepoNotInConfig`, here's what to check".

## Acceptance Criteria

- [ ] Running `pilot start` against a fresh `~/.pilot/config.yaml` whose `projects[]` does NOT include `qf-studio/pilot` cannot create issues on `qf-studio/pilot`, regardless of which directory it's invoked from. Confirmed by integration test.
- [ ] The same scenario with `PILOT_ALLOW_UNMANAGED_REPO=1` proceeds, but logs `WARN` containing the resolved `owner/repo`.
- [ ] Existing happy-path projects continue to work — no regression on the 323-feature smoke. Confirmed by `make test`.
- [ ] `CreatePilotIssue` cannot be called from any in-tree caller without a `*config.Config` (compiler-enforced via signature change).
- [ ] No new dependencies.

## Out of Scope

- GitHub App auth migration (P3 backlog item, separate concern).
- PAT scope tightening (handled at user setup time, not at runtime).
- Validating Linear/Jira/GitLab target projects — same class of bug,
  filed as follow-up `TASK-287` *if* maintainers want symmetric coverage.

## Why Now

Real-world incident on the upstream repo. Six bogus issues in 27
minutes; could have been 60. The cost is 3–4h of work for a guardrail
that protects every Pilot user *and* every upstream they accidentally
target.

## References

- Incident: closed dupes `qf-studio/pilot#3021`–`#3026` (2026-05-20)
- Diagnosis comment: `qf-studio/pilot#3021#issuecomment-4508477616`
- Existing parent-task guard pattern: `internal/executor/runner.go:1130`
  (`ValidateRepoProjectMatch`) — reuse style and error wrapping.
- Knowledge graph: link to `pitfall_*` once written (see Step 5 below).

## Step 5 (Navigator) — Memories

After Pilot ships the PR, write the following memories:

- **Pitfall** `.agent/knowledge/memories/pitfalls/pitfall_external_repo_issue_create.md` — describes the 2026-05-20 incident, the missing guard at `epic.go:1218`, and the user-visible signature (`autopilot-meta` body on unrelated repo).
- **Pattern** `.agent/knowledge/memories/patterns/pattern_target_repo_validation.md` — the chokepoint `ValidateTargetRepo` and its bypass envvar; cite call sites.
- Update `.agent/knowledge/graph.json` with both nodes + an `addresses` edge from pitfall → pattern.
