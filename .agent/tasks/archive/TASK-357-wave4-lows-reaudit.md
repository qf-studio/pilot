# TASK-357: TASK-322 Wave 4 — Lows + tests (re-audit gated)

**Status:** ✅ **DONE — TASK-322 audit closed.** Re-audited against `main` @ ab15125b (all 10 actionable lows survived; none pre-fixed by Waves 2–3), implemented manually via nav-loop in one worktree → [PR #3603](https://github.com/qf-studio/pilot/pull/3603). Gates: build ✅ · `make lint` 0 issues ✅ · changed-package tests ✅. Ledger ticked in `TASK-322-security-audit-findings.md` § `low`. Archived to `.agent/tasks/archive/` in PR #3603. **One carry-over:** board-GraphQL partial-data tolerance (unmarshal `Data` on partial errors + tolerant board caller) → TASK-319 board track; only error-aggregation shipped here.
**Parent:** [[TASK-322-remediation-roadmap]] · final remaining tranche.

> **Reminder context:** Waves 0–3 of the TASK-322 security audit are DONE (3 crit · 14 high · 17 med,
> all shipped; last batch v2.166.5 on 2026-06-01). **Wave 4 = the only thing left: 13 lows + tests.**
> Deliberately deferred — all low severity (slow unbounded-map leaks, error-wrap, a heartbeat, a test
> gap). No urgency. Per the roadmap **Gates**: re-audit ~2 weeks out, THEN file the survivors.

## ▶ When you pick this up (the 2-week reminder)

1. **Re-verify each low against current `main` FIRST.** Waves 2–3 incidentally shipped some adjacent
   fixes — do NOT file an already-fixed low. (Wave 3 kickoff did exactly this and dropped 3 re-filed
   items.) Use `nav-graph` / grep the cited files before filing.
2. File survivors as `pilot`-labeled issues — parallel-safe, distinct files, spec-guard'd
   (`## Context|Approach|Acceptance` headers — see [[learning_pilot_issue_spec_guard_headers]]).
3. Expect **~half to no-op** under Pilot (standing pattern) → finish those manually via nav-loop/task
   mode, one worktree, one PR each.

## Wave 4 items (13 lows + tests) — re-verify before filing

| Bucket | Finding | File(s) | Notes |
|---|---|---|---|
| **B6** | `recordedMerges` + `discoveryStart`/`discoveredChecks` maps never evicted | `autopilot/controller.go`, `autopilot/ci_monitor.go` | slow unbounded leak; pair as one eviction sweep |
| **E7** | `retryTracker` has no TTL — abandoned/escalated sources leak forever | `alerts/engine.go:25,260` | slow leak (bounded by distinct-source count); `taskLastProgress` got this in GH-2204, `retryTracker` didn't |
| **G4** | `internal/transcription` = 3 src / **0 `*_test.go`** — parses untrusted media-API responses | `internal/transcription/*` | pure test-add; error-path coverage (malformed/empty/API-fail) |
| A4 | hook-tmp + subagent-argv hardening | executor hooks | robustness |
| G1 | cleanup-return value ignored | (per ledger) | correctness-cosmetic |
| G2 | `%w` error-wrap missing | (per ledger) | diagnosability |
| G3 | discord heartbeat | `discord` adapter | robustness |
| (board low) | `ExecuteGraphQL` discards partial responses — one bad node aborts the whole page | `github/project_source.go:121` + `client.go:822` | folds into TASK-319 board track; pair w/ no-retry |

(Full per-finding detail in `TASK-322-security-audit-findings.md` § `low`, line 223+.)

## Recommended order
B6 + E7 (the two clean leak fixes — natural pair) → G4 (pure tests) → A4/G1/G2/G3 (small) →
board-GraphQL partial-response (with the TASK-319 track). All parallel-safe across distinct files.

## Done when
13 lows either shipped or explicitly closed as already-fixed-on-`main`; tick each off in
`TASK-322-security-audit-findings.md`; archive this file → `.agent/tasks/archive/`. That closes
the entire TASK-322 audit.
