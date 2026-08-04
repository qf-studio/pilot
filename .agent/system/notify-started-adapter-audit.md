# Notify-Started Adapter Audit (GH-4710 / TASK-441 Leg 3)

**Status**: AUDIT ONLY — no code changed by this doc. Navigator reviews and files fix issues.
**Scope**: the 6 non-GitHub SDK-poller dispatch handlers in `cmd/pilot/handlers.go` — did the
GH-4692 class of bug ("dispatch path performs zero start-of-work notification because nothing
wires the handler-level `NotifyTaskStarted` call") reproduce on each sibling?
**studio-sdk version audited**: `v0.31.2-0.20260721122825-7d17e12412ff` (pinned in `go.mod:11`);
evidence cited against the local checkout at `/var/lib/pilot/repos/startups/studio-sdk`.

## Answer, up front

**No** — not in the form GH-4692 fixed. GH-4692's bug was specific to the GitHub SDK poller:
`studio-sdk/sdk/integrations/github/poller.go` performs **zero** label operations internally on
dispatch (confirmed: no `AddLabels(...LabelInProgress...)` call anywhere in that file), so the
`pilot-in-progress` label — and therefore `recoverOrphanedIssues` — depended entirely on the
pilot-side handler doing it. Before GH-4692, nothing did.

All six sibling SDK pollers (`linear`, `jira`, `asana`, `plane`, `gitlab`, `azuredevops`) are
built differently: each applies its own in-progress label/tag **internally**, inside
`processIssueAsync`/`processWorkItemAsync`, unconditionally before invoking the pilot-supplied
handler callback. That is a structural guarantee GitHub's poller never had — so orphan recovery
and dispatch-dedup (the correction #5 invariant in `saas-architecture.md`: never strip pilot
status labels) are **not** at risk for these six.

**What *is* missing** across 5 of the 6 (all but Plane): the human-facing "🤖 Pilot started
working on this" **comment**, and for Jira specifically, the native **workflow-status
transition** (e.g. board column "To Do" → "In Progress") — a separate JIRA concept from the
`labels` field the poller manages. These are real, but they are a UX/visibility gap, not a
dispatch-correctness gap. Sized S–M below.

## Summary table

| Adapter | Status mechanism(s) | Poller auto-labels on dispatch? | GH-4692-class (label/dedup) gap? | Notify-started comment wired? | Gap | Size |
|---|---|---|---|---|---|---|
| Linear | label (`pilot-in-progress`, SDK-managed) | Yes | No | **Yes** (GH-4717, `poller_linear.go`) | none | — |
| Jira | label (SDK-managed) **+** native workflow-status transition | Yes (label only) | No | **Yes** (GH-4718, `poller_jira.go`) | none | — |
| Asana | tag (`pilot-in-progress`, SDK-managed) | Yes | No | No (GH-4719 filed, not yet merged to main as of this edit) | comment-only | S |
| Plane | label (SDK-managed) **+** native issue-state transition | Yes (label; state via `startedStateIDs`) | No | **Yes** (GH-2132, `poller_plane.go:58`) | none | — |
| GitLab | label (SDK-managed) | Yes | No | **Yes** (GH-4720, `poller_gitlab.go`) | none | — |
| AzureDevOps | tag (SDK-managed) | Yes | No | No | comment-only | S |

## Per-adapter detail

### Linear — `handleLinearIssueWithResult` (`cmd/pilot/handlers.go:124`) — **fixed, GH-4717**

- **Status mechanism**: label only. `linear.Config.TriggerLabel`/workspace `PilotLabel`
  (`cmd/pilot/poller_linear.go:33-45`) drives an SDK-internal `pilot-in-progress` label.
- **Poller auto-labels**: `studio-sdk/sdk/integrations/linear/poller.go:309-310` —
  `processIssueAsync` calls `p.client.AddLabel(ctx, issue.ID, p.inProgressLabelID)`
  unconditionally before `p.onIssue(ctx, issue)`. `recoverOrphanedIssues`
  (`poller.go:198-…`, `hasStatusLabel` at `poller.go:341-349`) works correctly — dispatch always
  labels, so restart recovery always finds in-flight issues.
- **Handler wiring**: `handleLinearIssueWithResult` never calls any `NotifyTaskStarted`.
  `poller_linear.go`'s `CreateAndStart` closure (`:63-65`) calls the handler directly with no
  pre-dispatch notification step (contrast with `poller_plane.go:56-63`, which wraps the handler
  call with a `NotifyTaskStarted`/`LinkPR` pair).
- **Notifier surfaces available but unused**: `internal/adapters/linear/notifier.go:22`
  (`(*Notifier).NotifyTaskStarted`, comment-only, unit-tested in `notifier_test.go` but never
  constructed/called from `cmd/pilot`) and `studio-sdk/sdk/integrations/linear/notifier.go:22`
  (SDK-native, same shape as the `planeSDK.NewNotifier` GH-2132 used) — both dead with respect to
  being invoked from `cmd/pilot`, confirmed via `grep -rn "NotifyTaskStarted(ctx" cmd/`, which
  only matches `handlers.go:889` (the GH-4692 GitHub path).
- **Gap (closed)**: no "Pilot started working on this issue" comment ever posted to the Linear
  issue. Fixed by GH-4717: `linearPollerRegistration().CreateAndStart`
  (`cmd/pilot/poller_linear.go`) now builds one `linearSDK.NewNotifier(linearSDK.NewClient(ws.APIKey))`
  per workspace, keyed by team ID, inside the existing `sdkWorkspaces` loop, and the
  `sdkcore.IssueHandlerFunc` closure calls `notifier.NotifyTaskStarted(issueCtx, ev.IssueID,
  ev.SequenceID)` (selecting the workspace notifier via `ev.ProjectID`, which the SDK's
  `toIssueEvent` populates with the issue's Linear team ID) before
  `handleLinearIssueWithResult(...)`, WARN-logging (non-fatal) on error. Tests:
  `cmd/pilot/linear_sdk_notify_started_test.go` (httptest fake over the `commentCreate` mutation +
  two source-inspection wiring tests). Label-driven orphan recovery and dashboard status were
  already unaffected by this gap.

### Jira — `handleJiraSDKIssueWithResult` (`cmd/pilot/handlers.go:192`)

- **Status mechanism**: **two independent surfaces**. (1) The JIRA `labels` custom field
  (`pilot-in-progress`, SDK-managed, same shape as Linear/GitLab/Asana). (2) The native JIRA
  **workflow status** (the board column, e.g. "To Do"/"In Progress"/"Done") — moved only via
  `TransitionIssue`/`TransitionIssueTo` (`studio-sdk/sdk/integrations/jira/client.go:265,276`),
  a completely different API call from the label mutation
  (`client.go:399`, `fields["labels"]` at `client.go:163`).
- **Poller auto-labels**: `studio-sdk/sdk/integrations/jira/poller.go:282` —
  `processIssueAsync` calls `p.client.AddLabel(ctx, issue.Key, LabelInProgress)` unconditionally
  before `p.onIssue`. `recoverOrphanedIssues` (`poller.go:175-200`) is label-driven and correct.
  **The poller never calls `TransitionIssue`/`TransitionIssueTo`** — confirmed via
  `grep -n "Transitions|TransitionIssue" poller.go` → no matches.
- **Dead config wiring (adjacent finding)**: `poller_jira.go:47-48` sets
  `sdkCfg.Transitions.InProgress = deps.Cfg.Adapters.Jira.Transitions.InProgress` (and `.Done`),
  populating `jiraSDK.Config.Transitions` (`types.go:15`). That field is **never read** anywhere
  in the `studio-sdk/sdk/integrations/jira` package (`grep -n "config.Transitions|\.Transitions\."`
  → no matches outside `types.go`'s own declaration and test fixtures). The only consumer of
  `Transitions.InProgress`/`.Done` is `jiraSDK.NewNotifier(client, inProgressTransition,
  doneTransition, ...)` (`notifier.go:21`), which is never constructed in `cmd/pilot` (confirmed:
  `grep -rn "jiraSDK.NewNotifier\|jira.NewNotifier"` → no matches). So a user who configures
  `adapters.jira.transitions.in_progress: "21"` today gets **no effect** — the value is plumbed
  end-to-end through config → SDK config struct and then silently dropped. Worth flagging to
  Navigator alongside the notify-started fix since the same wiring closes both gaps.
- **Handler wiring**: `handleJiraSDKIssueWithResult` never calls `NotifyTaskStarted`, and
  `poller_jira.go`'s closure (`:50-53`) calls the handler directly, same pattern as Linear.
- **Gap**: no start comment posts to the Jira issue, and — more visibly than the other five — the
  ticket's board column never moves off its pre-dispatch status while Pilot works, because the
  only surface that would move it (`Notifier.NotifyTaskStarted`'s `TransitionIssue` call) is
  unwired. A human watching the Jira board sees no indication work started; only the (largely
  invisible) `labels` field changes.
  **Constraint check (saas-architecture.md correction #5)**: a status *transition* is not a label
  strip — it changes the workflow state field, not the `pilot-in-progress`/`pilot-done` label set
  the ProcessedStore dedup depends on. Safe to recommend as-is.
- **Fix shape (M — two independent behaviors, both already implemented and tested in
  `jiraSDK.Notifier`, just need wiring + the dead-config fix)**: construct
  `jiraSDK.NewNotifier(jiraClient, cfg.Adapters.Jira.Transitions.InProgress,
  cfg.Adapters.Jira.Transitions.Done)` in `jiraPollerRegistration().CreateAndStart` (a
  `jiraClient` needs to be split out the same way `poller_gitlab.go:48-60` and
  `poller_plane.go:49-52` already do — currently `poller_jira.go` never constructs a
  package-level client separate from the one the SDK poller builds internally, so this is the one
  adapter needing an extra client-construction step, not just a notifier call). WARN-log on
  error, non-fatal. Test: httptest fake covering both the transition POST and the comment POST
  (three-way table test: label-only success / transition-failure-then-comment-success /
  comment-failure), plus the wiring-order source-inspection test.

### Asana — `handleAsanaIssueWithResult` (`cmd/pilot/handlers.go:254`)

- **Status mechanism**: tag only (`pilot-in-progress`, SDK-managed via `TagInProgress`,
  `studio-sdk/sdk/integrations/asana/poller.go:18`). Asana has no native "in progress" status
  field pilot integrates with (Asana's built-in field is a binary complete/incomplete boolean,
  handled separately at task completion via `CompleteTask`).
- **Poller auto-tags**: `poller.go:297-298` — `processIssueAsync` calls
  `p.client.AddTag(ctx, task.GID, p.inProgressTagGID)` unconditionally before `p.onIssue`.
  `recoverOrphanedTasks` (`poller.go:198-…`) is tag-driven and correct.
- **Handler wiring**: `handleAsanaIssueWithResult` never calls `NotifyTaskStarted`.
  `poller_asana.go`'s closure (`:46-48`) calls the handler directly, no pre-dispatch step.
- **Notifier surfaces available but unused**: `internal/adapters/asana/notifier.go:28` (in-tree,
  comment-only, unit-tested but dead in production) and
  `studio-sdk/sdk/integrations/asana/notifier.go:32` (SDK-native, same signature as the
  `planeSDK`/`githubSDK` ones already wired elsewhere).
- **Gap**: no start comment posts to the Asana task.
- **Fix shape (S)**: same pattern as Linear — construct `asanaSDK.NewNotifier(asanaClient,
  pilotTag)` once in `asanaPollerRegistration().CreateAndStart`, call `NotifyTaskStarted(issueCtx,
  ev.IssueID, ev.SequenceID)` before `handleAsanaIssueWithResult(...)`, WARN-log on error. Test:
  httptest fake + wiring-order test, mirroring GH-4692's pattern.

### Plane — `handlePlaneIssueWithResult` (`cmd/pilot/handlers.go:381`) — **no gap, already fixed**

- **Status mechanism**: label (SDK-managed) **+** native issue-state transition
  (`p.startedStateIDs[item.ProjectID]` → `client.UpdateIssueState`,
  `studio-sdk/sdk/integrations/plane/poller.go:401-408`).
- **Poller auto-labels + auto-transitions**: `poller.go:395-408` — `processIssueAsync` adds the
  in-progress label AND transitions the work item's native state, unconditionally, before
  `p.onIssue`.
- **Handler wiring**: **already wired**, but at the poller level rather than the handler level —
  `cmd/pilot/poller_plane.go:56-63` explicitly calls
  `planeNotifier.NotifyTaskStarted(issueCtx, ev.ProjectID, ev.IssueID, ev.SequenceID)` before
  invoking `handlePlaneIssueWithResult`, and `LinkPR` after. Comment says `// GH-2132: Notify
  task started` — this predates GH-4692 by a different issue number, confirming Plane's fix
  shipped independently and earlier.
- **Gap**: none for start-of-work notification. (Out of scope: `NotifyTaskCompleted` /
  `NotifyTaskFailed` are not called for Plane or any of the other five — see "Adjacent, explicitly
  out of scope" below.)

### GitLab — `handleGitlabIssueWithResult` (`cmd/pilot/handlers.go:507`) — **fixed, GH-4720**

- **Status mechanism**: label only (`pilot-in-progress`, SDK-managed,
  `studio-sdk/sdk/integrations/gitlab/poller.go:18`).
- **Poller auto-labels**: `poller.go:520` — `processIssueAsync` calls
  `p.client.AddIssueLabels(ctx, issue.IID, []string{LabelInProgress})` unconditionally before
  `p.onIssueWithResult`/`p.onIssue`. `recoverOrphanedIssues` (`poller.go:199-…`, `hasStatusLabel`
  at `:571-575`) is label-driven and correct.
- **Handler wiring**: `handleGitlabIssueWithResult` never calls `NotifyTaskStarted`, despite
  already holding a `*gitlabSDK.Client` (`client` param, used for `AddIssueNote` post-execution
  comments at `handlers.go:570-617`) — the client is right there, just never passed through a
  `NotifyTaskStarted` call before dispatch. `poller_gitlab.go`'s closure (`:62-65`) calls the
  handler directly.
- **Notifier surfaces available but unused**: `internal/adapters/gitlab/notifier.go:24` (in-tree,
  comment **+ redundant label add**, since the poller already labels — unit-tested, dead in
  production) and `studio-sdk/sdk/integrations/gitlab/notifier.go:24` (SDK-native).
- **Gap (closed)**: no start comment/note posted to the GitLab issue at dispatch time (the first
  note the issue got before this fix was the post-execution success/failure note already wired at
  `handlers.go:570-617`). Fixed by GH-4720: `gitlabPollerRegistration().CreateAndStart`
  (`cmd/pilot/poller_gitlab.go`) now builds `gitlabNotifier := gitlabSDK.NewNotifier(gitlabClient,
  pilotLabel)` from the client already constructed there (`:48-60`), and the
  `sdkcore.IssueHandlerFunc` closure calls `gitlabNotifier.NotifyTaskStarted(issueCtx, iid,
  ev.SequenceID)` (IID parsed from `ev.IssueID` via `strconv.Atoi`, mirroring
  `handlers.go:569,743`) before `handleGitlabIssueWithResult(...)`, WARN-logging (non-fatal) on
  error — including the `strconv.Atoi` parse-failure path. `NotifyTaskStarted`'s own
  `AddIssueLabels` call is additive/idempotent (the client merges label sets, `client.go:187-213`),
  so it does not conflict with or replace the poller's own unconditional pre-dispatch labeling at
  `poller.go:520`. Tests: `cmd/pilot/gitlab_sdk_notify_started_test.go` (httptest fake over the
  label GET/PUT + note POST endpoints, plus two source-inspection wiring tests — one for
  call-ordering, one confirming the SDK-native notifier is used rather than
  `internal/adapters/gitlab/notifier.go`).

### AzureDevOps — `handleAzureDevOpsIssueWithResult` (`cmd/pilot/handlers.go:624`)

- **Status mechanism**: tag only (`pilot-in-progress`, SDK-managed,
  `studio-sdk/sdk/integrations/azuredevops/poller.go` `TagInProgress`).
- **Poller auto-tags**: `poller.go:527` — `processWorkItemAsync` calls
  `p.client.AddWorkItemTag(ctx, wi.ID, TagInProgress)` unconditionally before `p.onIssue`.
  `recoverOrphanedWorkItems` (`poller.go:207-…`) is tag-driven and correct.
- **Handler wiring**: `handleAzureDevOpsIssueWithResult` never calls `NotifyTaskStarted`.
  `poller_azuredevops.go`'s closure (`:50-52`) calls the handler directly.
- **Notifier surfaces available but unused**: `internal/adapters/azuredevops/notifier.go:24`
  (in-tree, comment + redundant tag add, unit-tested, dead in production) and
  `studio-sdk/sdk/integrations/azuredevops/notifier.go` (SDK-native — no `notifier_test.go` in
  this one package, unlike its five siblings; worth a note but not blocking, since the function
  is a thin wrapper matching the tested pattern of every other adapter).
- **Gap**: no start comment posts to the Azure DevOps work item.
- **Fix shape (S)**: construct `azuredevopsSDK.NewNotifier(adoClient, pilotTag)` in
  `azuredevopsPollerRegistration().CreateAndStart` (currently no separate client is constructed
  there at all — `poller_azuredevops.go` passes everything through the SDK's own internal poller
  client, so this adapter, like Jira, needs a small client-construction addition, mirroring
  `poller_gitlab.go:48-60`), call `NotifyTaskStarted` before `handleAzureDevOpsIssueWithResult`,
  WARN-log on error. Test: httptest fake + wiring-order test; consider adding
  `studio-sdk/sdk/integrations/azuredevops/notifier_test.go` upstream too if none exists (verify
  before assuming — a follow-up issue, not part of this fix, and not filed by this audit run).

## Adjacent, explicitly out of scope

- **`NotifyTaskCompleted` / `NotifyTaskFailed` are unwired for all six adapters** (and for
  GitHub too — GH-4692 only wired the *started* notification). `grep -rn
  "NotifyTaskCompleted\|NotifyTaskFailed" cmd/pilot/*.go` matches nothing outside test files. This
  is a materially different, larger audit (completion/failure notification, not start-of-work)
  and is not part of this task's acceptance criteria — flagging for Navigator to scope
  separately if desired.
- **Jira's dead `Transitions` config wiring** (see Jira section above) is a pre-existing,
  independent bug (config accepted, silently has no effect) that a Jira notify-started fix would
  incidentally resolve as a side effect of wiring `jiraSDK.NewNotifier` — not filing as a separate
  issue per this task's "no follow-up issues" constraint, but noting so the eventual fix PR
  description can credit closing it.

## Recommended fix shape, in general (mirrors GH-4692 / GH-2132)

For the remaining gaps (Asana, AzureDevOps — Linear closed by GH-4717, Jira by GH-4718, GitLab by
GH-4720; Asana's fix (GH-4719) is filed but not yet merged into main as of this edit):

1. Construct the adapter's SDK-native `Notifier` (`{adapter}SDK.NewNotifier(client, ...)`) once
   inside that adapter's `*PollerRegistration().CreateAndStart` closure in `cmd/pilot/poller_*.go`
   — **not** the in-tree `internal/adapters/{adapter}` `Notifier`, which is unused/dead in
   production and, for GitLab/AzureDevOps, would double-apply labels the poller already
   guarantees.
2. Call `notifier.NotifyTaskStarted(issueCtx, <tracker-native-id>, ev.SequenceID)` **before** the
   `handle*IssueWithResult(...)` call, inside the `sdkcore.IssueHandlerFunc` closure — exactly
   where `poller_plane.go:56-63` already does it. WARN-log (never propagate/abort dispatch) on
   error, matching the established non-fatal pattern
   (`pilot.go:1191-1195` / `controller.go:3011-3015` / `notifyTaskStartedSDK` at
   `handlers.go:888-905`).
3. Tests per adapter, mirroring `cmd/pilot/github_sdk_notify_started_test.go`:
   - An `httptest.Server`-backed fake covering the notifier's actual HTTP calls (comment POST,
     plus label/tag/transition POST where applicable), table-driven over success/failure-per-call.
   - A source-inspection "wiring order" test proving the poller registration file calls
     `NotifyTaskStarted(` before the `handle*IssueWithResult(` call, and that a failure is logged
     as WARN rather than returned/aborting (same shape as
     `TestGithubHandlerSDK_NotifyTaskStartedWired`).
4. **saas-architecture.md correction #5 compliance**: every recommended change here is additive
   (a new comment post, or for Jira a workflow-status transition) — none strips or replaces the
   `pilot-in-progress`/`pilot-done`/`pilot-failed` label/tag set the poller's own
   `processIssueAsync`/`processWorkItemAsync` already manages structurally. No recommendation in
   this doc touches that mechanism.

Estimated total: 3×S + 1×M across four remaining PRs (one per adapter, matching the existing
one-PR-per-adapter granularity of GH-4692/GH-2132), no shared blocking dependency between them.
Linear (S) shipped as GH-4717.
