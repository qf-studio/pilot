# TASK-480: Safe no-op / decline contract — a dedicated signal, never the mandatory exit signal

**Status**: 📋 Planned — NOT dispatched. Research complete 2026-08-17 (full read of the post-execution classification path; no sampling gaps in the critical section).
**Created**: 2026-08-17
**Origin**: review of external contributor PR #4901 (lkshrk). Two of its three legs are correct and welcome; the third re-opens the TASK-460 false-success class at scale. This task salvages the good legs and specifies the safe version of the third.

## Problem

There is no way for the executor to say "I looked, and correctly nothing needed to change." PR #4901 tried to infer it from `state.exitSignalSuccess` + zero commits — but `{"v":2,"type":"exit","exit_signal":true,"success":true}` is the **mandatory** completion signal (`workflow.go:169-172`, Phase 5 "REQUIRED"), emitted on every run the model *believes* finished. That includes the GH-916 class (model claims success, never committed — ~10% of failures per `runner.go:3967`). Inferring a decline from it would:

1. render forgot-to-commit failures as "⏸ No changes needed",
2. make the GH-916 retry unreachable in production, and
3. run before `preserveDirtyWorktreeAsWIP`, deleting real uncommitted work — the pilot-console#26/B8 incident GH-4517 fixed.

The signal vocabulary has a *failure* exit (`success:false, reason:"blocked: …"`, `workflow.go:210-213`) but nothing symmetric for "succeeded because nothing was needed."

Also found: **`DECLINED:` is prompted nowhere in the first-pass prompt.** It appears only in the GH-916 *retry* prompt (`runner.go:4008-4021`, "Option B — Decline"). `workflow.go`'s Phase 5 contract never mentions it, so first-pass decline handling only fires when the model emits the marker spontaneously.

## Current flow (main, `runner.go` `Execute()`)

| Step | Lines | Behavior |
|---|---|---|
| CommitSHA harvest fallbacks | 3805-3844 | git-log → `GetCurrentCommitSHA` → structured summary, only if `CommitSHA == ""` |
| Ghost-SHA guard | 3850 → `git_freshness.go:23-68` | **Internally correct**: `preserveDirtyWorktreeAsWIP` runs first (`:54`); only if nothing preserved does it set `Error = "no new commit produced…"` (`:63-65`). Skipped when `CommitSHA == ""` |
| Metrics | 3882-3897 | ⚠ `RecordExecution` label derives from `result.Success` **before** decline/no_op classification — pre-existing gap, declines count as `success` |
| Success/failure branch | 3900 / 3939 | |
| GH-916 no-commit block | 3966-4202, gated `CreatePR && !DirectCommit && Branch != ""` | count commits (3982) → retry prompt (3988-4021) → retry (4025-4054) → recount (4065) |
| — DECLINED check | 4079-4104 | `parseDeclinedReason`, `Declined=true`, `recorder.Finish("declined")`, no alert |
| — GH-4517 preserve backstop | 4106-4135 | **runs after the DECLINED check** — latent pre-existing ordering bug (dirty tree + DECLINED marker ⇒ diff discarded) |
| — generic no_op | 4137-4181 | `Outcome="no_op"`, alert emitted, `recorder.Finish("no_commits")` |

Comms: `handler.go:842-848` passes only `result.Success` to `SendResult`; `Messenger.SendResult` (`types.go:41`) has no outcome/declined param, so every adapter renders a decline as ❌ failed. Only GitHub branches on decline today (`github/notifier.go:140-157` `NotifyTaskDeclined` → `pilot-needs-clarification`).

## Fix

### Producer — `workflow.go`

Add an explicit opt-in branch to the Phase 5 contract, distinct from the mandatory signal:

```
**No-op exit** (only when no code change was needed — task already satisfied,
requirement not applicable, nothing to do):
{"v":2,"type":"exit","exit_signal":true,"success":true,"no_op":true,"reason":"<one sentence, mandatory>"}
```

Keep the plain signal as the default for "did work and committed." The distinguishing bits are `no_op:true` **and** a non-empty `reason` — never inferred.

### Parser — `signal.go`

- Add `NoOp bool \`json:"no_op,omitempty"\`` to `PilotSignal` (:18-30).
- Add `NoOpExitReason(signals) (string, bool)` returning true **only** when `(ExitSignal || Type == SignalTypeExit) && Success && NoOp && strings.TrimSpace(Reason) != ""`. No `Message` fallback, no fabricated default — mirrors `parseDeclinedReason`'s empty-reason rejection (`runner.go:1613-1615`).
- Runner state capture records `exitSignalNoOp`/`exitSignalReason` only when reason is non-empty.

### Runner ordering (load-bearing)

`preserveDirtyWorktreeAsWIP` must confirm a clean tree **before** any decline classification is honored, at every insertion point:

