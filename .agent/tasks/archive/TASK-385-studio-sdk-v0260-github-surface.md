# TASK-385: studio-sdk v0.26.0 — github poll/board/PR surface (studio-sdk#71)

**Status**: ✅ COMPLETED 2026-07-06 — **released as studio-sdk v0.27.0**; [#71](https://github.com/qf-studio/studio-sdk/issues/71) closed. PR-A [#72](https://github.com/qf-studio/studio-sdk/issues/72)→PR #74 (+607, v0.26.0); PR-B [#73](https://github.com/qf-studio/studio-sdk/issues/73)→PR #75 (+1909) — both Pilot-built, `Blocked by:` gate sequenced them live (first production exercise of the v2.214.1 fix). PR-C human-led → [PR #76](https://github.com/qf-studio/studio-sdk/pull/76) (+870/−2): six `PollerDeps` hooks, `RateLimitScheduler.QueueRetryIfRateLimited` seam (classification+task construction host-side), config-driven board wiring (`SourceEnabled`), golden re-blessed additive-only (+39/−0). Tag v0.27.0 cut manually (auto-tagger only fires on pilot-branch merges). **Pilot 4b pin: v0.27.0.**
**Created**: 2026-07-05
**Repo**: `qf-studio/studio-sdk` (work happens there; this doc is the Navigator plan)
**Unblocks**: Pilot M7 phases 4b–4d ([TASK-368](TASK-368-m7-github-cutover-phase4.md), [#3423](https://github.com/qf-studio/pilot/issues/3423))

---

## Context

**Problem**: GitHub is the last of 10 connectors still running in-tree in Pilot
because `studio-sdk@v0.25.0`'s github connector lacks the poll/board/PR surface
autopilot needs. The gap is spec'd as
[studio-sdk#71](https://github.com/qf-studio/studio-sdk/issues/71): 6 poller
hooks on `core.PollerDeps`, 5 `Client` methods, and a Projects-v2 board layer.

**Goal**: Land the surface in studio-sdk as three dependency-ordered PRs, cut
**v0.26.0**, then return to Pilot for 4b–4d.

**Research basis** (2026-07-05, both repos mapped file-by-file):
- All 5 client methods exist verbatim in Pilot's pre-extraction
  `internal/adapters/github/client.go` — direct ports (~230 LOC slice).
- Board layer = `project_source.go` (206 LOC) + `project_board.go` (306 LOC)
  in Pilot; **no SDK connector has a board pattern yet** — Pilot's code is the
  only template. Read path depends on `ExecuteGraphQLTolerant`.
- 5 of 6 poller hooks are clean interface ports; `Scheduler` is NOT — Pilot
  injects concrete `*executor.Scheduler` (host-only `executor.Task` param),
  needs a fresh slim `core` interface. `sdk/core` is golden-locked
  (`TestPublicAPILocked`, `sdk/core/api.golden`).

---

## Work Packages (1 PR each, in order)

### PR-A: Five Client methods + tolerant GraphQL (dispatchable)

Port into `sdk/integrations/github/client.go` from Pilot
`internal/adapters/github/client.go`:

| Symbol | Pilot source | Endpoint |
|---|---|---|
| `PartialGraphQLError` + `isTolerable` | `client.go:99-119` | — (NOT_FOUND/FORBIDDEN tolerable) |
| `CompareStatus` | `client.go:759-768` | `GET /repos/{o}/{r}/compare/{base}...{head}` |
| `executeGraphQLCore` (tolerant/strict flag) + `ExecuteGraphQLTolerant` | `client.go:926-1033` | `POST /graphql`, wrapped in `WithRetryVoid` |
| `SearchOpenPRsForIssue` | `client.go:1078-1108` | Search API `is:pr is:open #N`, populates `User` |
| `GetAuthenticatedUser` | `client.go:1111-1117` | `GET /user` |
| `FindOpenPRByBranch` | `client.go:1175-1192` | `GET /pulls?head={o}:{b}&state=open` (REST, not Search — avoids index lag) |

**Decisions baked in**:
- Refactor SDK's existing `ExecuteGraphQL` (`sdk/.../client.go:766-810`, currently
  raw `http.Do`, NO retry) onto the shared `executeGraphQLCore` so strict+tolerant
  share transport and both gain `WithRetryVoid` — matches Pilot semantics, fixes
  the SDK's REST-retried/GraphQL-unretried inconsistency.
- Port `executeGraphQLCore` **verbatim** — the tolerant/strict error
  classification is subtle; do not reinterpret.
- Tests: table-driven `httptest` per existing `client_test.go:44-80` pattern;
  `testutil.FakeGitHubToken` only. `sdk/integrations/github` is NOT golden-locked.

### PR-B: Projects-v2 board layer (dispatchable, `Blocked by:` PR-A's issue)

Port into `sdk/integrations/github/`:
- `project_source.go` → `ProjectBoardSource`, `FindIssuesFromProject` (uses
  `ExecuteGraphQLTolerant`; on `*PartialGraphQLError` logs dropped nodes and
  continues — load-bearing behavior).
- `project_board.go` → `ProjectBoardSync`, `UpdateProjectItemStatus`
  (strict GraphQL; nil-safe constructor when `config==nil || !Enabled`;
  non-fatal returns for disabled/unmapped; idempotent column check).
- Shared package-level `resolveProjectID` (port once — used by both).
- Poller options `WithProjectBoardSource` / `WithBoardSync` mirroring Pilot
  `poller.go:360-373`.
- Config types `ProjectBoardConfig`/`ProjectStatuses` already exist in SDK
  `types.go:36-56` — reuse, don't duplicate.
- Tests modeled on Pilot's `project_source_test.go`/`project_board_test.go`
  (597+615 LOC reference), rewritten against the SDK client.

### PR-C: `core.PollerDeps` hook parity + golden re-bless (human-led)

- Add to `sdk/core` (near `ActiveExecutionLister`, `registry.go:112-120`):
  `TaskChecker`, `ExecutionChecker`, `PreFlightJudger` (+ `Verdict` value type),
  `ExecutionSaver`, `IssueMetricsRecorder` — signatures verbatim from Pilot
  `poller.go:47-91`.
- **Design fresh**: slim `core.RateLimitScheduler` seam (Pilot's `Scheduler` is
  concrete `*executor.Scheduler`; `QueueTask(*executor.Task, *RateLimitInfo)`
  cannot cross into the SDK). Needs a small `core.RateLimitInfo`
  (mirror `internal/executor/ratelimit.go:11-15`: ResetTime/Timezone/RawError)
  and a method shape decided against the SDK poller's dispatch loop.
- Extend `PollerDeps` (`registry.go:124-134`) with the 6 hooks (+ board wiring),
  bridge in `adapter.NewPoller` (`adapter.go:93-111`), matching `With*` options
  in the SDK github poller (pattern `poller.go:88-171`).
- Re-bless golden: `go test ./sdk/core -run TestPublicAPILocked -update`,
  commit `api.golden`. Treat as external contract change — coordinate with
  Pilot 4b consumption (`cmd/pilot/poller_github.go:61-84` is the literal
  downstream diff target).
- Human-led because: golden-locked contract + one real design decision +
  cross-repo coordination. Everything else in A/B is mechanical porting.

### Then: cut v0.26.0

Releases on studio-sdk are daemon-automated per merged PR (conventional
commits); verify the tag after PR-C merges, update studio-sdk#71 checkboxes,
close it. (The manual-tag SOP in studio-sdk `.agent/sops/deployment/` is stale —
flagged by research.)

---

## Acceptance Criteria

- [ ] PR-A: 5 methods + `PartialGraphQLError` on SDK github `Client`, tests
      green, `ExecuteGraphQL` retry unified via shared core.
- [ ] PR-B: `ProjectBoardSource` + `ProjectBoardSync` + 2 poller options in
      SDK, tests green.
- [ ] PR-C: `PollerDeps` exposes the 6 hooks + board wiring, `api.golden`
      re-blessed deliberately, no drift on other connectors.
- [ ] studio-sdk tagged v0.26.0; #71 closed.
- [ ] Pilot TASK-368 unblocked → 4b next.

## Out of Scope

- Pilot-side 4b–4d (flag flip, autopilot client unification, deleting
  `internal/adapters/github`) — TASK-368, after v0.26.0.
- Autopilot's hard-typed `*github.Client` interface seam — Phase 4d.
- M8 bot template; SDK v1.0.0 stabilization.

## Refs

- SDK spec: https://github.com/qf-studio/studio-sdk/issues/71
- Pilot tracker: [#3423](https://github.com/qf-studio/pilot/issues/3423) / TASK-368
- Port sources: Pilot `internal/adapters/github/{client.go,project_source.go,project_board.go,poller.go}`
- SDK insertion points: `sdk/core/registry.go:112-134`, `sdk/integrations/github/{adapter.go:73-111,poller.go:88-171,client.go}`

---

**Last Updated**: 2026-07-06 (completed; archived)
