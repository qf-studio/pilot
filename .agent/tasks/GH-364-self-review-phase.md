# GH-361: Add Self-Review Phase Before PR Creation

**Status**: 📋 Ready for Pilot
**Created**: 2026-02-03
**Priority**: P2 (High impact, medium effort)

---

## Context

**Problem**:
Pilot creates PRs without reviewing its own work. GH-356 had obvious issues (undefined methods, unwired config) that would have been caught with a simple diff review.

**Goal**:
After implementation but before PR creation, run a self-review phase where Claude examines its changes and fixes obvious issues.

**Why this helps**:
- Catches incomplete wiring
- Catches undefined methods
- Catches orphaned code
- Works even without tests

---

## Implementation Plan

### Phase 1: Add Self-Review Step to Runner

**File**: `internal/executor/runner.go`

**Location**: After quality gates pass, before PR creation (around line 1000-1050)

Add new method `runSelfReview()`:

```go
// runSelfReview executes a self-review phase where Claude examines its changes.
// This catches issues like unwired config, undefined methods, or incomplete implementations.
// Returns nil if review passes, error if issues found that couldn't be auto-fixed.
func (r *Runner) runSelfReview(ctx context.Context, task *Task, backend Backend) error {
	// Skip self-review if disabled or for trivial tasks
	if r.config != nil && r.config.SkipSelfReview {
		return nil
	}
	complexity := DetectComplexity(task)
	if complexity.ShouldSkipNavigator() {
		return nil
	}

	logging.WithTask(task.ID).Info("Running self-review phase")
	r.reportProgress(task.ID, "Self-Review", 95, "Reviewing changes...")

	reviewPrompt := r.buildSelfReviewPrompt(task)

	// Execute self-review with shorter timeout
	reviewCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	result, err := backend.Execute(reviewCtx, reviewPrompt, task.Verbose)
	if err != nil {
		// Self-review failure is not fatal - log and continue
		logging.WithTask(task.ID).Warn("Self-review failed", slog.Any("error", err))
		return nil
	}

	// Check if review found issues that were fixed
	if strings.Contains(result.Output, "REVIEW_FIXED:") {
		logging.WithTask(task.ID).Info("Self-review fixed issues")
	}

	return nil
}

// buildSelfReviewPrompt constructs the prompt for self-review phase.
func (r *Runner) buildSelfReviewPrompt(task *Task) string {
	var sb strings.Builder

	sb.WriteString("## Self-Review Phase\n\n")
	sb.WriteString("Review the changes you just made for completeness. Run these checks:\n\n")

	sb.WriteString("### 1. Diff Analysis\n")
	sb.WriteString("```bash\ngit diff --cached\n```\n")
	sb.WriteString("Examine your staged changes. Look for:\n")
	sb.WriteString("- Methods called that don't exist\n")
	sb.WriteString("- Struct fields added but never used\n")
	sb.WriteString("- Config fields that aren't wired through\n")
	sb.WriteString("- Import statements for unused packages\n\n")

	sb.WriteString("### 2. Build Verification\n")
	sb.WriteString("```bash\ngo build ./...\n```\n")
	sb.WriteString("If build fails, fix the errors.\n\n")

	sb.WriteString("### 3. Wiring Check\n")
	sb.WriteString("For any NEW struct fields you added:\n")
	sb.WriteString("- Search for the field name in main.go\n")
	sb.WriteString("- Verify the field is assigned when creating the struct\n")
	sb.WriteString("- Verify the field is used somewhere\n\n")

	sb.WriteString("### 4. Method Existence Check\n")
	sb.WriteString("For any NEW method calls you added:\n")
	sb.WriteString("- Search for `func.*methodName` to verify the method exists\n")
	sb.WriteString("- If method doesn't exist, implement it\n\n")

	sb.WriteString("### Actions\n")
	sb.WriteString("- If you find issues: FIX them and commit the fix\n")
	sb.WriteString("- Output `REVIEW_FIXED: <description>` if you fixed something\n")
	sb.WriteString("- Output `REVIEW_PASSED` if everything looks good\n\n")

	sb.WriteString("Work autonomously. Fix any issues you find.\n")

	return sb.String()
}
```

---

### Phase 2: Wire Self-Review into Execution Flow

**File**: `internal/executor/runner.go`

**Location**: After quality gates pass (around line 824-830)

**Current code**:
```go
		// Quality gates passed
		if outcome.Passed {
			logging.WithTask(task.ID).Info("Quality gates passed")
			break // Exit retry loop, continue to PR creation
		}
```

**New code**:
```go
		// Quality gates passed
		if outcome.Passed {
			logging.WithTask(task.ID).Info("Quality gates passed")

			// Run self-review phase (GH-361)
			if err := r.runSelfReview(ctx, task, backend); err != nil {
				logging.WithTask(task.ID).Warn("Self-review error", slog.Any("error", err))
				// Continue anyway - self-review is advisory
			}

			break // Exit retry loop, continue to PR creation
		}
```

---

### Phase 3: Add Config Option

**File**: `internal/executor/backend.go`

Add to `BackendConfig` struct (around line 50):

```go
	// SkipSelfReview disables the self-review phase before PR creation.
	// Default: false (self-review enabled)
	SkipSelfReview bool `yaml:"skip_self_review"`
```

---

### Phase 4: Add Tests

**File**: `internal/executor/runner_test.go`

```go
func TestBuildSelfReviewPrompt(t *testing.T) {
	runner := NewRunner()

	task := &Task{
		ID:          "TEST-001",
		Title:       "Test task",
		Description: "Test description",
		ProjectPath: "/tmp/test",
	}

	prompt := runner.buildSelfReviewPrompt(task)

	// Verify key elements
	if !strings.Contains(prompt, "Self-Review Phase") {
		t.Error("Prompt should contain Self-Review Phase header")
	}
	if !strings.Contains(prompt, "git diff") {
		t.Error("Prompt should include diff analysis")
	}
	if !strings.Contains(prompt, "go build") {
		t.Error("Prompt should include build verification")
	}
	if !strings.Contains(prompt, "REVIEW_PASSED") {
		t.Error("Prompt should include success signal")
	}
}
```

---

## Verification

```bash
# Run tests
go test ./internal/executor/... -v -run SelfReview

# Test with a real task
pilot task "Add a new config field to telegram handler" --verbose
# Should show: "Running self-review phase"
```

---

## Success Criteria

- [ ] Self-review runs after quality gates pass
- [ ] Self-review checks diff for common issues
- [ ] Self-review auto-fixes obvious problems
- [ ] Self-review can be disabled via config
- [ ] Trivial tasks skip self-review
- [ ] Tests pass

---

## Flow Diagram

```
Task Start
    ↓
Claude Implements
    ↓
Claude Commits
    ↓
Quality Gates Run
    ↓
Gates Pass?
  No → Retry with feedback
  Yes ↓
Self-Review Phase ← NEW
    ↓
Issues Found?
  Yes → Auto-fix, re-commit
  No ↓
Create PR
```

---

**Estimated effort**: 1-2 hours
