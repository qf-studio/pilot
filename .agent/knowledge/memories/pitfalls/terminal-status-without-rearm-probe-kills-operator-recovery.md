---
name: terminal-status-without-rearm-probe-kills-operator-recovery
description: Adding a status to HasTerminalCompletion without a matching re-arm probe silently deletes the operator recovery path — PR#5402 made needs_human terminal, so clearing pilot-needs-human stopped re-admitting tasks while the hold comment still promised it would (found 2026-09-09 reviewing PR#5411; filed #5414)
type: pitfall
---

# A status made terminal in `HasTerminalCompletion` needs a re-arm probe, or the operator recipe dies silently

**What happened (2026-09-09):** PR#5402 (GH-5399) added `needs_human` to `memory.HasTerminalCompletion` to stop a false retry. Correct for that bug, but the SDK poller's `HasCompletedExecutionReason` now returns skip for any task with a `needs_human` row, and the only statuses with a re-arm probe are `canceled` (GH-5139, `cmd/pilot/rearm_canceled.go`) and `superseded` (GH-5249). Result: the documented recovery — strip `pilot-needs-human`, fix the body, task re-admits on the next poll (used live on #5375 on 09-08) — stopped working the moment v2.274.0 hit the box, and the hold comment in `runner.go` still tells operators to do exactly that. `nextRetryGeneration` refuses gen+1 for the same reason. Only recovery: close + refile (loses branch + ledger). PR#5411 then extended the same terminality into the store CAS guard and cleanup mirror, which is consistent, so the fix belongs in the re-arm layer (#5414), not in un-terminalizing.

## Why it matters
- The regression is invisible to every test: terminality tests pass, the poller logs a plausible "stalled: awaiting re-arm evidence", and nothing fails loudly. It only shows as "I removed the label and nothing happened."
- Three places encode the operator promise (hold comment, SOP recipe, `pilot task cancel` hint) and none of them is checked against the probe set.

## Rule
When a status joins any terminal set consulted before dispatch (`HasTerminalCompletion`, the dispatcher terminal maps, `EffectiveSuccess`), ask: **is this status operator-recoverable?** If an operator gesture on GitHub (reopen, relabel, unlabel) is supposed to resume it, a `tryRearm<Status>` probe mirroring `tryRearmCanceled` MUST ship in the same PR, with a distinct reason string. Review checklist item for any PR touching terminal sets.

Related: [[stalled-rearm-sweep-ignores-body-edit-loops-forever]], [[nextretrygeneration-blind-to-dead-owner-nonterminal-claims]], [[hand-merge-leaves-pilot-needs-human-and-stale-body-capture-at-dispatch]].
