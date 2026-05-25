# SOP: Quality gate parallel execution — shared cache race

**Status:** Active · **Created:** 2026-05-25 · **Related:** TASK-289

## Symptom

`pilot start` reports intermittent quality-gate failures: `make build` fails with
"file already exists" errors in `~/.cache/go-build`, or `make lint` exits with
"failed to acquire lock on .golangci-lint.cache". Re-running the gate succeeds
without code changes. Most visible during back-to-back task execution.

The 2026-05-21 workshop run hit 11 spurious failures in 3 hours.

## Root cause

`internal/quality/runner.go:62-83` runs all configured gates as goroutines when
`quality.parallel: true`. The three default gates (`make build`, `make test`,
`make lint`) all invoke the Go toolchain, which serializes access to:

- `~/.cache/go-build` — Go's build cache (file-level locks)
- `~/.cache/golangci-lint` — golangci-lint cache (process-level lock at
  `~/.cache/golangci-lint/.golangci-lint.cache.lock`)

Two `go build` invocations in flight will deadlock or fail with "input file
appears to be modified" if both try to write the same cached artifact.

## Resolution (current default)

Per **TASK-289**, the project default for `quality.parallel` is now `false`
(sequential execution). Gates run one at a time:

```yaml
# ~/.pilot/config.yaml
quality:
  enabled: true
  # parallel: false  # default; no need to set explicitly
  gates: [...]
```

This eliminates the race at the cost of total gate wall time (≈3× for the
default 3 gates, typically still <5min for the workshop-sized projects).

## When to opt back into parallel

Set `quality.parallel: true` ONLY if you have isolated caches per gate. The
common patterns:

1. **Per-gate `GOCACHE`** — set `GOCACHE=$(mktemp -d)` in the gate's `command`
   string so each goroutine writes to its own build cache.
2. **Per-gate `GOLANGCI_LINT_CACHE`** — same idea for the lint cache.
3. **Container-per-gate** — wrap each `command` in `docker run ...` so the
   cache lives in the container scratch.

Until one of these ships as a Pilot feature (Wave 4+ candidate per audit
2026-05-25 §3.7), prefer the sequential default.

## How to verify the default is active

```bash
$ ./bin/pilot config show | grep -A1 quality:
quality:
  enabled: true

$ ./bin/pilot start  # observe logs
INFO Starting quality gate checks gate_count=3 mode=sequential
```

If `mode=parallel` shows without an explicit `parallel: true` in the config,
something is overriding the default — file an issue.

## References

- `internal/quality/types.go` — `IsParallel()` and the default
- `internal/quality/runner.go:62-83` — execution branch
- TASK-289 — the flip itself
- Audit 2026-05-25 §2 Action #5, §3.7 P1, §3.3 P1
