# GH-369: Hot Reload on Release Update

**Status:** Planning
**Priority:** P2 - Enhancement
**Depends on:** GH-368 (Dashboard State Persistence)

## Overview

Enable seamless in-place upgrades when new Pilot releases are available. Dashboard state persists across restart, making hot reload viable.

## Prerequisites

- [x] SQLite sessions table (GH-367 code exists)
- [ ] GH-368 merged (persistence code committed)
- [ ] Token usage survives restart
- [ ] Task history loads on startup

## Design

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                      HOT RELOAD SYSTEM                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐      │
│  │   Version    │    │   Upgrade    │    │   Process    │      │
│  │   Checker    │───►│   Manager    │───►│   Restarter  │      │
│  │  (periodic)  │    │  (existing)  │    │ (syscall.Exec)│     │
│  └──────────────┘    └──────────────┘    └──────────────┘      │
│         │                   │                    │              │
│         ▼                   ▼                    ▼              │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐      │
│  │  Dashboard   │    │    State     │    │    New       │      │
│  │ Notification │    │   Persist    │    │   Binary     │      │
│  │ "⬆️ v0.13.1"  │    │  (SQLite)    │    │   Starts     │      │
│  └──────────────┘    └──────────────┘    └──────────────┘      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Flow

```
1. Background goroutine checks GitHub releases every 30 min
                    │
                    ▼
2. New version detected (v0.13.1 > v0.13.0)
                    │
                    ▼
3. Dashboard shows: "⬆️ Update available: v0.13.1 (press 'u' to upgrade)"
                    │
                    ▼
4. User presses 'u' OR auto-trigger if idle for 5 min
                    │
                    ▼
5. Wait for running task to complete (existing GracefulUpgrader)
                    │
                    ▼
6. Flush session to SQLite (tokens, timestamp)
                    │
                    ▼
7. Download new binary (existing Upgrader.Upgrade)
                    │
                    ▼
8. syscall.Exec() - replace current process with new binary
                    │
                    ▼
9. New binary starts, runs migrations, hydrates from SQLite
                    │
                    ▼
10. Dashboard restored with full state
```

## Implementation

### Phase 1: Background Version Checker

**New file:** `internal/upgrade/checker.go`

```go
type VersionChecker struct {
    upgrader     *Upgrader
    checkInterval time.Duration
    onUpdate     func(info *VersionInfo)
}

func NewVersionChecker(currentVersion string, interval time.Duration) (*VersionChecker, error) {
    upgrader, err := NewUpgrader(currentVersion)
    if err != nil {
        return nil, err
    }
    return &VersionChecker{
        upgrader:      upgrader,
        checkInterval: interval,
    }, nil
}

func (c *VersionChecker) Start(ctx context.Context) {
    ticker := time.NewTicker(c.checkInterval)
    defer ticker.Stop()

    // Check immediately on start
    c.check(ctx)

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.check(ctx)
        }
    }
}

func (c *VersionChecker) check(ctx context.Context) {
    info, err := c.upgrader.CheckVersion(ctx)
    if err != nil {
        slog.Debug("version check failed", slog.Any("error", err))
        return
    }

    if info.UpdateAvail && c.onUpdate != nil {
        c.onUpdate(info)
    }
}
```

### Phase 2: Dashboard Integration

**File:** `internal/dashboard/tui.go`

Add to Model:
```go
type Model struct {
    // ... existing fields
    updateAvailable *upgrade.VersionInfo  // NEW
    upgrading       bool                   // NEW
}

// New message types
type updateAvailableMsg struct {
    Info *upgrade.VersionInfo
}

type upgradeCompleteMsg struct {
    NewVersion string
    Err        error
}
```

Add key handler for 'u':
```go
case "u":
    if m.updateAvailable != nil && !m.upgrading {
        m.upgrading = true
        return m, m.performUpgrade()
    }
```

Add to View():
```go
// Show update notification in header
if m.updateAvailable != nil {
    b.WriteString(warningStyle.Render(
        fmt.Sprintf("  ⬆️ Update available: %s (press 'u' to upgrade)",
            m.updateAvailable.Latest)))
    b.WriteString("\n")
}
```

### Phase 3: Process Restart via syscall.Exec

**File:** `internal/upgrade/restart.go`

```go
// RestartWithNewBinary replaces the current process with the new binary
// This preserves the PID and terminal attachment
func RestartWithNewBinary(binaryPath string, args []string) error {
    // Flush any buffered output
    os.Stdout.Sync()
    os.Stderr.Sync()

    // Get current environment
    env := os.Environ()

    // Add marker to indicate this is a restart (for logging)
    env = append(env, "PILOT_RESTARTED=1")

    // Replace current process
    // This never returns on success
    return syscall.Exec(binaryPath, args, env)
}
```

