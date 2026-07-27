# TASK-425: Graph drift local prevention — check-graph in pre-commit/pre-push + `--fix` auto-index

**Created**: 2026-07-27 · **Status**: ✅ Shipped 2026-07-27 (#4574 → PR #4576, merged 13:18Z; released v2.247.0 — pre-commit/pre-push gates live) · **Last Updated**: 2026-07-27

## Problem

Interactive sessions can commit a memory file under
`.agent/knowledge/memories/` without indexing it in
`.agent/knowledge/graph.json`. Nothing local catches it: the pre-commit hook
(`scripts/install-hooks.sh`) only greps `_test.go` files for secret
patterns, and `scripts/pre-push-gate.sh` never runs `scripts/check-graph.py`.
First detection is the Knowledge Graph Drift Gate on CI — after the push,
red on main (occurred 2026-07-27: commit `88fad61c` added a learning file
un-indexed; main CI red for ~55 minutes across 4 pushes).

## Required behavior

1. **`check-graph.py --fix`**: for each "unindexed active memory file"
   finding, generate a stub node in `graph.json` under `nodes.memories`,
   keyed by the file's frontmatter `name`, with `type` from frontmatter
   `type`, `summary` from frontmatter `description`, `file` set to the
   repo-relative path, and `created`/`last_validated` set to today. Print
   each node added. `--fix` only repairs class 2 (unindexed files); classes
   1 and 3 (broken links, dangling edges) still FAIL — they need human
   judgment. Without `--fix`, behavior is byte-identical to today (CI must
   stay read-only).
2. **Pre-commit**: in the hook installed by `scripts/install-hooks.sh`, when
   any staged path is under `.agent/knowledge/`, run
   `python3 scripts/check-graph.py`; on failure, block the commit and print
   the hint `python3 scripts/check-graph.py --fix`.
3. **Pre-push gate**: add a check-graph step to `scripts/pre-push-gate.sh`
   (fail closed, same as the other gate steps).
4. Keep `make check-graph` as the manual entry point (already exists,
   Makefile:133).

## Acceptance criteria

- [ ] `scripts/check_graph_test.py` extended: `--fix` indexes an unindexed
      file from its frontmatter; `--fix` does NOT touch broken links or
      dangling edges; no-`--fix` output unchanged.
- [ ] Fresh `make install-hooks` installs a pre-commit that blocks a commit
      staging an un-indexed memory file, and passes once indexed (or after
      `--fix`).
- [ ] `pre-push-gate.sh` fails when the graph drifts.
- [ ] CI workflow (`Knowledge Graph Drift Gate`) unchanged — still read-only.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4574
- Checker: `scripts/check-graph.py` (+ `scripts/check_graph_test.py`)
- Hooks: `scripts/install-hooks.sh`, `scripts/pre-push-gate.sh`, Makefile
  `check-graph` target
- Incident: `88fad61c` → fixed by `0c2392eb` (2026-07-27)
- Related: TASK-410 (executor-side deletions), TASK-424 (origin-relative
  guards)
