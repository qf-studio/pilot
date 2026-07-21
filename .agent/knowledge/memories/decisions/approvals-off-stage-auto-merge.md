---
name: approvals-off-stage-auto-merge
description: Founder decision 2026-07-20 — stage.require_approval flipped to false globally (all wired repos incl. pilot itself); auto-merge on green CI, with the size-floor/scope-drift/test-evidence escalation gates as the remaining human-approval rails
type: decision
---

# Approvals off on stage env — auto-merge on green CI (founder, 2026-07-20)

`environments.stage.require_approval: false` on the box config (backup:
`config.yaml.bak-approval-flip`) and the laptop copy. Active since the
21:41Z restart (v2.243.0-7+).

## Scope

- Global: `environment: stage` is the single active env for ALL wired repos
  — pilot itself, pointer, auth-service, fleet-manager, console repos. The
  projects schema has NO per-project env override (only release overlays).
- `approval_source: slack` stays configured; unused for gating.

## What still forces human approval (per-PR, code-level rails)

- size-floor gate, scope-drift gate, test-evidence gate (opt-in) —
  escalate-only, deliberately NOT config-defeatable (OAuth cascade lesson,
  controller.go:1705).
- `prod` env still has `require_approval: true` (unused).

## Prerequisite that made this safe

GH-4477 → PR #4487 (merged 2026-07-20 21:16Z): CI is re-validated at the
merge chokepoint and approval is rescinded when a check fails after
`handleCIPassed` — the stale-`ci_status=success` hole that approval was
backstopping is closed. Flipping approvals off before #4487 would have been
reckless; the order mattered.

## Rollback

Restore line 188 of box config from `config.yaml.bak-approval-flip` (or set
`require_approval: true`) + daemon restart. Watch out for
[[require-approval-flip-doesnt-release-held-prs]] in the reverse direction:
already-escalated PRs keep their held state across flips.
