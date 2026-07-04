# TASK-382: Post-Restart & Epic-Lifecycle Defect Burn-Down

**Status**: 🚀 Dispatched to Pilot (D1–D6)
**Created**: 2026-07-03
**Assignee**: Pilot

---

## Context

**Problem**:
The 2026-07-03 shipping session (TASK-378 B-chain + TASK-379 waves V1–V5, ~20 PRs) doubled as a live stress test of the daemon's restart and epic-decomposition lifecycles. Six distinct defects were observed with reproducible evidence. All were survivable only because a human (or watching session) intervened; each defect is filed as its own `pilot` issue so nothing is lost.

**Goal**:
Every defect below has a GitHub issue, dispatched to Pilot, with observed evidence and acceptance criteria.

---

## Defect Register (all observed 2026-07-03, daemon v2.201.2 → v2.206.1)

| # | Defect | Evidence | Issue |
|---|---|---|---|
| D1 | Autopilot loses live PR tracking after daemon restart — post-restart `OnPRCreated` PRs get no `autopilot_pr_state` row; green MERGEABLE PRs strand unmerged indefinitely | #3778 (55 min), #3781, #3782 all stranded green and merged manually; boot recovery merged #3776 fine at 18:06; last autopilot merge 18:08 | [#3784](https://github.com/qf-studio/pilot/issues/3784) |
| D2 | Child worker completes + commits but push/PR step fails silently — work stranded in local odb, parent reports "committed work (sha) but produced no PR" | GH-3764 sha `05c8271` chain (6 commits) salvaged manually into PR #3773 | [#3785](https://github.com/qf-studio/pilot/issues/3785) |
| D3 | Epic parent phantom child-failure — parent fails with "sub-issue N failed: unknown: exit status 1" while the child execution continues and later succeeds | GH-3760 failed 18:20 claiming #3769 failed; #3769 ran until 18:43, completed, PR #3778 shipped | [#3786](https://github.com/qf-studio/pilot/issues/3786) |
| D4 | Retry accounting counts infra noise and exhausts retries on already-shipped work — stale-reap rows and transient `claude_available` preflight failures during restart counted as attempts; `pilot-failed-retry-exhausted` applied to a task whose PR was merged | GH-3759: work merged 17:52 (PR #3773), retry-exhausted label applied 18:0x from restart noise; closed manually | [#3787](https://github.com/qf-studio/pilot/issues/3787) |
| D5 | Queue re-adoption (#3732, in v2.204.1+) did not fire at boot — all queued tasks reaped as `stale queued task recovered (no worker picked up)` at 18:00:31 on v2.206.1; recovery happened via the slower label-cleanup path instead | 4 rows reaped at 18:00:31 (GH-3759/3764/3765/3726) despite re-adoption being merged | [#3788](https://github.com/qf-studio/pilot/issues/3788) |
| D6 | `Blocked by: #N` not enforced at queue/dispatch time — gated task queued and executed while its blocker was still open; per-project FIFO masked it | #3759 (`Blocked by: #3754`) queued 16:20, ran while #3754 open until 16:40 | [#3789](https://github.com/qf-studio/pilot/issues/3789) ⏸️ **PARKED — human-led.** 4 autonomous attempts (PRs #3802/#3822/#3824/#3835) all died on `stress` 600s timeout: any blocker-state API lookup in the poll cycle deadlocks the stress fake. Fix guidance on issue: resolve blockers from the already-fetched candidates list, no new API call. `pilot` label removed |
| D7 | Self-upgrade went silent — daemon stayed on v2.201.2 through 8 releases (v2.202–v2.206) after the 09:49Z hot-upgrade, until manual restart | binary mtime 09:49:32Z vs releases through 15:5x | [#3790](https://github.com/qf-studio/pilot/issues/3790) |
| D8 | `pilot upgrade` hangs forever on `[y/N]` prompt (`upgrade.go:194-201`, no timeout/TTY check) and is unkillable by SIGTERM (handler at `:151-157` catches it but stdin read isn't context-aware) | 3h zombie PID 35747, survived SIGTERM, needed SIGKILL; disrupted two daemon restarts | [#3791](https://github.com/qf-studio/pilot/issues/3791) |
| D9 | Autopilot closes CI-failed PRs silently — no PR comment naming the reason/check, source issue keeps `pilot-done` claiming success for discarded work | PR #3802 closed UNSTABLE 00:30 UTC 07-04, branch deleted, zero comments; #3789 left `pilot-done`; re-queued manually | [#3806](https://github.com/qf-studio/pilot/issues/3806) ✅ v2.207.12 |
| D10 | Poller shipped-ness check trusts `completed` execution rows without verifying a merged PR — discarded-PR tasks permanently re-marked `pilot-done`, never re-executed | #3789 re-marked done at 02:00:38 despite PR #3802 discarded; needed DB surgery (reclassify row + delete dedup) to unblock | [#3818](https://github.com/qf-studio/pilot/issues/3818) |
| D11 | `adapter_processed` rows stamped with wrong repo (PK is `(adapter, issue_id)`, repo added later) — cross-project dedup contamination, colliding issue numbers silently skipped | issue 3787 (pilot repo) stamped `repo='alekspetrov/ai-coding-summit'` | [#3819](https://github.com/qf-studio/pilot/issues/3819) |
| D12 | Scope-drift gate regex abstains silently on `GH-NNNN: ` title prefixes — defense-in-depth bypass (`scope_guard.go:21`) | PRs #3796/#3816 never compared | [#3827](https://github.com/qf-studio/pilot/issues/3827) |
| D13 | Approval decisions lost after restart — `Rehydrate()` restores UI but not the decision goroutine; tap = fake success, `approval_decision` never written. Incl. dead `PrunePendingApprovals` (zero callers) | GH-3790/3788/3818/3819: request IDs set, decisions blank forever | [#3825](https://github.com/qf-studio/pilot/issues/3825) |
| D15 | `--telegram` flag gates inbound only — approval sends work without any way to receive answers, stranding merges | 4 approval requests sent into a void overnight | [#3826](https://github.com/qf-studio/pilot/issues/3826) |
| D16 | Orphan-reconciler races `OnPRCreated` — PRs registered twice, duplicate approval requests + double merged-comments | #3808/#3820 dual register→escalate cycles | [#3828](https://github.com/qf-studio/pilot/issues/3828) |

**Approval-gate explanation (research 2026-07-04, NOT a bug):** pre-merge approval on `require_approval: false` envs is triggered by two intentional config-blind rails in `handleCIPassed` (`controller.go:838-903`) evaluated before env config: size floor (>500 additions, `scope_guard.go:70`) and scope drift (PR `type(scope)` vs linked issue's, `scope_guard.go:40`). #3801=size(864); #3808=type drift(test≠fix); #3820/#3821=scope drift(autopilot≠adapters); #3798 matched its issue → auto. Env resolution (`ResolvedEnv().RequireApproval`) is correct.

## Out of Scope

- Epic empty-branch PR bug — fixed (#3765 → PRs #3781, #3782)
- Duplicate-work refusal misclassified as failed — fixed (V5 → PR #3773)
- TASK-379 remaining waves (V4, V6, V7, V8) — tracked in TASK-379

## Refs

- Parent plan: `.agent/tasks/TASK-379-runtime-self-verification.md`
- Session evidence: watch-loop checks 16:22–20:30 UTC, `executions` table rows 2026-07-03
- Issues: #3784 (D1), #3785 (D2), #3786 (D3), #3787 (D4, blocked by #3786), #3788 (D5), #3789 (D6), #3790 (D7)

---

**Last Updated**: 2026-07-03
