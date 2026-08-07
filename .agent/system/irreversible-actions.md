# Irreversible-action inventory (TASK-459 Phase 1)

**Status**: Phase 1 of 5 — inventory + `Verdict` contract only. No behaviour
change; no call site below is migrated in this phase.

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
| `internal/autopilot/controller.go:2453` (`handleCIFailed`, `MaxCIFixIterations` rung) | autopilot | irreversible | Discards the PR + all executor work on it (the #4765/#4768/#4770 incident shape); triggers the follow-on branch-delete-adjacent cleanup | `counter` — `iteration >= MaxCIFixIterations`, iteration parsed by regex from issue body text. Reached only after upstream `classifyPRFailure`/platform-breaker gates (2350-2404), but the close trigger itself is evidence-blind to *what* failed, only *how many times* | **Authoritative for the iteration count itself (scope-agnostic), but the whole `handleCIFailed` function is only entered because `CIMonitor.CheckCI`/`checkRequiredChecks` aggregated `CIFailure` from in-scope checks only** — a failing but unscoped check never reaches this rung at all |
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
| `internal/autopilot/controller.go:2531` (`handleCIFailed` main rung) | autopilot | costly-reversible — a spurious issue can be closed by a human, but burns a full executor dispatch first (the #4766/#4769/#4775 incident shape: ~$ per junk fix run) | Executor $ + operator triage time | `typed`+`raw-string` — `failedChecks` from `CIMonitor.GetFailedChecks`, already `isScopedCheck`-filtered | **Yes — decorative for unscoped checks.** The issue body's diagnosis (failed-check names + logs) only ever names checks the allowlist let through; an actually-failing unlisted check contributes zero evidence to the spawned issue |
| `internal/autopilot/controller.go:3977` (post-merge CI failure rung) | autopilot | costly-reversible | Same as above, post-merge variant | Same evidence chain | Yes, same scoping |
| `internal/autopilot/feedback_loop.go:156` (`FeedbackLoop.CreateFailureIssue`, the shared implementation both rungs above call) | autopilot | — | — | Has its own dedup guard: SQLite claim (`ClaimSpawnedFix`, GH-4307/#4319) + GitHub-search belt-and-suspenders (`SpawnedFixExists`) so two ticks racing on the same failure spawn exactly one issue — this guard is about *duplication*, not about evidence quality | Inherits caller's scoping |
| `internal/executor/epic.go:1501` (`subIssueCreator.CreateIssue`, epic decomposition) | executor | costly-reversible | Different family — decomposition sub-issue spawn, not CI-fix-driven | `raw` decomposition logic, not CI evidence | N/A |
| `internal/comms/issue_intake.go:144` (generic issue-intake `CreateIssue`) | comms | costly-reversible | Adapter-driven (e.g. Slack), not autonomous-daemon-invoked in the CI-fix sense | N/A | N/A |

## 4. Retry/repick budget counter increments (irreversible in the sense that budget burn cannot be un-burned once spent)

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/controller.go:2662` (`maybeRetryInfraFailure`, `InfraRerunCount`) | autopilot | costly-reversible — resets on new push (keyed to `HeadSHA`) but exhausting it before then routes to the destructive `handleCIFailed` path | One rerun budget slot; exhaustion escalates to close/fix-issue rungs | `typed` — gated by `classifyPRFailure == FailureClassInfra*`, itself fed by `isScopedCheck`-filtered `GetFailedCheckLogsByCheck` | **Yes** — an infra-signature failure on an unscoped check never gets a rerun budget in the first place (invisible upstream) |
| `internal/autopilot/controller.go:3232` (`handleMerging`, `MergeAttempts`, capped at 3260 by `MaxMergeAttempts`) | autopilot | costly-reversible — cap exhaustion becomes terminal (`StageFailed`), requiring human intervention | Merge-retry budget; exhaustion parks the PR | `raw-bool` (success/fail of `MergePR`), but gated by a live `CheckCI` re-validation immediately before (3222-3229) | **Yes** — the live re-check is itself `isScopedCheck`-filtered; an unscoped check still failing at merge time is invisible to this gate |
| `internal/autopilot/controller.go:4873` (`handleMergeConflict`, `RebaseAttempts`, GH-3715) | autopilot | costly-reversible | Rebase-retry budget; exhaustion routes to `escalateAndHold` | `raw-bool` — success/fail of `UpdatePullRequestBranch` | N/A — not CI-scoped |
| `internal/executor/dispatcher.go:1787` (consecutive-repick backoff counter, persisted via `SetRepickBackoffState`) | executor | costly-reversible — exponential-backoff-then-hard-cap ladder; hard cap routes to `escalateStalledTask` | Repick budget | `counter` — raw drop-count | N/A — pre-GitHub, executor-level |
| `internal/executor/title_rejection.go:45-56` (consecutive-same-title-rejection counter) | executor | costly-reversible | Escalates to `pilot-failed`+`pilot-title-rejected` at threshold 2 | `raw-string` — exact SHA-256(title) match; a different title resets to 1 | N/A |

## 5. Terminal label writes (routing-state changes — cheap-reversible individually, but drive downstream irreversible behaviour)

| Site | Subsystem | Reversibility | Blast radius | Evidence |
|---|---|---|---|---|
| `internal/autopilot/controller.go:2977` | autopilot | cheap-reversible | `labelParkedAwaitingApproval` on config-gap (no approval channel configured) | `nil-check` on config |
| `internal/autopilot/controller.go:3301-3308` | autopilot | cheap-reversible | `LabelDone` add / `LabelInProgress`+`LabelFailed` remove on normal successful merge/close | Normal success path (`re-read` — post-merge) |
| `internal/autopilot/controller.go:3702-3709` | autopilot | cheap-reversible | `pilot-done` on decomposed-epic parent, gated by count-verified `openSubIssueCount` | `counter` — positive count check, not inferred |
| `internal/autopilot/controller.go:5157-5165` (`closeAndReexecute`) | autopilot | cheap-reversible | Restores dispatch-ready labels after unresolved-conflict close (family 1) | Inherits family-1 evidence |
| `internal/autopilot/controller.go:5204-5215` (`escalateAndHold`) | autopilot | cheap-reversible (label only) — but see family 8 for what it *prevents* | Adds `labelNeedsHuman` + caller labels; sets `Stage = StageFailed`; fires `EventTypeTaskFailed` alert (pages a human) | Inherits caller's evidence (see family 8 table) |
| `internal/autopilot/controller.go:6641-6649` (external-merge twin of 3301-3308, via `checkExternalMergeOrClose`) | autopilot | cheap-reversible | `LabelDone` add, `LabelInProgress`/`LabelFailed` remove, `clearRetryLabels` | `raw-bool` — `PullRequest.Merged` field read directly, not inferred |
| `internal/autopilot/controller.go:6930-6992` (`notifyExternalClose`) | autopilot | cheap-reversible (label), but gates family-7 ledger writes | Splits `pilot-superseded` vs `pilot-failed` on `prState.TerminalLabel == LabelSuperseded` | `raw-string` compare against a flag set upstream (by family-1's `closeConflictSourceIssueClosed`), not re-evidenced here |
| `internal/autopilot/controller.go:6863` | autopilot | cheap-reversible | Generic `RemoveLabel` in retry-label cleanup helper | — |
| `internal/autopilot/controller.go:7077-7086` | autopilot | cheap-reversible | Generic terminal-label-application helper shared by multiple ladder rungs | Inherits caller's evidence |
| `internal/executor/title_rejection.go:220` | executor | cheap-reversible | `pilot-failed`+`pilot-title-rejected` on 2nd consecutive same-title rejection | `raw-string` exact-hash match (family 4) |
| `internal/executor/dispatcher.go:2019` (`surfaceStalledIssue`, via `escalateStalledTask`) | executor | cheap-reversible | `pilot-blocked` add, `pilot-failed`+`pilot-in-progress` remove | `counter` — raw drop-count threshold; idempotent via matching prior `Error` string |
| `internal/adapters/github/notifier.go:34,75,81,86,122,127,147` | adapters/github | cheap-reversible | Baseline (non-autopilot) issue-lifecycle labels on the dispatch/execution-complete path | Execution result (success/fail), not CI-check-scoped |
| `internal/adapters/github/cleanup.go:336,444,521` | adapters/github | cheap-reversible | Background sweep removing stale labels on closed issues (extended by #4800/GH-4794 to strip `pilot-retry-ready`) | Time/state-based sweep |
| `cmd/pilot/commands.go:1462,1499,1502,1515,1525,1546` | cmd/pilot CLI | cheap-reversible | Operator-invoked `pilot task cancel`/label commands | N/A — manual, not autonomous-daemon-invoked; distinct category, out of scope for the daemon decision ladder proper |

## 6. PR merge (`MergePullRequest`) — irreversible (a bad merge to main cannot be un-merged by the daemon; requires a human revert)

| Site | Subsystem | Reversibility | Blast radius | Evidence | required_checks scoping |
|---|---|---|---|---|---|
| `internal/autopilot/auto_merger.go:95` (`AutoMerger.MergePR`, the sole production merge call) | autopilot | irreversible | Bad code on main; strips retry labels on success so a future regression starts its budget fresh | `re-read` — caller (`handleMerging`, controller.go:3222-3229) re-validates CI live via `CheckCI` immediately before merging; a definitive `CIFailure` here rescinds approval instead of merging. **Explicitly fails open on a transient CI-check API error** (documented: "must not block a legitimate merge on a flaky status-check call") | **Yes — decorative for unscoped checks.** `CheckCI`'s aggregate is built from `isScopedCheck`-filtered check runs; an out-of-allowlist check still red at merge time is invisible to this final gate |

## 7. Cancel/supersede/stall/failed writes to the executions ledger (`internal/memory/store.go`)

| Site | Subsystem | Reversibility | Blast radius | Evidence |
|---|---|---|---|---|
| `internal/memory/store.go:1383` `ReclassifyCompletionAsFailed` | memory store | costly-reversible — dashboard/history correctness, not code | Called from `controller.go:6963` (`notifyExternalClose`), when a PR closes externally without merge and `TerminalLabel != LabelSuperseded` |
| `internal/memory/store.go:1407` `ReclassifyCompletionAsSuperseded` (GH-4701) | memory store | costly-reversible | Called from `controller.go:6961`, sibling branch when `TerminalLabel == LabelSuperseded` |
| `internal/memory/store.go:1439` `TerminateNonTerminalExecution` | memory store | costly-reversible | Called from `controller.go:6986`, terminates a still-running row as `failed` when a close is observed before completion |
| `internal/memory/store.go:1460` `TerminateNonTerminalExecutionAsSuperseded` | memory store | costly-reversible | Called from `controller.go:6984`, superseded sibling |
| `internal/memory/store.go:2183` `UpdateExecutionStatus` | memory store | costly-reversible | Generic unconditional status writer; callers include `dispatcher.go:372` (boot-time orphan reap -> `stalled`) and `dispatcher.go:1936` (`escalateStalledTask` -> `stalled`, family 4/8) |
| `internal/memory/store.go:2247` `UpdateExecutionStatusIfNotTerminal` | memory store | costly-reversible | CAS-guarded (race-safe) writer; callers `dispatcher.go:623,803,926`, `lifecycle.go:220,318,547` (`ExecutionLifecycle.Cancel`, the `pilot task cancel` CLI path, GH-4586 — refuses if already `running` or already terminal) |
| `internal/memory/store.go:2320` `UpdateExecutionStatusByTaskID` | memory store | costly-reversible | Task-ID-keyed variant |
| `internal/executor/dispatcher.go:79` `IsTerminalByDesignStatus` (GH-4794, #4800, landed same cycle as this inventory) | executor | N/A — this is the fix, not the hazard | Converts `handleIssueGeneric`'s classification from **`side-effect-inferred`** ("no PR produced" read as failure) to **`typed`** (consult the recorded ledger status vocabulary — `superseded`/`canceled` vs `failed` — directly). Consumed by `cmd/pilot/handlers.go:560,675,873`. This is the concrete instance of root pattern 2 from the TASK-459 brief, already fixed for this one site — the general case is what `Verdict.Scope`+evidence enforcement in later phases generalizes |

## 8. `escalateAndHold` and hold/escalation (semi-destructive: never closes/deletes/merges, but pages a human and parks the PR — operator-costly)

Defined once at `internal/autopilot/controller.go:5204`. Call sites:

| Site | Reason string | Evidence gating this call |
|---|---|---|
| `controller.go:2411` | "CI failure with zero gathered evidence" | `typed` + `nil-check` — `classifyPRFailure == FailureClassUnknown` *and* `perCheckLogs` came back empty. This is the GH-4779 fix that already implements the TASK-459 invariant for one site: zero evidence routes here, never to `ClosePullRequest`/`CreateFailureIssue` |
| `controller.go:2516` | "CI fix size guard fired" | `counter` — production-line diff stat vs `MaxCIFixPRSize`; fails open (does not hold) if `ListPullRequestFiles` errors |
| `controller.go:2551` | "CI-fix continuation declined at preflight" | `nil-check` — `CreateFailureIssue` returned an error or a legitimate dedup-claim-in-flight `(0, nil)`; chosen over closing to avoid GH-4415's double-lost-work dead end |
| `controller.go:4929` | "auto-rebase failed" | `raw-bool` — `attemptMechanicalConflictResolution` (local merge replay) found the conflict surface isn't confined to go.mod/go.sum |
| `controller.go:5130` (`holdClosedIssueWorkNotOnMain`) | "source issue #N is closed but {situation}" | `re-read` — `checkPRWorkOnMain` reachability check errored or returned false; fail-safe against discarding unmerged work |
| `internal/executor/dispatcher.go:1811/1832/1851` (`stallTaskAfterRepickHardCap`/`StallCap`/`InfraCap`, via `escalateStalledTask` at 1911) | Three distinct named reasons (repick-hard-cap / stall-watchdog-cap / infra-cap) | `counter` — raw consecutive-drop-count thresholds, each with its own distinct reason string (deliberately not conflating "code is broken" vs "environment kept failing"); idempotent via matching the prior `Error` string exactly. This is the executor-level (pre-GitHub-PR) analog of `escalateAndHold` |

## 9. Cross-cutting findings

- **`isScopedCheck` blind spot** (`internal/autopilot/ci_monitor.go:799`) gates every evidence-gathering function that feeds families 1, 3, 4, and 6: `GetFailedChecks` (:777), `GetFailedCheckLogs`, `GetFailedCheckLogsByCheck`, `GetFailedCheckExcerpts`, and `checkRequiredChecks`/`CheckCI` itself (:715). When a project's `required_checks`/`ci_checks.required` allowlist is set, a check whose name isn't listed is **structurally invisible** to that project's entire destructive-action chain — not under-weighted, never observed. Scoping is per-`Controller` (one per project), so this is a per-project hazard, not global. Policy itself is out of scope for this task (founder config decision, `.agent/system/FEATURE-MATRIX.md`).
- **`PlatformBreaker.Observe`** (`internal/autopilot/platform_breaker.go:139`, GH-4791/#4797, landed 2026-08-07) sits upstream of the whole family-1/3/8 ladder for CI-failure handling specifically: while `Open`, `handleCIFailed` (controller.go:2385-2390) suppresses every downstream destructive action for that tick regardless of that PR's own classification. This is a cross-PR correlation gate, not a per-call-site evidence contract — it complements but does not replace the `Verdict` contract this task defines.
- **Approval-decision writes** (`SetApprovalDecision`, referenced near controller.go:3150, gating `StageAwaitApproval -> StageMerging`) — flagged but not fully traced in this pass; a Phase 2/3 candidate to confirm its evidence shape.
- **`handleReleasing`/release-pipeline retry/escalation ladder** (controller.go ~4196-4615) — likely belongs in families 4/8 but not individually traced in this pass; Phase 2/3 candidate.
- **`internal/executor/watchdog.go`** (stall-kill mechanism feeding `stallTaskAfterStallCap`) — not traced in this pass; Phase 3 candidate.

---

## How Phase 2+ consumes this

Phase 2 gates `handleCIFailed`'s three rungs (family 1's `controller.go:2453`,
family 3's `:2531`/`:3977`, family 8's `:2411`) behind the `Verdict` type
defined in `internal/autopilot/failure_class.go`. Phase 3 does the same for
families 4/6/7's executor/dispatcher/poller sites. Phase 4 adds
`scripts/check-destructive-calls.sh` to keep new call sites from bypassing
the contract. This document is not re-derived per phase — update the
relevant row(s) in place as each site migrates.
