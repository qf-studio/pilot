---
name: global-required-checks-leak-across-projects
description: A single global autopilot.required_checks/ci_checks allowlist is shared by every project controller — a repo whose check-run names differ polls waiting_ci forever even with all checks green; fixed by a per-project ProjectCIChecksOverride (GH-4478)
type: pitfall
---

# Global `required_checks` leaks across every project controller — silent forever-pending CI

**What happened (live repro 2026-07-20, qf-studio/pointer#108):** all three
check-runs (`integration`, `go`, `web`) completed SUCCESS by 12:20:25Z, but
`autopilot_pr_state` stayed `stage=waiting_ci, ci_status=pending` for
4-6 minutes / ≥8 consecutive polls — not a timing artifact. GH-4384/#4408
(auto-discovery vs. combined-status fallback) was confirmed working correctly
and was NOT the cause; the bug was upstream of that logic.

## Root cause

`cmd/pilot/main.go` constructs every project's `autopilot.Controller` with
the SAME `*autopilot.Config` pointer
(`cfg.Orchestrator.Autopilot`). `NewCIMonitor` picks its polling branch by
precedence: `cfg.CIChecks.Required` (GH-4307) → `cfg.RequiredChecks` (GH-4333,
forces `mode=manual`) → auto-discovery. Production has a global
`required_checks: [test, lint]` (tuned for the qf-studio/pilot repo's own CI
job names). That list applied to the `pointer` controller too, so
`checkRequiredChecks` seeded a `requiredStatus` map keyed on `test`/`lint`
and never saw a live check-run whose `Name` matched — `aggregateStatus`
returns the `CIPending` zero-value forever, no matter how green the actual
checks are. `Release`, `ProjectBoard` (GH-4472) and `Quality` all already had
a per-project overlay for exactly this global-vs-per-repo class of bug;
`RequiredChecks`/`CIChecks` did not.

## Fix

`internal/autopilot.ProjectCIChecksOverride` (nil-inherits-global, mirrors
`ProjectReleaseConfig`), wired via `autopilot.WithCIChecksOverride(...)`
`ControllerOption`, applied inside `NewController` as a shallow-copy overlay
of `cfg` **before** `NewCIMonitor` is constructed — never mutate the shared
global `cfg` in place, or the overlay itself becomes a cross-controller leak.
Config surface: `ProjectConfig.CIChecks *autopilot.ProjectCIChecksOverride`
(`ci_checks:` block under a project entry in `~/.pilot/config.yaml`).

**Deploy note:** this PR is code-only. The live `pointer` project entry in
production `config.yaml` still needs an operator to add its own `ci_checks:
{required_checks: [...]}` (or `required_checks: []` to fall through to
auto-discovery) — a config + restart step, out of scope for the code fix.

## Diagnostic signature

`stage=waiting_ci` / `ci_status=pending` that never clears despite
`gh pr checks <n>` (or the GitHub UI) showing all green, on a repo whose CI
job names don't literally match the global `required_checks`/`ci_checks.required`
list. Check `checkRequiredChecks` is the branch being taken (log line
`discovered CI checks ... mode=manual`) before assuming it's another
GH-4384-class combined-status issue.

## Recommended approach

For any "CI watcher stuck pending with green checks" report: (1) confirm via
`daemon.log` which polling branch fired (`mode=manual` vs `mode=auto`) for
the repo/SHA in question — per mem-159, pull ground truth (log + actual
config.yaml + real check-run names) before filing from code-reading alone;
(2) if `mode=manual`, diff the live check-run names against the effective
`required_checks`/`ci_checks.required` list for THAT repo, not just the
global block; (3) any new global-vs-per-project autopilot config field should
get an overlay from day one (see `Release`/`ProjectBoard` precedent) rather
than being retrofitted after a live incident.

## Related

- Files: internal/autopilot/ci_monitor.go, internal/autopilot/controller.go,
  internal/autopilot/types.go, internal/config/config.go, cmd/pilot/main.go
- Refs: #4384, #4408, #4415, GH-4472 (ProjectBoard, same overlay pattern)
- [[hard-cap-rearm-in-memory-gate]] (same mem-159 ground-truth-first discipline)
