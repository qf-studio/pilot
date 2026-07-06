---
name: Hot upgrade silently swallows macOS codesign failure
description: PrepareForExecution error is discarded with `_ =` at upgrade.go:407, so a failed ad-hoc codesign produces an unsigned binary that Gatekeeper later refuses to syscall.Exec — manifests as "hit u, see restart, old version still running"
type: pitfall
---

## What

`internal/upgrade/upgrade.go:407`:

```go
// Prepare binary for execution (removes quarantine, signs on macOS)
// Errors are non-fatal - binary may still work
_ = PrepareForExecution(u.binaryPath)
```

`PrepareForExecution` on macOS (`internal/upgrade/codesign_darwin.go:11-17`):
1. `xattr -d com.apple.quarantine <binary>` — error ignored *inside* the function
2. `codesign -s - <binary>` (ad-hoc sign) — error **returned to caller**

The caller throws the returned error away. If ad-hoc signing fails (codesign tool missing, hardened-runtime mismatch, transient FS issue), the binary on disk after the atomic rename is **unsigned and quarantined**. When `syscall.Exec` later tries to load it, macOS Gatekeeper refuses — exec returns EPERM / EACCES.

## Why this matches the symptom

User reports: hit `u` → "update loading" → "restart" message → old version still running → only full stop + manual relaunch fixes it.

Manual relaunch works because terminal-initiated process spawn gets different Gatekeeper treatment than a `syscall.Exec` from a running daemon. Same on-disk binary, different verdict.

## Where the failure is visible (or isn't)

- `RestartWithNewBinary` returns the exec error (`internal/upgrade/restart.go:53`)
- `PerformHotUpgrade` wraps it ("restart failed: ...") and returns (`hot.go:145-147`)
- Goroutine in `cmd/pilot/main.go:2602-2605` sends `NotifyUpgradeComplete(false, errStr)` + `AddLog("❌ Upgrade failed: ...")`
- TUI transitions to `UpgradeStateFailed` with the error visible (`tui.go:1131-1135`, `2753-2755`)

The failure **is** displayed, but is easy to miss in a busy dashboard. The bigger smell is that the *root cause* (codesign failure) is invisible — the user sees the *symptom* (exec failed) without the upstream trigger.

## How to apply

When touching `internal/upgrade/upgrade.go` or the codesign helpers:

- Do NOT swallow `PrepareForExecution` errors — at minimum log them at WARN; ideally fail the upgrade if codesign returns non-nil on macOS.
- Treat ad-hoc signing as a *required* step, not a best-effort one, on Darwin builds.
- Before `syscall.Exec`, smoke-test the new binary with `exec.Command(binaryPath, "--version").Run()` — a 100ms guard that catches Gatekeeper rejection cleanly without losing the running process.

## Evidence

- `internal/upgrade/upgrade.go:405-407` — the swallowed error
- `internal/upgrade/codesign_darwin.go:11-17` — what gets swallowed
- `internal/upgrade/restart.go:53` — where Gatekeeper rejection surfaces
- `internal/upgrade/graceful.go:125` — pre-existing GH-272 comment acknowledging this class of issue

## Related

- [[bug_hot_upgrade_restarting_ui_trap]] — sibling pitfall on the "Restarting..." UX confusion
- [[pattern_hot_upgrade_bootstrap]] — chicken-and-egg risk for persistence fixes
