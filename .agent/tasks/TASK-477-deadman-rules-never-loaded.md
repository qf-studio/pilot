# fix(alerts): dead-man streak alerts can never fire — daemon rule set omits all dead-man rules

**Status**: 🚀 Dispatched 2026-08-13 → [pilot#4866](https://github.com/qf-studio/pilot/issues/4866) (`pilot` + `no-decompose`)
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
