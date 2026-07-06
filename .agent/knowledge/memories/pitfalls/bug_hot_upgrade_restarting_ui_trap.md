---
name: Hot upgrade "Restarting..." UI hides the failure
description: TUI renders "Now running X - Restarting..." in UpgradeStateComplete and "Installing X... 100%" in InProgress without surfacing upgradeMessage, so a successful-message render can be confused for an actual exec — and the failure case scrolls past as a log line
type: pitfall
---

## What

Two related TUI rendering issues in `internal/dashboard/tui.go` that make hot-upgrade outcomes ambiguous:

### 1. `UpgradeStateInProgress` ignores `upgradeMessage`

`tui.go:2744-2747`:
```go
case UpgradeStateInProgress:
    bar := m.renderProgressBar(m.upgradeProgress, 30)
    content.WriteString(fmt.Sprintf("  Installing %s... %s %d%%", m.updateInfo.LatestVersion, bar, m.upgradeProgress))
```

The `upgradeMessage` field is updated by every `upgradeProgressMsg` (line 1124-1125), including the final `progress(100, "Restarting...")` from `hot.go:139`. But the InProgress renderer never reads it — the user only sees "Installing v2.x.x... [████] 100%", never "Restarting...".

### 2. `UpgradeStateComplete` text implies a restart that didn't happen

`tui.go:2749-2751`:
```go
case UpgradeStateComplete:
    content.WriteString(fmt.Sprintf("  Now running %s - Restarting...", m.updateInfo.LatestVersion))
```

This state is reached when `NotifyUpgradeComplete(true, "")` arrives. On Unix, that should be unreachable — a successful `syscall.Exec` replaces the process before the message can be sent. In practice this state IS visible in the wild, which means either:
- The Windows code path is being exercised (it isn't — build tags are clean), OR
- `syscall.Exec` returned nil without actually swapping the image, OR
- Some other path returns nil from `PerformHotUpgrade` without exec

Whatever the cause, the *text* is misleading: it claims "Now running v2.x.x" but the old process is still alive and the version banner elsewhere in the TUI still shows the old version. The user reasonably concludes "the message is lying."

### 3. Failure `AddLog` competes with success rendering

The goroutine sends `AddLog("❌ Upgrade failed: ...")` on error (`main.go:2603`), but this scrolls into the regular log panel while the upgrade panel may still be rendering a stale "Installing... 100%". Two competing surfaces, no single source of truth.

## Why this matters

The bug `[[bug_hot_upgrade_silent_codesign]]` produces a failure that exec'd-with-Gatekeeper makes visible only as a log line. Combined with the InProgress renderer not showing "Restarting...", the user perceives the upgrade as "stuck halfway" rather than "failed."

## How to apply

When touching `internal/dashboard/tui.go` upgrade rendering:

- Display `m.upgradeMessage` in `UpgradeStateInProgress` (e.g., below the progress bar).
- Re-word `UpgradeStateComplete` to reflect reality: on Unix this state should be unreachable in practice — if it IS reached, say "Upgrade installed — restart manually to apply" (matches Windows behavior, which is the only legit way to reach this state).
- Ensure `UpgradeStateFailed` is shown prominently — replace any "Installing... 100%" or "Restarting..." text rather than letting them coexist.
- Consider adding a `lastUpgradeAttempt` timestamp + last-error to a persistent status line so the user doesn't lose the signal when logs scroll.

## Evidence

- `internal/dashboard/tui.go:2735-2755` — render switch on `UpgradeState`
- `internal/dashboard/tui.go:1123-1135` — `upgradeProgressMsg` / `upgradeCompleteMsg` handlers
- `cmd/pilot/main.go:2602-2607` — goroutine notify pattern
- `internal/upgrade/hot.go:139` — `progress(100, "Restarting...")` that's never displayed

## Related

- [[bug_hot_upgrade_silent_codesign]] — the upstream cause this UX hides
- [[pattern_hot_upgrade_bootstrap]] — design rationale for the upgrade flow