### Phase 4: Graceful Upgrade Flow

**File:** `internal/upgrade/hot.go`

```go
type HotUpgrader struct {
    graceful *GracefulUpgrader
    store    *memory.Store
}

func (h *HotUpgrader) PerformHotUpgrade(ctx context.Context, release *Release) error {
    // 1. Flush session state
    if h.store != nil {
        // Session is auto-saved via persistTokenUsage, but force a sync
        slog.Info("flushing session state before upgrade")
    }

    // 2. Perform the upgrade (download, backup, install)
    opts := &UpgradeOptions{
        WaitForTasks: true,
        TaskTimeout:  2 * time.Minute,
        OnProgress: func(pct int, msg string) {
            slog.Info("upgrade progress", slog.Int("percent", pct), slog.String("msg", msg))
        },
    }

    if err := h.graceful.PerformUpgrade(ctx, release, opts); err != nil {
        return fmt.Errorf("upgrade failed: %w", err)
    }

    // 3. Get the binary path
    binaryPath := h.graceful.GetUpgrader().BinaryPath()

    // 4. Restart with same arguments
    args := os.Args

    slog.Info("restarting with new binary", slog.String("path", binaryPath))

    // 5. Exec into new binary (this never returns on success)
    if err := RestartWithNewBinary(binaryPath, args); err != nil {
        return fmt.Errorf("restart failed: %w", err)
    }

    return nil // Never reached
}
```

## Files to Create/Modify

| File | Action | Description |
|------|--------|-------------|
| `internal/upgrade/checker.go` | Create | Background version checker |
| `internal/upgrade/restart.go` | Create | syscall.Exec wrapper |
| `internal/upgrade/hot.go` | Create | Hot upgrade orchestrator |
| `internal/dashboard/tui.go` | Modify | Add update notification, 'u' key handler |
| `cmd/pilot/main.go` | Modify | Wire version checker to dashboard |

## Configuration

```yaml
# ~/.pilot/config.yaml
upgrade:
  auto_check: true           # Enable background version checking
  check_interval: 30m        # How often to check (default: 30 min)
  auto_upgrade_on_idle: false # Auto-upgrade after 5 min idle (optional)
  notify_channel: telegram   # Where to send update notifications
```

## Dashboard UX

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│   ▄▄▄▄▄▄▄  ▄▄▄▄▄▄▄  ▄▄        ▄▄▄▄▄▄▄  ▄▄▄▄▄▄▄            │
│   ██   ██     ██     ██       ██   ██     ██               │
│   ▀▀▀▀▀██     ██     ██       ██   ██     ██   v0.13.0     │
│   ██▄▄▄██  ▄▄▄██▄▄▄  ██▄▄▄▄▄▄ ██▄▄▄██     ██               │
│                                                             │
│   ⬆️ Update available: v0.13.1 (press 'u' to upgrade)       │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│ METRICS                                                     │
│ ...                                                         │
```

During upgrade:
```
│   🔄 Upgrading to v0.13.1... [████████░░░░░░░░░░] 45%       │
```

## Acceptance Criteria

- [ ] Background version check every 30 min
- [ ] Dashboard shows "Update available" notification
- [ ] 'u' key triggers upgrade
- [ ] Upgrade waits for running tasks
- [ ] State persists across restart (tokens, history)
- [ ] Process restarts seamlessly via syscall.Exec
- [ ] Works on macOS and Linux
- [ ] Rollback works if new version fails to start

## Testing

```bash
# Manual test flow
1. Run: pilot start --dashboard --telegram --github
2. Execute a task, note token count
3. Create a new release (v0.X.Y+1)
4. Wait for "Update available" notification (or trigger manually)
5. Press 'u'
6. Verify: Dashboard shows new version
7. Verify: Token count preserved
8. Verify: Task history preserved
```

## Edge Cases

| Case | Handling |
|------|----------|
| Upgrade during task | Wait for task completion |
| Network failure during download | Show error, keep running |
| New binary fails to start | Auto-rollback to backup |
| Homebrew installation | Block hot upgrade, show brew command |
| No write permission | Show error with instructions |

## Notes

- syscall.Exec replaces process in-place (same PID, terminal attached)
- SQLite handles concurrent access gracefully
- Existing GracefulUpgrader handles task waiting
- macOS may need codesign handling (already in PrepareForExecution)
