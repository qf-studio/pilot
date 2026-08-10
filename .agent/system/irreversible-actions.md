# Irreversible-action inventory (TASK-459 Phase 1)

**Status**: Phase 4 of 4 (GH-4823) landed 2026-08-10 (Phase 5's false-success class split to TASK-460, 2026-08-08). Phase 1 built the inventory + `Verdict`
contract with no behaviour change. Phase 2 migrated the CI-failure path —
`handleCIFailed`'s ladder (family 1's `MaxCIFixIterations` close, family 3's
pre-merge `CreateFailureIssue`, family 8's zero-evidence `escalateAndHold`)
and the post-merge CI rung (family 3's post-merge `CreateFailureIssue`, plus
a *new* family-8 zero-evidence hold that didn't exist before Phase 2 — the
post-merge path previously spawned a fix issue for any `CIFailure` with no
classification at all) — to gate on `Verdict.AuthorizesDestructive()`
instead of raw `FailureClass`/string/nil-check comparisons. Their rows below
are marked `typed-verdict` accordingly. Phase 3 (GH-4817, this leg) migrated
family 4/5/6/7's executor/dispatcher/poller sites named in the Phase-1 §9
findings: `recoverStaleRunningTasks`/`recoverStaleQueuedTasks` now write
typed `stalled`/`canceled` instead of collapsing every boot-time orphan to
`failed`; the GitLab and CLI GH-3053 "no commit, no PR" demotions now
consult `IsNoArtifactExplainedOutcome` (typed outcome check) before
inferring failure from an absent artifact alone (closing the
`side-effect-inferred` gap flagged in family 7's `IsTerminalByDesignStatus`
row below); five additive label/comment-write sites (family 5) now gate on
a fresh `fetchIssueState` re-read of the target issue and skip the write on
positive evidence it's already closed, failing open on a lookup error; and
`checkWorktreePruned` (finish_tripwires.go) now excludes `superseded`/
`canceled` rows from its "commits but no PR" violation, alongside the
pre-existing `decomposed` carve-out. Rows below are updated in place per
site; see the row-level Evidence column for the `re-read` tag Phase 3 added.
All rows not called out as `typed-verdict` or Phase-3-updated are still
Phase 1-only classification. Phase 4 (GH-4823) exported the duplicated
approval-source vocabulary to one table, fixed the explicit
`approval_source: ""` overlay bug, replaced two reason-string-as-protocol
routing decisions with typed carriers (`scope_release.go`'s retry-vs-park
fork, `handleAwaitApproval`'s expiry-decision decider source), added
regression coverage pinning `escalateStalledTask`'s idempotence-key
stability, shipped `scripts/check-destructive-calls.sh` (grep gate against
new ungated call sites and stray `Verdict{}` construction), and labeled
family 5/6's success-side rows below ("green CI — decorative for the
delivery claim") as the TASK-460 hook. See
`.agent/sops/quality/irreversible-actions.md` for the invariant and how to
add a new destructive call site.

**Purpose**: Every call site in the daemon that closes a PR, deletes a
branch, spawns a fix issue, burns retry budget, writes a terminal label, or
otherwise takes an action a human can't cheaply undo. This table is the
input to Phase 2 (`handleCIFailed` migration to `Verdict`), Phase 3
(executor/dispatcher/poller), and Phase 4 (the `check-destructive-calls.sh`
grep gate). Do not let it silently drift from the code — when a Phase
2–4 leg migrates a site, update its row's evidence column here rather than
deleting the row (the row is also the record of *what changed*).

**Reversibility tiers**:
- **irreversible** — cannot be undone by the daemon itself; recovering
  requires a human noticing and manually reopening/recreating/re-running.
- **costly-reversible** — technically undoable (relabel, reopen, re-dispatch)
  but the undo burns real cost first (discarded executor work, a second
  fix-issue cycle, operator attention to notice it happened).
- **cheap-reversible** — undoing is a label flip or a log line; no discarded
  work.

**Evidence column** uses these tags: `typed` (a `FailureClass` or equivalent
enum), `raw-string`/`raw-bool` (a string/bool comparison with no typed
vocabulary), `counter` (integer threshold compare), `nil-check` (presence/
absence of an error or empty slice), `re-read` (the code re-fetches current
state immediately before acting, the strongest evidence available),
`side-effect-inferred` (concluded from something *not happening*, e.g. "no
PR appeared" — pattern 2 from the task brief).

**Authoritative/decorative** records whether the evidence feeding the action
is filtered through `CIMonitor.isScopedCheck` (`ci_monitor.go:799`) — i.e.
gated by the project's `required_checks`/`ci_checks.required` allowlist. If
a project sets that allowlist, any check *not* on it is invisible to that
project's `Controller`/`CIMonitor` instance — evidence "authoritative" here
means "positive for the checks it actually looked at", not "the repo's CI
is actually green". This scoping is per-`Controller` (one per project), so
the same code path is authoritative for a project with no/correct allowlist
and decorative-blind-spot for a project with a stale one. See
`.agent/system/FEATURE-MATRIX.md` § required-checks and the pitfall memory
`required-checks-allowlist-makes-other-gates-decorative` (confidence 0.95,
unfixed as of 2026-07-25 — founder config decision, not touched by this
task).

---

## 1. `ClosePullRequest` — PR close (irreversible: discards the PR; the
branch/commits may also be deleted immediately after, see family 2)

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/controller.go:2550` (`handleCIFailed`, `MaxCIFixIterations` rung) | autopilot | irreversible | Discards the PR + all executor work on it (the #4765/#4768/#4770 incident shape); triggers the follow-on branch-delete-adjacent cleanup | `typed-verdict` (TASK-459 Phase 2) — the counter rung is unreachable unless `verdict.AuthorizesDestructive()` gated earlier in the function (controller.go:2501); the close trigger itself is still `counter`-driven (`iteration >= MaxCIFixIterations`) but is now provably behind positive-evidence, not just behind the platform-breaker/upstream gates | **Authoritative for the iteration count itself (scope-agnostic), but the whole `handleCIFailed` function is only entered because `CIMonitor.CheckCI`/`checkRequiredChecks` aggregated `CIFailure` from in-scope checks only** — a failing but unscoped check never reaches this rung at all |
| `internal/autopilot/controller.go:2579` (`handleReviewRequested` tail) | autopilot | irreversible | Discards the PR | `raw-string`/`raw-bool` — driven by `ListPullRequestReviews`/`GetPullRequestComments` review state, not CI | N/A — review path, not CI-scoped |
| `internal/autopilot/controller.go:2784` (`handleReviewRequested`, `ReviewFeedback.MaxIterations` rung) | autopilot | irreversible | Discards the PR | `counter` — same `parseAutopilotIteration` mechanism as 2453 | N/A |
| `internal/autopilot/controller.go:2842` (`handleReviewRequested` fallthrough, immediately followed by branch delete at 2847) | autopilot | irreversible | Discards the PR + branch | `raw-bool`/`counter` per the preceding review-state checks | N/A |
| `internal/autopilot/controller.go:5103` (`closeConflictSourceIssueClosed`, via `handleMergeConflict`) | autopilot | irreversible (but the most rigorously gated close in the codebase) | Discards the PR — but only after confirming the work already landed elsewhere | `re-read` + `raw-string` — gates on `GetIssue().State == "closed"` *and* `checkPRWorkOnMain` (a real git-reachability check that the PR's commits are already on main), not an inference | N/A — merge-conflict path |
| `internal/autopilot/controller.go:5149` (`closeAndReexecute`, fallback rung of `handleMergeConflict`) | autopilot | costly-reversible — closes the PR but restores the issue to dispatch-ready for re-execution | Discards the PR; re-execution burns a fresh executor run | `nil-check` — reached only after auto-rebase (`UpdatePullRequestBranch`) failed *and* `attemptMechanicalConflictResolution` (local merge replay) found a non-trivial conflict, not a raw string match | N/A |

## 2. Branch deletion (`DeleteBranch`)

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/controller.go:2847` (`handleReviewRequested` tail, follows the 2842 close) | autopilot | irreversible | Deletes the branch immediately after an unverified close; error only `Debug`-logged, no re-verification | `side-effect-inferred` — follows directly from the close with no independent re-check | N/A |
| `internal/autopilot/controller.go:3359` (post-successful-merge cleanup) | autopilot | cheap-reversible — PR is already merged, branch content is preserved in main; GitHub itself may have already deleted it (404/422 ignored) | Minimal — this is the low-risk case, deletion only follows a confirmed merge | `re-read` (implicit — only reached after `MergePR` succeeded) | N/A |
| `internal/autopilot/controller.go:6813` (`finalizeExternalClose`, via `checkExternalMergeOrClose`) | autopilot | irreversible | Deletes the branch of a PR closed *outside* the daemon's own action | `re-read` — re-fetches the PR immediately before deleting and aborts if state flipped back to open; gated upstream by a consecutive-reads-within-grace-window counter (`ClosedReadCount` vs `externalCloseConfirmThreshold`, GH-4570) — the best-evidenced deletion in the codebase | N/A — external-close/orphan-tracking path |
| `internal/executor/git.go:173` (local error-cleanup) / `internal/executor/git.go:1260` (impl) | executor | cheap-reversible | Local worktree branch only, not the remote/PR branch | `nil-check` | N/A (not a GitHub API call) |

## 3. `CreateFailureIssue` / fix-issue spawning

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/controller.go:2628` (`handleCIFailed` main rung) | autopilot | costly-reversible — a spurious issue can be closed by a human, but burns a full executor dispatch first (the #4766/#4769/#4775 incident shape: ~$ per junk fix run) | Executor $ + operator triage time | `typed-verdict` (TASK-459 Phase 2) — unreachable unless `verdict.AuthorizesDestructive()` gated earlier (controller.go:2501); `failedChecks` feeding the issue body is still `CIMonitor.GetFailedChecks`, already `isScopedCheck`-filtered | **Yes — decorative for unscoped checks.** The issue body's diagnosis (failed-check names + logs) only ever names checks the allowlist let through; an actually-failing unlisted check contributes zero evidence to the spawned issue |
| `internal/autopilot/controller.go:4122` (post-merge CI failure rung) | autopilot | costly-reversible | Same as above, post-merge variant | `typed-verdict` (TASK-459 Phase 2) — unreachable unless `postMergeVerdict.AuthorizesDestructive()` gated earlier, same construction as the pre-merge rung. **GH-4813**: also now unreachable for any `postMergeClass.IsInfra()` verdict — the post-merge path previously had no analog of `maybeRetryInfraFailure`/family-4's `internal/autopilot/controller.go:2662`, so an evidenced infra-class post-merge failure (a runner outage on the post-merge SHA, not the repo's code) fell straight through to this rung; `maybeRetryPostMergeInfraFailure` now intercepts it first (rerun via `RerunFailedJobs`, budget-tracked) and, if rerun plumbing can't reach the SHA, routes to `escalateAndHold` (family 8) instead — never this rung | Yes, same scoping |
| `internal/autopilot/feedback_loop.go:156` (`FeedbackLoop.CreateFailureIssue`, the shared implementation both rungs above call) | autopilot | — | — | Has its own dedup guard: SQLite claim (`ClaimSpawnedFix`, GH-4307/#4319) + GitHub-search belt-and-suspenders (`SpawnedFixExists`) so two ticks racing on the same failure spawn exactly one issue — this guard is about *duplication*, not about evidence quality | Inherits caller's scoping |
| `internal/executor/epic.go:1501` (`subIssueCreator.CreateIssue`, epic decomposition) | executor | costly-reversible | Different family — decomposition sub-issue spawn, not CI-fix-driven | `raw` decomposition logic, not CI evidence | N/A |
| `internal/comms/issue_intake.go:144` (generic issue-intake `CreateIssue`) | comms | costly-reversible | Adapter-driven (e.g. Slack), not autonomous-daemon-invoked in the CI-fix sense | N/A | N/A |

## 4. Retry/repick budget counter increments (irreversible in the sense that budget burn cannot be un-burned once spent)

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/controller.go:2662` (`maybeRetryInfraFailure`, `InfraRerunCount`) | autopilot | costly-reversible — resets on new push (keyed to `HeadSHA`) but exhausting it before then routes to the destructive `handleCIFailed` path | One rerun budget slot; exhaustion escalates to close/fix-issue rungs | `typed` — gated by `classifyPRFailure == FailureClassInfra*`, itself fed by `isScopedCheck`-filtered `GetFailedCheckLogsByCheck` | **Yes** — an infra-signature failure on an unscoped check never gets a rerun budget in the first place (invisible upstream) |
| `internal/autopilot/controller.go` (`maybeRetryPostMergeInfraFailure`, `PostMergeInfraRerunCount`, GH-4813) | autopilot | costly-reversible — resets on new `mainSHA` (keyed to `PostMergeInfraRerunSHA`, independent of the pre-merge budget above) but exhausting it routes to `escalateAndHold`, **not** `CreateFailureIssue` (unlike the pre-merge budget's exhaustion path) | One rerun budget slot; exhaustion escalates to the family-8 hold, never the fix-issue rung | `typed` — gated by `postMergeClass.IsInfra()`, same `classifyPRFailure`/`isScopedCheck`-filtered evidence as the pre-merge budget above | **Yes** — same scoping as the pre-merge budget |
| `internal/autopilot/controller.go:3232` (`handleMerging`, `MergeAttempts`, capped at 3260 by `MaxMergeAttempts`) | autopilot | costly-reversible — cap exhaustion becomes terminal (`StageFailed`), requiring human intervention | Merge-retry budget; exhaustion parks the PR | `raw-bool` (success/fail of `MergePR`), but gated by a live `CheckCI` re-validation immediately before (3222-3229) | **Yes** — the live re-check is itself `isScopedCheck`-filtered; an unscoped check still failing at merge time is invisible to this gate |
| `internal/autopilot/controller.go:4873` (`handleMergeConflict`, `RebaseAttempts`, GH-3715) | autopilot | costly-reversible | Rebase-retry budget; exhaustion routes to `escalateAndHold` | `raw-bool` — success/fail of `UpdatePullRequestBranch` | N/A — not CI-scoped |
| `internal/executor/dispatcher.go:1787` (consecutive-repick backoff counter, persisted via `SetRepickBackoffState`) | executor | costly-reversible — exponential-backoff-then-hard-cap ladder; hard cap routes to `escalateStalledTask` | Repick budget | `counter` — raw drop-count | N/A — pre-GitHub, executor-level |
| `internal/executor/title_rejection.go:45-56` (consecutive-same-title-rejection counter) | executor | costly-reversible | Escalates to `pilot-failed`+`pilot-title-rejected` at threshold 2 | `raw-string` — exact SHA-256(title) match; a different title resets to 1 | N/A |

## 5. Terminal label writes (routing-state changes — cheap-reversible individually, but drive downstream irreversible behaviour)

| Site | Subsystem | Reversibility | Blast radius | Evidence |
|---|---|---|---|---|
| `internal/autopilot/controller.go:2977` | autopilot | cheap-reversible | `labelParkedAwaitingApproval` on config-gap (no approval channel configured) | `nil-check` on config |
| `internal/autopilot/controller.go:3301-3308` | autopilot | cheap-reversible | `LabelDone` add / `LabelInProgress`+`LabelFailed` remove on normal successful merge/close | Normal success path (`re-read` — post-merge). **Success-side evidence: "green CI — decorative for the delivery claim"** (TASK-459 Phase 4 scope decision, 2026-08-08) — the re-read confirms the merge happened and CI was green, not that the merged change actually satisfies the ticket; TASK-460 inherits this row for any future delivery-evidence check |
| `internal/autopilot/controller.go:3702-3709` | autopilot | cheap-reversible | `pilot-done` on decomposed-epic parent, gated by count-verified `openSubIssueCount` | `counter` — positive count check, not inferred. **Success-side evidence: "green CI — decorative for the delivery claim"** (TASK-459 Phase 4 scope decision, 2026-08-08) — the sub-issue count proves every child closed, not that the parent epic's intent was actually satisfied by their combined work; TASK-460 inherits this row (decomposed-epic parent close) for any future delivery-evidence check |
| `internal/autopilot/controller.go:5157-5165` (`closeAndReexecute`) | autopilot | cheap-reversible | Restores dispatch-ready labels after unresolved-conflict close (family 1) | Inherits family-1 evidence |
| `internal/autopilot/controller.go:5204-5215` (`escalateAndHold`) | autopilot | cheap-reversible (label only) — but see family 8 for what it *prevents* | Adds `labelNeedsHuman` + caller labels; sets `Stage = StageFailed`; fires `EventTypeTaskFailed` alert (pages a human) | Inherits caller's evidence (see family 8 table) |
| `internal/autopilot/controller.go:6641-6649` (external-merge twin of 3301-3308, via `checkExternalMergeOrClose`) | autopilot | cheap-reversible | `LabelDone` add, `LabelInProgress`/`LabelFailed` remove, `clearRetryLabels` | `raw-bool` — `PullRequest.Merged` field read directly, not inferred |
| `internal/autopilot/controller.go:6930-6992` (`notifyExternalClose`) | autopilot | cheap-reversible (label), but gates family-7 ledger writes | Splits `pilot-superseded` vs `pilot-failed` on `prState.TerminalLabel == LabelSuperseded` | `raw-string` compare against a flag set upstream (by family-1's `closeConflictSourceIssueClosed`), not re-evidenced here |
| `internal/autopilot/controller.go:6863` | autopilot | cheap-reversible | Generic `RemoveLabel` in retry-label cleanup helper | — |
| `internal/autopilot/controller.go:7077-7086` | autopilot | cheap-reversible | Generic terminal-label-application helper shared by multiple ladder rungs | Inherits caller's evidence |
| `internal/executor/title_rejection.go:220` (`postTitleRejectionEscalation`) | executor | cheap-reversible | `pilot-failed`+`pilot-title-rejected` on 2nd consecutive same-title rejection | `raw-string` exact-hash match (family 4), **plus `re-read` (GH-4817 Phase 3 Task 5c)** — `fetchIssueState` gate skips the comment+labels on positive evidence the issue is already closed, fails open on lookup error |
| `internal/executor/dispatcher.go:2036` (`surfaceStalledIssue`, via `escalateStalledTask`) | executor | cheap-reversible | `pilot-blocked` add, `pilot-failed`+`pilot-in-progress` remove | `counter` — raw drop-count threshold; idempotent via matching prior `Error` string. **Plus `re-read` (GH-4817 Phase 3 Task 5a)** — `fetchIssueState` gate skips the label write on positive evidence the issue is already closed, fails open on lookup error |
| `cmd/pilot/handlers.go:837` (`notifyTaskStartedSDK` call, SDK-dispatch path) | cmd/pilot | cheap-reversible | `pilot-in-progress` add + start comment on SDK-poller dispatch (GH-4687) | `re-read` (GH-4817 Phase 3 Task 5b) — gated on `issueState != githubSDK.StateClosed`; `issueState` defaults to `""` (unknown) on fetch failure or nil `specClient`, so the comparison is against the positive `closed` value only, preserving fail-open on unresolvable state |
| `internal/executor/epic.go:756` (`handleSubIssueCoverageGap`) | executor | cheap-reversible | `pilot-needs-clarification` add + coverage-gap comment on the parent issue | `counter`-adjacent (planned vs. created sub-issue count mismatch), **plus `re-read` (GH-4817 Phase 3 Task 5d)** — `fetchIssueState` gate on the parent issue skips the label+comment on positive evidence it's already closed (avoids the misleading "this issue stays open" comment on an externally-closed parent), fails open on lookup error |
| `internal/autopilot/controller.go:7242` (`notifyExternalClose`, label-correction block only — the informational comment below it still posts unconditionally) | autopilot | cheap-reversible | `LabelInProgress`/`LabelFailed` remove + issue-label add, on external PR close | `raw-string` (`prState.TerminalLabel`), **plus `re-read` (GH-4817 Phase 3 Task 5e)** — reuses the `issue`/`err` already fetched earlier in the function for the pilot-done check (no fresh GitHub call) and skips the label writes when `issue.State == github.StateClosed`; the informational comment is deliberately left unconditional since a closed issue has no label/retry state left to protect, only history worth recording |
| `internal/adapters/github/notifier.go:34,75,81,86,122,127,147` | adapters/github | cheap-reversible | Baseline (non-autopilot) issue-lifecycle labels on the dispatch/execution-complete path | Execution result (success/fail), not CI-check-scoped |
| `internal/adapters/github/cleanup.go:336,444,521` | adapters/github | cheap-reversible | Background sweep removing stale labels on closed issues (extended by #4800/GH-4794 to strip `pilot-retry-ready`) | Time/state-based sweep |
| `cmd/pilot/commands.go:1462,1499,1502,1515,1525,1546` | cmd/pilot CLI | cheap-reversible | Operator-invoked `pilot task cancel`/label commands | N/A — manual, not autonomous-daemon-invoked; distinct category, out of scope for the daemon decision ladder proper |

## 6. PR merge (`MergePullRequest`) — irreversible (a bad merge to main cannot be un-merged by the daemon; requires a human revert)

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/auto_merger.go:95` (`AutoMerger.MergePR`, the sole production merge call) | autopilot | irreversible | Bad code on main; strips retry labels on success so a future regression starts its budget fresh | `re-read` — caller (`handleMerging`, controller.go:3222-3229) re-validates CI live via `CheckCI` immediately before merging; a definitive `CIFailure` here rescinds approval instead of merging. **Explicitly fails open on a transient CI-check API error** (documented: "must not block a legitimate merge on a flaky status-check call"). **Success-side evidence: "green CI — decorative for the delivery claim"** (TASK-459 Phase 4 scope decision, 2026-08-08) — this gate is sound for *should we block this merge*, but green CI at merge time is not evidence the change satisfies the ticket's intent; TASK-460 inherits this row for any future delivery-evidence check | **Yes — decorative for unscoped checks.** `CheckCI`'s aggregate is built from `isScopedCheck`-filtered check runs; an out-of-allowlist check still red at merge time is invisible to this final gate |

## 7. Cancel/supersede/stall/failed writes to the executions ledger (`internal/memory/store.go`)

| Site | Subsystem | Reversibility | Blast radius | Evidence |
|---|---|---|---|---|
| `internal/memory/store.go:1383` `ReclassifyCompletionAsFailed` | memory store | costly-reversible — dashboard/history correctness, not code | Called from `controller.go:6963` (`notifyExternalClose`), when a PR closes externally without merge and `TerminalLabel != LabelSuperseded` |
| `internal/memory/store.go:1407` `ReclassifyCompletionAsSuperseded` (GH-4701) | memory store | costly-reversible | Called from `controller.go:6961`, sibling branch when `TerminalLabel == LabelSuperseded` |
| `internal/memory/store.go:1439` `TerminateNonTerminalExecution` | memory store | costly-reversible | Called from `controller.go:6986`, terminates a still-running row as `failed` when a close is observed before completion |
| `internal/memory/store.go:1460` `TerminateNonTerminalExecutionAsSuperseded` | memory store | costly-reversible | Called from `controller.go:6984`, superseded sibling |
| `internal/memory/store.go:2183` `UpdateExecutionStatus` | memory store | costly-reversible | Generic unconditional status writer; callers include `dispatcher.go:541` `recoverStaleRunningTasks` (boot-time running-orphan reap) and `dispatcher.go:778` `recoverStaleQueuedTasks` (boot-time queued-orphan reap), both **typed as of GH-4817 Phase 3 Tasks 1/2** — `recoverStaleRunningTasks` now writes `stalled` and `recoverStaleQueuedTasks` now writes `canceled`, replacing a prior blanket `failed` write that collapsed every boot-time orphan (including ones later explainable as superseded/canceled work) into the same non-terminal-by-design bucket; and `dispatcher.go:1936` (`escalateStalledTask` -> `stalled`, family 4/8) |
| `internal/memory/store.go:2247` `UpdateExecutionStatusIfNotTerminal` | memory store | costly-reversible | CAS-guarded (race-safe) writer; callers `dispatcher.go:623,803,926`, `lifecycle.go:220,318,547` (`ExecutionLifecycle.Cancel`, the `pilot task cancel` CLI path, GH-4586 — refuses if already `running` or already terminal) |
| `internal/memory/store.go:2320` `UpdateExecutionStatusByTaskID` | memory store | costly-reversible | Task-ID-keyed variant |
| `internal/executor/dispatcher.go:79` `IsTerminalByDesignStatus` (GH-4794, #4800) / `dispatcher.go:92` `IsNoArtifactExplainedOutcome` (GH-4817, TASK-459 Phase 3 Tasks 3/4) | executor | N/A — this is the fix, not the hazard | Converts three consumers' classification from **`side-effect-inferred`** ("no PR produced" read as failure) to **`typed`** (consult the recorded ledger status vocabulary — `no_op`/`superseded`/`canceled` vs `failed` — directly). `IsTerminalByDesignStatus` (superseded/canceled only) is consumed by `cmd/pilot/handlers.go:560,675,873`. `IsNoArtifactExplainedOutcome` (superset: adds `no_op`) is the GH-4817 Phase 3 addition consumed by the two GH-3053 "no commit, no PR" demotion sites named in the TASK-459 brief's root pattern 2 — `cmd/pilot/handlers.go:597` (`handleGitlabIssueWithResult`, guards `issueResult.Success = false`) and `cmd/pilot/commands.go:1527` (`newGitHubRunCmd`'s CLI no-artifact check) — both previously inferred failure from an absent commit/PR alone with no outcome check at all. The general case is what `Verdict.Scope`+evidence enforcement in later phases generalizes |
| `internal/executor/finish_tripwires.go:264` `checkWorktreePruned` | executor | N/A — a false-positive-avoidance guard, not itself a destructive write | Flags a "commits with no PR" tripwire violation for rows that requested a PR but produced none. **GH-4817 Phase 3 Task 6** extended the pre-existing `decomposed`-parent carve-out to also exclude `superseded`/`canceled` rows — a superseded/canceled execution can legitimately have a commit with no PR (the work was intentionally abandoned mid-flight), and flagging it as a tripwire violation was a false positive of the same `side-effect-inferred` shape as the GH-3053 sites above. A plain `failed` row still trips the violation (guard is deliberately narrow — see `TestCheckWorktreePruned`'s "guard is narrow" subtest) |

## 8. `escalateAndHold` and hold/escalation (semi-destructive: never closes/deletes/merges, but pages a human and parks the PR — operator-costly)

Defined once at `internal/autopilot/controller.go:5349`. Call sites:

| Site | Reason string | Evidence gating this call |
|---|---|---|
| `controller.go:2508` | "CI failure with zero gathered evidence" | `typed-verdict` (TASK-459 Phase 2) — `!verdict.AuthorizesDestructive()`, i.e. `Class() == FailureClassUnknown` or `Evidence() == ""`, superseding the prior direct `classifyPRFailure == FailureClassUnknown` + `nil-check` comparison with the same GH-4779 invariant expressed through the shared gate helper |
| `controller.go:4117` (new in Phase 2 — no equivalent pre-Phase-2 site) | "post-merge CI failure with zero gathered evidence" | `typed-verdict` — `!postMergeVerdict.AuthorizesDestructive()`. Previously the post-merge rung had no zero-evidence gate at all; a GH-4779-style race on the post-merge check-runs re-fetch would have spawned a fix issue blind. Closes that gap using the same `newCIFailureVerdict`/`AuthorizesDestructive()` construction as the pre-merge site |
| `controller.go` (new in GH-4813 — no pre-merge equivalent reason string; pre-merge's infra-budget exhaustion instead falls through to `CreateFailureIssue`) | "post-merge CI failure classified infra" | `typed` — `postMergeClass.IsInfra()` and `maybeRetryPostMergeInfraFailure` returned false (no `StepLogClient` wired, budget exhausted, or nothing could be resolved/rerun). Closes the post-merge-only gap where an evidenced infra-class failure (GitHub's problem, not the repo's) had no non-destructive rung at all and fell straight to `CreateFailureIssue` |
| `controller.go:2613` | "CI fix size guard fired" | `counter` — production-line diff stat vs `MaxCIFixPRSize`; fails open (does not hold) if `ListPullRequestFiles` errors |
| `controller.go:2648` | "CI-fix continuation declined at preflight" | `nil-check` — `CreateFailureIssue` returned an error or a legitimate dedup-claim-in-flight `(0, nil)`; chosen over closing to avoid GH-4415's double-lost-work dead end |
| `controller.go:5074` | "auto-rebase failed" | `raw-bool` — `attemptMechanicalConflictResolution` (local merge replay) found the conflict surface isn't confined to go.mod/go.sum |
| `controller.go:5274` (`holdClosedIssueWorkNotOnMain`) | "source issue #N is closed but {situation}" | `re-read` — `checkPRWorkOnMain` reachability check errored or returned false; fail-safe against discarding unmerged work |
| `internal/executor/dispatcher.go:1811/1832/1851` (`stallTaskAfterRepickHardCap`/`StallCap`/`InfraCap`, via `escalateStalledTask` at 1911) | Three distinct named reasons (repick-hard-cap / stall-watchdog-cap / infra-cap) | `counter` — raw consecutive-drop-count thresholds, each with its own distinct reason string (deliberately not conflating "code is broken" vs "environment kept failing"); idempotent via matching the prior `Error` string exactly. This is the executor-level (pre-GitHub-PR) analog of `escalateAndHold` |

## 9. Cross-cutting findings

- **`isScopedCheck` blind spot** (`internal/autopilot/ci_monitor.go:799`) gates every evidence-gathering function that feeds families 1, 3, 4, and 6: `GetFailedChecks` (:777), `GetFailedCheckLogs`, `GetFailedCheckLogsByCheck`, `GetFailedCheckExcerpts`, and `checkRequiredChecks`/`CheckCI` itself (:715). When a project's `required_checks`/`ci_checks.required` allowlist is set, a check whose name isn't listed is **structurally invisible** to that project's entire destructive-action chain — not under-weighted, never observed. Scoping is per-`Controller` (one per project), so this is a per-project hazard, not global. Policy itself is out of scope for this task (founder config decision, `.agent/system/FEATURE-MATRIX.md`).
- **`PlatformBreaker.Observe`** (`internal/autopilot/platform_breaker.go:139`, GH-4791/#4797, landed 2026-08-07) sits upstream of the whole family-1/3/8 ladder for CI-failure handling specifically: while `Open`, `handleCIFailed` (controller.go:2385-2390) suppresses every downstream destructive action for that tick regardless of that PR's own classification. This is a cross-PR correlation gate, not a per-call-site evidence contract — it complements but does not replace the `Verdict` contract this task defines.
- **Approval-decision writes** (traced 2026-08-08, Phase 3 prep): the *write* (`Controller.SetApprovalDecision`, controller.go:3264) is properly arbitrated — the store-level CAS (`memory.SetApprovalDecision`, atomic `AND approval_decision = ''`, doc controller.go:3253-3263) is the authoritative arbiter; the in-memory field is applied only after the store write returns nil; duplicates surface as typed `memory.ErrApprovalAlreadyDecided`. The *gate* (`applyApprovalDecision`, :3209, switch at :3226) consumes the raw in-memory `prState.ApprovalDecision` string with no ledger re-read before `Stage = StageMerging` (:3229), and the expiry path (:3087-3095) *synthesizes* a decision string from a wall-clock default with no decider evidence at all. Unknown strings fail closed to `StageFailed` (:3237-3243). No `Verdict` on this path. **Phase 4 (task 4b, GH-4823) closed the expiry-path half of this gap**: `PRState.ApprovalDecisionBy` now records the decider source (the real "by" identity for the webhook/channel-tap path, `approvalDecisionSourceWallClockExpiryDefault` for the wall-clock "post-restart guard" synthesis), and `applyApprovalDecision` logs it on every branch. The store-level CAS arbitration was already sound and stayed out of scope; the gate still consumes the raw in-memory string with no ledger re-read, unchanged by design (minimal-fix instruction).
- **Release-pipeline ladder** (traced 2026-08-08): spans `handlePostMergeCI` (controller.go:4011-4273 — post-Phase-2 this rung already constructs a typed `Verdict` at :4120-4122 and holds on `!AuthorizesDestructive()` at :4200-4207) and `handleReleasing` (:4429-4733). The single retry-vs-escalate fork is `checkReleasingRetryOrEscalate` (:4794-4802): pure `ReleasingAttempts` counter vs `MaxReleasingAttempts`. `guardReleaseSHAReachable` (:5009) escalates immediately with **no retry budget** and **fails open** on API error (:5019, :5036). All escalations are family-8-shaped (`escalateReleasingFailed` :4806 — comment + `StageFailed`, never close/delete). Raw-string protocol hazards recorded for Phase 4: `isDuplicateTagError`/`isDuplicateReleaseError` (:4740-4745, `strings.Contains(err, "already exists")` — idempotence drains, low stakes, deliberately not touched by Phase 4 per the task brief's out-of-scope list) and **`internal/autopilot/scope_release.go:433`** — a retry-vs-park routing decision made by `strings.Contains(reason, postMergeCITimeoutReasonSubstr)` on a human-formatted message assembled at controller.go:4064/:4231. Reason-string-as-protocol; highest-value Phase 4 vocabulary target. **Fixed in Phase 4 task 4a (GH-4823)**: `handleScopeReleaseFailure` now takes an explicit `isTimeout bool` carried from both assembly sites; the human-formatted message stays for logs/comments only, no longer the routing protocol. `postMergeCITimeoutReasonSubstr` and the `strings.Contains` check are removed.
- **`internal/executor/watchdog.go`** (traced 2026-08-08): the stall path is already status-authoritative end-to-end — the watchdog (:27-60) only flips a flag and cancels the session ctx on wall-clock silence; the runner writes typed `Outcome == "stalled"` (runner.go:3443-3491, nil error) → `TerminalStatus` maps the typed outcome directly (runner.go:280-281, not the string-sniff fallback) → ledger `stalled` → dispatcher's `priorClaimWasStalled` (:1297) reads the recorded status → `stallTaskAfterStallCap`. No change needed. Fragile link for Phase 4: `escalateStalledTask` idempotence relies on exact `Error`-string equality (dispatcher.go:1938-1950). **Addressed in Phase 4 task 4c (GH-4823)**: rather than replace the key with a typed schema (judged disproportionate — the dynamic parts of the string are stable by construction; only a reviewed prose edit could break it, and the blast radius is one duplicate alert), added `internal/executor/gh4823_escalate_stalled_idempotence_test.go`, a regression test pinning the current idempotence key's stability across all five reason-generating escalation paths (repick hard cap, stall-watchdog cap, infra cap, deterministic failure, identical failure streak), plus a companion test (`TestEscalateStalledTask_ProseOnlyChangeBreaksIdempotence_GH4823`) that documents, rather than hides, the known limitation: a prose-only reword of the reason string still breaks idempotence and fires a duplicate alert.
- **`internal/executor/runner.go` `outcomeClassifiers`** (:245-254, signature table :228-238): ordered raw-error-substring matching is the largest remaining string-sniffing classifier feeding the ledger. Classification internals — how a status is *derived* — are out of TASK-459 scope (which governs *consumers* of recorded status); recorded here so Phase 4's vocabulary work doesn't miss it.
- **`internal/executor/lifecycle.go:274` PR-existence promotion** (GH-4404): a side-effect (`result.PRUrl != ""`) force-promotes any non-completed classification to `completed` unless the caller overrides. The inverse of the Phase-3 rule, but deliberate and documented (a shipped PR must not be lost to a late runner error). Standing documented exception — not migrated.

---

## How Phase 2+ consumes this

**Phase 2 (landed, TASK-459)** gated `handleCIFailed`'s rungs (family 1's
`controller.go:2550`, family 3's `:2628`, family 8's `:2508`) and the
post-merge CI rung (family 3's `:4122`, plus the new family-8 hold at
`:4117` that didn't exist pre-Phase-2) behind `Verdict.AuthorizesDestructive()`,
using the `Verdict` type defined in `internal/autopilot/failure_class.go`.
The gate requires both a non-`Unknown` `Class()` and non-empty `Evidence()`
— a bare `Class() != FailureClassUnknown` check was explicitly rejected
during Phase 2 review because a zero-value `Verdict{}` has `Class() ==
FailureClassUnknown` by construction but that alone doesn't rule out every
degenerate-construction path, so `Evidence()` is checked independently.
`maybeRetryInfraFailure`'s infra-retry gate deliberately does *not* require
`AuthorizesDestructive()` (retry is non-destructive and must keep admitting
`FailureClassUnknown` per GH-4779) and `PlatformBreaker.Observe` remains
upstream of and independent from this gate. Phase 3 does the same for
families 4/6/7's executor/dispatcher/poller sites. Phase 4 adds
`scripts/check-destructive-calls.sh` to keep new call sites from bypassing
the contract. This document is not re-derived per phase — update the
relevant row(s) in place as each site migrates.
