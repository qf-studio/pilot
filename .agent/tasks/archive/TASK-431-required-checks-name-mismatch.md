# fix(autopilot): required_checks naming a check that never posts on a repo → permanent CI pending — detect the mismatch and fail loudly (GH-4643 misdiagnosis class)

**Created**: 2026-07-31 · **Status**: ✅ Delivered (PR #4647 merged 16:12Z — all 4 mandates + StageFailed routing to avoid bogus CI-fix spawn; 264-line test file) · **Live on box**: v2.251.1 carries it (board 08-03 08:04Z) — ⚠️ VERIFY LEG OPEN: panel still shows stale `failed` rows (console PR#70, canary 87/90/94) that the stale-panel reconcile should clear/flag; check after the Mon 08-03 16:00 Berlin train restart, then archive this doc · **Last Updated**: 2026-08-03
**Repo**: `qf-studio/pilot`
**Pilot issue**: https://github.com/qf-studio/pilot/issues/4646

## Incident (2026-07-31, operator-resolved)

18 release-train scopes across auth-service and studio-sdk were stuck or
parked; auth-service went **11 days without a release** (v0.67.3, 07-20 →
07-31) while 27 merged PRs accumulated. Root cause was NOT "no post-merge CI"
(GH-4643's diagnosis) and NOT GH-4384 (fix present and working):

- Both repos inherit the **global** `autopilot.required_checks: [test, lint]`
  (no per-project `ci_checks` override).
- auth-service push-to-main runs: `test`, `Migration Dry-Run Check`,
  `Build & Push Docker Image` — **no run named `lint`**, ever.
- studio-sdk push-to-main runs: `lint`, `Check Secret Patterns`,
  `build-test` — **no run named `test`**, ever.
- `checkRequiredChecks` (`internal/autopilot/ci_monitor.go:242`) seeds every
  required name `CIPending`; a name that never posts stays pending forever,
  and a non-empty allowlist always wins over green discovered runs (GH-4307,
  `ci_monitor.go:227`).
- Post-merge: `handlePostMergeCI`'s GH-4643 no-workflow probe
  (`HasAnyCIConfigured`) passes — CI *exists*, the names just mismatch — so
  every carrier rode the full 30m timeout; GH-4643's cap then parked the
  scopes with the misleading message "this repo likely has no post-merge CI
  configured".

`internal/config/config.go:271` already documents this exact class in a
comment. The `pointer` project already carries the working idiom
(per-project `ci_checks.required_checks: []`).

Operator resolution (all on the box, 15:0x–15:32Z): config backup
(`config.yaml.bak-2026-07-31-cichecks`) + `ci_checks: {required_checks: []}`
added to the auth-service and studio-sdk project blocks · 10 parked + 8
superseded-failed scopes marked `done` (today's rolled-up train carried all
their members) · graceful restart. Two ticks later: auto-discovery found the
real check names, post-merge CI passed for carriers 484/106, and
**auth-service v0.68.0** + **studio-sdk v0.31.2** released cleanly.

## Fix (mandated shape)

1. **Mismatch detection in `checkRequiredChecks`**: when the SHA's discovered
   check-runs are all `completed` and a required name has never appeared
   (after the existing discovery grace period), stop returning silent
   `CIPending`. Classify as a config error: loud WARN naming
   repo + missing required name(s) + the discovered names, and surface a
   distinct failure reason (so scope-release failure/park paths carry it).
   MUST NOT fire while any run on the SHA is still executing, and MUST NOT
   change behavior for genuinely-pending required checks.
2. **Honest park message**: `parkScopeReleaseAfterTimeouts`
   (`scope_release.go:480`) currently asserts "no post-merge CI configured" —
   include discovered-vs-required names when available so the next operator
   isn't sent chasing workflows that exist.
3. **Startup lint**: for each project whose effective `required_checks` is
   non-empty, log a WARN at controller start when the latest main SHA's
   check-run names don't cover the required list (cheap one-shot probe;
   feature-flag or best-effort on rate-limit).
4. **Cosmetic follow-through**: stale `autopilot_pr_state` rows whose scope is
   terminal are skipped at RestoreState but linger in the dashboard's
   non-released panel (439/443/446/476/103/104 today) — reconcile or age them
   out.

## Tests

- Table-driven `checkRequiredChecks`: required name absent + all runs
  completed → mismatch classification; required name absent + a run still
  in_progress → still pending; required name present in any status →
  unchanged behavior.
- Post-merge path: mismatch reason propagates into
  `handleScopeReleaseFailure`/park message.

## Refs

- Latent in every override-less project (navigator, pilot-console,
  pilot-console-ui, pilot-cloud-infra, ai-coding-summit…) — any of them grows
  a check-name drift, this recurs.
- Prior art: GH-4307 (allowlist-wins), GH-4384 (check-runs source of truth),
  GH-4643/#4644 (timeout park — correct mechanism, wrong diagnosis string).
