# TASK-26: Hot Version Upgrade Strategy

**Status**: Backlog
**Priority**: High
**Created**: 2026-01-26

---

## Overview

Implement safe Claude Code version management to prevent breaking changes from disrupting automation. After 2.1.19 regression broke headless mode, we need version pinning, testing, and automatic rollback.

---

## Problem Statement

- Claude Code 2.1.18/2.1.19 introduced a bug with duplicate `tool_use` IDs
- Auto-updates via brew/npm can silently break running bots
- No way to test new versions before production deployment
- No automatic recovery when new version fails

---

## Proposed Solution

### 1. Version Configuration

```go
type VersionConfig struct {
    PinnedVersion    string   `json:"pinned_version"`    // e.g., "2.1.17"
    AllowedVersions  []string `json:"allowed_versions"`  // tested versions
    AutoRollback     bool     `json:"auto_rollback"`     // rollback on failures
    FailureThreshold int      `json:"failure_threshold"` // errors before rollback
    CanaryPercent    int      `json:"canary_percent"`    // % of tasks for new version
}
```

### 2. Startup Version Check

```go
func (r *Runner) CheckClaudeVersion() error {
    version, err := r.getClaudeVersion()
    if err != nil {
        return fmt.Errorf("failed to get Claude version: %w", err)
    }

    if !r.isVersionAllowed(version) {
        log.Printf("[WARNING] Claude %s not in allowed list: %v", version, r.config.AllowedVersions)
        if r.config.PinnedVersion != "" {
            return fmt.Errorf("Claude %s not tested, expected %s", version, r.config.PinnedVersion)
        }
    }

    log.Printf("[startup] Claude Code version: %s ✓", version)
    return nil
}

func (r *Runner) getClaudeVersion() (string, error) {
    cmd := exec.Command("claude", "--version")
    output, err := cmd.Output()
    if err != nil {
        return "", err
    }
    // Parse "2.1.17 (Claude Code)"
    parts := strings.Fields(string(output))
    if len(parts) > 0 {
        return parts[0], nil
    }
    return "", fmt.Errorf("could not parse version")
}
```

### 3. Failure Tracking & Auto-Rollback

```go
type VersionHealth struct {
    Version      string
    Successes    int
    Failures     int
    LastFailure  time.Time
    FailureRate  float64
}

func (r *Runner) trackExecution(version string, success bool, err error) {
    r.mu.Lock()
    defer r.mu.Unlock()

    health := r.versionHealth[version]
    if success {
        health.Successes++
    } else {
        health.Failures++
        health.LastFailure = time.Now()

        // Check for API errors indicating version bug
        if isVersionBug(err) {
            health.VersionBugCount++
        }
    }

    health.FailureRate = float64(health.Failures) / float64(health.Successes + health.Failures)
    r.versionHealth[version] = health

    // Auto-rollback check
    if r.config.AutoRollback && health.VersionBugCount >= r.config.FailureThreshold {
        r.triggerRollback(version)
    }
}

func isVersionBug(err error) bool {
    if err == nil {
        return false
    }
    errStr := err.Error()
    // Known version-specific bugs
    return strings.Contains(errStr, "tool_use` ids must be unique") ||
           strings.Contains(errStr, "invalid_request_error")
}
```

### 4. Canary Deployment

```go
func (r *Runner) shouldUseCanary() bool {
    if r.config.CanaryPercent <= 0 {
        return false
    }
    return rand.Intn(100) < r.config.CanaryPercent
}

func (r *Runner) Execute(ctx context.Context, task *Task) (*ExecutionResult, error) {
    version := r.config.PinnedVersion

    if r.shouldUseCanary() && r.config.CanaryVersion != "" {
        version = r.config.CanaryVersion
        log.Printf("[canary] Using Claude %s for task %s", version, task.ID)
    }

    // Execute with selected version...
}
```

### 5. Health Endpoint

```go
// GET /health
type HealthResponse struct {
    Status         string                   `json:"status"`
    ClaudeVersion  string                   `json:"claude_version"`
    PinnedVersion  string                   `json:"pinned_version"`
    VersionHealth  map[string]VersionHealth `json:"version_health"`
    Uptime         time.Duration            `json:"uptime"`
}
```

### 6. Telegram Commands

| Command | Function |
|---------|----------|
| `/version` | Show current Claude version + health |
| `/pin <version>` | Pin to specific version (admin only) |
| `/canary <version> <percent>` | Enable canary testing |
| `/rollback` | Manual rollback to pinned version |

---

## Version Management Script

```bash
#!/bin/bash
# scripts/manage-claude-version.sh

case "$1" in
  check)
    claude --version
    ;;
  pin)
    VERSION=$2
    npm install -g @anthropic-ai/claude-code@$VERSION
    echo "Pinned to Claude Code $VERSION"
    ;;
  test)
    VERSION=$2
    echo "Testing Claude $VERSION..."
    npm install -g @anthropic-ai/claude-code@$VERSION
    claude -p "Say 'version test ok'" --dangerously-skip-permissions
    ;;
  rollback)
    VERSION=$(cat ~/.pilot/pinned-version)
    npm install -g @anthropic-ai/claude-code@$VERSION
    echo "Rolled back to $VERSION"
    ;;
esac
```

---

## Configuration File

```json
// ~/.pilot/version-config.json
{
  "pinned_version": "2.1.17",
  "allowed_versions": ["2.1.17", "2.1.16", "2.1.15"],
  "blocked_versions": ["2.1.18", "2.1.19"],
  "auto_rollback": true,
  "failure_threshold": 3,
  "canary_percent": 0,
  "canary_version": ""
}
```

---

## Acceptance Criteria

- [ ] Startup checks Claude version and warns if not in allowed list
- [ ] Failures tracked per version with automatic rollback
- [ ] `/version` command shows health stats
- [ ] Canary mode allows testing new versions on subset of tasks
- [ ] Blocked versions list prevents known-bad versions
- [ ] `manage-claude-version.sh` script for manual control

---

## Dependencies

- None (builds on existing runner.go)

---

## Notes

- Keep `2.1.17` as known-good baseline
- Test new versions in canary mode before full deploy
- Monitor Anthropic releases for breaking changes
- Consider webhook notification on auto-rollback
