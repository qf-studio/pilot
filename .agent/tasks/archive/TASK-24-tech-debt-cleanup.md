# TASK-24: Tech Debt Cleanup

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Maintenance

---

## Quick Fix List

### Linter Errors (33 total)
```bash
# Fix all with one command
make lint  # shows all locations
```

Main pattern: unchecked `resp.Body.Close()` and `rows.Close()` errors.
Fix: `defer func() { _ = resp.Body.Close() }()`

### TODOs (4 total)
1. `server.go:42` - WebSocket origin check (security)
2. `server.go:161` - /tasks endpoint stub
3. `patterns.go:215` - Pattern extraction
4. `main.go:111` - Daemon shutdown signal

### Missing Tests
- `internal/orchestrator/`
- `internal/pilot/`

---

## Priority

1. Security TODO (origin check)
2. Linter errors (30 min fix)
3. Tests (nice to have)
4. Other TODOs (when needed)

---

**Estimate**: 2-3 hours
