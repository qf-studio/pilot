#!/usr/bin/env python3
"""Knowledge-graph drift gate.

Fails when .agent/knowledge/graph.json disagrees with the memory files on
disk under .agent/knowledge/memories/. Ports the four checks from Navigator
v6.17.0's graph_maintenance.py (reimplemented, not vendored):

  1. Broken file links   — memory node file/path/memory_file refs that don't
                            resolve on disk (FAIL)
  2. Unindexed active    — memory files with no node referencing them (FAIL)
  3. Dangling edges      — edge endpoints not present in nodes (FAIL)
  4. Invalid concept refs — node concepts with no matching concept key (WARN)

Exit 0 when classes 1-3 are empty (regardless of class 4). Exit 1 otherwise.
"""
import glob
import json
import os
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GRAPH_PATH = os.path.join(REPO_ROOT, ".agent", "knowledge", "graph.json")
MEMORIES_BASE = os.path.join(REPO_ROOT, ".agent", "knowledge", "memories")
PATH_FIELDS = ("file", "path", "memory_file")


def load_graph(graph_path):
    with open(graph_path, "r", encoding="utf-8") as f:
        return json.load(f)


def resolve_path(repo_root, raw_path):
    """Resolve a node path field against both known styles: root-relative
    (.agent/knowledge/memories/...) and base_dir-relative (memories/...)."""
    candidates = [
        os.path.join(repo_root, raw_path),
        os.path.join(repo_root, ".agent", "knowledge", raw_path),
    ]
    for candidate in candidates:
        if os.path.isfile(candidate):
            return candidate
    return None


def find_broken_file_links(graph, repo_root):
    broken = []
    for node_id, node in graph.get("nodes", {}).get("memories", {}).items():
        raw_path = None
        for field in PATH_FIELDS:
            if field in node:
                raw_path = node[field]
                break
        if raw_path is None:
            continue
        if resolve_path(repo_root, raw_path) is None:
            broken.append((node_id, raw_path))
    return broken


def find_unindexed_memory_files(graph, memories_base, repo_root):
    indexed = set()
    for node in graph.get("nodes", {}).get("memories", {}).values():
        for field in PATH_FIELDS:
            if field in node:
                resolved = resolve_path(repo_root, node[field])
                if resolved:
                    indexed.add(os.path.normpath(resolved))

    unindexed = []
    pattern = os.path.join(memories_base, "**", "*.md")
    for filepath in glob.glob(pattern, recursive=True):
        rel = os.path.relpath(filepath, memories_base)
        parts = rel.split(os.sep)
        if parts[0] == "resolved" or "resolved" in parts[:-1]:
            continue
        if os.path.basename(filepath).startswith("README"):
            continue
        if os.path.normpath(filepath) not in indexed:
            unindexed.append(os.path.relpath(filepath, repo_root))
    return sorted(unindexed)


def find_dangling_edges(graph):
    node_ids = set()
    for category in graph.get("nodes", {}).values():
        node_ids.update(category.keys())

    dangling = []
    for edge in graph.get("edges", []):
        src, dst = edge.get("from"), edge.get("to")
        if src not in node_ids or dst not in node_ids:
            dangling.append(edge)
    return dangling


def find_invalid_concept_refs(graph):
    concept_ids = set(graph.get("nodes", {}).get("concepts", {}).keys())
    invalid = []
    for node_id, node in graph.get("nodes", {}).get("memories", {}).items():
        for concept in node.get("concepts", []):
            if concept not in concept_ids:
                invalid.append((node_id, concept))
    return invalid


def main():
    graph = load_graph(GRAPH_PATH)

    broken_links = find_broken_file_links(graph, REPO_ROOT)
    unindexed = find_unindexed_memory_files(graph, MEMORIES_BASE, REPO_ROOT)
    dangling_edges = find_dangling_edges(graph)
    invalid_concepts = find_invalid_concept_refs(graph)

    fail = False

    print("== Broken file links ==")
    if broken_links:
        fail = True
        for node_id, raw_path in broken_links:
            print(f"  FAIL {node_id}: {raw_path}")
    else:
        print("  none")

    print("== Unindexed active memory files ==")
    if unindexed:
        fail = True
        for rel_path in unindexed:
            print(f"  FAIL {rel_path}")
    else:
        print("  none")

    print("== Dangling edges ==")
    if dangling_edges:
        fail = True
        for edge in dangling_edges:
            print(f"  FAIL {edge.get('from')} -> {edge.get('to')} ({edge.get('type')})")
    else:
        print("  none")

    print("== Invalid concept refs (warn only) ==")
    if invalid_concepts:
        for node_id, concept in invalid_concepts:
            print(f"  WARN {node_id}: unknown concept '{concept}'")
    else:
        print("  none")

    if fail:
        print("\nFAIL: knowledge graph drift detected (see FAIL lines above)")
        return 1

    print("\nOK: knowledge graph matches disk state")
    return 0


if __name__ == "__main__":
    sys.exit(main())
