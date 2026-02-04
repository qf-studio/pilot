# GH-360: Auto-Enable Build Gate When Quality Gates Not Configured

**Status**: 📋 Ready for Pilot
**Created**: 2026-02-03
**Priority**: P1 (High impact, prevents broken PRs)

---

## Context

**Problem**:
Quality gates are disabled by default. If user hasn't configured them, Pilot can create PRs with code that doesn't compile. GH-356 would have been caught if build gate was running.

**Goal**:
When Pilot runs a task and quality gates are not explicitly configured, auto-enable a minimal build gate to catch compilation errors.

**Why this helps**:
- Catches obvious errors before PR creation
- Works out of the box without config
- Users can still override with explicit config

---

## Implementation Plan

### Phase 1: Add Auto-Enable Logic

**File**: `internal/quality/types.go`

Add after `DefaultConfig()` (around line 184):

```go
// MinimalBuildGate returns a minimal quality gate config with just build verification.
// Used when quality gates are not explicitly configured but we still want basic safety.
func MinimalBuildGate() *Config {
	return &Config{
		Enabled: true,
		Gates: []*Gate{
			{
				Name:     "build",
				Type:     GateBuild,
				Command:  "go build ./...", // Default for Go projects
				Required: true,
				Timeout:  3 * time.Minute,
			},
		},
		OnFailure: FailureConfig{
			Action:     ActionRetry,
			MaxRetries: 1, // Single retry for build fixes
		},
	}
}

// DetectBuildCommand returns appropriate build command for the project.
// Checks for common project indicators and returns the build command.
func DetectBuildCommand(projectPath string) string {
	// Check for Go project
	if fileExists(filepath.Join(projectPath, "go.mod")) {
		return "go build ./..."
	}
	// Check for Node.js project
	if fileExists(filepath.Join(projectPath, "package.json")) {
		// Check for TypeScript
		if fileExists(filepath.Join(projectPath, "tsconfig.json")) {
			return "npm run build || npx tsc --noEmit"
		}
		return "npm run build --if-present"
	}
	// Check for Python project
	if fileExists(filepath.Join(projectPath, "setup.py")) || fileExists(filepath.Join(projectPath, "pyproject.toml")) {
		return "python -m py_compile $(find . -name '*.py' -not -path './venv/*')"
	}
	// Check for Rust project
	if fileExists(filepath.Join(projectPath, "Cargo.toml")) {
		return "cargo check"
	}
	// Check for Makefile
	if fileExists(filepath.Join(projectPath, "Makefile")) {
		return "make build --dry-run || true" // Dry run to check syntax
	}
	// Default: no build command
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

---

### Phase 2: Wire Auto-Enable in Runner

**File**: `internal/executor/runner.go`

**Location**: Around line 775-786 (quality gate execution)

**Current code**:
```go
	// Run quality gates if configured
	if r.qualityCheckerFactory != nil {
```

**New code** (add auto-detection before the check):
```go
	// Auto-enable minimal build gate if not configured (GH-360)
	if r.qualityCheckerFactory == nil && r.config != nil {
		buildCmd := quality.DetectBuildCommand(task.ProjectPath)
		if buildCmd != "" {
			logging.WithTask(task.ID).Info("Auto-enabling build gate (no quality config)",
				slog.String("command", buildCmd))

			// Create minimal quality checker with auto-detected build command
			minimalConfig := quality.MinimalBuildGate()
			minimalConfig.Gates[0].Command = buildCmd

			r.qualityCheckerFactory = func(taskID, taskProjectPath string) QualityChecker {
				return &simpleQualityChecker{
					config:      minimalConfig,
					projectPath: taskProjectPath,
					taskID:      taskID,
				}
			}
		}
	}

	// Run quality gates if configured
	if r.qualityCheckerFactory != nil {
```

---

### Phase 3: Add Simple Quality Checker

**File**: `internal/executor/runner.go`

Add at the end of the file (around line 2300):

```go
// simpleQualityChecker is a minimal quality checker for auto-enabled build gates.
// Used when quality gates aren't configured but we still want basic build verification.
type simpleQualityChecker struct {
	config      *quality.Config
	projectPath string
	taskID      string
}

func (c *simpleQualityChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	runner := quality.NewRunner(&quality.RunnerConfig{
		Config:      c.config,
		ProjectPath: c.projectPath,
	})

	results, err := runner.RunAll(ctx, c.taskID)
	if err != nil {
		return nil, err
	}

	// Convert to QualityOutcome
	outcome := &QualityOutcome{
		Passed:      results.AllPassed,
		ShouldRetry: !results.AllPassed && c.config.OnFailure.Action == quality.ActionRetry,
		Details:     make([]QualityGateDetail, 0, len(results.Results)),
	}

	for _, r := range results.Results {
		outcome.Details = append(outcome.Details, QualityGateDetail{
			Name:    r.GateName,
			Passed:  r.Status == quality.StatusPassed,
			Output:  r.Output,
			Command: r.Command,
		})
	}

	return outcome, nil
}
```

---

## Verification

```bash
# Run tests
go test ./internal/executor/... -v
go test ./internal/quality/... -v

# Test auto-detection
# In a Go project without quality config:
pilot task "Add a comment to main.go" --dry-run
# Should show: "Auto-enabling build gate"
```

---

## Success Criteria

- [ ] `DetectBuildCommand()` correctly identifies Go, Node, Python, Rust projects
- [ ] Build gate auto-enables when quality gates not configured
- [ ] Build failures trigger retry with error feedback
- [ ] Explicit quality config overrides auto-enable
- [ ] Tests pass

---

## Config Behavior

| Scenario | Behavior |
|----------|----------|
| `quality.enabled: true` with gates | Use configured gates |
| `quality.enabled: false` | Skip quality gates entirely |
| No quality config at all | Auto-enable minimal build gate |
| Project type not detected | Skip quality gates (no build command) |

---

**Estimated effort**: 45 minutes
