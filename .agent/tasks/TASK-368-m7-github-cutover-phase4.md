---
status: phase-4d2-shipped
priority: P3
created: 2026-06-22
sdk_version: v0.30.0
execution: manual
github_issue: 3423
labels: [m7, sdk, github, adapter, human-led]
---

# TASK-368: M7 Phase 4 — GitHub adapter → studio-sdk cutover (MANUAL)

**Status**: 🚀 Phases **4b + 4d.1 + 4d.2 + 4d.3 + 4d.4 SHIPPED**, studio-sdk pinned **v0.30.0**. **4d.2 (per-repo SDK poller fan-out) SHIPPED 2026-07-09**: 4d.2a cross-poller reachability (#4110 → PR #4114, GH-4110 registry) + 4d.2b/c/d fan-out (PR #4115, `githubSDKPollerTargets` + explicit repo-identity handler + main.go mutual-exclusion/gate) → **released v2.236.0**. `use_sdk_poller` now drives the default repo AND every `projects[]` github repo. **Endgame planned 2026-07-09 (§ "Phase 4d.5 + 4d.6" below): 4d.2e live rollout (human-owned), 4d.5 webhook repoint (Pilot), 4d.6 cleanup (Pilot, gated on 4d.2e + 4d.5). Delete-vs-residual DECIDED: GitLab precedent — delete dead machinery, keep live remnants.**

<details><summary>Prior status (4b/4d.1/4d.3/4d.4, pre-4d.2)</summary>

🚀 Phases **4b + 4d.1 + 4d.3 + 4d.4 SHIPPED 2026-07-06**, studio-sdk pinned **v0.29.0**. **`use_sdk_poller` live trial VERIFIED end-to-end 2026-07-06** (poll → dispatch → spec-guard → veto/self-correct → SDK PR-create → CI → autopilot merge → auto-release); two live incidents found + fixed same day: **#3919** (SDK poller fail-loud token — `resolveGitHubToken` chain + `verifySDKGithubToken` startup check, v2.220.1; config token literal reverted to `""` 2026-07-07) and **#3922** (cross-repo PR-state collision — `repo` column + per-repo `RestoreState`, direct 4d.2 groundwork; commit `29ebeb0f` references GH-3903). SDK Logger gap closed: studio-sdk#79 (`PollerDeps.Logger`) → **v0.29.0**, consumed via pilot#3921 (bump + component-tagged logger; label-as-trigger pattern, mem-087). SDK-path defects RESOLVED: #4021 ✅ (auto-retry guard) · **#4050 ✅ FIXED (PR #4085, v2.235.4)** — `handleGithubIssueEventSDK` had dropped `task.Labels`+`State`+MemberID/AcceptanceCriteria/FromPR (pitfall mem-089). Fix verified live: GH-3994 retry-3 honored `no-decompose` → clean single-task PR #4096.

</details>

**Assignee**: Manual (human-led per #3423 — daemon must NOT claim this)
**Tracking issue**: https://github.com/qf-studio/pilot/issues/3423
**Template**: GitLab cutover — commit `07467a9d` / GH-3456 / PR #3459

## Shipped history (condensed — details in git log / PRs)

- **4a** (2026-06-30, v0.25.0): dormant scaffolding — `ProcessGithubIssueEvent`, `resolveGithubRepo`, `githubPollerRegistration()` (default-OFF), `handleGithubIssueEventSDK`, `UseSDKPoller` flag.
- **4b** (PR #3890, v0.27.0): SDK poller live behind `use_sdk_poller` for the DEFAULT repo — full `PollerDeps` hooks + `sdkRateLimitScheduler` seam; registered in `adapterPollerRegistrations()`; mutual exclusion in gateway + standalone blocks; `projects:` stay in-tree.
- **4d.1** (PR #3894, v0.28.0→v0.28.1): autopilot fully on the SDK client — concrete-type swap, `internal/ghissue` package, `apGHClient` at all 3 controller construction sites, release CLI swapped. Forced upstream v0.28.0 (typed errors, 4 methods) + v0.28.1 (exhaustive `GetTagForSHA` pagination — see `pitfalls/pitfall_sdk_ports_go_stale_vs_intree.md`).
- **4d.3** (PR #3895): spec-guard on SDK path — `ghissue.ValidateSpec` (marker byte-identical to legacy; rule changes must land in BOTH copies until 4d.6) + `applySpecGuardSDK` two-strike gate.
- **4d.4** (PR #3896): SDK PR-create — `sdkshim.GitHubPRCreator` + runner `RegisterPRCreator` per-repo registry keyed `adapter:owner/repo`; gh CLI remains fallback.
- 4c subsumed by v0.27.0 (SDK board layer; in-tree `project_source.go`/`project_board.go` retire with 4d.6).

---

# Phase 4d.2 — projects-loop → per-repo SDK pollers (PLANNED 2026-07-08)

## Context

With #4050 fixed and #3922 (repo-scoped PR state) shipped, the SDK poll path has field
parity and multi-repo-safe state. The default repo (`adapters.github.repo`) polls via
SDK; the `projects:` multi-repo loop still creates in-tree pollers **unconditionally**
(`main.go:2607-2634`, loop over `createPollerForRepo` at `:2434-2579`). 4d.2 moves those
to per-repo SDK poller instances, after which only webhook (4d.5) and cleanup (4d.6)
remain. All line refs below are `origin/main @ 6ab2ae53` — **the repo-root working tree
is stale (v2.220.0); base the worktree on `origin/main`, verify refs there.**

### Current-state facts (code-verified 2026-07-08)

- `githubPollerRegistration()` (`cmd/pilot/poller_github.go:125-343`) is single-repo:
  one `githubSDK.New(cfg).NewPoller(deps)` for `ghCfg.Repo`; poller handle never leaves
  the `CreateAndStart` closure.
- `deps.AutopilotControllers map[string]*autopilot.Controller` is **already plumbed**
  through `PollerDeps` in standalone mode (`poller_registry.go:33`, `main.go:2840`) but
  unread — the registration wires `OnPRCreated`/`IssueMetricsRecorder` from the singular
  default `deps.AutopilotController` (`poller_github.go:212-220`).
- Per-project autopilot controllers already exist (`main.go:1731-1774`, keyed
  `owner/repo`), shared `StateStore` + per-controller `RestoreState()` scoped by
  `repoKey()` (#3922). `AggregateMetrics` sums all controllers (#4068).
- SDK side (v0.29.0): `githubSDK.Config` is one-repo (`types.go:5-16`); N repos = N
  adapter/poller instances. Pilot never touches the SDK global registry (name-collision
  limit irrelevant). `core.ProcessedStore` is repo-scoped by contract
  (`registry.go:101-111`) and each SDK poller self-scopes via its own `repoKey()` —
  one shared `*autopilot.StateStore` safely backs N instances.
- In-tree per-repo poller deps that must carry over per instance: own
  `rateLimitScheduler` (`main.go:2508-2544`), `ExecutionChecker(store, projPath)`
  (`:2473-2475`), `IssueMetricsRecorder(controller.Metrics())`, `OnPRCreated`,
  `TaskChecker`, `PreFlightJudge`/`ExecutionSaver`, `ProcessedStore`.
- Board source/sync is default-repo-only even in-tree (`main.go:2547-2555`) — parity
  means project-repo SDK pollers get NO board wiring.
- Gateway mode has zero multi-repo support today (single-repo block ~`main.go:860-935`)
  — out of scope; standalone/dashboard mode is the only `projects:` path.

### ⚠️ Latent live defect found during planning (verify FIRST)

`runner.SetSubIssuePollerSkip` (GH-3240 cross-poller sub-issue skip) and the stale-label
`ClearProcessed` recovery loops iterate only the in-tree `ghPollers` slice
(`main.go:~2722-2787`). Since 2026-07-06 the pilot repo polls via SDK — **those loops
reach no poller for the default repo**. SDK poller exports `ClearProcessed`/`IsProcessed`
but has NO exported `MarkProcessed` (only unexported, `sdk poller.go:1510-1538`), and the
instance isn't exposed anyway. Consequence: sub-issues created by the runner on the pilot
repo may be dispatched by the SDK poller without the intended skip-marking, and
stale-label recovery can't clear SDK-poller dedup. **Step 0 below: confirm against daemon
logs whether this has bitten since 07-06; if live-broken, 4d.2a ships as an expedited
standalone fix.**

## Known Pitfalls & Patterns (graph recall)

- **PITFALL mem-089 (95%)**: SDK handler dropped task fields legacy handler sets → label
  gates no-op'd fleet-wide. *Applied:* per-repo handler closure (Step 3) must delegate to
  the one existing `handleGithubIssueEventSDK` body — no second field-mapping copy;
  parity test asserts every `InternalTask` field against the legacy handler's mapping.
- **PITFALL mem-048 (95%)**: never add GitHub API calls inside the poll/dispatch cycle —
  stress fakes serve only the issues-LIST route. *Applied:* fan-out adds N pollers but
  zero new per-issue API calls; the existing dispatch-time `GetIssue` (shipped in #4085)
  stays the only extra call, and the stress gate must stay green with N>1 pollers.
- **PATTERN mem-087**: upstream SDK gaps close via label-as-trigger studio-sdk PR +
  release + pin bump (precedent: studio-sdk#79 Logger → v0.29.0). *Applied:* 4d.2a.
- **LEARNING mem-079**: spec-guard two-strike gate semantics — unchanged here; guard
  already runs per-event in the shared handler body.

## Design Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Fan-out structure | (A) loop inside `CreateAndStart`; (B) registry returns `[]PollerRegistration` per repo | **A** | Registry contract untouched; mirrors `createPollerForRepo`; one place owns github SDK wiring |
| Opt-in granularity | (A) adapter-level `use_sdk_poller` governs default + all `projects:`; (B) per-project override flag | **A** (recommended) | SDK path is field-parity-complete + live-verified; project repos are quiet (trains); per-project flag is throwaway scaffolding 4d.6 would delete. Revisit to (B) only if Step 0 finds live damage |
| Repo identity → handler | (A) per-repo handler closure carrying owner/repo; (B) rely on `resolveGithubRepo` name-match | **A** | Kills the documented name-only ambiguity (`repo_resolver.go:64-68`) without an upstream `IssueEvent.ProjectID` change; resolver remains as webhook/fallback path |
| Sub-issue skip + stale-label reach | (A) upstream export `MarkProcessed` + pilot-side poller-handle registry; (B) side-channel writes to shared `ProcessedStore` | **A** | SDK poller's in-memory dedup is authoritative at runtime — store writes don't reach a running poller; (B) is silently wrong |
| Board sync for project repos | wire / don't wire | **Don't** | Parity: in-tree gates board to the default repo (`main.go:2547-2555`); board-per-project is a 4d.6+ question |

## Acceptance Criteria

- [ ] `use_sdk_poller=true` ⇒ SDK pollers run for the default repo AND every
  `cfg.Projects[]` entry with `GitHub != nil`; zero in-tree github pollers created.
- [ ] `use_sdk_poller=false` ⇒ byte-identical behavior to today (in-tree everything);
  `go test -race ./internal/adapters/github/...` green.
- [ ] Each SDK poller instance gets: own scheduler, `OnPRCreated` +
  `IssueMetricsRecorder` from `deps.AutopilotControllers[owner/repo]`, per-repo
  `ExecutionChecker(store, proj.Path)` + `ProjectPath`, `RegisterPRCreator("github:owner/repo")`.
- [ ] Per-repo handler closure threads owner/repo explicitly; `githubIssueURL` uses the
  event's resolved repo, not `cfg.Adapters.GitHub.Repo` (`handlers.go:1342-1346` fixed).
- [ ] `SetSubIssuePollerSkip` + stale-label `ClearProcessed` loops reach SDK pollers
  (via exported handles + upstream `MarkProcessed`), covered by tests.
- [ ] No new API calls inside any poll cycle (mem-048); stress gate green.
- [ ] Task-field parity vs legacy handler asserted by test (mem-089 regression fence).
- [ ] Live smoke: canary-labeled issue on ONE quiet project repo dispatches via SDK
  poller → PR → autopilot merge; pilot repo unaffected; per-controller metrics visible
  in the aggregate.

## Implementation (sub-phases, each a gate)

### Step 0 — verify the latent sub-issue-skip defect ✅ DONE 2026-07-08 — **CONFIRMED LIVE-BROKEN**
Daemon-log forensics (2026-07-08 interactive session): epic **GH-3927** created children
3952/3953/3954 at 07:53 on 07-07 and began sequential execution of 3952 at 07:53:16;
the SDK poller independently dispatched **all three** for parallel execution 12–46s later
(3953 @07:53:28, 3952 @07:53:42, 3954 @07:54:02; 3954 again @10:06). Untagged dispatch
lines attributed to the SDK poller by timeline: component-tagged `github-sdk-poller`
logging only began 07-07T15:29 (pilot#3921 deploy); before that the SDK poller logged
untagged, and no in-tree pilot-repo poller existed (flag on). **Concurrent duplicate
execution proven:** GH-3954 worktree @09:31:52 (poller dispatch) vs @09:35:13 (sequential
epic executor). Downstream guards (ExecutionChecker, scope-overlap deferral) contained
the damage — children eventually shipped — but wasted duplicate runs occurred.
**Verdict: 4d.2a ships expedited + standalone** (upstream `MarkProcessed` export +
pilot-side SDK-poller handle wiring for the GH-3240 skip and GH-3271 `SetOnIssueDone`
loops). Interim mitigation available: `use_sdk_poller=false` reverts pilot repo to the
in-tree poller with working skip-marking (user decision — trades back all 4b+ SDK-path
gains).

### 4d.2a — export `MarkProcessed` + pilot wiring — ✅ SHIPPED 2026-07-09
Upstream **studio-sdk#81** merged (PR #82) → cut **studio-sdk v0.30.0** (tag at
`04040490`, 1 additive commit; SDK releases are bare tags). Pilot pin bumped v0.29.0
→ **v0.30.0**. Pilot-side wiring **pilot#4110** (human-led, NOT `pilot`-labeled) →
**PR #4114**: new repo-keyed `githubPollerRegistry` (both in-tree + SDK pollers register;
SDK poller adds itself from `CreateAndStart` via a `githubProcessedMarker` assertion,
fail-loud if the SDK ever drops the surface); sub-issue-skip + `SetOnIssueDone` +
stale-label `ClearProcessed` loops now route through it, reaching the SDK poller and
**repo-scoped** (`SubIssuePollerSkipFn` now carries `ParentTask.SourceRepo`; per-controller
key on done; default-repo on cleaner). Fixes the confirmed GH-3927 duplicate-child
dispatch AND the latent cross-repo issue-number collision. Stale-label `len(ghPollers)>0`
guard dropped (registry is a no-op when unregistered → covers flag-on/no-projects too).
All gates green incl. full `go test -race ./...`. Lands BEFORE the 4d.2b fan-out.
**PR #4114 auto-merged** (squash `379c274b`, 2026-07-08 22:15Z) via stage autopilot
(user-approved); **#4110 closed COMPLETED**; post-merge CI green; release tag pending
the daemon's on-merge release step.
**Gate:** ✅ SDK v0.30.0 published · ✅ pilot builds against pin · ✅ #4110 merged.

### 4d.2b — per-repo fan-out in `githubPollerRegistration()`
Rework `CreateAndStart` (`poller_github.go:125-343`): derive repo set = default repo +
`cfg.Projects[]` GitHub entries; loop constructing per-repo `githubSDK.Config` +
`sdkcore.PollerDeps` (per-repo scheduler / controller lookup from
`deps.AutopilotControllers` with fail-loud log when a controller is missing / per-repo
`ProjectPath`+`ExecutionChecker` / `RegisterPRCreator`). Collect poller handles into an
exported registry (e.g. `sdkGHPollers` returned via deps or accessor) for main.go loops.
Mutual exclusion: standalone `projects:` loop (`main.go:2607-2634`) skips in-tree poller
creation entirely when the flag is on; drop the now-dead `polledRepos` special-case at
`:2586-2604` only if it falls out naturally.
**Gate:** `go build ./... && go vet ./... && go test -race ./cmd/pilot/...` + new
table-driven tests (repo-set derivation, mutual exclusion, per-repo dep wiring).

### 4d.2c — handler repo-identity fixes
Per-repo closure `handleGithubIssueEventSDKForRepo(owner, repo)` delegating to the
existing body (single field-mapping copy, mem-089); fix `githubIssueURL`
(`handlers.go:1342-1346`) to take resolved owner/repo; `task.SourceRepo` always set from
the closure (removes the silent gh-CLI fallback on resolution miss, `handlers.go:1284-1288`).
`resolveGithubRepo` stays for the 4d.5 webhook path.
**Gate:** `go test -race ./cmd/pilot/... ./internal/adapters/sdkshim/...` + field-parity test.

### 4d.2d — main.go loop reach + docs
`SetSubIssuePollerSkip` + stale-label `ClearProcessed` loops (`main.go:~2722-2787`)
iterate SDK poller handles alongside `ghPollers` (using 4d.2a `MarkProcessed`). Update
`configs/pilot.example.yaml:85-92` (flag now covers `projects:`; remove "stay on the
in-tree poller until Phase 4d" caveat) + stale doc comment `github/types.go:17-22`.
**Gate:** full `make build && go vet ./... && go test -race ./...` + stress gate.

### 4d.2e — live rollout + smoke
Deploy; restart daemon; confirm N pollers in component-tagged logs
(`github-sdk-poller`); canary issue on one quiet train repo (e.g. canary sandbox);
verify dispatch → PR → merge; confirm pilot-repo epics still skip-mark sub-issues;
check aggregate metrics show per-controller activity.
**Gate:** smoke green; marker/README updated.

## Out of Scope

- 4d.5 webhook repoint; 4d.6 in-tree deletion / residual decision.
- Gateway-mode multi-repo polling (doesn't exist in-tree today either).
- Per-project board sync, per-project tokens, per-project polling intervals (no config
  schema exists; deliberate non-goals until a concrete need).
- Growing `sdkcore.IssueEvent.ProjectID` to owner/repo upstream (obviated by the
  per-repo handler closure).

## Risks

| Risk | Sev | Mitigation |
|---|---|---|
| Sub-issue skip gap is ALREADY live-broken on pilot repo | high | Step 0 first; expedite 4d.2a standalone if confirmed |
| All-repos-at-once flag flip repeats a #4050-style fleet-wide regression | high | Field parity now fenced by test (mem-089); repos are quiet; smoke on one repo before trusting the rest; rollback = flag off (in-tree path untouched until 4d.6) |
| N pollers × shared token → rate-limit pressure | med | Per-repo `sdkRateLimitScheduler` already throttles; quiet repos poll cheaply; watch `github-sdk-poller` logs post-deploy |
| Missing controller for a `projects:` entry (map miss) → nil deref | med | Fail-loud log + skip repo (mirror in-tree `:2456-2462` lookup pattern) |
| Stress suite deadlock from added routes | med | mem-048: no new poll-cycle API calls; N pollers all hit issues-LIST only |

## Verify

```bash
make build && go vet ./... && go test -race ./cmd/pilot/... ./internal/adapters/sdkshim/... ./internal/adapters/github/...
go test -race ./...            # full, incl. stress gate
# flag-off parity: in-tree tests green, no SDK pollers in logs
# live: N "github-sdk-poller" component logs; canary issue on quiet repo → PR → merge
```

## Done

- [ ] All gates green; PRs merged (upstream SDK release + pilot cutover PR(s), daemon +
  executor rolled together per #3423).
- [ ] Live smoke passed on ≥1 project repo + pilot repo epic sub-issue skip confirmed.
- [ ] `configs/pilot.example.yaml` + `types.go` doc comment updated.
- [ ] TASK-368 status header updated → `phase-4d2-shipped`; README Current State synced.

## Refs

- Tracking: #3423 · plan `.agent/research/2026-06-03-m7-sdk-cutover.md`
- Template: gitlab cutover `07467a9d` (GH-3456 / PR #3459)
- Groundwork: #3922 (repo-scoped PR state) · #4050/PR #4085 (field parity, mem-089) ·
  #4068/PR #4081 (multi-controller metrics aggregate)
- SDK: studio-sdk v0.29.0 pinned; 4d.2a upstream ask = export `MarkProcessed`
- Research: origin/main @ `6ab2ae53`, agent-mapped 2026-07-08 (projects-loop
  `main.go:2434-2634`, registration `poller_github.go:125-343`, handler
  `handlers.go:1196-1346`, state `state_store.go`, SDK `registry.go`/`poller.go`)

---

# Phase 4d.5 + 4d.6 — webhook repoint + in-tree cleanup (PLANNED + DISPATCHED 2026-07-09)

**Issues**: 4d.2e tracker **[#4153](https://github.com/qf-studio/pilot/issues/4153)**
(human-led, NO pilot label — merge gate for 4d.6) · 4d.5
**[#4154](https://github.com/qf-studio/pilot/issues/4154)** (`pilot`, dispatched, ungated)
· 4d.6 **[#4155](https://github.com/qf-studio/pilot/issues/4155)** (`pilot`,
`Blocked by: #4153, #4154`).

## Context (research-verified 2026-07-09, working tree == origin/main for all cited files)

- **Webhook issue dispatch is DEAD CODE in the live deployment path.** `runPollingMode`
  registers the in-tree `github.NewWebhookHandler` wiring **`OnPRReview` only**
  (`cmd/pilot/main.go:1978-1997`); `OnIssue` is never wired anywhere in `cmd/pilot`.
  Gateway receives → verifies (`internal/gateway/server.go:616-660`, in-tree
  `VerifyWebhookSignature` from `internal/adapters/github/webhook.go:81`) → PR-review
  events reach autopilot; issue events fetch-then-drop. All issue dispatch is poller-owned.
- **Path B**: `internal/pilot.Pilot` (legacy gateway-only mode, `cmd/pilot/main.go:428-445`,
  instantiated `:1144`) has its own full webhook engine that DOES wire `OnIssue`
  (`internal/pilot/pilot.go:324-330,1131-1165`) → pre-SDK `orchestrator.ProcessGithubTicket`
  — no spec-guard, no SDK PR-create, no epic handling. Never exercised by our
  `--github --telegram` deployment. **Untouched by all M7 phases; explicitly OUT OF SCOPE.**
- **SDK webhook surface** (v0.30.0): `sdk/integrations/github/webhook.go` is a near-clone
  of the in-tree handler (`OnIssue`/`OnPRReview`/`VerifyWebhookSignature`) + input
  sanitization the in-tree copy lacks. **No exported webhook→`core.IssueEvent` bridge**
  (`toIssueEvent` unexported, `adapter.go:163-186`) — wiring webhook issue dispatch would
  need an upstream export ask (mem-087 pattern). Not needed: OnIssue stays unwired (parity).
  ⚠️ Semantics diff: SDK `VerifyWebhookSignature` returns **true** on empty secret
  (fail-open); in-tree fail-closes unless `PILOT_ALLOW_UNSIGNED_WEBHOOKS=1`. Gateway only
  calls verify when `secret != ""` (`server.go:635`) — preserve that gate + add a test.
- **GitLab precedent decides delete-vs-residual**: cutover `07467a9d` (#3456/PR #3459)
  swapped poller construction only; `internal/adapters/gitlab/` was NEVER deleted (Config
  type, `ExtractMRNumber`, preflight verifier, path B all still consume it). 4d.6 follows:
  **delete dead machinery, keep live remnants.**
- **Board-source trap**: `NewProjectBoardSource` (read/pull path) is in-tree-only, called
  ONLY from flag-off branches (`main.go:1024`, `:2554`) — SDK-owned repos have zero board
  wiring today. SDK exports its own `ProjectBoardSource`
  (`sdk/integrations/github/project_source.go`) — 4d.6 must SWAP, not drop. Board write
  path already SDK (`githubSDK.NewProjectBoardSync`, `main.go:638`/`:1681`).
- Live-caller inventory for `internal/adapters/github` when flag-on:
  **dead** = `poller.go` (2421), `cleanup.go` (508), `merger.go` (233), `retry.go` (197),
  `spec_validator.go` (85, legacy copy of `ghissue.ValidateSpec` — in-tree handler at
  `handlers.go:331` is its only caller), `issue_creator.go`/`issue_create.go`,
  `project_source.go` (post-swap);
  **live** = `client.go` (webhook + preflight + path B), `types.go` (Config schema,
  `internal/config/config.go:136`), `webhook.go` (path B keeps it after 4d.5), `converter.go`
  (`ExtractAcceptanceCriteria` on SDK path `handlers.go:1291`; `ConvertIssueToTask` path B),
  `grouping.go` (`ParseParentIssueNumber` ← `internal/autopilot/scope_membership.go:39`),
  `verify.go` (preflight), `notifier.go` (path B).

## Design Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| 4d.5 scope | (A) full webhook→SDK dispatch migration; (B) surface swap only, OnIssue stays unwired | **B** | OnIssue is dead code in live mode; no SDK IssueEvent bridge exists; parity-preserving swap removes gateway's `internal/adapters/github` dependency at minimal risk |
| Empty-secret semantics | adopt SDK fail-open / keep gateway gate | **keep gateway gate** (`secret != ""` check) + regression test | SDK verify is fail-open on empty secret; in-tree fail-closed behavior must survive the swap |
| Path B (`internal/pilot.Pilot`) | migrate / delete / leave | **leave untouched** | Separate pre-SDK engine, unknown external usage; its retirement is its own future decision, not a 4d.6 rider |
| Delete-vs-residual | delete package / delete dead machinery only | **dead machinery only** | GitLab precedent; `client.go`/`types.go`/`webhook.go`/`converter.go`/`grouping.go`/`verify.go`/`notifier.go` have live callers |
| `use_sdk_poller` flag | keep / remove (SDK always-on) | **remove; warn-and-ignore if set** | Flag-off fallback is deleted with the in-tree poller; removing silently would fail configs — parse, log deprecation warning, proceed on SDK path |
| Board source | drop / swap to SDK `ProjectBoardSource` | **swap** (default repo, parity with today's wiring) | Feature exists in configs; SDK exports the equivalent; dropping silently breaks `source_enabled` deployments |
| Execution | human-led / Pilot | **Pilot, gated** | 4d.5 independent surface, dispatchable now; 4d.6 `Blocked by:` 4d.2e tracker + 4d.5 issue (deleting the fallback before live rollout proves the SDK path would kill the rollback) |

## Sub-phases

### 4d.2e — live rollout + smoke (HUMAN-OWNED, tracker issue, gate anchor for 4d.6)
As specified in § 4d.2 above. Flip `use_sdk_poller` for default + `projects:` repos,
daemon restart (combine with release-cycle `on_schedule` cutover restart), N
`github-sdk-poller` component logs, canary issue on one quiet train repo → PR → merge,
pilot-repo epic sub-issue skip-marking confirmed, per-controller metrics in aggregate.

### 4d.5 — gateway webhook → SDK surface (Pilot, dispatch now)
- Swap `cmd/pilot/main.go:1978-1997` to `githubSDK.NewWebhookHandler` (or SDK equivalent
  constructor), `OnPRReview` → `autopilotController.OnReviewRequested` unchanged; OnIssue
  stays unwired.
- Swap `internal/gateway/server.go` signature verification to the SDK export; **preserve**
  the `secret != ""` gate and fail-closed behavior; drop the gateway's
  `internal/adapters/github` import.
- Tests: signature valid/invalid/empty-secret (fail-closed), PR-review event reaches the
  callback, issue event is a no-op; `go test -race ./internal/gateway/... ./cmd/pilot/...`.
- Out of scope: path B, webhook issue dispatch, upstream converter export.

### 4d.6 — in-tree cleanup (Pilot, `Blocked by:` 4d.2e tracker + 4d.5)
1. ✅ SHIPPED (GH-4168, sub-issue of #4155): board source swapped — default-repo
   `NewProjectBoardSource` (`main.go:1024`, `:2554`) now builds studio-sdk's
   `ProjectBoardSource` (own `githubSDK.NewClient(token)`, `toSDKProjectBoardConfig`
   bridge). `github.WithProjectBoardSource` took a second `sourceStatus` param since
   the SDK type's `config` field is unexported; `Poller.fetchCandidates` converts the
   returned `[]*githubSDK.Issue` back to the internal `Issue` type via new
   `convertSDKBoardIssues` (field-identical mirror of what the in-tree reader
   populated). Write path (`WithBoardSync`/`ProjectBoardSync`) and the in-tree
   `project_source.go` type are untouched — parity, default repo only, unchanged.
2. Delete in-tree poller machinery: `poller.go`, `cleanup.go`, `merger.go`, `retry.go`,
   `issue_creator.go`, `issue_create.go`, `project_source.go`, `project_board.go` read
   path, `spec_validator.go` + the legacy in-tree handler path (`handlers.go:331` region)
   — `ghissue.ValidateSpec` becomes the single spec-guard copy (closes the 4d.3
   "both copies" liability).
3. `cmd/pilot/main.go`: delete `createPollerForRepo` (`:2439-2584`) + both flag-off
   branches (`:2598-2602` default, `:2630-2637` projects, invocations `:2605`/`:2644`);
   gateway-mode in-tree poller block (`:860-1055` flag-off leg). SDK poller becomes
   unconditional; `use_sdk_poller` parsed → deprecation warning, ignored.
4. Docs: `configs/pilot.example.yaml` flag block, `github/types.go:17-22` comment,
   README Current State.
- Tests: full `go test -race ./...` incl. stress gate (mem-048: deletion adds zero
  poll-cycle API calls); grep-gate asserting no `internal/adapters/github` imports outside
  the residual-consumer allowlist.

## Done (Phase 4 exit)

- [ ] 4d.2e smoke green (human); tracker issue closed.
- [ ] 4d.5 merged: gateway webhook on SDK surface, fail-closed test in place.
- [ ] 4d.6 merged: in-tree poller machinery gone, single spec-guard copy, board source
  swapped, flag deprecated; daemon runs the release ≥1 day with N SDK pollers healthy.
- [ ] #3423 updated: remaining M7 scope = path B disposition + other adapters (separate
  decisions); TASK-368 archived.

---

**Last Updated**: 2026-07-09
