# Pattern: hasMakefileTarget (line-prefix scan) is the probe for a make target; never make -n or make -q

## Summary
To ask whether a Makefile defines a target, read the file: hasMakefileTarget(path, target) in internal/quality/types.go scans for a line starting with 'target:'. Never probe with make -n or make -q: both expand $(shell ...) and run recursive $(MAKE) lines, so a probe has side effects and cost. The scan is conservative by design: when it cannot see the target but the Makefile has an include/-include/sinclude line, keep the configured command (makefileHasInclude); a false 'absent' must never replace a valid command.

## Context
Set by TASK-628 / #6002 (2026-11-01); DetectTestCommand already used hasMakefileTarget to prefer make test.

## Details
Known blind spots of the scan: multi-target lines (test test-changed:), pattern rules (%-changed:), targets defined in included files, and a -f/-C flag that points make at another Makefile (#6005 adds a guard: leave the command unchanged when such a flag is present). A 'target:=' variable assignment reads as present, which is the safe direction.

## Recommended Approach
Use hasMakefileTarget for presence; add guards that keep the configured command whenever the scan may be wrong (include lines, -f/-C/--file/--directory flags). Default-target substitution order: gate type's default (test/build via defaultMakeTarget) -> nativeFallbackCommand -> configured command unchanged. Log the substitution once per gate through fallbackWarned.

## Related
- TASK-628
- `internal/quality/types.go`
- `internal/quality/runner.go`

---
**Captured**: 2026-11-01
**Confidence**: 95%
**Concepts**: quality-gate, executor
