---
name: required-checks-allowlist-makes-other-gates-decorative
description: With autopilot.required_checks set to an allowlist, CIMonitor.isScopedCheck reports ONLY those checks — every other CI gate can fail red and auto-merge proceeds anyway; on the pilot repo that makes the Knowledge Graph Drift Gate and Check Secret Patterns decorative
type: pitfall
---

# A `required_checks` allowlist silently demotes every other CI gate to decoration

**What happened (2026-07-24):** PR #4534 merged with the **Knowledge Graph
Drift Gate failing** (`fail`, 5s, run 30118463122). It deleted five indexed
memory files; #4535 deleted a sixth. The gate that exists to catch exactly
this fired correctly and changed nothing.

The box config:
```yaml
required_checks:
  - test
  - lint
```
`CIMonitor.isScopedCheck` (`internal/autopilot/ci_monitor.go:562-570`): when a
required-checks allowlist is configured, **only failures among those names are
reported**. `GetFailedChecks` filters through it, so autopilot literally cannot
see a failure in any other check. With approvals off (since 2026-07-20),
`test` and `lint` are the only two gates on any merge in this repo.

Currently decorative on pilot: **Knowledge Graph Drift Gate**, **Check Secret
Patterns**, **Box Scripts (ShellCheck + tests)**. A PR that trips secret
scanning auto-merges today.

## Why this kept the GH-4387 class alive

The "strip unindexed memory doc" deletion class has recurred at least four
times despite TASK-410 building a guard *and* a CI gate being added to catch
escapes. Neither could work: the guard runs inside the executor that does the
deleting, and the gate has never been able to block a merge. **More guard code
cannot fix a config problem.**

## How to avoid

1. Treat `required_checks` as the definitive merge gate list, not a "which
   checks matter most" hint. Anything omitted is advisory-only.
2. When adding a CI gate whose purpose is to *block* something, add its exact
   check-run name to `required_checks` in the same change — otherwise it is
   theatre.
3. Auditing: compare the repo's actual check-run names
   (`gh pr checks <n>`) against `required_checks` in `~/.pilot/config.yaml`.
   Names must match exactly, including spaces and capitalisation
   ("Knowledge Graph Drift Gate", not `drift-gate`).
4. Config change requires an operator edit on the box + daemon restart; it
   changes what can merge for **every** repo sharing the config, so review the
   full check inventory first (see
   [[global-required-checks-leak-across-projects]] — the same allowlist is
   shared across project controllers).

**Status 2026-07-27: FIXED for the pilot project** (founder-approved).
Per-project override added on the box (`projects[pilot].ci_checks.required_checks`,
`ProjectCIChecksOverride` / GH-4478 plumbing at `cmd/pilot/main.go:1773/1834`):
`[test, lint, Knowledge Graph Drift Gate, Check Secret Patterns,
Box Scripts (ShellCheck + tests)]`. Daemon restarted 2026-07-27 12:46Z;
startup log confirms the 5-item allowlist on pilot's CI monitor. Backup:
`config.yaml.bak-drift-gate-required`. Other projects still inherit the
global `[test, lint]` (pointer keeps auto-discovery via its `[]` override) —
the cross-project leak in [[global-required-checks-leak-across-projects]]
remains for repos without an override.

Related: [[global-required-checks-leak-across-projects]] (same config, the
cross-project leak), [[ci-infra-failure-misclassified-as-code]] (also lives in
`isScopedCheck`'s scoping decisions).
