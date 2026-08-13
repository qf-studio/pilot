# fix(alerts): dead-man streak alerts can never fire — daemon rule set omits all dead-man rules

**Status**: ✅ MERGED + REVIEWED — 2026-08-13 13:58Z merge → [pilot#4866](https://github.com/qf-studio/pilot/issues/4866) → **PR#4867**; post-merge review posted 2026-08-13 (**verdict: APPROVE** — [comment](https://github.com/qf-studio/pilot/pull/4867#issuecomment-5284704121)). All 5 legs faithful; mutation-verified (union killed from 3 packages; fired-reset + lifecycle-emit killed). Two minor defects, unfiled founder calls: **D1** — the no-match WARN is unasserted (acceptance's named mutant SURVIVES; `WithLogger` injection point existed, ~30 LOC follow-up) · **D2 latent** — `label_strip`/`push_retry_exhausted` trackers register only in `startGithubSDKPollerForRepo`, so non-GitHub-poller daemon shapes (Telegram-only, gateway path) relay those seams into silent `namedDeadManTracker` misses that the new doctor check cannot see (it reads rule coverage, not registration); box unaffected (GitHub polling on). Notes N1–N4 on the PR. Rode the 14:00Z train into **v2.259.2 — LIVE on the box** (hot restart 14:19Z, metric-confirmed). **Re-drill PASS 2026-08-13 18:36Z** — `label_lifecycle_failure_streak` paged slack-engineering 10 min after the archive-403 break (per-repo tracker name on the wire); `push_retry_exhausted` seam paged 18:41Z. TASK-441 acceptance box 1 ticked. **DONE — archived.**
**Created**: 2026-08-13
**Assignee**: Pilot

## Context

Found by the TASK-441 kill-drill A (2026-08-13): 57 minutes of continuous seam
failures on an archived sandbox produced zero dead-man output while per-task
`task_failed` alerts fired normally. Post-mortem (session research, static-
provable): the four dead-man rules live only in `alerts.defaultRules()`
(`internal/alerts/types.go:466-521`), reachable solely via the one-shot
`--alerts` CLI; the daemon seeds rules from the separate 7-rule list in
`internal/config/config.go:543-624` (deployed config: legacy 5-rule list) and
`handleDeadManStreak` returns silently on no-match. Compounding: global
(non-per-repo) `label_lifecycle` streak reset by healthy repos; lifecycle
label-strip + push-retry seams wired to no tracker; strict-equality fire
condition; doctor blind (and one check hardcoded `return false`).

Five legs in #4866: rule-set union · loud no-match + `>=` fire · per-repo
streak keying · wire the two seams · doctor rule-coverage check.

## Acceptance

Per issue body. After merge + box deployment: **operator re-drill** per
`.agent/sops/operations/task441-kill-drill.md` — TASK-441 acceptance box 1
ticks only when the re-drill's streak alert actually pages.

## Refs

- Issue: https://github.com/qf-studio/pilot/issues/4866
- Drill record: `.agent/sops/operations/task441-kill-drill.md` § 2026-08-13
- Blocks: TASK-441 archive · memory `hot-restart-preserves-pid-uptime-false-mismatch` (sibling observability find, same day)
