# TASK-350: log-rotation cleanup spawned per-rotation with no serialization (E6)

## Context

`cleanOldLogs` runs as `go w.cleanOldLogs()` both at startup (`internal/logging/rotation.go:71`) and
after **every** rotation (`rotation.go:141`). It runs entirely OUTSIDE the writer mutex (takes no lock),
reading `w.filename`/`maxAge`/`maxBackups` and `os.Remove`-ing matched files concurrently with
`Write`/`rotate`. Under a high-throughput burst that triggers rapid rotations, multiple `cleanOldLogs`
goroutines run simultaneously, each globbing the same directory and racing to `os.Remove` the same backup
paths (Remove errors are ignored — silent but wasteful, and can spuriously delete a file another goroutine
just rotated in). It also re-globs/stats the whole log dir on every rotation. (Distinct from the
already-known rotation cleanup-loop leak; this is the unsynchronized per-rotation spawn.)

## Approach

Serialize cleanup: guard `cleanOldLogs` with a dedicated `sync.Mutex` or an atomic "cleanup in progress"
flag so only one runs at a time, and skip spawning a new one if one is already running. Read the config
fields under a brief `w.mu` hold. Longer term, run cleanup on a single ticker goroutine owned by the
writer (closed via `Close`) rather than ad-hoc per-rotation spawns.

## Acceptance

- [ ] At most one `cleanOldLogs` runs at a time (dedicated mutex or atomic in-progress flag); redundant spawns are skipped.
- [ ] Config fields read by cleanup are accessed under the writer lock (no race with `Write`/`rotate`).
- [ ] Test: drive many rapid rotations concurrently under `-race` and assert no data race and no spurious deletion of the active/just-rotated file.
- [ ] `make test` green for `internal/logging` (including `-race`); `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (E6, medium)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md`
- File: `internal/logging/rotation.go:71,141,147-195`
