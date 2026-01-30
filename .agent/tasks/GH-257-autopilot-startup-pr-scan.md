# GH-257: Autopilot Startup PR Scanning

**Status**: 🎯 Ready for Pilot
**Priority**: P1 - Core functionality gap
**Created**: 2026-01-30

---

## Context

**Problem**:
Autopilot only tracks PRs created during the current session via `OnPRCreated` callback. When Pilot restarts, the `activePRs` map is empty and existing open PRs are invisible to autopilot.

**Symptom**:
Dashboard shows `Active PRs: 0` even when open PRs exist (e.g., PR #252).

**Goal**:
Scan for existing open PRs with `pilot/GH-*` branch pattern on startup and restore their tracking state.

---

## Implementation Plan

### Phase 1: Enhance GitHub Client

**Goal**: Add ability to list pull requests

**Tasks**:
- [ ] Add `ListPullRequests(ctx, owner, repo, state)` method to `github.Client`
- [ ] Update `PullRequest` struct - change `Head string` to `HeadRef` struct:
  ```go
  type PRRef struct {
      Ref string `json:"ref"`  // branch name
      SHA string `json:"sha"`  // commit sha
  }
  ```
- [ ] Update existing `GetPullRequest` to populate new struct

**Files**:
- `internal/adapters/github/client.go` - add ListPullRequests method
- `internal/adapters/github/types.go` - update PullRequest struct

### Phase 2: Add Startup Scanning

**Goal**: Restore PR state on controller startup

**Tasks**:
- [ ] Add `ScanExistingPRs(ctx context.Context) error` method to `Controller`
- [ ] Filter PRs by branch pattern `pilot/GH-*`
- [ ] Extract issue number from branch name
- [ ] Register each PR via existing `OnPRCreated` logic
- [ ] Log summary of restored PRs

**Implementation**:
```go
// ScanExistingPRs scans for open PRs created by Pilot and restores their state.
func (c *Controller) ScanExistingPRs(ctx context.Context) error {
    prs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "open")
    if err != nil {
        return fmt.Errorf("failed to list PRs: %w", err)
    }

    restored := 0
    for _, pr := range prs {
        // Filter for Pilot branches
        if !strings.HasPrefix(pr.HeadRef.Ref, "pilot/GH-") {
            continue
        }

        // Extract issue number
        var issueNum int
        if _, err := fmt.Sscanf(pr.HeadRef.Ref, "pilot/GH-%d", &issueNum); err != nil {
            c.log.Warn("failed to parse branch", "branch", pr.HeadRef.Ref)
            continue
        }

        // Register PR
        c.OnPRCreated(pr.Number, pr.HTMLURL, issueNum, pr.HeadRef.SHA)
        restored++
    }

    c.log.Info("restored existing PRs", "count", restored)
    return nil
}
```

**Files**:
- `internal/autopilot/controller.go` - add ScanExistingPRs method

### Phase 3: Wire Startup Scanning

**Goal**: Call scanner before starting autopilot loop

**Tasks**:
- [ ] Call `ScanExistingPRs` after controller creation, before `Run()`
- [ ] Handle errors gracefully (warn, don't fail startup)

**Location**: `cmd/pilot/main.go` line ~725

```go
if autopilotCtrl != nil {
    // Scan for existing PRs created by Pilot
    if err := autopilotCtrl.ScanExistingPRs(ctx); err != nil {
        logging.WithComponent("autopilot").Warn("failed to scan existing PRs",
            slog.Any("error", err))
    }

    fmt.Printf("🤖 Autopilot enabled: %s environment\n", cfg.Orchestrator.Autopilot.Environment)
    // ... existing Run() goroutine
}
```

**Files**:
- `cmd/pilot/main.go` - wire ScanExistingPRs call

### Phase 4: Tests

**Tasks**:
- [ ] Test `ListPullRequests` with mock response
- [ ] Test branch pattern filtering (`pilot/GH-*` vs other branches)
- [ ] Test issue number extraction from branch name
- [ ] Test PR state restoration (verify activePRs populated)

**Files**:
- `internal/adapters/github/client_test.go` - ListPullRequests tests
- `internal/autopilot/controller_test.go` - ScanExistingPRs tests

---

## Verification

```bash
# Start Pilot with existing open PRs
pilot start --github --autopilot stage

# Dashboard should show:
# Active PRs: 1 (or more)

# Logs should show:
# INFO autopilot restored existing PRs count=1
```

---

## Files to Modify

| File | Change |
|------|--------|
| `internal/adapters/github/types.go` | Update PullRequest.Head to HeadRef struct |
| `internal/adapters/github/client.go` | Add ListPullRequests method |
| `internal/autopilot/controller.go` | Add ScanExistingPRs method |
| `cmd/pilot/main.go` | Wire ScanExistingPRs before Run() |
| `internal/adapters/github/client_test.go` | Tests for ListPullRequests |
| `internal/autopilot/controller_test.go` | Tests for ScanExistingPRs |

---

## Acceptance Criteria

- [ ] Autopilot scans existing open PRs on startup
- [ ] Only PRs with `pilot/GH-*` branch pattern are tracked
- [ ] Dashboard shows correct Active PRs count after restart
- [ ] Existing autopilot functionality unchanged
- [ ] Tests pass
