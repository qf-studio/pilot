# GH-359: Add Pre-Commit Verification Instructions to BuildPrompt

**Status**: 📋 Ready for Pilot
**Created**: 2026-02-03
**Priority**: P1 (High impact, low effort)

---

## Context

**Problem**:
GH-356 created code that compiled but was incomplete - methods were called that didn't exist, config fields were added but never wired. Claude committed without verifying the implementation actually worked.

**Goal**:
Add verification instructions to `BuildPrompt()` that tell Claude to verify completeness before committing.

**Why this helps**:
- Zero runtime changes - just better instructions
- Catches issues at commit time, not after PR
- Teaches Claude to self-verify

---

## Implementation Plan

### File: `internal/executor/runner.go`

**Location**: `BuildPrompt()` function, lines 1363-1384 (Navigator path)

**Current code** (around line 1383-1384):
```go
		sb.WriteString("Run until done. Use Navigator's autonomous completion protocol.\n\n")
		sb.WriteString("CRITICAL: You MUST commit all changes before completing. A task is NOT complete until changes are committed. Use format: `type(scope): description (TASK-XX)`\n")
```

**New code** (replace the above):
```go
		sb.WriteString("Run until done. Use Navigator's autonomous completion protocol.\n\n")

		// Pre-commit verification checklist (GH-359)
		sb.WriteString("## Pre-Commit Verification\n\n")
		sb.WriteString("BEFORE committing, verify:\n")
		sb.WriteString("1. **Build passes**: Run `go build ./...` (or equivalent for the project)\n")
		sb.WriteString("2. **Config wiring**: Any new config struct fields must flow from yaml → main.go → handler\n")
		sb.WriteString("3. **Methods exist**: Any method calls you added must have implementations\n")
		sb.WriteString("4. **Tests pass**: Run `go test ./...` for changed packages\n\n")
		sb.WriteString("If any verification fails, fix it before committing.\n\n")

		sb.WriteString("CRITICAL: You MUST commit all changes before completing. A task is NOT complete until changes are committed. Use format: `type(scope): description (TASK-XX)`\n")
```

---

### Also update non-Navigator path (lines 1405-1428)

**Current code** (around line 1426-1428):
```go
		sb.WriteString("2. Implement EXACTLY what is requested - nothing more, nothing less\n")
		sb.WriteString("3. Commit with format: `type(scope): description`\n")
		sb.WriteString("\nWork autonomously. Do not ask for confirmation.\n")
```

**New code**:
```go
		sb.WriteString("2. Implement EXACTLY what is requested - nothing more, nothing less\n")
		sb.WriteString("3. Before committing, verify: build passes, tests pass, no undefined methods\n")
		sb.WriteString("4. Commit with format: `type(scope): description`\n")
		sb.WriteString("\nWork autonomously. Do not ask for confirmation.\n")
```

---

### Also update trivial task path (lines 1401-1404)

**Current code**:
```go
		sb.WriteString("2. Make the minimal change required\n")
		sb.WriteString("3. Commit with format: `type(scope): description`\n\n")
		sb.WriteString("Work autonomously. Do not ask for confirmation.\n")
```

**New code**:
```go
		sb.WriteString("2. Make the minimal change required\n")
		sb.WriteString("3. Verify build passes before committing\n")
		sb.WriteString("4. Commit with format: `type(scope): description`\n\n")
		sb.WriteString("Work autonomously. Do not ask for confirmation.\n")
```

---

## Verification

```bash
# Run tests
go test ./internal/executor/... -v -run BuildPrompt

# Verify prompt contains verification instructions
go test ./internal/executor/... -v -run TestBuildPrompt
```

### Manual verification

```bash
# Dry run a task to see the prompt
pilot task "Test task" --dry-run
```

---

## Success Criteria

- [ ] BuildPrompt includes verification checklist for Navigator tasks
- [ ] BuildPrompt includes build check for non-Navigator tasks
- [ ] BuildPrompt includes build check for trivial tasks
- [ ] Tests pass
- [ ] Prompt visible in dry-run mode

---

**Estimated effort**: 15 minutes
