# TASK-633: Billing mode fast-follow — PR#6017 post-merge review notes verified on main, two fix legs

**Status**: 🚀 **F6 DISPATCHED 11-04 09:4xZ → [pilot#6050](https://github.com/qf-studio/pilot/issues/6050) (`pilot` + `no-decompose`) — the F1/F2 post-merge minors that F3 and F5 left (alert text + CLI runners drop the billing alert, post-merge reviewer resolver failure raises none, bootstrap scaffold order, discard after a failed delete, subtask cost assertion), one PR (§ LEG F6). F5 [pilot#6038](https://github.com/qf-studio/pilot/issues/6038) → PR#6046 MERGED 11-03 (`e599ff06b`, post-merge APPROVE-WITH-NOTES), unreleased (next tag). Held after F6: A2 runtime override + `pilot billing set` · B usage-limit fallback · C budget scope · console writes the billing block (TASK-631 D4).** · ✅ **F4 [pilot#6032](https://github.com/qf-studio/pilot/issues/6032) → [PR#6036](https://github.com/qf-studio/pilot/pull/6036) MERGED 16:56Z by hand (squash `cf93e82c6`; `gh pr review --approve` refused: same account as the PR author, so the merge is the approval). F5 [pilot#6038](https://github.com/qf-studio/pilot/issues/6038) DISPATCHED 16:5xZ (`pilot` + `no-decompose`). **v2.311.0 RELEASED 17:21Z** (tag on `cf93e82c6` after a 654 s gate; release run green, 6 assets, F4 line prepended to the notes; docs-version PR#6040 armed); **box hot-upgraded 17:27:01Z** (`upgrade verified complete from=2.310.0 to=v2.311.0`, 1 proc). **FLEET HOLD LIFTED**: the AMI bake and SSM hops may target v2.311.0; Nelya's golden AMI (16:54 CET) is on 2.309.0, pre-regression, so nothing was exposed. Nelya told 17:34Z in #infrastructure (ts `1793731657.978579`): 2.311.0 out, hold lifted, 2.310.0 never for tenant images, next re-bake on 2.311.0; we owe her a note after the console-api redeploy.** Was: PR OPEN 16:41Z, CI test running, pre-PR shadow REQUEST-CHANGES (1 real blocking: bare-ctx spawn sites keep the inherited key under an explicit `subscription`; no live instance) → `pilot-review-rc` label → autopilot escalates to approval on green. Navigator review posted 16:48Z: APPROVE-w-notes, merge now, fast-follow F5 (§ LEG F5, not filed). Release v2.311.0 after merge, then lift the fleet hold.** Was: P1 DISPATCHED 2026-11-03 ~15:50Z — v2.310.0 regression: unset `billing.mode` = subscription = inherited `ANTHROPIC_API_KEY` stripped from every model subprocess; every hosted tenant (env key, no OAuth login) breaks on upgrade. None exposed yet (tenants on 2.282.0 / 2.277.4). HOLD: no AMI bake (infra#23) or SSM hop to 2.310.0 until F4 is released.** Shipped in v2.310.0: A1 PR#6017 · F1 #6021→PR#6024 · F2 #6022→PR#6025 · F3 #6026→PR#6027; box on it since 14:16Z (OAuth, unaffected), 4 tasks completed. Parent: TASK-631.
**Created**: 2026-11-03
**Assignee**: Pilot

---

## Context

**Problem**: the post-merge review of PR#6017 listed nine minor defects. navigator-research verified them on main `f72c25810` (2026-11-03): eight confirmed, one not a defect (additive migration matches the key_source idiom), and two new gaps found while tracing: backend retries rooted in `context.Background()` drop the billing credential, and the Navigator bootstrap run has no credential and no mode on its row. None blocks; together they make `api_key` mode leak to the subscription on four paths, show a false dashboard state, add repick backoff to tasks that never ran, and keep resolved keys in memory.

**Goal**: close every confirmed gap in two one-PR legs, so that in `api_key` mode no model subprocess bills the subscription without an error, every execution row carries the right `billing_mode`, a key outage never touches retry accounting, and a resolved key lives no longer than its execution.

**Verified defects** (file:line on main):

| # | Verdict | Where | What |
|---|---|---|---|
| 1 | confirmed, regression | `backend_opencode.go:122`, `:144-146`; `backend_qwencode.go:203`, `:257` | `modelSubprocessEnvFor` strips `ANTHROPIC_API_KEY`; `billingContext` returns nil for non-claude backends (`billing_mode.go:269-271`), so OpenCode/Qwen with the anthropic provider lose the key. The comment still claims the keep-prefix saves it. |
| 2 | confirmed | `autopilot/post_merge_review.go:300`, `post_merge_confine.go:141-175` | The reviewer ctx carries no credential; the confine strips the ambient key and copies the login file → subscription billed silently. Controller has no Runner; reviewer built in `cmd/pilot/post_merge_reviewer.go:60-62` from cfg. Pre-PR review is unaffected (runs inside Execute, `pre_pr_review.go:179-184`). |
| 3 | confirmed | `dispatcher.go:4028-4037` | credErr branch does `continue` after Finish; no `EmitProgress("Failed")` (`:4076`) and no `monitor.Fail` (`:4108-4114`) after "Running" fired at `:3971`. Card stays Running (mem-142 pitfall). |
| 4 | confirmed, wider | `billing_admission.go:102`, `:110`; `dispatcher.go:2015-2023`, `:2390-2408`, `:1761`, `:1862-1877` | First outage row takes a real claim and finishes `failed`. Next pickup → `ErrClaimLost` → generic path → `consecutiveDrops` + backoff; identical error text ×2 trips the streak check → stalled + pilot-blocked. `ExecStatusSkipped` takes the same path. |
| 5 | confirmed | `billing_mode.go:252-284` | `prepareBilling`/`billingContext` return the error with no alert. Callers bypassing the dispatcher: `cmd/pilot/handler_common.go:445` (Dispatcher nil), `commands.go:1009`, `:1624`, `interactive.go:229`, `comms/handler.go:494,541,592,685,935`, chat turns via `chat_turn.go:93`. Runner has no Dispatcher → alert is the seam. |
| 6 | confirmed, test | `billing_mode_test.go:313` | Classifier half uses a hand-built credential, not the resolver. |
| 7 | confirmed, wider | `billing_admission.go:220-221`, `:705`; `dispatcher.go:1071,1088,1115`; `:3890`, `:4001`, `:4006-4016` | Stash cleared only by `billingCredentialForRun` or Stop. CLI cancel (`lifecycle.Cancel`, separate process) and stale queued recovery never touch it; worker `continue` paths (base-presence park, supersede, merged-PR short-circuit) leak too. |
| 8 | not a defect | `store.go:207` | Additive ALTER, same as key_source; old rows read `unknown`. |
| 9 | confirmed (decompose only) | `decompose.go:497-519`; `runner.go:5763` | Subtask `&Task{}` has no `billingMode` → cost labelled unknown. Epic children (`epic.go:3006`) and parallel (`parallel.go:271`) are fine. |
| A | **new** | `runner.go:5470-5475`, `:6449-6450` | Smart retry and quality-gate feedback retry use `context.WithTimeout(context.Background(), …)` → credential dropped → key stripped → CLI falls back to login → subscription billed silently. Idiom to use: `context.WithoutCancel(ctx)` as at `runner.go:374`, `:3299`, `:3336`. |
| B | **new** | `navigator_bootstrap.go:109`, `:386`; `cmd/pilot/navigator.go:74` | Bootstrap `Task` has no mode (row `unknown`); the standalone CLI path never calls `billingContext` → no credential in `api_key` mode. |

**Carrier**: `Task.billingMode` (unexported, `runner.go:820`), set in `admitBilling`, `prepareBilling`, the worker rebuild (`dispatcher.go:4546`) and epic children; stamped by `lifecycle.Begin` (`lifecycle.go:237`). After `Zero()` the credential's `Env()` returns nil, the same as subscription: a late spawn fails **open**. Nothing in-process spawns after the worker's `Zero` (`dispatcher.go:4050-4051`); the retries in A are inside Execute.

---

## Known Pitfalls & Patterns

- **PITFALL** (95%, mem-142): dashboard/ledger stage strip marks state wrongly when a path skips the normal failure branch → F1 mirrors `:4076`/`:4108` exactly.
- **PITFALL** (95%, mem-024): every non-success collapsed into `failed` inflated the failed count → the outage row must not be `failed`; F1 uses the infra class.
- **PITFALL** (95%, terminal-status-without-rearm-probe): a new terminal status without a re-arm probe kills operator recovery → F1 adds **no** new status; it reuses `ExecStatusInfra` and its existing carve-out.
- **PITFALL** (90%, nextretrygeneration-blind-to-dead-owner): claim accounting is subtle → F1 pins the no-backoff behaviour with a test on `nextRetryGeneration` after an outage row.
- **LEARNING** (feedback_subprocess_not_api): every LLM primitive spawns `claude`; so every spawn site is a billing site → F2 covers reviewer, bootstrap, OpenCode/Qwen.

---

## Acceptance Criteria

- [ ] F1: a billing-key failure in the worker emits "Failed" progress and `monitor.Fail`; the outage row finishes `infra`, not `failed`; the next pickup of that task has no repick backoff and no identical-failure streak.
- [ ] F1: a stashed credential for a queued row that is cancelled, deleted by stale recovery, parked, superseded or short-circuited is zeroed within one stale-recovery tick.
- [ ] F1: decomposed subtasks carry the parent's `billingMode`; smart retry and gate-feedback retry keep the credential on their ctx.
- [ ] F2: OpenCode and Qwen subprocess envs keep an inherited `ANTHROPIC_API_KEY`; the stale comment is corrected.
- [ ] F2: the autopilot post-merge reviewer resolves a credential in `api_key` mode, zeroes it after, and fails closed with the `billing_key_unavailable` error when the command fails.
- [ ] F2: `billingContext` emits the `billing_key_unavailable` alert on failure for non-dispatcher callers; Navigator bootstrap resolves a credential and stamps `billing_mode` on its row.

---

## Implementation

Two legs, both `pilot` + `no-decompose`, both in `internal/executor` with small spill-over. F1 and F2 touch disjoint files except tests; no dependency between them.

### Leg F1 — dispatcher state, retry accounting, credential lifetime, attribution
Defects 3, 4, 6, 7, 9, A. Files: `internal/executor/dispatcher.go`, `billing_admission.go`, `decompose.go`, `runner.go`, `billing_mode_test.go`.

### Leg F2 — paths outside the dispatcher
Defects 1, 2, 5, B. Files: `internal/executor/backend_opencode.go`, `backend_qwencode.go`, `post_merge_reviewer.go`, `billing_mode.go`, `navigator_bootstrap.go`, `cmd/pilot/post_merge_reviewer.go`, `cmd/pilot/navigator.go`.

---

## Out of Scope

- A2 runtime switch, B usage-limit fallback, C budget scope (TASK-631).
- Scrubbing quality-gate / git env; key_source semantics; the additive migration (not a defect).
- A Runner→Dispatcher pause hook for in-daemon comms callers (alert only in F2; revisit if the alert fires in practice).
- Making `Env()` after `Zero()` fail closed (would break the subscription path; the leak is already prevented by A).

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Outage row status | keep `failed`; `ExecStatusSkipped`; new status; `ExecStatusInfra` | **`ExecStatusInfra`** | Reuses the GH-5211 infra carve-out (own counter, cap 8, no streak check); no new terminal status, so no re-arm probe needed; semantically the same class the env-class signatures already assign to a missing key. |
| Non-dispatcher fail-closed signal | pause via a Runner hook; alert only | **alert only** | Runner has no Dispatcher; CLI callers are other processes where a pause is meaningless; the alert rule's cooldown bounds noise. |
| OpenCode/Qwen env | thread a credential; plain scrub | **plain `modelSubprocessEnv`** | Those backends have no billing mode; the pre-change behaviour is the spec ("non-Anthropic setups behave as today"). |
| Reviewer credential | Controller resolves; reviewer resolves | **reviewer resolves** (setter on `PostMergeReviewer`) | One seam serves autopilot and `pilot review-merge`; pre-PR path unaffected. |
| Stash cleanup | hook every cancel path; periodic sweep | **sweep in `runStaleRecoveryLoop` + direct discard at the 3 stale points** | CLI cancel happens in another process; only a sweep sees it. |

---

## Verify

```bash
go test -race ./internal/executor/ -run TestBillingKey
go test -race ./internal/executor/ -run TestBillingCredential
go test -race ./internal/executor/ -run TestBillingRetryCtx
go test -race ./internal/executor/ -run TestOpenCode_EnvKeepsAnthropicKey
go test -race ./internal/executor/ -run TestPostMergeReviewer_Billing
```

---

## Done

- [ ] F1 and F2 merged; post-merge reviews read.
- [ ] On the founder box in `subscription` mode (default): no behaviour change observed over one day of runs.
- [ ] TASK-631 status updated; A2 unblocked.

---

## Dispatched legs

### LEG F1 — repo: qf-studio/pilot — title: fix(executor): billing-mode fast-follow — worker failure state, infra-class outage row, credential sweep, subtask and retry attribution

## Context

Post-merge review of PR#6017 (billing mode, GH-6016), verified on main at f72c25810. Six gaps inside the dispatcher and runner; none blocks, together they leak the key to the subscription on retries, show a false dashboard state and add repick backoff to tasks that never ran.

- Worker billing-key failure (`internal/executor/dispatcher.go` near lines 4028-4037): after lifecycle.Finish the branch runs continue. It skips the EmitProgress Failed call (near line 4076) and monitor.Fail (near lines 4108-4114) that the normal failure branch runs, while EmitProgress Running already fired near line 3971. The card stays Running.
- Outage row (`internal/executor/billing_admission.go` near lines 102 and 110): failBillingKeyUnavailable takes a real claim with beginClaimWithGenerationRetry and finishes the row failed. On resume the next pickup gets ErrClaimLost, nextRetryGeneration treats the failed row as not done (`internal/executor/dispatcher.go` near lines 2015-2023), falls into the generic path and grows consecutiveDrops and the backoff window (near lines 2390-2408). Two outages with the identical error text trip consecutiveIdenticalFailureThreshold (near line 1761, check near lines 1862-1877) and escalate the task to stalled plus pilot-blocked. ExecStatusSkipped is terminal but not done and takes the same path. The infra class already has a carve-out: priorClaimWasInfra (near line 1668, used near lines 2350-2388) with its own counter capped at 8 and no streak check.
- Credential stash (`internal/executor/billing_admission.go` near lines 199-235): a stashed credential is removed only by billingCredentialForRun (called from the worker near line 4026) or by stopBillingState. recoverStaleQueuedTasks (`internal/executor/dispatcher.go` near lines 1048-1135) deletes or cancels rows at three points (near lines 1071, 1088, 1115) without touching the stash. pilot task cancel runs lifecycle.Cancel in another process. Worker paths that continue before line 4026 also leak: base-presence park (near line 3890), supersede (near line 4001), merged-PR short-circuit (near lines 4006-4016).
- Decomposed subtasks (`internal/executor/decompose.go` near lines 497-519): createSubtasks builds a fresh Task literal without billingMode; RecordCost (`internal/executor/runner.go` near line 5763) labels their cost unknown. Epic children and parallel research are already correct.
- Retry contexts (`internal/executor/runner.go` near lines 5470-5475 and 6449-6450): the smart retry and the quality-gate feedback retry build their ctx from context.Background with a timeout, so the credential set by WithBillingCredential is gone. modelSubprocessEnvFor then strips the inherited key and the CLI falls back to the login file: in api_key mode the retry bills the subscription with no error. The idiom already used near lines 374, 3299 and 3336 is context.WithoutCancel of the task ctx.
- Test gap (`internal/executor/billing_mode_test.go` near line 313): the effort-classifier half of the api_key test puts a hand-built credential on ctx instead of resolving one through the fake command.

probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]failBillingKeyUnavailable\(
probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]billingCredentialForRun\(
probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnvFor\(
probe: present qf-studio/pilot ExecStatusInfra
probe: absent qf-studio/pilot func[[:space:]]+\(d[[:space:]]+\*Dispatcher\)[[:space:]]+discardBillingCredential

## Scope

- Worker failure state: in the credErr branch of the worker loop, before continue, emit the same Failed progress event and monitor.Fail call the normal failure branch emits. No other change to that branch.
- Outage row class: finish the outage row with the ExecStatusInfra override in failBillingKeyUnavailable and in the worker credErr branch (lifecycle.Finish takes an override status). Keep the error text prefix billing_key_unavailable. Do not add a status. Confirm with a test that nextRetryGeneration after an outage row yields no repick backoff and no identical-failure streak, and that the infra counter cap still parks the task after repeated outages.
- Credential sweep: a new Dispatcher method discardBillingCredential(execID) that zeroes and deletes a stash entry. Call it at the three stale-recovery points and at the three worker continue paths named above. Add a sweep step to runStaleRecoveryLoop that zeroes every stash entry whose execution row is missing or not queued; this covers pilot task cancel from another process.
- Subtask attribution: createSubtasks copies the parent's billingMode into each subtask literal (same package, unexported field).
- Retry contexts: both retry sites derive their ctx with context.WithoutCancel from the task ctx plus the existing timeout, so the credential survives. No change to the timeout values.
- Test: the classifier half of the api_key test resolves its credential with NewBillingResolver over the fake command, so both halves exercise the resolver.

Tests to add, named exactly: TestBillingKeyFailure_WorkerEmitsFailedAndMonitorFail, TestBillingKeyOutage_RowIsInfraNoBackoffNoStreak, TestBillingKeyOutage_InfraCapParks, TestBillingCredential_StaleSweepZeroesOrphans, TestBillingCredential_WorkerSkipPathsDiscard, TestBillingRetryCtx_SmartRetryKeepsCredential, TestBillingRetryCtx_GateFeedbackRetryKeepsCredential, TestDecompose_SubtasksInheritBillingMode, TestBillingMode_ClassifierResolvesViaCommand.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBillingKey` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestBillingCredential` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestBillingRetryCtx` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestDecompose_SubtasksInheritBillingMode` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestBillingMode` passes; paste the output into the PR body.
- [ ] A test drives a worker through a billing-key failure and asserts a Failed progress event and a monitor.Fail call were recorded for the task.
- [ ] A test asserts the outage row's status is infra, that a following nextRetryGeneration for the same task reports no backoff and no identical-failure streak, and that the row count toward the infra cap.
- [ ] A test queues a task in api_key mode, cancels its row through the store from outside the dispatcher, runs one stale-recovery tick and asserts the stash is empty and the credential zeroed.
- [ ] A test decomposes a parent in api_key mode and asserts every subtask records cost under billing_mode api_key.
- [ ] A test runs a smart retry and a gate-feedback retry with a recording backend and asserts both retry envs carry ANTHROPIC_API_KEY from the credential.
- [ ] remove the Failed progress emit from the credErr branch -> TestBillingKeyFailure_WorkerEmitsFailedAndMonitorFail fails
- [ ] finish the outage row as failed instead of infra -> TestBillingKeyOutage_RowIsInfraNoBackoffNoStreak fails
- [ ] remove the sweep from runStaleRecoveryLoop -> TestBillingCredential_StaleSweepZeroesOrphans fails
- [ ] build the smart-retry ctx from context.Background again -> TestBillingRetryCtx_SmartRetryKeepsCredential fails
- [ ] drop the billingMode copy in createSubtasks -> TestDecompose_SubtasksInheritBillingMode fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

OpenCode and Qwen env, the autopilot post-merge reviewer credential, alerts for non-dispatcher callers, Navigator bootstrap (all in the sibling leg). A new terminal status. A Runner-to-Dispatcher pause hook. Any change to key_source or to the migrations.

## Refs

- PR#6017 post-merge review comment (nine minors); GH-6016; GH-5211 (infra carve-out); Navigator TASK-633 in this repo

### LEG F2 — repo: qf-studio/pilot — title: fix(executor): billing-mode fast-follow — OpenCode/Qwen keep their key, post-merge reviewer and Navigator bootstrap resolve a credential, non-dispatcher callers alert

## Context

Post-merge review of PR#6017 (billing mode, GH-6016), verified on main at f72c25810. Four paths outside the dispatcher either lost a key they had before or bill the subscription silently in api_key mode.

- OpenCode and Qwen (`internal/executor/backend_opencode.go` near line 122 and the comment near lines 144-146; `internal/executor/backend_qwencode.go` near lines 203 and 257): these sites were switched to modelSubprocessEnvFor, which always strips ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN. billingContext (`internal/executor/billing_mode.go` near lines 269-271) returns a nil credential for any non-claude backend, so nothing puts the key back. OpenCodeConfig has no key field (`internal/executor/backend.go` near lines 1391-1411); OpenCode reads the provider key from its process env. Before PR#6017 the ANTHROPIC_ keep-prefix preserved it. The comment still says so and is now false. The issue promised that non-Anthropic setups behave as today.
- Autopilot post-merge review (`internal/autopilot/post_merge_review.go` near line 300): reviewer.Review runs on a context.WithoutCancel of the controller ctx (near line 244) that carries no credential. postMergeReviewEnviron (`internal/executor/post_merge_confine.go` near lines 141-175) strips the ambient key and, with no auth variable left, copies the login file. In api_key mode the review bills the subscription with no error. The Controller has no Runner or resolver; the reviewer is built in `cmd/pilot/post_merge_reviewer.go` near lines 60-62 from the config, which carries the billing block. The same constructor serves pilot review-merge. The pre-PR review runs inside Execute with the credential on ctx (`internal/executor/pre_pr_review.go` near lines 179-184) and is unaffected.
- Non-dispatcher callers (`internal/executor/billing_mode.go` near lines 252-284): prepareBilling and billingContext fail closed but return the error with no alert and no pause. Callers: `cmd/pilot/handler_common.go` near line 445 when the Dispatcher is nil, `cmd/pilot/commands.go` near lines 1009 and 1624, `cmd/pilot/interactive.go` near line 229, `internal/comms/handler.go` near lines 494, 541, 592, 685 and 935, and chat turns through `internal/executor/chat_turn.go` near line 93. The Runner has no Dispatcher, and the CLI callers are separate processes, so a pause is not the seam; the alert is. The Runner's alert emitter is EmitAlertEvent (`internal/executor/runner.go` near line 9203) and the event type for a billing key failure already exists from PR#6017.
- Navigator bootstrap (`internal/executor/navigator_bootstrap.go` near lines 109 and 386; `cmd/pilot/navigator.go` near line 74): the bootstrap Task literal has no billingMode, so its row reads unknown; the standalone CLI path never calls billingContext, so in api_key mode it spawns with no credential and bills the subscription.

probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]billingContext\(
probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnv\(
probe: present qf-studio/pilot AlertEventTypeBillingKeyUnavailable
probe: absent qf-studio/pilot \)[[:space:]]+SetBillingResolver\(

## Scope

- OpenCode and Qwen: the three spawn sites go back to the plain modelSubprocessEnv scrub (the pre-change behaviour). Rewrite the comment near line 144 of the OpenCode backend to say the key passes through the ANTHROPIC_ keep-prefix and that billing mode applies to the claude backend only. No credential threading for these backends.
- Post-merge reviewer: add a SetBillingResolver method on PostMergeReviewer (`internal/executor/post_merge_reviewer.go`) and wire it in the constructor in cmd/pilot from the config's billing block. In Review, when a resolver is set, resolve a credential, attach it with WithBillingCredential to the ctx passed down to the spawn, zero it on return, and on a resolver error return that error so the review fails closed and the autopilot path records the failure. subscription mode resolves to an empty credential and behaves as today.
- Non-dispatcher callers: in billingContext, when the error satisfies IsBillingKeyUnavailable, emit one alert event of the existing billing key type through the Runner's emitter with the task id and project when known, then return the error. Rely on the alert rule's cooldown for repetition.
- Navigator bootstrap: the bootstrap Task literal gets the mode from the resolver's result, and the standalone path in cmd/pilot/navigator.go goes through the Runner's billing context so the spawn carries the credential; the row then stamps billing_mode from the task like every other execution.

Tests to add, named exactly: TestOpenCode_EnvKeepsAnthropicKey, TestQwenCode_EnvKeepsAnthropicKey, TestPostMergeReviewer_BillingAPIKeyCredentialOnSpawn, TestPostMergeReviewer_BillingKeyUnavailableFailsClosed, TestPostMergeReviewer_BillingSubscriptionUnchanged, TestBillingContext_UnavailableEmitsAlert, TestNavigatorBootstrap_BillingModeStampedAndCredentialOnSpawn.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestOpenCode_EnvKeepsAnthropicKey` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestQwenCode_EnvKeepsAnthropicKey` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestPostMergeReviewer_Billing` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestBillingContext_UnavailableEmitsAlert` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestNavigatorBootstrap_BillingMode` passes; paste the output into the PR body.
- [ ] A test sets ANTHROPIC_API_KEY in the base env and asserts the OpenCode server command env and both Qwen spawn envs still carry it.
- [ ] A test gives the reviewer a resolver in api_key mode with a fake command and asserts the recorded reviewer spawn env carries that value as ANTHROPIC_API_KEY and that the credential is zeroed after Review returns.
- [ ] A test gives the reviewer a resolver whose command exits 1 and asserts Review returns an error satisfying IsBillingKeyUnavailable and spawns nothing.
- [ ] A test calls billingContext with a failing command and asserts exactly one alert event of the billing key type reached a recording alert processor.
- [ ] A test runs the bootstrap in api_key mode and asserts the execution row reads billing_mode api_key and the spawn env carries the key.
- [ ] switch the OpenCode spawn back to modelSubprocessEnvFor -> TestOpenCode_EnvKeepsAnthropicKey fails
- [ ] drop the WithBillingCredential call in the reviewer -> TestPostMergeReviewer_BillingAPIKeyCredentialOnSpawn fails
- [ ] return nil from Review on a resolver error -> TestPostMergeReviewer_BillingKeyUnavailableFailsClosed fails
- [ ] remove the alert emit from billingContext -> TestBillingContext_UnavailableEmitsAlert fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Worker failure state, outage row class, credential sweep, subtask and retry attribution (all in the sibling leg). A Runner-to-Dispatcher pause hook. Any change to the pre-PR review path, to key_source or to the migrations.

## Refs

- PR#6017 post-merge review comment (nine minors); GH-6016; Navigator TASK-633 in this repo

### LEG F3 — repo: qf-studio/pilot — title: fix(executor): billing credential stash — keep the key while the row is queued or running, drop it when the move to running fails, assert the real subprocess env in the retry tests

## Context

Pre-PR review notes on PR#6024 (GH-6021, billing-mode fast-follow F1), verified on main after the merge. Two state gaps and two test-depth gaps; none blocks.

- Sweep race (`internal/executor/billing_admission.go` near line 267): sweepBillingCredentials keeps a stashed key only while its execution row is queued. The worker moves the row to running with lifecycle.Transition (`internal/executor/dispatcher.go` near line 3983), then runs the merged-PR network check (near line 4000), and only then takes the key with billingCredentialForRun (near line 4049). The sweep runs on the stale-recovery loop every 5 minutes (`internal/executor/dispatcher.go` near line 658). A tick inside that gap zeroes a key that admission already resolved. billingCredentialForRun then resolves again (fallback near line 232 of the admission file, Resolve on context.Background); if that second resolve fails, the run fails closed although admission succeeded, and the api_key_command runs twice per task in the race.
- Transition-failure leak (`internal/executor/dispatcher.go` near lines 3983-3986): when the move to running fails the worker logs and runs continue without dropBillingCredential. The row stays queued, so the sweep keeps the key too. The stash leaks until Stop.
- Retry tests (`internal/executor/billing_fastfollow_test.go` near line 463): both retry-ctx tests read BillingCredentialFromContext on the ctx inside a fake backend. They do not assert the env that modelSubprocessEnvFor builds for the subprocess, so a regression between ctx and env would pass.
- Skip-path test (`internal/executor/billing_fastfollow_test.go` near line 339): TestBillingCredential_WorkerSkipPathsDiscard covers the merged-PR short-circuit and the terminal-success ledger guard only; the base-presence park and the supersede paths that F1 also changed are untested.
- Not a defect, decision recorded: discardBillingCredential after a failed DeleteExecution in stale recovery (near lines 1077 and 1096) is kept. A key must not outlive the decision to drop the row; a later pickup re-resolves through the fallback.

probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]sweepBillingCredentials\(
probe: present qf-studio/pilot func[[:space:]]\(w[[:space:]]\*ProjectWorker\)[[:space:]]dropBillingCredential\(
probe: present qf-studio/pilot Status[[:space:]]==[[:space:]]string\(ExecStatusQueued\)
probe: absent qf-studio/pilot func[[:space:]]+billingStashRowLive\(

## Scope

- Sweep predicate: a new helper billingStashRowLive(status string) bool in `internal/executor/billing_admission.go` returns true for queued and running. sweepBillingCredentials keeps an entry when the row exists and billingStashRowLive is true, and discards it when the row is missing or in any other status. A row the worker is between Transition and the key take is running, so its key survives the tick; a row whose worker already took the key has no stash entry, so nothing changes there.
- Transition failure: in the worker loop, when lifecycle.Transition to running returns an error, call dropBillingCredential for the row before continue.
- Retry tests: both retry-ctx tests build the subprocess env with modelSubprocessEnvFor over the retry ctx inside the recording backend and assert that env carries ANTHROPIC_API_KEY equal to the credential's key and that the base env's inherited key is absent.
- Skip-path test: extend the worker skip-path coverage so a task parked by the base-presence hold and a task superseded both end with an empty stash.

Tests to add, named exactly: TestBillingCredential_SweepKeepsRunningRowUntilTaken, TestBillingCredential_SweepDropsTerminalAndMissingRows, TestBillingCredential_TransitionFailureDrops, TestBillingRetryCtx_SubprocessEnvCarriesKey, TestBillingCredential_WorkerSkipPathsDiscard_ParkAndSupersede.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBillingCredential` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestBillingRetryCtx` passes; paste the output into the PR body.
- [ ] A test stashes a credential, moves the row to running without taking the key, runs one sweep and asserts the stash still holds the credential unzeroed; then takes the key and asserts the stash is empty.
- [ ] A test stashes credentials for a failed row and for a deleted row, runs one sweep and asserts both entries are gone and zeroed.
- [ ] A test makes the queued-to-running Transition fail and asserts the stash is empty after the worker iteration.
- [ ] A test asserts the subprocess env built from the smart-retry ctx and from the gate-feedback retry ctx each carry the credential's key once and no inherited key.
- [ ] A test asserts the base-presence park path and the supersede path each leave an empty stash.
- [ ] keep the sweep predicate at queued only -> TestBillingCredential_SweepKeepsRunningRowUntilTaken fails
- [ ] remove the drop on the Transition failure path -> TestBillingCredential_TransitionFailureDrops fails
- [ ] make billingStashRowLive return true for every status -> TestBillingCredential_SweepDropsTerminalAndMissingRows fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Paths outside the dispatcher (OpenCode/Qwen, reviewer, bootstrap, alerts: GH-6022). Reordering the worker so the key is taken before the Transition. Any change to discard-after-failed-delete. Any change to key_source or the migrations.

## Refs

- PR#6024 pre-PR review notes; GH-6021; GH-6016 / PR#6017; Navigator TASK-633 in this repo

### LEG F4 — repo: qf-studio/pilot — title: fix(executor): billing mode unset keeps the inherited Anthropic key (legacy passthrough) — only an explicit subscription mode strips it; attribution derived from the env

## Context

Regression shipped in v2.310.0 (PR#6017, GH-6016), found 2026-11-03 while auditing the release against the hosted fleet. Nobody is broken yet; the fix must land before any env-key deployment upgrades.

- EffectiveMode (`internal/executor/billing_mode.go` near line 60) returns subscription when billing.mode is unset.
- modelSubprocessEnvFor (`internal/executor/model_env.go` near line 216) always removes ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN inherited from the daemon env, then appends the credential on ctx. In subscription mode that credential is empty.
- Result: a daemon with no billing block and ANTHROPIC_API_KEY in its environment, which worked on every release up to v2.309.0, now starts the claude CLI with no credential. The CLI falls back to the OAuth login file; where none exists every run fails.
- Who runs that way: every hosted tenant. Their env file carries ANTHROPIC_API_KEY (platform or own key, written by the console's key selector) and no OAuth login; their config has no billing block. Verified 2026-11-03 on all three tenant instances (versions 2.282.0 and 2.277.4, so not yet exposed). Self-hosted operators who export ANTHROPIC_API_KEY are in the same position; the founder box is not, because it authenticates with CLAUDE_CODE_OAUTH_TOKEN, which the strip keeps.
- The requester's own ask (#5955) was that subscription mode drops a stray key. That stays true for an explicit mode. The default for an unset mode must be the pre-v2.310.0 behaviour.

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*BillingConfig\)[[:space:]]EffectiveMode\(
probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnvFor\(
probe: present qf-studio/pilot BillingModeSubscription[[:space:]]BillingMode
probe: absent qf-studio/pilot BillingModeUnset[[:space:]]+BillingMode

## Scope

- Config: billing.mode gains the unset state as a first-class value. EffectiveMode returns a new BillingModeUnset constant when the block or the field is empty; it returns subscription only when the operator wrote subscription. Validation accepts subscription, api_key and empty.
- Resolver: in the unset mode Resolve returns a credential whose Env is empty and whose mode is unset, never an error, and runs no command.
- Subprocess env: modelSubprocessEnvFor removes the two inherited credential names only when the credential on ctx has mode subscription or api_key. With mode unset, or with no credential on ctx at all, it returns the plain modelSubprocessEnv result, which is the v2.309.0 behaviour (the ANTHROPIC_ keep-prefix passes the key). This covers every spawn site at once, including the post-merge reviewer and the Navigator bootstrap added in GH-6022.
- Attribution in the unset mode: the recorded billing_mode is derived, not asserted. When the daemon env carries ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN the row reads api_key; otherwise subscription. Add a DerivedBillingMode(environ) helper next to KeySourceFromEnv and use it wherever the mode is stamped for an unset credential (admission, chat turns, bootstrap, decomposed subtasks inherit as today). Explicit modes stamp their own value as today.
- Fail-closed paths are unchanged for api_key. The unset mode never pauses admission and never alerts.
- Example config: document the three states in `configs/pilot.example.yaml` under the billing block: unset = passthrough of the daemon environment (default, recommended for env-key deployments until the console sets the mode), subscription = strip inherited keys, api_key = inject the resolved key only.
- Release note line for the next tag: v2.310.0 stripped an inherited ANTHROPIC_API_KEY when billing.mode was unset; this release restores passthrough for the unset mode.

Tests to add, named exactly: TestBillingMode_UnsetKeepsInheritedKey, TestBillingMode_ExplicitSubscriptionStripsInheritedKey, TestBillingMode_UnsetDerivesAttribution, TestBillingMode_UnsetNoCredentialOnCtxPassesThrough, TestConfig_Validate_BillingUnsetAccepted.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBillingMode` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestModelSubprocessEnvFor` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/config/ -run TestConfig_Validate_Billing` passes; paste the output into the PR body.
- [ ] A test with no billing block and ANTHROPIC_API_KEY in the base env asserts the claude backend's recorded command env, the effort classifier's env and the post-merge reviewer's env all carry that key.
- [ ] A test with billing.mode subscription and the same base env asserts none of those envs carries the key.
- [ ] A test with no billing block asserts the execution row reads billing_mode api_key when the base env carries the key and subscription when it does not.
- [ ] A test calls modelSubprocessEnvFor with a ctx that carries no credential and asserts the result equals modelSubprocessEnv of the same base.
- [ ] make EffectiveMode return subscription for an empty mode -> TestBillingMode_UnsetKeepsInheritedKey fails
- [ ] strip the credential names regardless of the credential's mode -> TestBillingMode_UnsetNoCredentialOnCtxPassesThrough fails
- [ ] stamp unknown instead of the derived value for the unset mode -> TestBillingMode_UnsetDerivesAttribution fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The runtime switch (A2), the usage-limit fallback (B), budget scope (C). Any console change that writes a billing block into tenant configs (hosted follow-up, TASK-631 D4). Any change to key_source.

## Refs

- v2.310.0 release (PR#6017, #6024, #6025, #6027); #5955 item 3; Navigator TASK-633 in this repo

### LEG F5 — repo: qf-studio/pilot — title: fix(executor): spawn sites with no billing credential on ctx follow the configured billing mode — an explicit subscription block strips the inherited key everywhere

## Context

PR#6036 (GH-6032) made modelSubprocessEnvFor in `internal/executor/model_env.go` (near line 216) return the plain scrub when the ctx carries no BillingCredential or an unset one. That restores passthrough for the unset mode, as intended. It also changes the explicit subscription mode at every spawn site that passes a bare context: the effort classifier (`internal/executor/model_routing.go` near lines 73 and 220 call Classify with context.Background(), reaching `internal/executor/effort_classifier.go` near line 116), the complexity classifier (`internal/executor/complexity_classifier.go` near line 75), the subtask parser (`internal/executor/subtask_parser.go` near line 71), the intent judge (`internal/executor/intent_judge.go` near line 208), the epic planner (`internal/executor/epic.go` near line 284) and parallel (`internal/executor/parallel.go` near line 271). Before PR#6036 those sites stripped ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN unconditionally; now they keep them. The spec sentence of GH-6032 says an explicit subscription mode strips inherited keys, so this is a regression of that sentence. No deployment runs an explicit subscription block together with an exported key today (box: no billing block, OAuth token; tenants: no block), which is why the merge went ahead. Found by the pre-PR shadow review of PR#6036 (first blocking defect) and confirmed in the Navigator review.

probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnvFor\(
probe: present qf-studio/pilot BillingModeUnset[[:space:]]+BillingMode
probe: present qf-studio/pilot effortClassifier\.Classify\(context\.Background\(\)
probe: absent qf-studio/pilot func[[:space:]]SetProcessBillingMode\(

## Scope

- Process-wide default: add SetProcessBillingMode(mode BillingMode) and processBillingMode() in `internal/executor/billing_mode.go`, guarded by a mutex, default BillingModeUnset. NewBillingResolver records cfg.EffectiveMode() there, and the Runner's billing setup calls it once at construction, so the daemon's configured mode is known to every spawn site without threading a credential.
- modelSubprocessEnvFor: when the ctx carries no credential, use processBillingMode() as the mode in force: unset → plain scrub (as today); subscription → strip the two credential names, append nothing; api_key → strip the two names and append nothing (a bare ctx in api_key mode is still the GH-6032 sibling gap A; log one WARN "model subprocess spawned without a billing credential in api_key mode" with the caller's component, no behaviour change beyond the strip). A credential on the ctx wins over the process default as today.
- Minors from the PR#6036 pre-PR review: the validation error text in `internal/config/config.go` (near line 1292) names the accepted states as subscription, api_key or empty; the comment at `internal/executor/model_env.go` near line 201 reads "the Anthropic credential variables an explicit billing mode owns"; TestModelSubprocessEnvFor_SubscriptionStripsInheritedKey in `internal/executor/billing_mode_test.go` loses its one-entry map loop.
- No new config key. Tests must reset the process mode with t.Cleanup.

Tests to add in a new file, plain text: internal/executor/billing_process_mode_test.go — named exactly: TestBillingMode_ExplicitSubscriptionStripsAtBareCtxSites (process mode subscription, ANTHROPIC_API_KEY in the env, drive ModelRouter.resolveComplexity with a fake CLI and assert the effort classifier's recorded env has no ANTHROPIC_API_KEY and no ANTHROPIC_AUTH_TOKEN), TestBillingMode_UnsetProcessModeBareCtxPassesThrough (process mode unset, same drive, the key is present), TestBillingMode_APIKeyProcessModeBareCtxStripsAndWarns (process mode api_key, bare ctx, key stripped, WARN logged once), TestBillingMode_CtxCredentialWinsOverProcessMode (process mode subscription, ctx credential api_key with a key, the resolved key is present and the inherited one is not).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBillingMode_` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestModelSubprocessEnvFor` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/config/ -run TestConfig_Validate_Billing` passes; paste the output into the PR body.
- [ ] make modelSubprocessEnvFor ignore the process mode when the ctx has no credential -> TestBillingMode_ExplicitSubscriptionStripsAtBareCtxSites fails
- [ ] make the process mode win over a credential on the ctx -> TestBillingMode_CtxCredentialWinsOverProcessMode fails
- [ ] drop the WARN in api_key mode with a bare ctx -> TestBillingMode_APIKeyProcessModeBareCtxStripsAndWarns fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Threading a credential through the classifier and planner call paths (that is the real fix for gap A and belongs to the TASK-631 A2 runtime switch), any change to the unset-mode passthrough, attribution, the console, or key_source.

### LEG F6 — repo: qf-studio/pilot — title: fix(executor): billing-mode residue — the billing key alert reaches CLI runners and the post-merge reviewer with the right text, the bootstrap scaffolds before it resolves a key, a failed delete keeps the stashed key, subtask cost rows are asserted

## Context

Post-merge reviews of PR#6024 (F1) and PR#6025 (F2), both APPROVE-WITH-NOTES, verified 2026-11-04 on main after F3 (PR#6027), F4 (PR#6036) and F5 (PR#6046) landed. These items are still open:

1. Alert text. handleBillingKeyUnavailable in `internal/alerts/engine.go` formats "Billing API key unavailable (task %s): task admission paused until it resolves: %s" for every emitter. Since F2 the alert is also raised by billingContext in `internal/executor/billing_mode.go` (near line 284) from chat turns, CLI commands and the Navigator bootstrap, where admission is not paused. The text is wrong for those.
2. CLI runners drop the alert. Only the daemon path installs an alert processor (`cmd/pilot/main.go` near line 3371, SetAlertProcessor). `cmd/pilot/commands.go` near line 880 and `cmd/pilot/navigator.go` near line 67 only call SetLogStore, so emitAlertEvent logs one GH-3734 warning and drops the event. A fail-closed `pilot navigator` bootstrap in api_key mode therefore alerts nobody.
3. Post-merge reviewer. A resolver failure in Review (`internal/executor/post_merge_reviewer.go`, the branch before WithBillingCredential near line 426) returns an error; `internal/autopilot/post_merge_review.go` near line 302 records the postMergeReviewError metric and a warn log. No billing_key_unavailable alert is raised, so a dead key silently stops every post-merge review.
4. Bootstrap order. In `internal/executor/navigator_bootstrap.go` billingContext (near line 107) runs before ensureNavigatorScaffold (near line 113). On the fail-closed path the project is left with no scaffold; the scaffold needs no model and should exist regardless.
5. Discard after a failed delete. In `internal/executor/dispatcher.go` the decomposed-parent guard (near line 1070) calls discardBillingCredential after DeleteExecution even when the delete failed, so the still-queued row loses its key and the worker must resolve again.
6. Test depth. TestDecompose_SubtasksInheritBillingMode in `internal/executor/billing_fastfollow_test.go` only t.Logf's (near line 923) when fewer subtasks record cost than were created, so the acceptance "every subtask records cost under billing_mode api_key" is not pinned.

probe: present qf-studio/pilot task admission paused until it resolves
probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]billingContext\(
probe: present qf-studio/pilot func[[:space:]]ensureNavigatorScaffold\(
probe: present qf-studio/pilot subtasks recorded cost
probe: absent qf-studio/pilot func[[:space:]]installCLIAlertProcessor\(

## Scope

- Alert text: AlertEvent gains a boolean AdmissionPaused (set true only by the admission emitter in `internal/executor/billing_admission.go`, false from billingContext). handleBillingKeyUnavailable formats "task admission paused until it resolves" only when it is true; otherwise "this run failed; admission is unaffected", naming the source (chat, cli, bootstrap, reviewer) from a new Source field.
- CLI runners: add installCLIAlertProcessor(runner, cfg) in a new file, plain text: cmd/pilot/alerts_cli.go that builds the same alert processor the daemon builds near line 3363 of `cmd/pilot/main.go` when alerts are enabled in cfg, and call it next to every CLI SetLogStore in `cmd/pilot/commands.go` and `cmd/pilot/navigator.go`. When alerts are disabled the runner keeps the warn-once drop.
- Post-merge reviewer: PostMergeReviewer gains SetAlertEmitter(func(AlertEvent)); on a resolver failure Review emits AlertEventTypeBillingKeyUnavailable with Project, Source "reviewer" and the PR number in the error text, then returns the error as today. `cmd/pilot/post_merge_reviewer.go` wires the emitter to the daemon's alert processor.
- Bootstrap: call ensureNavigatorScaffold before billingContext; a scaffold error still returns before any model spawn.
- Dispatcher: in the decomposed-parent guard, discardBillingCredential runs only when DeleteExecution returned nil; on error the row keeps its key and the guard logs the retained stash.
- Test: TestDecompose_SubtasksInheritBillingMode asserts len(rec.costCalls) == len(res.Subtasks) and that every cost call carries billing_mode api_key; the t.Logf goes.

Tests to add, named exactly: TestBillingAlert_TextByAdmissionPaused (`internal/alerts/engine_test.go`), TestCLIRunner_InstallsAlertProcessor (`cmd/pilot/`, new file plain text: cmd/pilot/alerts_cli_test.go; a navigator bootstrap in api_key mode with a failing api_key_command delivers one billing_key_unavailable event to a fake processor), TestPostMergeReviewer_ResolverFailureAlerts (`internal/executor/post_merge_reviewer_test.go`), TestNavigatorBootstrap_ScaffoldBeforeBilling (`internal/executor/billing_gh6022_test.go`: failing resolver, scaffold exists afterwards), TestDispatcher_DecomposedParentGuardKeepsKeyOnFailedDelete (`internal/executor/billing_fastfollow_test.go`).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestBilling` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/alerts/ -run TestBillingAlert` passes; paste the output into the PR body.
- [ ] `go test -race ./cmd/pilot/ -run TestCLIRunner_InstallsAlertProcessor` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestNavigatorBootstrap_ScaffoldBeforeBilling` passes; paste the output into the PR body.
- [ ] format the paused text for every emitter -> TestBillingAlert_TextByAdmissionPaused fails
- [ ] drop the installCLIAlertProcessor call in navigator.go -> TestCLIRunner_InstallsAlertProcessor fails
- [ ] remove the emit on resolver failure -> TestPostMergeReviewer_ResolverFailureAlerts fails
- [ ] put billingContext back before ensureNavigatorScaffold -> TestNavigatorBootstrap_ScaffoldBeforeBilling fails
- [ ] discard regardless of the delete result -> TestDispatcher_DecomposedParentGuardKeepsKeyOnFailedDelete fails
- [ ] replace the cost assertion with t.Logf -> TestDecompose_SubtasksInheritBillingMode fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The runtime switch (A2), the usage-limit fallback (B), budget scope (C), any console change, key_source, the unset-mode passthrough (F4) and the process-wide mode fallback (F5), the Transition-failure backoff (pinned by TestProcessQueue_TransitionFailureDoesNotSpin).

## Refs

- F1 → https://github.com/qf-studio/pilot/issues/6021
- F2 → https://github.com/qf-studio/pilot/issues/6022
- F3 → https://github.com/qf-studio/pilot/issues/6026
- F4 → https://github.com/qf-studio/pilot/issues/6032
- F5 → https://github.com/qf-studio/pilot/issues/6038
- F6 → https://github.com/qf-studio/pilot/issues/6050

- Parent: TASK-631 · [pilot#6016](https://github.com/qf-studio/pilot/issues/6016) → PR#6017 (merged 2026-11-03 12:14Z)
- Post-merge review: Opus APPROVE-WITH-NOTES on PR#6017, 12:18Z
- Verification: navigator-research on main f72c25810, 2026-11-03

**Last Updated**: 2026-11-03
