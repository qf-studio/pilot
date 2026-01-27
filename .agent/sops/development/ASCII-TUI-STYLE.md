# ASCII TUI Style Guide

## Purpose

Minimalistic terminal UI patterns for CLI tools and status displays.

## Core Principles

1. **Compact over verbose** - Information density matters
2. **Symbols over words** - Use ✓ ○ ✗ · instead of "enabled", "warning", etc.
3. **Grid layouts** - Align items in columns when showing lists
4. **Single separator** - Use ━━━ lines sparingly (header only)

---

## Status Symbols

| Symbol | Meaning | Usage |
|--------|---------|-------|
| ✓ | OK/Enabled | Feature working, check passed |
| ○ | Warning/Degraded | Feature partially available |
| ✗ | Error/Failed | Critical issue, feature broken |
| · | Disabled | Feature not configured |
| │ | Separator | Inline divider between sections |

---

## Patterns

### Header

```
APPNAME v1.0.0 │ Context
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### Feature Status Grid

Compact 3-column layout:
```
✓ Feature1     ✓ Feature2     ○ Feature3*
✓ Feature4     · Feature5     ✓ Feature6

* Feature3: warning note
```

### Inline Features

```
✓ Telegram, Briefs, Alerts, Memory
○ Voice*

* Voice: no ffmpeg
```

---

## Implementation (Go)

```go
// Header
fmt.Printf("PILOT v%s │ Telegram Bot\n", version)
fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

// Features
fmt.Printf("✓ %s\n", strings.Join(enabled, ", "))
if len(warnings) > 0 {
    fmt.Printf("○ %s\n", strings.Join(warnings, ", "))
}

// Context
fmt.Printf("Project: %s\n", project)
fmt.Println()
fmt.Println("Listening... (Ctrl+C to stop)")
fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
```

---

## Avoid

- ❌ Verbose descriptions ("Press Ctrl+C to stop the bot")
- ❌ Decorative boxes (┌─────┐ box art)
- ❌ Multiple separator styles
- ❌ Emoji overload
- ❌ Dynamic loading animations in non-interactive contexts

---

## Reference Implementation

See: `pilot/internal/banner/banner.go` - `StartupTelegram()` function
