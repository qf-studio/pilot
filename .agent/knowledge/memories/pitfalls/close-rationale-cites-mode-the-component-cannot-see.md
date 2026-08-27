---
name: close-rationale-cites-mode-the-component-cannot-see
description: Autopilot's ClosePullRequest sites justify closes as "unblock the sequential poller" but internal/autopilot.Config has no execution-mode field — the cited condition is invisible at the decision site, and the default mode (auto) makes the rationale inapplicable (GH-5227 → #5241)
type: pitfall
---

# An in-code rationale that cites a runtime condition the component can't observe is an unwired gate

**What happened (GH-5227, verified 2026-08-27):** three close sites in
`internal/autopilot/controller.go` (~3591, ~3679, ~3746) carry the comment
"Close the failed PR so the sequential poller can unblock". The condition is
real — the SDK poller's `startSequential` blocks on `MergeWaiter` — but:

1. `orchestrator.execution.mode` **defaults to `auto`**, where no poller
   blocks on anything, so the rationale is inapplicable in the common case;
2. `internal/autopilot.Config` (`types.go:82`) has **no execution-mode field**
   — the Controller could not gate on the mode even if it wanted to;
3. even in genuine sequential mode the close is an optimization: the
   `MergeWaiter` self-unblocks on a bounded timeout (default 1h), and
   `controller.go:3751`'s own comment admits it.

The result: healthy PRs closed unconditionally at iteration limits under a
justification that applies to a minority configuration.

**Compounding wiring history:** `execution.mode` was itself silently dropped
between config and the SDK poller until PR #5207 (2026-08-24) — sequential
configs ran as auto while the startup banner claimed otherwise. And
`cmd/pilot/main.go:67-73` still carried a stale GH-4191-era comment saying
the SDK adapter is unconditionally auto. Same disease family as
[[unwired-config-field-validated-but-dead]]: every surface signal (comment,
banner, validated config) claimed a behavior the wiring didn't deliver.

**How to apply:** when a comment justifies a destructive action with "so that
X can proceed", verify X's triggering condition is (a) plumbed into the
deciding component and (b) actually the common case — grep the component's
Config struct for the field before trusting the comment. Fix for the close
sites: TASK-486 / [#5241](https://github.com/qf-studio/pilot/issues/5241)
(mode threaded into `autopilot.Config`; non-sequential → hold via
`escalateAndHold` idiom instead of close).
