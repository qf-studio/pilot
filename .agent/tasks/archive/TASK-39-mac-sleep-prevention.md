# TASK-39: Mac Sleep Prevention Setup

**Status**: ✅ Complete
**Priority**: High (P2)
**Created**: 2026-01-28

---

## Context

**Problem**:
Mac goes to sleep, Pilot process suspends, backlog stops processing.

**Goal**:
Add setup option and docs for preventing Mac sleep on always-on machines (Mac Mini).

---

## Implementation

### 1. Add `pilot setup --no-sleep` command

```go
// In cmd/pilot/setup.go or main.go
func setupNoSleep() error {
    // Check if macOS
    if runtime.GOOS != "darwin" {
        return fmt.Errorf("--no-sleep only supported on macOS")
    }

    // Disable sleep
    cmd := exec.Command("sudo", "pmset", "-a", "sleep", "0")
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    cmd.Stdin = os.Stdin
    return cmd.Run()
}
```

### 2. Add to `pilot doctor` checks

Show sleep status in health check:
```
Sleep Prevention:
  ✓ Sleep disabled (pmset sleep = 0)
  - OR -
  ⚠ Sleep enabled - Pilot may pause when idle
    → Run: pilot setup --no-sleep
```

### 3. Update setup wizard

Add prompt during `pilot setup`:
```
Is this machine always-on (Mac Mini/server)? [y/N]
→ If yes: offer to disable sleep
```

---

## Files to Modify

| File | Change |
|------|--------|
| `cmd/pilot/setup.go` | Add `--no-sleep` flag and logic |
| `internal/health/health.go` | Add sleep status check |
| `cmd/pilot/main.go` | Wire up new flag |

---

## Code Changes

### setup.go - Add flag

```go
var noSleep bool

func init() {
    setupCmd.Flags().BoolVar(&noSleep, "no-sleep", false, "Disable Mac sleep (requires sudo)")
}

func runSetup() error {
    // ... existing setup ...

    if noSleep {
        if err := disableMacSleep(); err != nil {
            return err
        }
    }
}

func disableMacSleep() error {
    if runtime.GOOS != "darwin" {
        fmt.Println("⚠️  --no-sleep only works on macOS")
        return nil
    }

    fmt.Println("🔋 Disabling Mac sleep (requires sudo)...")
    cmd := exec.Command("sudo", "pmset", "-a", "sleep", "0")
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    cmd.Stdin = os.Stdin
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("failed to disable sleep: %w", err)
    }
    fmt.Println("   ✓ Sleep disabled")
    return nil
}
```

### health.go - Add check

```go
func checkSleepStatus() Check {
    if runtime.GOOS != "darwin" {
        return Check{Name: "sleep", Status: StatusDisabled, Message: "N/A (not macOS)"}
    }

    out, err := exec.Command("pmset", "-g", "custom").Output()
    if err != nil {
        return Check{Name: "sleep", Status: StatusWarning, Message: "could not check"}
    }

    if strings.Contains(string(out), "sleep		0") {
        return Check{Name: "sleep", Status: StatusOK, Message: "disabled"}
    }

    return Check{
        Name:    "sleep",
        Status:  StatusWarning,
        Message: "enabled - Pilot may pause",
        Fix:     "pilot setup --no-sleep",
    }
}
```

---

## Acceptance Criteria

- [x] `pilot setup --no-sleep` disables Mac sleep
- [x] `pilot doctor` shows sleep status
- [x] Only runs on macOS
- [x] Warns about sudo requirement
- [x] Works on Mac Mini

---

## Testing

1. Run `pmset -g custom` - note current sleep value
2. Run `pilot setup --no-sleep`
3. Verify `pmset -g custom` shows `sleep 0`
4. Run `pilot doctor` - verify shows ✓

---

## Notes

- Requires sudo - user must enter password
- Only affects system sleep, not display sleep
- Can be reversed with `sudo pmset -a sleep 1`
