# TASK-20: Quality Gates

**Status**: ✅ Completed
**Created**: 2026-01-26
**Completed**: 2026-01-27
**Category**: Safety / Quality

---

## Context

**Problem**:
Pilot might create PRs that break tests, have lint errors, or lack coverage.

**Goal**:
Enforce quality standards before PR creation.

---

## Implementation Summary

### Implemented Features (Phase 1)

- **Gate Types**: Build, Test, Lint, Coverage, Security, TypeCheck, Custom
- **Configuration**: YAML-based gate configuration via `quality:` section
- **Retry Logic**: Configurable retries with delay between attempts
- **Coverage Parsing**: Supports Go, Jest, and Python coverage output formats
- **Error Feedback**: Formats gate failures for Claude retry

### Files Created

- `internal/quality/types.go` - Gate types, config, and result structures
- `internal/quality/runner.go` - Gate execution with retry and coverage parsing
- `internal/quality/executor.go` - High-level executor for task pipeline integration
- `internal/quality/runner_test.go` - Comprehensive tests for gate runner
- `internal/quality/types_test.go` - Tests for types and configuration

### Config Integration

Added `Quality *quality.Config` to main config in `internal/config/config.go`

---

## Gates

### Required
- [x] Build passes
- [x] Tests pass
- [x] No lint errors

### Configurable
- [x] Test coverage >= X%
- [x] No security vulnerabilities (gate type)
- [x] Type check passes (gate type)
- [ ] Bundle size < X KB (future)
- [ ] Performance benchmarks (future)

---

## Configuration

```yaml
quality:
  enabled: true
  gates:
    - name: build
      type: build
      command: "make build"
      required: true
      timeout: 5m
      max_retries: 2
      failure_hint: "Fix compilation errors in the changed files"

    - name: test
      type: test
      command: "make test"
      required: true
      timeout: 10m
      max_retries: 2
      failure_hint: "Fix failing tests or update test expectations"

    - name: lint
      type: lint
      command: "make lint"
      required: false  # warn only
      timeout: 2m
      max_retries: 1
      failure_hint: "Fix linting errors: formatting, unused imports, etc."

    - name: coverage
      type: coverage
      command: "go test -cover ./..."
      required: true
      threshold: 80
      timeout: 10m

  on_failure:
    action: retry  # or 'fail', 'warn'
    max_retries: 2
    notify_on: [failed]
```

---

## Usage

### In Executor

```go
import "github.com/alekspetrov/pilot/internal/quality"

// Create executor
qe := quality.NewExecutor(&quality.ExecutorConfig{
    Config:      cfg.Quality,
    ProjectPath: task.ProjectPath,
    TaskID:      task.ID,
})

// Run quality gates
outcome, err := qe.Check(ctx)
if err != nil {
    return err
}

if !outcome.Passed {
    if outcome.ShouldRetry {
        // Feed error back to Claude
        prompt += outcome.RetryFeedback
    } else {
        // Stop and notify
        return quality.ErrGateFailed
    }
}
```

---

## Flow

```
Implementation → Quality Gates → Pass? → Create PR
                      ↓ Fail
                Retry (up to N times)
                      ↓ Still Fail
                Notify & Stop
```

---

## Test Coverage

All tests passing:
- Gate execution (enabled/disabled)
- Passing and failing gates
- Required vs optional gates
- Retry logic
- Context cancellation
- Coverage threshold enforcement
- Coverage parsing (Go, Jest, Python)
- Error feedback formatting
- Configuration validation

---

## Future Work (Phase 2 & 3)

- Smart retries: Parse specific errors and auto-fix common issues
- Plugin system for custom checks
- Integration with CI tools
- Pre-flight checks before implementation starts
- Bundle size and performance gates

---

**Monetization**: Premium gates (security scanning, performance) for enterprise
