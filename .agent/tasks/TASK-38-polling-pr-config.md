# TASK-38: GitHub Polling PR Config

**Status**: 📋 Planned
**Priority**: High (P2)
**Created**: 2026-01-28

---

## Context

**Problem**:
GitHub polling hardcodes `CreatePR: true`. Users who trust Pilot + CI want direct commits without PR overhead.

**Goal**:
Make PR creation configurable for polled issues.

---

## Design

### Configuration

```yaml
adapters:
  github:
    polling:
      enabled: true
      interval: 30s
      label: pilot
      create_pr: true  # default: true for safety
```

### Implementation

1. Add `CreatePR` field to `PollingConfig` in `types.go`
2. Pass config value to task in `cmd/pilot/main.go` poller callback
3. Update default to `true` (safe default)

---

## Files to Modify

| File | Change |
|------|--------|
| `internal/adapters/github/types.go` | Add `CreatePR bool` to PollingConfig |
| `cmd/pilot/main.go` | Read config, pass to task |

---

## Code Changes

### types.go

```go
type PollingConfig struct {
    Enabled  bool          `yaml:"enabled"`
    Interval time.Duration `yaml:"interval"`
    Label    string        `yaml:"label"`
    CreatePR bool          `yaml:"create_pr"` // Add this
}

func DefaultConfig() *Config {
    return &Config{
        // ...
        Polling: &PollingConfig{
            Enabled:  false,
            Interval: 30 * time.Second,
            Label:    "pilot",
            CreatePR: true,  // Safe default
        },
    }
}
```

### main.go (poller callback)

```go
task := &executor.Task{
    // ...
    CreatePR: cfg.Adapters.Github.Polling.CreatePR,
}
```

---

## Acceptance Criteria

- [ ] `create_pr: false` skips PR creation
- [ ] `create_pr: true` (default) creates PRs
- [ ] Config validation warns if `create_pr: false`

---

## Testing

1. Set `create_pr: false`, label issue
2. Verify commit lands on branch, no PR created
3. Set `create_pr: true`, label issue
4. Verify PR created

---

## Notes

- Default `true` for safety - explicit opt-out required
- Consider adding warning log when `create_pr: false`
