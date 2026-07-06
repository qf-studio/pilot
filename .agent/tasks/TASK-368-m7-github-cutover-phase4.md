---
status: phase-4d-in-progress
priority: P3
created: 2026-06-22
sdk_version: v0.28.1
execution: manual
github_issue: 3423
labels: [m7, sdk, github, adapter, human-led]
---

# TASK-368: M7 Phase 4 — GitHub adapter → studio-sdk cutover (MANUAL)

**Status**: 🚀 Phases **4b + 4d.1 + 4d.3 + 4d.4 SHIPPED 2026-07-06**, studio-sdk pinned **v0.28.1**. Remaining: 4d.2 (projects-loop → SDK poller), 4d.5 (webhook repoint), 4d.6 (cleanup + delete-vs-residual decision — gitlab precedent kept a ~4.7K-LOC residual; decide, don't assume).
- **4b** (PR [#3890](https://github.com/qf-studio/pilot/pull/3890), v0.27.0): `githubPollerRegistration()` live behind `use_sdk_poller` — full `PollerDeps` hooks + `sdkRateLimitScheduler` seam + config-driven board wiring; SDK owns the DEFAULT repo when flagged (mutual exclusion in gateway + standalone blocks); `projects:` stay in-tree; auto mode only. **Live trial: flag set in `~/.pilot/config.yaml` 2026-07-06, daemon restart pending (user); needs binary ≥ the #3890 release.**
- **4d.1** (PR [#3894](https://github.com/qf-studio/pilot/pull/3894), v0.28.0→v0.28.1): autopilot fully on the SDK client — concrete-type swap (no 35-method interface; httptest suites transfer), `internal/ghissue` package (CreatePilotIssue/allowlist), `apGHClient` at all 3 controller construction sites, release CLI swapped. Forced two upstream releases: **v0.28.0** (typed `RateLimitError`/`AuthError` + retry fast-paths, `GetOpenSubIssueNumbers`, `LabelPilot`/failed-retry labels, `ParseParentIssueNumber`) and **v0.28.1** (stale bounded `GetTagForSHA` → exhaustive pagination; caught by `TestHandleReleasing_ExhaustiveTagDrain` — see `pitfalls/pitfall_sdk_ports_go_stale_vs_intree.md`).
- **4d.3** (PR [#3895](https://github.com/qf-studio/pilot/pull/3895)): spec-guard on the SDK path — `ghissue.ValidateSpec` (marker byte-identical to legacy; rule changes must land in BOTH copies until 4d.6) + `applySpecGuardSDK` two-strike gate wired into `handleGithubIssueEventSDK`. Closed the documented enforcement gap before the live trial.
- **4d.4** (PR [#3896](https://github.com/qf-studio/pilot/pull/3896)): SDK PR-create — `sdkshim.GitHubPRCreator` (executor.PRCreator, 'already exists' URL recovery) + runner `RegisterPRCreator` per-repo registry keyed `adapter:owner/repo` (startup-registered; deliberately NOT the per-event `SetPRCreator` shared slot — cross-adapter/repo race-free); gh CLI remains the fallback.
- 4c subsumed by v0.27.0 (SDK board layer; in-tree `project_source.go`/`project_board.go` retire with 4d.6). (4a history: dormant scaffolding 2026-06-30 vs v0.25.0.)
**Assignee**: Manual (human-led per #3423 — daemon must NOT claim this)
**Tracking issue**: https://github.com/qf-studio/pilot/issues/3423
**Template**: GitLab cutover — commit `07467a9d` / GH-3456 / PR #3459

---

## Verdict (re-verified against studio-sdk v0.25.0 — 2026-06-30)

**Full retirement of `internal/adapters/github` is STILL NOT possible at SDK v0.25.0.** v0.25.0
shipped only `linear CreateIssue` — the github poll/board/PR surface is byte-for-byte the
same gap as v0.24.0. Re-verified file-by-file in `sdk@v0.25.0/sdk/integrations/github/`:
no `project_board.go`/`project_source.go`; 0/5 of the missing Client methods present;
`core.PollerDeps` is still the 4-field subset (`ProcessedStore`, `MaxConcurrent`, `Handler`,
`OnPRCreated`). Only the additive, feature-flagged *poll-path scaffolding* (Phase 4a) is
achievable; the live cutover is now gated on **SDK v0.26.0+**. Four independently-verified blockers:

1. **Board layer absent from SDK.** No `project_board.go`/`project_source.go` in the SDK
   github package (only the YAML `ProjectBoardConfig` type, `types.go:34`). Autopilot
   writes the board via `WithProjectBoardSync` (`internal/autopilot/controller.go:116`, 6
   sites); the poller sources issues via `WithProjectBoardSource`/`WithBoardSync`
   (`internal/adapters/github/poller.go:343`/`:351`). Also needs
   `ExecuteGraphQLTolerant`/`PartialGraphQLError` (shipped in TASK-319) — absent in SDK.
2. **5 Pilot-only Client methods missing:** `CompareStatus`, `GetAuthenticatedUser`,
   `SearchOpenPRsForIssue`, `FindOpenPRByBranch`, `ExecuteGraphQLTolerant`. Three are
   load-bearing for autopilot (GH-3417 human-recovery guard + release-tag dedup).
3. **`core.PollerDeps` is a strict subset** (4 fields, `sdk/core/registry.go:124`) of the
   in-tree poller's 8 host-coupled options (`poller.go:239`-`:351`): WithScheduler,
   WithTaskChecker, WithExecutionChecker, WithPreFlightJudge, WithExecutionSaver,
   WithIssueMetricsRecorder, WithProjectBoardSource, WithBoardSync.
4. **Autopilot hard-typed to concrete `*github.Client`** (`NewController`,
   `controller.go:221`) and `*github.ProjectBoardSync` (`:116`) — no interface seam.

SDK `sdkshim/doc.go` already states the in-tree github adapter "stays in tree for
autopilot's PR/CI/release/branch surface (M7 retires only its issue-poller role)."

---

## Context

M7 migrates Pilot's adapters to consume studio-sdk behind the `sdk/core` contract. 9 of 10
adapters' poll/chat paths already cut over. GitHub is last + riskiest (autopilot is
github-only). This task scopes ONLY the achievable additive scaffolding (Phase 4a),
mirroring the GitLab template, with the live poller / board / gh-CLI PR path / autopilot
all staying in-tree.

> ⚠️ **Phase 4a is dormant until SDK v0.25.0+.** The registration is default-OFF and NOT
> added to `adapterPollerRegistrations()`; the orchestrator sibling is unused by the live
> handler-driven github path. This is scaffolding/parity code, not a behavior change.

---

## Acceptance Criteria (Phase 4a only) — ✅ all verified 2026-06-30

- [x] `internal/orchestrator/orchestrator.go` gains `ProcessGithubIssueEvent(ctx, ev sdkcore.IssueEvent, projectPath string) error` mirroring `ProcessGitlabIssueEvent` — present at `orchestrator.go:759`. Uses `ev.SequenceID` **verbatim** (NO `GH-%d` re-prefix) and `sdkshim.PriorityFromSDK(ev.Priority)`.
- [x] `internal/adapters/sdkshim/repo_resolver.go` implements the `github` branch (`resolveGithubRepo` / `githubCloneURL`): per-project routing via `cfg.Projects[].GitHub.{Owner,Repo}` matched on `ev.ProjectID`, `cfg.Adapters.GitHub` fallback; `ErrRepoNotResolved` only when nothing matches.
- [x] `cmd/pilot/poller_github.go` defines `githubPollerRegistration()` (`:29`) gated on `adapters.github.use_sdk_poller`; **NOT** added to `adapterPollerRegistrations()` (confirmed: registry lists linear/jira/asana/azuredevops/plane/discord/gitlab — no github).
- [x] `handleGithubIssueEventSDK(...)` lives alongside the legacy handler (`handlers.go:1167`); `taskID = ev.SequenceID` verbatim (explicit `// do NOT re-prefix` comment; branch `pilot/GH-42`).
- [x] Additive `UseSDKPoller bool` on the GitHub adapter config (resolved by build at `poller_github.go:34`); documented in `configs/pilot.example.yaml:85`.
- [x] With the flag OFF, github behavior is unchanged — `internal/adapters/github` tests green (10.5s, `-race`).
- [x] No edits under `.claude/worktrees` (canonical tree only; work done in a fresh worktree off origin/main).

---

## Implementation (sub-phases, each a gate)

### 4a.1 — orchestrator sibling
Add `ProcessGithubIssueEvent` after `:717`. Copy `ProcessGitlabIssueEvent` body:
`TicketData{ID:ev.IssueID, Identifier:ev.SequenceID, Title:ev.Title, Description:ev.Body, Priority:sdkshim.PriorityFromSDK(ev.Priority), Labels:ev.Labels}` → `PlanTicket` → `saveTaskDocument` → internalTask `Branch=pilot/<ev.SequenceID>`.
**Gate:** `go test -race ./internal/orchestrator/...`

### 4a.2 — repo resolver github branch
Implement the `github` case in `ResolveRepoForEvent` + table-driven tests (per-project match / adapters fallback / unresolved→ErrRepoNotResolved).
**Gate:** `go test -race ./internal/adapters/sdkshim/...`

### 4a.3 — config flag
Add `UseSDKPoller` to the GitHub adapter config; document in `configs/pilot.example.yaml`.
**Gate:** `go build ./...`

### 4a.4 — dormant SDK poller registration + handler
Create `cmd/pilot/poller_github.go` (mirror `poller_gitlab.go`): cfg→`sdkGithub.Config` (`PilotLabel`→`TriggerLabel`), handler-side `*sdkGithub.Client`, `sdkcore.PollerDeps{Handler: …}`. Add `handleGithubIssueEventSDK` next to the legacy handler. Do NOT inject a PRCreator, do NOT relax `runner.go:3601`, do NOT register github.
**Gate:** `go build ./... && go vet ./... && go test -race ./cmd/pilot/...`

---

## Out of Scope (deferred phases, all SDK-blocked)

- **4b** — SDK poller option parity (Scheduler/TaskChecker/ExecutionChecker/PreFlightJudge/ExecutionSaver/IssueMetricsRecorder) + flip flag on for single-repo. Blocked on SDK v0.26.0+ (still absent at v0.25.0).
- **4c** — board source/sync on SDK poller + `ExecuteGraphQLTolerant`; retire `project_source.go`/`project_board.go`. Blocked on SDK board support.
- **4d** — autopilot client unification (`NewController` swap / interface seam), gh-CLI→SDK PR-create (`runner.go:3601` guard), spec-guard/CreatePilotIssue/label-vocab porting, **delete `internal/adapters/github`**.
- Live `createPollerForRepo` / multi-repo loop (`cmd/pilot/main.go` ~2195-2400).
- Webhook handler migration.

---

## Behavior Deltas (github-specific)

- **SequenceID already `GH-`-prefixed** by the SDK (`adapter.go:143`). Legacy handler re-prefixes via `fmt.Sprintf("GH-%d", issue.Number)` (`handlers.go:223`); the new SDK handler must use `ev.SequenceID` verbatim or it produces `GH-GH-42` (breaks branch names, dedup, sub-issue parent parsing).
- **PRCreator injection does NOT apply to github** — SDK Client has `CreatePullRequest(owner,repo,*PullRequestInput)`, not `executor.PRCreator.CreatePR(ctx,src,tgt,title,body)`. GitHub keeps the gh-CLI PR path; `runner.go:3601` excludes `SourceAdapter=="github"`. (GitLab template Step 4 is intentionally skipped.)
- **`SourceAdapter` goes empty→"github"** in the new handler — benign under current `runner.go:3601` (still routes github to gh-CLI).
- **Flag OFF ⇒ zero observable change.**

---

## Risk Register (headline items)

| Risk | Sev | Mitigation |
|---|---|---|
| Autopilot hard-coupled to concrete `*github.Client`; premature swap breaks CI/merge/release/board at compile time | **critical** | 4a touches ZERO autopilot files; autopilot deferred to 4d; consider interface-seam prerequisite PR |
| SequenceID double-prefix → `GH-GH-42` | high | New handler uses `ev.SequenceID` verbatim; explicit no-re-prefix test |
| Two pollers (SDK discovery-only + in-tree) double-dispatch if both run | high | Flag default-OFF + NOT registered ⇒ SDK poller dormant; shared `ProcessedStore` dedup |
| `core.PollerDeps` silently drops Scheduler/Judge/board/metrics | high | 4a never flips flag / registers; 4b gated on SDK option parity |
| `ResolveRepoForEvent` github branch = first real impl; wrong owner/repo → push to wrong repo | high | Strict `ev.ProjectID` match; fallback only when unambiguous; return `ErrRepoNotResolved` rather than guess; table tests |
| Stale `.claude/worktrees` copies have duplicate github importers | low | Edit only canonical `cmd/`,`internal/`; fresh worktree off origin/main |

---

## Open Questions (gate later phases)

1. ~~Will SDK v0.25.0 add the 5 missing Client methods + ProjectBoardSync runtime + …?~~ **Answered (2026-06-30): NO — v0.25.0 added only `linear CreateIssue`; the github surface is unchanged.** The dependency is now spec'd as **qf-studio/studio-sdk#71** targeting **v0.26.0** (the 5 methods + board layer + 6 `PollerDeps` options + `ExecuteGraphQLTolerant`). Gates 4b/4c/4d entirely.
2. How to augment the thin `sdkcore.IssueEvent` for github needs (issue.State for sub-issue gating, NodeID for board + OnPRCreated, per-issue owner/repo, assignee for RBAC) — grow IssueEvent in SDK, or re-fetch `*github.Issue` via `client.GetIssue` (doubles API calls)?
3. Introduce a github-client **interface seam** in autopilot as a prerequisite PR before 4d?
4. Is the board layer meant to move INTO the SDK, or stay host-side on generic `ExecuteGraphQL`? Biggest long-term gate.
5. PR-create: drop gh-CLI for an SDK-backed PRCreator (needs CreatePR + relax `runner.go:3601` + preserve `Closes #N` autoclose + dirty-worktree `--head`), or keep gh CLI indefinitely?
6. Multi-repo polling → one SDK adapter instance per repo, or wait for an SDK multi-repo story?
7. Where does the `MarkProcessed`/`ClearProcessed` side-channel live once the Start-only `core.Poller` owns dedup?
8. Is `ProcessGithubIssueEvent` worth adding in 4a given github's live path is handler-driven (not queue-driven)? Parity vs unused code.

---

## Verify

```bash
make build && go vet ./... && go test -race ./internal/orchestrator/... ./internal/adapters/sdkshim/... ./cmd/pilot/...
go test -race ./internal/adapters/github/...   # flag-off: unchanged
grep -rl 'internal/adapters/github"' --include='*.go' cmd internal | grep -v _test.go | grep -v .claude/worktrees | wc -l  # NOT yet reduced
# staging smoke with use_sdk_poller=false: issue pickup / PR / board / autopilot unchanged
```

---

## Done (Phase 4a) — closed out 2026-06-30 @ studio-sdk v0.25.0

- [x] Gates green: `go build ./...` + `go vet ./...` clean; `go test ./...` = EXIT 0 (43 pkgs, 0 fail); SDK-consuming pkgs (adapters/orchestrator/cmd) green under `-race`. _(Full `-race ./...` not re-run this session; CI covers it on the PR.)_
- [x] `ProcessGithubIssueEvent` present (`orchestrator.go:759`); `ev.SequenceID` verbatim.
- [x] `ResolveRepoForEvent` github branch implemented + tested (`sdkshim` green).
- [x] `githubPollerRegistration()` exists, default-OFF, NOT registered.
- [x] `handleGithubIssueEventSDK` sets `SourceAdapter:"github"`; legacy handler untouched.
- [~] Zero behavior change with flag off — **code-verified** (flag default false, github not registered, github adapter tests green). Live staging smoke NOT run this session.
- [x] Daemon + executor build together (`go build ./...`). No `.claude/worktrees` edits.

---

## Refs

- Tracking: #3423 · plan `.agent/research/2026-06-03-m7-sdk-cutover.md`
- Template: gitlab cutover `07467a9d` (GH-3456 / PR #3459)
- SDK: `studio-sdk v0.25.0` (`go.mod`, bumped 2026-06-30); github connector still lacks board + 5 methods + poller options
- SDK unblock spec (v0.26.0): qf-studio/studio-sdk#71 — github poll/board/PR surface for M7
- Map workflow: `m7-github-cutover-map` (run `wf_e4f5bcdc-d3c`, 6 agents, 512k tokens)

---

**Last Updated**: 2026-06-30
