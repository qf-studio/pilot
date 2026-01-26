# TASK-15: Structured Logging

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Logging

---

## Context

**Problem**:
Current logging is basic `log.Printf` - hard to parse, filter, or integrate with log management systems.

**Goal**:
Production-grade structured logging with levels, context, and rotation.

---

## Requirements

### Log Format
```json
{
  "timestamp": "2026-01-26T14:30:00Z",
  "level": "info",
  "component": "executor",
  "task_id": "TG-123",
  "message": "Task started",
  "duration_ms": 1234,
  "tokens": 5000
}
```

### Log Levels
- `debug` - Verbose, development only
- `info` - Normal operations
- `warn` - Recoverable issues
- `error` - Failures

### Features
- JSON output for production
- Human-readable for development
- Context propagation (task_id, project)
- Log rotation (size/time based)
- Configurable output (stdout, file, syslog)

---

## Implementation

### Library Options
1. **zerolog** - Fast, zero-allocation
2. **zap** - Uber's structured logger
3. **slog** - Go 1.21+ standard library

**Recommendation**: `slog` (stdlib, no dependencies)

---

## Configuration

```yaml
logging:
  level: info
  format: json  # or "text"
  output: stdout  # or "/var/log/pilot/pilot.log"
  rotation:
    max_size: 100MB
    max_age: 7d
    max_backups: 3
```

---

**Monetization**: Log export to cloud (Datadog, Splunk) for enterprise
