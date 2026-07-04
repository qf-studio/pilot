---
name: self-upgrade now auto-triggers on detection, plus a fail-loud "N releases behind" check
description: GH-3790 fix — VersionChecker.OnUpdate auto-enqueues the hot upgrade (config-gated, keypress still works as a manual override) instead of requiring a human to press 'u' in the TUI; a new staleness check logs WARN + fires an alerts.Event once the daemon falls upgrade.stale_release_threshold releases behind, with a pilot doctor counterpart.
type: decision
---
Fixes the root cause traced in [[pitfall_self_upgrade_requires_manual_keypress]] (mem-044): `main.go`'s `versionChecker.OnUpdate` callback only logged and notified the dashboard — `PerformHotUpgrade` never ran unless a human was watching `--dashboard` and pressed `'u'`. The daemon sat 8 releases stale for 7h11m on 2026-07-03 with zero errors logged, because nothing was actually broken — the trigger simply never existed.

**Fix 1 — auto-trigger (`cmd/pilot/main.go`, `internal/config`):** `OnUpdate` now sends to `upgradeRequestCh` (the same channel the TUI keypress writes to) whenever `cfg.Upgrade.AutoHotUpgrade` is true (default). Non-blocking send on the buffered-1 channel — a request already queued/running is a no-op, so this can't double-fire. The keypress path is untouched, so a human can still force an upgrade manually. Gated by config (not hardcoded) per the pitfall's own recommendation, so it's a documented behavior change (`upgrade.auto_hot_upgrade`), not a silent one.

**Fix 2 — fail-loud staleness (`internal/upgrade`, `internal/health`, `internal/config`):**
- `Upgrader.CheckVersion` now also returns `VersionInfo.ReleasesBehind`, computed by the new exported `upgrade.ReleasesBehind(releases, currentVersion)` (counts stable, non-draft/non-prerelease releases newer than current, from the same `/releases?per_page=30` fetch — bumped from 10 so an 8-releases-behind daemon like the incident doesn't undercount).
- `VersionChecker.SetStaleThreshold(n)` / `OnStale(fn)`: once `ReleasesBehind >= n`, every check logs `slog.Warn` and invokes the callback. Wired in `main.go` to also emit `alerts.Event{Type: EventTypeConfigError, Metadata: {"check": "self_upgrade_stale", ...}}` when an alerts engine is configured — reuses the existing `AlertTypeServiceUnhealthy` rule/handler (`internal/alerts/engine.go handleConfigError`) rather than inventing a new event/rule type for a single check.
- Also added a `pilot doctor` counterpart (`checkSelfUpgradeStaleness` in `internal/health/health.go`) using the same `upgrade.ReleasesBehind` helper against an unauthenticated GitHub API call — a one-shot equivalent of the periodic runtime check, per the task's "doctor check candidate" framing and TASK-379's Verifiable/preflight family.
- Threshold defaults to 3 (`config.DefaultConfig()`'s `UpgradeConfig.StaleReleaseThreshold`); 0 disables the check.

**Side fix:** `defaultAlertRules()` had no default `service_unhealthy`-type rule at all, so the existing GH-3718 config-error alert path (and this new one) would never actually dispatch on a fresh install even with `alerts.enabled: true` — the WARN log would fire but the alert wouldn't. Added `service_unhealthy` (enabled, warning, 1h cooldown) to the defaults list so both paths are live out of the box.

**Not done:** the whole checker+hot-upgrader subsystem is still only constructed when `dashboardMode && program != nil` — non-dashboard (`pilot start --telegram --github` without `--dashboard`) still has zero self-upgrade capability, automatic or manual. Root-caused in [[pitfall_self_upgrade_requires_manual_keypress]] but out of scope for this fix (GH-3790-3); flag if a future task needs self-upgrade in polling-only mode.

Related: [[pitfall_self_upgrade_requires_manual_keypress]], [[bug_daemon_autoupgrade_reverts_dev_binary]].
