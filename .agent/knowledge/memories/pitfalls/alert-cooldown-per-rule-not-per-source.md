# Pitfall: alert engine cooldown is per rule name, not per task or source

## Summary
Engine cooldown (shouldFire) is keyed on lastAlertTimes[rule.Name], so it is per RULE, not per task/source: two different tasks tripping the same rule inside the cooldown produce one alert, and cooldown 0 fires every time. Per-source dedupe exists only when Defaults.SuppressDuplicates is on, keyed on rule|source|message within duplicateSuppressTTL, where source is event.Source or task:<TaskID>. Keep alert messages stable per task (no raw seconds/ages in the message) or dedupe never matches.

## Context
2026-09-30, #5498 research into how a second heartbeat kill would dedupe.

## Details
Engine cooldown (shouldFire) is keyed on lastAlertTimes[rule.Name], so it is per RULE, not per task/source: two different tasks tripping the same rule inside the cooldown produce one alert, and cooldown 0 fires every time. Per-source dedupe exists only when Defaults.SuppressDuplicates is on, keyed on rule|source|message within duplicateSuppressTTL, where source is event.Source or task:<TaskID>. Keep alert messages stable per task (no raw seconds/ages in the message) or dedupe never matches.

## Recommended Approach
Assume one alert per rule per cooldown window regardless of how many tasks trip it. If per-task dedupe matters, turn on `Defaults.SuppressDuplicates` and keep the alert message stable per task (round ages to minutes, no raw durations). `Source` defaults to `task:<TaskID>` when the event carries no Source.

## Related
- [[deadlock-alert-idle-refire-mark-sent-asymmetry]]
- `internal/alerts/engine.go` (`shouldFire`, `fireAlert`, `eventSource`)

---
**Captured**: 2026-09-30
**Confidence**: 90%
**Concepts**: alerts
