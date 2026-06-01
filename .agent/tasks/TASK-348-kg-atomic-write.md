# TASK-348: knowledge-graph persistence is non-atomic + rewrites whole file per node (D4)

## Context

`saveUnlocked` (`internal/memory/graph.go:70-82`) persists with `os.WriteFile(kg.path, data, 0644)`,
which truncates then writes — **not atomic**. A crash/OOM/disk-full between truncate and full write leaves
a truncated `knowledge.json`. On next start, `load()` Unmarshal fails; production wiring treats it as
non-fatal (Warn, graph simply not set — `cmd/pilot/main.go:1039-1045`), so cross-project learning is
**silently disabled forever** with no backup/recovery. Compounding this: `AddExecutionLearning`
(`graph.go:213-283`) calls `kg.Add` once per file node, per pattern node, the outcome node, and the
learning node — and EACH `Add` re-marshals the full node map and rewrites the whole file. For N existing
nodes and k touched files that's O(k) full-file rewrites of an O(N) document under the write lock, every
execution — both a wider corruption window and unbounded O(N·k) write amplification (no node pruning exists).

## Approach

- Write atomically: marshal to a temp file in the same dir, `fsync`, then `os.Rename` over `knowledge.json`.
- Add a batch path so `AddExecutionLearning` writes the file ONCE: insert all nodes into the map under one
  lock, then a single `saveUnlocked` (e.g. an `addBatch`/`addUnlocked`-without-save helper).
- Keep a `.bak` of the last good file and fall back to it on load failure so a single bad write doesn't
  permanently disable learning.

## Acceptance

- [ ] `saveUnlocked` writes via temp-file + `fsync` + `os.Rename` (atomic replace).
- [ ] `AddExecutionLearning` performs exactly one file write regardless of node count (test counts writes or asserts via a seam).
- [ ] On a corrupted/truncated `knowledge.json`, load falls back to `.bak` and the graph remains enabled.
- [ ] Test: simulate a partial write (or invalid JSON) and assert recovery from `.bak`.
- [ ] `make test` green for `internal/memory`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (D4, medium)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md`
- File: `internal/memory/graph.go:70-82, 85-103, 213-283`; load handling `cmd/pilot/main.go:1039-1045`
