---
status: shipped
priority: P1
created: 2026-07-10
execution: pilot (W1/W2) + manual (W3)
github_issue: 3423
labels: [m7, sdk, cleanup, close-out]
---

# TASK-397: M7 close-out — final dead-code deletion + #3423 milestone closure

**Status**: ✅ **SHIPPED 2026-07-11.** W1 [#4189](https://github.com/qf-studio/pilot/issues/4189)→PR #4192 ✅ · W2 [#4190](https://github.com/qf-studio/pilot/issues/4190) (children #4194/#4196/#4195 → PRs #4197/#4196/#4198) ✅ CLOSED COMPLETED. W3 done: [#3423](https://github.com/qf-studio/pilot/issues/3423) close-out comment posted + milestone CLOSED; docs truthfulness fixed (`architecture.mdx` path A/B callout, `github.mdx` polling-primary callout). M7 milestone complete.

**Goal**: Ship the last two genuinely-dead deletion batches found by the final-mile
audit, record the accepted-residual policy, and close #3423.

**Research basis**: two `navigator-research` agent reports, 2026-07-10 (verified
against origin/main @ `2d381fa4`): (a) path-B disposition map, (b) 10-adapter
residual-importer audit + SDK v0.30.0 equivalent matrix.

## Audit verdicts (evidence in agent reports; spot refs below)

| Surface | Verdict | Evidence |
|---|---|---|
| Path B `internal/pilot.Pilot` + `internal/orchestrator` (~5.2k LOC, 15 files) | **KEEP — accepted residual.** Retirement = separate future initiative | Sole code path for ALL webhook-only deployments (Linear/Jira/GitLab/AzDO/Asana/Plane + GitHub webhook-mode; mode gate `cmd/pilot/main.go:390-444`, construct `:913`). Jira has NO SDK ingestion path (orchestrator `ProcessJiraIssueEvent` is dormant/uncalled). No SDK webhook→`core.IssueEvent` bridge exists (`toIssueEvent` unexported). Docs present it undeprecated (`docs/content/integrations/github.mdx:124-146`). Feature commit `4ec05247` 2026-06-26. `internal/orchestrator` has exactly ONE production importer: `internal/pilot/pilot.go:29` |
| `internal/adapters/github/poller.go` (~2.4k LOC) | **DELETE (W1)** — SOP GH-4169 misclassified it as live | Zero production callers of `github.NewPoller`/all 8 `With*` options; only tethers are test files: in-package `gh3819_cross_project_test.go` + `stress/concurrent_test.go` + `stress/memory_test.go`. Live poller is SDK (`poller_github.go:407`) |
| `internal/adapters/github/project_board.go` | **DELETE (W1)** | Zero callers of in-tree `NewProjectBoardSync`/`ProjectBoardSync`; write path is SDK (`main.go:637`, `:1450`; `autopilot/controller.go:18` imports SDK github). `ProjectBoardConfig` type lives in `types.go`, unaffected |
| `internal/adapters/registry.go` + `internal/adapters/jira/adapter.go` | **DELETE (W2)** — abandoned phase-9 scaffold | `adapters.Register/Get/All` + `jira.NewAdapter`/`JiraAdapter`: zero callers repo-wide incl. tests. Real dispatch is `cmd/pilot/poller_registry.go` (unrelated same-vocabulary types). SDK `sdk/core/registry.go` equally unused — neither is the live mechanism |
| Orchestrator dormant `ProcessGithubIssueEvent`/`ProcessGitlabIssueEvent`/`ProcessJiraIssueEvent` | **DELETE (W2, rider)** — M7 scaffolding orphans | Uncalled in production (live SDK path `handlers.go` bypasses orchestrator); only their own package tests exercise them (`orchestrator.go:476,548,759`) |
| GitHub kept files `cleanup.go`/`merger.go`/`issue_creator.go`+`issue_create.go`/`retry.go` | **KEEP — accepted residual** | Live callers: `main.go:2397` (stale-label cleaner), `:2354` (merge-waiter), `:1849` (bot intake); `retry.go` is `client.go`'s in-package dep. SDK equivalents exist (byte-compatible `NewCleaner`/`NewMergeWaiter`, `CreateIssue` primitive) — migrate opportunistically only when `client.go`'s live callers (path B, preflight, notifier) ever move |
| Config TYPES + HELPER converters, all 10 adapters | **KEEP — the GitLab-precedent residual, working as designed** | `ExtractMRNumber`, `ParseParentIssueNumber`, `ExtractAcceptanceCriteria`, `ConvertIssueToTask`, `VerifyLinearSignature`, preflight verify clients, `skipreason/` (live Prometheus label vocabulary) |
| Telegram/Slack outbound-notify transport (in-tree `telegram.Client`/`slack.Client` in `alerts/channels.go`, `briefs/delivery.go`, `autopilot/telegram_notifier.go`) | **OUT OF M7 SCOPE — new finding, future-milestone candidate** | M7 targeted poll/chat paths only; notify transport was never scoped. Split state: SDK bridge for poll/chat, in-tree for outbound sends |
| Linear live `linear.NewClient` sub-issue creator (`cmd/pilot/handlers.go:155`, via `runner.SetSubIssueCreator`) | **KEEP — note in close-out** so future audits don't miss it | Genuine non-path-B machinery dependency, unique to linear |

## Work items

### W1 (Pilot issue): delete `github/poller.go` + `project_board.go` + retire their test tethers
- Delete `internal/adapters/github/poller.go`, `project_board.go`,
  `gh3819_cross_project_test.go`.
- `stress/concurrent_test.go` + `stress/memory_test.go`: they stress the DELETED
  in-tree poll loop — retire the in-tree-poller constructions. Before deleting
  outright, check whether any test in the stress package covers behavior NOT owned
  by the deleted poller (if a case exercises shared components, keep/rehome it).
  Concurrent-dispatch coverage for the SDK poller is upstream's concern
  (studio-sdk) — do NOT write a new SDK stress harness here; if a gap is found,
  note it in the PR body as an upstream ask (mem-087 pattern).
- `merger.go` keeps its production caller (`main.go:2354`); its `poller.go:450`
  internal caller disappears — do not delete merger.go.
- **Spec discipline (SOP `sops/integrations/github-poller-dead-code-cascade-gh4169.md`)**:
  grep-audit every symbol before delete; refuse any file with a live caller.
- Gates: `go build ./... && go vet ./... && go test -race ./...` (full, incl.
  remaining stress gate); grep-gate: `github.NewPoller|github.NewProjectBoardSync`
  zero hits outside deleted files.

### W2 (Pilot issue): delete abandoned registry scaffold + orchestrator scaffolding orphans
- Delete `internal/adapters/registry.go` (+ its test if any),
  `internal/adapters/jira/adapter.go` (+ `adapter_test.go` if present).
- Delete `internal/orchestrator/orchestrator.go` methods
  `ProcessGithubIssueEvent`, `ProcessGitlabIssueEvent`, `ProcessJiraIssueEvent`
  + their dedicated test files (`orchestrator_github_test.go`,
  `orchestrator_gitlab_test.go`, `orchestrator_jira_test.go`) — keep everything
  path B actually calls (`ProcessTicket`, `ProcessGithubTicket`,
  `ProcessJiraTicket`, `ProcessAsanaTicket`, `ProcessPlaneTicket`,
  `ProcessGitlabTicket`) and their tests.
- Same grep-audit discipline + full `-race` gates.

### W3 (manual, this session after W1/W2 merge): milestone closure
1. #3423 close-out comment: per-phase ship record (1–8 poll/chat cutovers, 4d
   github endgame), accepted-residual policy table (above), path-B keep decision
   + retirement preconditions (SDK webhook bridge, gateway-only deployment
   census, Jira SDK path), telegram/slack notify-split + linear sub-issue-creator
   notes for future audits. Close #3423.