1. **Ghost-SHA path** (after `runner.go:3850`): safe here *only* because the guard already preserved internally, so reaching `Error == "no new commit produced — worktree HEAD matches base branch parent"` structurally proves a clean tree. Gate on that **exact** string (not a prefix — the preserved-WIP error reads differently). Accept only DECLINED text or `no_op`+reason.
2. **GH-916 pre-retry** (`runner.go:3988`, where #4901's dangerous hunk sits): call `preserveDirtyWorktreeAsWIP` first. If preserved ⇒ "auto-preserved, needs manual review" failure, never declined, regardless of marker/signal (real diffs contradict any no-op claim). Only if not preserved is DECLINED/`no_op` honored. Everything whose only evidence is the bare mandatory signal falls through to the retry — today's behavior, unchanged.
3. **Post-retry** (`4079-4135`): reorder so the existing preserve call runs **before** the DECLINED check, closing the latent pre-existing gap as part of the same restructuring.

### Ordering after fix

```
backend execute (first pass)
  → CommitSHA harvest (3805-3844)
  → applyGhostSHAGuardWithPreserve (3850)
      ├─ stale SHA → preserve first
      │    ├─ dirty  → preserved, Success=false, "auto-preserved"  (never declined)
      │    └─ clean  → Error="no new commit produced…"
      │                 → [NEW] DECLINED text OR no_op+reason only → Declined
      └─ empty SHA → no-op, fall through
  → metrics  ⚠ pre-existing: label reflects pre-classification Success
  → !Success ? failed path : GH-916 block
      → CountNewCommitsAgainstOrigin == 0?
          → [NEW] preserveDirtyWorktreeAsWIP FIRST
              ├─ dirty → preserved, Success=false (never declined, retry skipped)
              └─ clean → DECLINED marker? → declined
                       → no_op+reason?   → declined
                       → neither         → RETRY (unconditionally reachable for bare signal)
                            → still 0? → preserve (reordered ahead of marker check)
                                       → DECLINED / no_op+reason → declined
                                       → neither → generic no_op failure (alert)
```

## Salvage map for PR #4901

**Keep** (re-sequenced per above): leg 1's shared `finishDeclined` dedup + first-pass `DECLINED:` marker check on `backendResult.LastAssistantText`; leg 2's intent (render `⏸ No changes needed` instead of ❌); the `signal.go` bare-JSON-line fallback parsing (orthogonal robustness win, no interaction with the hazard — but it needs its own `signal_test.go` coverage, which the PR lacks).

**Rewrite**: `declineReasonFromRun`'s `state.exitSignal && state.exitSignalSuccess` branch with its fabricated default reason; the pre-retry `finishDeclined` insertion; the comms branch's `SendText` — use the existing thread-correct `SendChunked(ctx, contextID, threadID, …)` (`types.go:44`; Slack threads via `ThreadTS`, `slack/messenger.go:88`; Telegram/Discord ignore `threadID` harmlessly).

## Test rows

Existing suites: `gh4517_test.go` (`:27`, `:140`, `:242`), `git_freshness_test.go:30`, `runner_test.go` (`TestParseDeclinedReason:4275`, `TestRunner_DECLINED_Path:4375`, `TestRunner_NoChanges_NoDecline:4431`, `TestGhostSHAGuard:4726`). PR #4901's `runner_decline_first_pass_test.go` asserts the unsafe behavior and must be rewritten.

| Row | Setup | Expected |
|---|---|---|
| Bare exit-success, zero commits | only the mandatory signal, no commit | **retry fires** (2 backend calls), not declined |
| `no_op`+reason, clean tree | `no_op:true,"reason":"watch ticket unchanged"` | `Declined=true`, reason preserved, 1 backend call |
| `no_op` with empty reason | `no_op:true, reason:""` | not a valid no-op ⇒ falls through to retry |
| Ghost SHA + `no_op`+reason + clean | guard rejects SHA | `Declined=true` via ghost-SHA insertion |
| **Ghost SHA + dirty tree + bare signal** | real uncommitted diffs, no `no_op` | preserve fires, `Success=false`, "auto-preserved", **`Declined=false`** — the GH-916 case #4901 would have declined |
| **Pre-retry dirty + bare signal** | empty CommitSHA, dirty tree | preserve wins before any decline check; retry NOT entered; `Declined=false` |
| **Pre-retry dirty + `no_op`+reason** | model claims no-op but left diffs | preserve wins; reason logged, does not override |
| Post-retry DECLINED + dirty tree | retry emits marker, tree dirty | preserve wins (closes the latent pre-existing gap) |
| Comms threadID | declined result, non-empty `threadID` | asserts `SendChunked(ctx, contextID, threadID, …)`, not `SendText` |
| Metrics label (optional) | declined/no_op | outcome label is `declined`/`no_op`, not `success` |

## Open decisions

- Metrics-label gap (`runner.go:3882-3897` records before final classification) — recommend a **separate** follow-up; orthogonal, needs `RecordExecution` moved/duplicated after `Outcome` is known.
- Field name `no_op` chosen for consistency with `result.Outcome == "no_op"` (`runner.go:4140`). Unknown JSON fields are ignored by `json.Unmarshal`, so older prompts stay compatible.
- Should `no_op` reuse `NotifyTaskDeclined`'s `pilot-needs-clarification` label (`github/notifier.go:140-157`)? A confident "nothing to do" is not "I don't understand the ask", and that label blocks re-dispatch (`epic.go:86`). Design review, not blocking the runner fix.

## Refs

- External PR: #4901 (split: keep legs 1+2 re-sequenced, rewrite leg 3)
- Related: TASK-460 (delivery-evidence false-success class), GH-4517 (work preservation), GH-916 (no-commit retry)
