# fix(logging): de-flake TestSafeGo_IncrementsCounter — leaked sibling goroutine corrupts the stub

## Context

`internal/logging/safego_test.go` → `TestSafeGo_IncrementsCounter` is flaky in CI: it
fails intermittently as `--- FAIL: TestSafeGo_IncrementsCounter` (seen twice on 2026-06-15,
PRs #3603 and #3604 — including the docs-only #3604, proving it is unrelated to the change
under test). It passes on re-run and passes locally, so each flake costs a ~3.5-minute CI
re-run.

**Root cause (test isolation via global state):**
- `SafeGo(component, fn)` (`safego.go`) runs `fn` in a goroutine with an *outer* deferred
  `recover()` that, after recovery, reads the package-global `panicCounter` and calls
  `panicCounter.Inc(component)` when wired.
- `TestSafeGo_RecoversFromPanic` (earlier in the same file, sequential) does
  `SafeGo("test-component", func(){ defer close(done); panic(...) })` and waits on `<-done`.
  Because `close(done)` runs **inside `fn`'s** defer — *before* `SafeGo`'s outer recover-defer
  reads `panicCounter` — that test returns while its recovery goroutine is still pending.
- `TestSafeGo_IncrementsCounter` then calls `SetPanicCounter(stub)`. The still-pending
  goroutine from the earlier test now reads the stub and calls `stub.Inc("test-component")`.
  `stubCounter.Inc` does `count.Add(1)` then `close(s.done)` unconditionally → either a
  second `close` of `s.done` (panic) or `count == 2`, failing the `count == 1` /
  `component == "counter-test"` assertions.

This is a pure test-isolation defect. `safego.go` production behavior is correct (recovery
is unconditional; the counter is best-effort) and must NOT change.

## Approach

Test-only fix in `internal/logging/safego_test.go`. Make the `stubCounter` robust against a
leaked `Inc` call from a sibling test, so the assertions only observe the panic this test
actually triggered:

1. Give `stubCounter` an expected-component filter and ignore non-matching calls:
   - Add a `want string` field; in `Inc`, `if s.want != "" && component != s.want { return }`
     before recording anything. The leaked call uses `"test-component"`, this test expects
     `"counter-test"`, so it is dropped.
2. Make the `done` close idempotent so a stray matching call can never double-close:
   - Wrap the `close(s.done)` in a `sync.Once` (add `once sync.Once` to the struct;
     `s.once.Do(func(){ close(s.done) })`).
3. Set `want: "counter-test"` when constructing the stub in `TestSafeGo_IncrementsCounter`.
   Keep the existing assertions (`count.Load() == 1`, `component == "counter-test"`).

Do NOT touch `safego.go`. Do NOT add `t.Parallel()`. Keep the other two tests
(`TestSafeGo_RecoversFromPanic`, `TestSafeGo_NormalCompletion`) behaviorally unchanged
(the `stubCounter` is only used by the counter test).

## Acceptance

- `go build ./...` ✅
- `go vet ./internal/logging/...` ✅
- `make lint` → 0 issues
- `go test ./internal/logging/ -run TestSafeGo -count=200 -race` passes with no failures and
  no `panic: close of closed channel` (stress the cross-test leak deterministically).
- `go test ./internal/logging/...` passes.
- No change to `internal/logging/safego.go`.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/3605 (CLOSED — completed)
- Pilot PR: https://github.com/qf-studio/pilot/pull/3606 (merged `5124359c`, in v2.186.9)
- Surfaced during TASK-357 Wave 4 CI (flaked on PR #3603 and #3604, 2026-06-15)

**Status:** ✅ SHIPPED v2.186.9 — Pilot implemented the spec verbatim (component `want` filter + `sync.Once` close; test-only, `safego_test.go` +7/-2). Full Navigator→Pilot loop, dispatch → merge in ~40 min.
**Last Updated:** 2026-06-15
