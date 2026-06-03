---
name: learn_pilot_finalization_bottleneck
description: "The daemon's *finalization* path (push → PR-create → label) is the active reliability bottleneck — the *execution* path (Claude Code subprocess) is reliable. Don't conflate them when triaging."
type: learning
---
Studio-sdk extraction (2026-06-02 / 2026-06-03, PRs #28–#56). 9/10 connectors ported and shipped (v0.10 → v0.22). Outcome was excellent. The honest read on *what carried it*, though, is different from the headline:

- **~7/12 of finalization runs needed manual recovery** (Shape A — stall-before-push: #29, #32, #33, #42, #43, #49, #55). Worker had committed the work in its worktree, but the push → PR-create did not reliably fire. The user manually `git push`ed each worker's worktree.
- **2 runs hit the retry-race** (Shape B — daemon adds `pilot-retry-ready`, resets `pilot/GH-<n>` back to the broken commit, closes the user's recovery PR mid-review: #33, #55; #55 nearly re-shipped a lint failure + an out-of-scope scratch file).
- **1 late-duplicate-PR** (Shape C — daemon opens a PR for already-merged work: #46, 6m after #45 landed on main, same headRefOid).
- **Clean baseline: 5 runs** (#38, #40, #50, #54, #56).
- **Review gate earned its keep**: the #28 webhook-sanitize gap (helper existed but wasn't called) and golangci `unused`/`ineffassign` fails on #33 and #55 — caught and blocked before merge.

**What was actually doing the work** — orchestration + review harness, not the daemon's finalization. The Pilot daemon's *build quality* (the code Claude Code wrote) was high. Its *finalization* (commit → push → PR → tag) is genuinely flaky, and the harness compensated.

**Why the conflation matters when triaging:** "Pilot worked great on this extraction" implies the daemon is healthy. The accurate framing is "Pilot's executor + the orchestration harness shipped the extraction; the daemon's finalization needs a structural fix before the next batch." Don't grade the daemon on the headline outcome — grade it on whether the finalization completed without manual recovery.

**Operational workaround that proved reliable:** the `no-decompose` label routes the issue through the direct path (`runner.go:3375–3511`) instead of the epic path (`runner.go:1594–1645`). The direct path's error handling is fatal (Success=false on push or PR-create failure); the epic path's is warn-only (continues, returns Success=true). Empirically, every `no-decompose` connector landed without manual recovery.

**Canonical fix plan:** [[TASK-359]] (daemon finalization hardening — Shapes A/B/C closure). Three layers: Layer 1 unified `finalizeExecution()` in `executor.Runner` (MANUAL — Pilot can't refactor its own execution path), Layer 2 boundary fixes in autopilot/poller (Pilot-eligible via `no-decompose` 2-way split), Layer 3 defense-in-depth on top of v2.166.9. [[TASK-321]] is the older root-cause record for the dispatch-idempotency symptom and is superseded by TASK-359.

**How to apply** when prioritizing daemon work: finalization-path bugs outrank executor-path bugs by frequency *and* by user-visible cost. A finalization stall produces a stranded issue with apparent "successful" execution-DB state — silently wrong and invisible until the user notices the missing PR. An executor-path bug produces a loud failure. Triage finalization concerns first.

Cross-refs: [[TASK-320-executor-false-negative-noop-fix]] (Layer B2 deferred for the same MANUAL reason), [[TASK-355]] (foreign-SHA root cause shipped v2.166.7), [[TASK-356]] (v2.166.7–9 partial fixes; warn-only contract + non-atomic dispatcher writes not addressed).
