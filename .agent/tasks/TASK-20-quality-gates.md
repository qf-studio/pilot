# TASK-20: Quality Gates

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Safety / Quality

---

## Context

**Problem**:
Pilot might create PRs that break tests, have lint errors, or lack coverage.

**Goal**:
Enforce quality standards before PR creation.

---

## Gates

### Required
- [ ] Build passes
- [ ] Tests pass
- [ ] No lint errors

### Configurable
- [ ] Test coverage >= X%
- [ ] No security vulnerabilities
- [ ] Type check passes
- [ ] Bundle size < X KB
- [ ] Performance benchmarks

---

## Configuration

```yaml
quality:
  gates:
    - name: build
      command: "make build"
      required: true

    - name: test
      command: "make test"
      required: true

    - name: lint
      command: "make lint"
      required: false  # warn only

    - name: coverage
      command: "go test -cover ./..."
      threshold: 80
      required: true

  on_failure:
    action: retry  # or 'fail', 'warn'
    max_retries: 2
```

---

## Implementation

### Phase 1: Basic Gates
- Run commands after implementation
- Parse exit codes
- Retry on failure

### Phase 2: Smart Retries
- Parse error output
- Feed errors back to Claude
- Auto-fix common issues

### Phase 3: Custom Gates
- Plugin system for custom checks
- Integration with CI tools
- Pre-flight checks

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

**Monetization**: Premium gates (security scanning, performance) for enterprise
