# Signal Protocol v2 and Stagnation Detection

**Status**: Planning
**Created**: 2026-02-12
**Related**: Navigator issues #1, #2

---

## Context

**Problem**:
Navigator-Pilot communication uses fragile text parsing (`strings.Contains`) that causes:
- False positives from prose mentioning "NAVIGATOR_STATUS" or "EXIT_SIGNAL"
- Silent failures when signals are malformed (no validation, no logging)
- No Pilot-side stagnation detection (relies on Navigator emitting signals)
- No timeout - execution waits indefinitely for EXIT_SIGNAL

**Goal**:
1. Implement robust JSON-based signal protocol (v2) with backward compatibility
2. Add Pilot-side stagnation detection with escalation (warn → pause → abort)

**Success Criteria**:
- [ ] Pilot parses `pilot-signal` JSON code blocks reliably
- [ ] Legacy NAVIGATOR_STATUS blocks still work (backward compatible)
- [ ] Progress values validated (clamped 0-100, logged on malformed)
- [ ] Stagnation detected independently by Pilot (state hash + timeout)
- [ ] Configurable thresholds via config.yaml

---

## Implementation Plan

### Phase 1: Signal Parser (`signal.go`)

**Goal**: Create robust JSON signal parser with validation

**Tasks**:
- [ ] Create `internal/executor/signal.go`
- [ ] Define `PilotSignal` struct with JSON tags
- [ ] Implement regex extraction for `pilot-signal` code blocks
- [ ] Add JSON parsing with validation
- [ ] Clamp progress to 0-100, log warnings on invalid values
- [ ] Create `signal_test.go` with test cases

**Files**:
- `internal/executor/signal.go` (NEW) - SignalParser, PilotSignal struct
- `internal/executor/signal_test.go` (NEW) - Unit tests

### Phase 2: Stagnation Monitor (`stagnation.go`)

**Goal**: Pilot-side stagnation detection with escalation

**Tasks**:
- [ ] Create `internal/executor/stagnation.go`
- [ ] Define `StagnationLevel` enum (None, Warn, Pause, Abort)
- [ ] Implement state hash calculation (phase + progress + iteration)
- [ ] Track consecutive identical hashes
- [ ] Implement timeout-based detection
- [ ] Create `stagnation_test.go`

**Files**:
- `internal/executor/stagnation.go` (NEW) - StagnationMonitor
- `internal/executor/stagnation_test.go` (NEW) - Unit tests

### Phase 3: Config and Alerts

**Goal**: Add configuration and alert types

**Tasks**:
- [ ] Add `StagnationConfig` struct to `backend.go`
- [ ] Add stagnation alert types to `alerts.go`
- [ ] Wire default config values

**Files**:
- `internal/executor/backend.go` - Add StagnationConfig after line 197
- `internal/executor/alerts.go` - Add alert types after line 44

### Phase 4: Wire into Runner

**Goal**: Integrate signal parser and stagnation into execution flow

**Tasks**:
- [ ] Update `parseNavigatorPatterns()` to try v2 first
- [ ] Add validation to legacy parser
- [ ] Wire stagnation monitor in `parseNavigatorStatusBlock()`
- [ ] Add stagnation handler methods
- [ ] Update prompt (~line 2273) for v2 format

**Files**:
- `internal/executor/runner.go` - Lines 2694-2760 (parsing), ~2273 (prompt)

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Signal format | JSON in code blocks vs structured text | JSON code blocks | Clear boundaries, easy regex, prevents false positives |
| Backward compat | Drop legacy vs dual parsing | Dual parsing | v2 first, fall back to legacy |
| Stagnation detection | Navigator-only vs Pilot-side | Pilot-side | Independent of Navigator, can't be bypassed |
| Escalation | Hard abort vs levels | 3 levels (warn/pause/abort) | Gradual escalation, human intervention option |

---

## Dependencies

**Requires**:
- [ ] Navigator #1 - status_generator.py JSON output
- [ ] Navigator #2 - SKILL.md v2 exit signal format

**Blocks**:
- Improved autonomous reliability
- Reduced manual intervention on stuck loops

---

## Verify

```bash
# Run unit tests
go test ./internal/executor/... -v -run "Signal|Stagnation"

# Run full test suite
make test

# Manual test
pilot task "Test task" --verbose
# Observe progress parsing and stagnation detection
```

---

## Done

- [ ] `signal.go` parses pilot-signal JSON blocks
- [ ] `stagnation.go` detects identical states and timeouts
- [ ] Legacy NAVIGATOR_STATUS blocks still parse correctly
- [ ] Config schema documented in example config
- [ ] All tests pass

---

## GitHub Issues Created

1. **GH-923**: Create signal.go with JSON parser for pilot-signal blocks
2. **GH-924**: Create stagnation.go with state hash tracking
3. **GH-925**: Add StagnationConfig to backend.go and alert types
4. **GH-926**: Wire signal parser and stagnation into runner.go

**Execution order**: 923 → 924 → 925 → 926 (sequential dependencies)

---

**Last Updated**: 2026-02-12
