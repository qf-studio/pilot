# feat(ci): knowledge-graph drift gate — fail CI on disk-vs-graph divergence (TASK-386)

**Status**: ✅ Shipped 2026-07-06 (via epic decomposition)
**Created**: 2026-07-06
**Assignee**: Pilot

---

## Context

**Problem**:
The `.agent/knowledge/graph.json` memory graph silently drifts from the
memory files on disk. A 2026-07-05 audit found **52 of 84 memory files with
zero graph presence**, 42 freeform concept tags, and 4 dangling edges — the
drift accumulated for ~6 weeks because nothing checked. It was fixed by a
one-off manual reconciliation (PRs #3886/#3887/#3888); without a gate it
will drift again.

Navigator v6.17.0 ships the reconciliation logic (`graph_maintenance.py
--action reconcile/health`), but CI cannot depend on a locally-installed
Claude Code plugin. The gate must be self-contained in this repo.

**Goal**:
A stdlib-only Python drift checker vendored into this repo, wired as
`make check-graph` and a CI job, that fails the build when the graph and
disk disagree.

---

## Known Pitfalls & Patterns

<!-- From knowledge graph (nav-task Step 2.5) -->

- **LEARNING** (90%, mem-033): Pilot done-states are claims, not evidence — verify the artifact (grep main for the expected signature), not status labels. Apply: the Done section greps for the workflow job and script on main.
- **PITFALL** (95%, mem-024): Dashboard failed-count conflation — collapsing distinct failure classes into one bucket hides real state. Apply: the checker reports each drift class separately (broken links / unindexed active / dangling edges / concept drift), never one merged count.
- **LEARNING** (95%, mem-019): Path-style mismatches (absolute FS path vs owner/repo) made a filter match 0 rows. Apply: the checker must resolve BOTH node path styles (`file` root-relative and `path` base_dir-relative) before comparing.

---

## Acceptance Criteria

- [ ] `scripts/check-graph.py` exists: stdlib-only Python 3, no external deps, no Navigator-plugin imports
- [ ] Detects and reports, each as a separate count with file/node lists:
  1. **Broken file links** — memory node `file`/`path`/`memory_file` refs that resolve to no file on disk (nodes with no path field are legal, NOT broken)
  2. **Unindexed active memory files** — `.agent/knowledge/memories/**/*.md` (excluding `README*` and files under a `resolved/` directory) with no graph node referencing them
  3. **Dangling edges** — edge endpoints that are not existing node ids
  4. **Invalid concept refs** (WARN only, non-fatal) — node concepts with no matching key in `nodes.concepts`
- [ ] Exit code 1 when classes 1–3 are non-zero; exit 0 with a warning block for class 4
- [ ] Path resolution handles both styles present in this repo's graph: root-relative (`.agent/knowledge/memories/...`) and base_dir-relative (`memories/...`)
- [ ] `make check-graph` target runs it
- [ ] CI job `graph-check` added to `.github/workflows/ci.yml`, runs on every PR + push to main, completes in <30s
- [ ] Running against the current repo state passes (the graph was reconciled 2026-07-06: 85 memories, 0 broken links, 0 unindexed active)
- [ ] Table-driven Python tests (or a shell fixture test) covering: clean repo, each drift class, both path styles, `resolved/` exclusion

---

## Implementation

### Phase 1: Checker script
**Goal**: Self-contained drift detector

**Tasks**:
- [ ] Write `scripts/check-graph.py` (~120 lines): load graph.json, glob memory files, run the 4 checks, print a per-class report, exit accordingly
- [ ] Reference algorithm: Navigator v6.17.0 `skills/nav-graph/functions/graph_maintenance.py` (`find_broken_file_links`, `find_unindexed_memory_files`, `find_invalid_concept_refs`, `find_dangling_edges`) — port the logic, do NOT import or vendor the file wholesale
- [ ] Tests in `scripts/check_graph_test.py` or table-driven fixtures under `scripts/testdata/`

**Files**:
- `scripts/check-graph.py` — the checker
- `scripts/` test file — fixtures for each drift class

### Phase 2: Wiring
**Goal**: Make it enforced, not optional

**Tasks**:
- [ ] `Makefile`: add `check-graph` target (python3 scripts/check-graph.py)
- [ ] `.github/workflows/ci.yml`: add `graph-check` job (ubuntu, setup nothing beyond checkout — system python3 suffices)
- [ ] Verify the job passes on the PR itself

**Files**:
- `Makefile`
- `.github/workflows/ci.yml`

---

## Out of Scope

- Auto-repair/reconcile in CI (report-only gate; fixing is a human/Navigator action via the plugin)
- Concept-vocabulary enforcement as a FAILURE (warn only — legacy freeform tags on old nodes are a known, accepted residue)
- Graph health scoring, staleness, confidence decay (Navigator-side concerns)
- Any change to graph.json itself

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the checker lives | (a) checkout Navigator repo in CI, (b) vendor self-contained script, (c) publish plugin as package | (b) vendor | CI must not depend on plugin availability or cross-repo auth; the algorithm is ~100 lines and stable |
| Language | Go (repo primary), Python | Python 3 stdlib | Mirrors the reference implementation 1:1; no build step; CI has python3 |
| Concept drift severity | fail, warn | warn | Old nodes carry accepted freeform tags; failing would force a mass rewrite with no value |
| `resolved/` files | count as drift, exclude | exclude from failure | Archived-without-node is this repo's established convention (2026-07-05 curation) |

---

## Verify

```bash
# Checker passes on current state
python3 scripts/check-graph.py

# Make target
make check-graph

# Tests
python3 -m unittest discover -s scripts -p "*_test.py" 2>/dev/null || python3 scripts/check_graph_test.py

# Break it on purpose: temporarily move a memory file, expect exit 1
mv .agent/knowledge/memories/patterns/pattern_hot_upgrade_bootstrap.md /tmp/ && \
  (python3 scripts/check-graph.py; echo "exit=$?") ; \
  mv /tmp/pattern_hot_upgrade_bootstrap.md .agent/knowledge/memories/patterns/
```

---

## Done

- [ ] `scripts/check-graph.py` on main, exits 0 against current repo state
- [ ] `make check-graph` target exists
- [ ] `graph-check` job visible and green in the PR's CI run
- [ ] Deliberate-drift test (moved file) exits 1 with the file named in output
- [ ] Tests pass in CI

---

## Refs

- Dispatched: https://github.com/qf-studio/pilot/issues/3898
- Audit + reconciliation this gates: PRs #3886, #3887, #3888 (2026-07-05/06)
- Reference implementation: Navigator v6.17.0 `skills/nav-graph/functions/graph_maintenance.py`
- Navigator release notes: alekspetrov/navigator releases/RELEASE-NOTES-v6.17.0.md

---

## Notes

The graph currently has 85 memories / 35 concepts / 160 edges and is clean
as of 2026-07-06 — the gate's first job is to keep it that way. Legacy
freeform concept tags exist on old nodes (mem-015..mem-045 range); that is
why concept drift is WARN, not FAIL.

---

**Last Updated**: 2026-07-06

---
**Shipped**: child GH-3901 → PRs #3904/#3906 (v2.216.0). Artifacts verified on main: `scripts/check-graph.py`, `make check-graph`, `graph-check` CI job. Parent #3898 closed manually (epic-close defect → #3924).
