# TASK-351: alerts engine_test relies on ~48 fixed time.Sleep calls — flaky + slow (E8)

## Context

`Engine.ProcessEvent` pushes onto a buffered channel (`eventCh`) consumed by a background goroutine
started in `Start` (`go e.processEvents(ctx)`). The tests assert on `mockCh.getAlerts()` after a fixed
`time.Sleep(50-100ms)` — there is no Flush/Drain/Wait hook and no deterministic synchronization
(`internal/alerts/engine_test.go` has **48** `time.Sleep` calls). On a loaded CI runner the consumer
goroutine may not have dispatched within the sleep window → intermittent "expected 1 alert, got 0";
conversely every run pays multiple seconds of unconditional sleep. Classic time-dependent flake pattern
that degrades both CI reliability and the suite's signal value.

> **Scope note:** test-only change. Do not alter production dispatch semantics. The async dispatch
> decoupling itself already shipped (TASK-337, `engine.go:649`); this task only makes the tests
> deterministic against it.

## Approach

Add a deterministic sync hook and replace the fixed sleeps with it:
- have `mockCh` expose a buffered `received chan struct{}` the test waits on with a generous timeout
  (e.g. `select` with a 2s deadline), OR
- add an Engine test-only method that processes a single event synchronously / drains `eventCh`.

Replace the fixed `time.Sleep` calls with that hook so tests are both faster and non-flaky.

## Acceptance

- [ ] A deterministic sync hook exists (mock `received` channel or test-only drain/flush).
- [ ] The `time.Sleep`-based waits in `engine_test.go` are replaced by the hook (count drops toward 0; any residual sleep justified in a comment).
- [ ] Tests assert with a bounded timeout (e.g. 2s) instead of a fixed unconditional sleep.
- [ ] No change to production dispatch behavior; `make test` green for `internal/alerts` (run repeatedly / `-count=5` to confirm non-flaky).
- [ ] `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (E8, medium, test-gap)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md`
- File: `internal/alerts/engine_test.go` (152, 214, +~46 more); production seam `internal/alerts/engine.go:649`