2. Docs truthfulness (small PR or fold into W2): `docs/content/concepts/architecture.mdx`
   still depicts `internal/pilot` as the central orchestration hub — annotate
   path A (SDK pollers/dispatcher, production) vs path B (gateway webhook-only,
   legacy); `docs/content/integrations/github.mdx` webhook-mode section gets a
   "polling is the primary/recommended mode" note.
3. Navigator: TASK-368 → archive; this doc → archive; README M7 row → closed;
   graph memories: decision `path-b-keep-2026-07-10`, decision
   `m7-residual-policy`, pitfall `sop-gh4169-poller-misclassified` (SOP listed
   poller.go as live; its callers were test-only — audit callers' liveness, not
   just existence).

## Out of scope
- Path B retirement / SDK webhook bridge (future initiative, preconditions above).
- Telegram/Slack outbound-notify SDK migration (future milestone candidate; needs
  its own decision first).
- Migrating the 4 live-caller GitHub files to SDK equivalents (opportunistic,
  not milestone-blocking).

## Risks
| Risk | Sev | Mitigation |
|---|---|---|
| Stress tests guard a risk class with no other coverage | med | W1 instructs case-by-case audit before delete + upstream-ask note, not silent drop |
| Repeat of 4d.6 over-listed-dead-files | low | Specs mandate grep-audit + refusal discipline (SOP GH-4169); lists here are already grep-verified against origin/main |
| W2 orchestrator edit destabilizes path B | low | Only dormant methods + their dedicated tests removed; path-B-called methods explicitly enumerated as keep |

## Refs
- Tracking: #3423 · TASK-368 (phase history) · SOP `sops/integrations/github-poller-dead-code-cascade-gh4169.md`
- Research: origin/main @ `2d381fa4`, agent-mapped 2026-07-10
- Pilot issues: W1 [#4189](https://github.com/qf-studio/pilot/issues/4189) · W2 [#4190](https://github.com/qf-studio/pilot/issues/4190) (`Blocked by: #4189`)
