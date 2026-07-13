# TASK-403: Canary lifecycle scenarios — scenario abstraction, epic-flow canary, canary metrics isolation

**Status**: ✅ SHIPPED overnight 2026-07-12→13 — A1 #4240→PR #4256 · A2 #4241→PR #4250 · A3 #4242→PR #4253, all merged. Manual actions 1–2 done (config de-trained; sandbox CI `6e38e9b` green). Remaining: review merged PRs, then `gh workflow enable 307188350` (cron re-enable — A1 is merged so metrics isolation gate is satisfied once the daemon runs a build containing #4256). Note: each issue produced one superseded duplicate PR (#4245/#4249) closed in favor of the merged one — watch whether that retry-duplicate pattern recurs.
**Type**: hardening (test-tier gap — every recent defect escaped unit tests and was caught only in production)
**Context**: TASK-379 V8 shipped the synthetic canary as pure GitHub Actions bash (`.github/workflows/pilot-canary.yml`), single hardcoded scenario (version bump → PR → merge), currently `disabled_manually` pending an auto-merge decision. The defect classes that keep escaping (TASK-394/395/401/402) are all epic-lifecycle: decomposition, sibling sequencing, parent close-out. No canary covers them.

## Problem

1. **No lifecycle scenario coverage.** The canary asserts only the direct path (1 issue → 1 PR → merge). The epic path — where all July incidents happened — has zero synthetic coverage.
2. **No scenario abstraction.** Task body, assertions, and poll loop are inline bash in one workflow job (`pilot-canary.yml:57-160`); the idempotency guard (any open `pilot` label) and alert dedup (literal title) assume one scenario per repo.
3. **Canary pollutes real telemetry.** The sandbox is an ordinary `projects:` entry — its executions flow into the same ledger/metrics as real work, and the sandbox rides the real Mon–Fri release train (`release.trigger: on_schedule` in `~/.pilot/config.yaml:453-463`, flagged 2026-07-09, never actioned). Re-enabling the cron would contaminate the M3 baseline week.

## Plan (3 Pilot issues)

- **A1 — Canary project isolation**: add `canary: true` to `ProjectConfig` (`internal/config/config.go:233-256`); tag executions from canary projects; exclude them from success-rate/issue-level metrics, hydrator, and dashboard history. Repo column already exists on executions (`internal/memory/store.go:285`).
- **A2 — Scenario abstraction**: extract the poll/assert loop (`pilot-canary.yml:100-160`) into `scripts/canary-poll.sh` parameterized by terminal condition (merged / N-children-merged / labeled); parameterize issue template; per-scenario labels (`pilot` + `canary-scenario-<name>`) so guards and alert dedup are scenario-scoped. Existing version-bump scenario must stay green.
- **A3 — Epic-lifecycle scenario** (blocked by A2): a scenario that files a deliberately decomposable issue on the sandbox (2 subtasks, second declares `Depends on:` the first) and asserts: N children created (no single-child cascade — Defect A), each child exactly one merged PR (no duplicates — Defect B), parent auto-closed once, no `pilot-needs-clarification` on the closed parent (TASK-395 class), zero FK-787 in the run.

## Manual actions (user config / sandbox repo — NOT Pilot-dispatchable)

- [x] Remove `release:` block from `pilot-canary-sandbox` entry in `~/.pilot/config.yaml` — DONE 2026-07-12 ~23:15. ⚠️ Running daemon loaded the old config; takes effect on next daemon restart (must happen before Mon 16:00 Berlin or the train fires once more).
- [x] Add CI workflow to `qf-studio/pilot-canary-sandbox` — DONE 2026-07-12, commit `6e38e9b` (`.github/workflows/ci.yml`, `go vet` + `go test`), first run green (29s). Resolves the TASK-379 auto-merge decision: canary PRs now get a real passing check for `handleCIPassed`.
- [ ] After A1–A3 land: `gh workflow enable 307188350` (re-enable cron). Still gated — do NOT enable before #4240 (metrics isolation) merges, or canary runs pollute the M3 baseline.

## Must NOT change

- The "zero in-daemon canary code" architecture decision (TASK-379 D2) — scenarios stay in GitHub Actions; only the isolation flag (A1) touches Go.
- Real-project metrics semantics — A1 excludes canary rows, it must not alter counting for non-canary projects (existing metrics tests stay green).

## Refs

- Canary workflow: `.github/workflows/pilot-canary.yml` · precedent `brew-tap-token-canary.yml`
- TASK-379 V8 design + open auto-merge decision: `.agent/tasks/TASK-379-runtime-self-verification.md`
- Release-train pollution flag: `.agent/.context-markers/2026-07-09_m7-4d2-fanout-shipped-v2236.1.md:66-67`
- Defect classes to assert against: TASK-394 (FK-787), TASK-395 (stale clarification label), TASK-401 (Defect A), TASK-402 (Defect B)
