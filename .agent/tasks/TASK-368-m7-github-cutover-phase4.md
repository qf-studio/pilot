---
status: phase-4a-implemented
priority: P3
created: 2026-06-22
execution: manual
github_issue: 3423
labels: [m7, sdk, github, adapter, human-led]
---

# TASK-368: M7 Phase 4 — GitHub adapter → studio-sdk cutover (MANUAL)

**Status**: ✅ Phase 4a IMPLEMENTED (additive scaffolding, dormant/flag-off). Verdict `poll-path-only`; full retirement blocked on studio-sdk v0.25.0+. Adversarially reviewed (verdict SHIP, 5 nits fixed). Phases 4b–4d deferred.
**Assignee**: Manual (human-led per #3423 — daemon must NOT claim this)
**Tracking issue**: https://github.com/qf-studio/pilot/issues/3423
**Template**: GitLab cutover — commit `07467a9d` / GH-3456 / PR #3459

---

## Verdict (verified against studio-sdk v0.24.0)

**Full retirement of `internal/adapters/github` is NOT possible at SDK v0.24.0.** Only an
additive, feature-flagged *poll-path scaffolding* leg is achievable now; the live cutover
is gated on SDK v0.25.0+. Four independently-verified blockers:

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

## Acceptance Criteria (Phase 4a only)

- [ ] `internal/orchestrator/orchestrator.go` gains `ProcessGithubIssueEvent(ctx, ev sdkcore.IssueEvent, projectPath string) error` mirroring `ProcessGitlabIssueEvent` (`:476`), placed after `ProcessAzureDevOpsIssueEvent` (`:717`). Uses `ev.SequenceID` **verbatim** (already `GH-`-prefixed by SDK `adapter.go:143` — NO `fmt.Sprintf("GH-%d", …)` re-prefix) and `sdkshim.PriorityFromSDK(ev.Priority)`.
- [ ] `internal/adapters/sdkshim/repo_resolver.go` implements the `github` branch at the `TODO(phase-4)` (`:39`): per-project routing via `cfg.Projects[].GitHub.{Owner,Repo}` matched on `ev.ProjectID`, `cfg.Adapters.GitHub` fallback; `ErrRepoNotResolved` only when nothing matches. (This is the resolver's first real implementation — all branches are currently stubs.)
- [ ] `cmd/pilot/poller_github.go` (NEW) defines `githubPollerRegistration()` gated on a NEW default-OFF flag `adapters.github.use_sdk_poller`; **NOT** added to `adapterPollerRegistrations()` (`cmd/pilot/poller_registry.go:43`).
- [ ] `handleGithubIssueEventSDK(...)` (NEW) lives ALONGSIDE the legacy `handleGitHubIssueWithResult` (`cmd/pilot/handlers.go:222`); sets `SourceAdapter:"github"`, `taskID = ev.SequenceID` verbatim, tolerates `ErrRepoNotResolved`. Legacy handler untouched.
- [ ] `internal/config/config.go`: additive `UseSDKPoller bool` (`use_sdk_poller`, default false) on the GitHub adapter config; documented in `configs/pilot.example.yaml`.
- [ ] With the flag OFF, github polling/PR/board/autopilot behavior is **byte-identical** to today; existing `internal/adapters/github` tests stay green.
- [ ] No edits under `.claude/worktrees`.

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

- **4b** — SDK poller option parity (Scheduler/TaskChecker/ExecutionChecker/PreFlightJudge/ExecutionSaver/IssueMetricsRecorder) + flip flag on for single-repo. Blocked on SDK v0.25.0+.
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

1. Will SDK v0.25.0+ add the 5 missing Client methods + ProjectBoardSync runtime + `LabelPilot` + exported `ParseParentIssueNumber`? Gates 4b/4c/4d entirely.
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

## Done (Phase 4a)

- [ ] All gates green; `go test -race ./...` passes.
- [ ] `ProcessGithubIssueEvent` present + tested; `ev.SequenceID` verbatim.
- [ ] `ResolveRepoForEvent` github branch implemented + tested.
- [ ] `githubPollerRegistration()` exists, default-OFF, NOT registered.
- [ ] `handleGithubIssueEventSDK` sets `SourceAdapter:"github"`; legacy handler untouched.
- [ ] Zero behavior change with flag off (staging smoke).
- [ ] Daemon + executor build/roll together. No `.claude/worktrees` edits.

---

## Refs

- Tracking: #3423 · plan `.agent/research/2026-06-03-m7-sdk-cutover.md`
- Template: gitlab cutover `07467a9d` (GH-3456 / PR #3459)
- SDK: `studio-sdk v0.24.0` (`go.mod`); github connector lacks board + 5 methods + poller options
- Map workflow: `m7-github-cutover-map` (run `wf_e4f5bcdc-d3c`, 6 agents, 512k tokens)

---

**Last Updated**: 2026-06-22
