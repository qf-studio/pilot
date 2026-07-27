#!/usr/bin/env python3
"""Table-driven tests for scripts/check-graph.py."""
import contextlib
import datetime
import importlib.util
import io
import json
import os
import sys
import tempfile
import unittest

SCRIPT_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "check-graph.py")


def _load_module():
    spec = importlib.util.spec_from_file_location("check_graph", SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


cg = _load_module()


def _write(path, content=""):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(content)


class ResolvePathTest(unittest.TestCase):
    def test_root_relative_style(self):
        with tempfile.TemporaryDirectory() as root:
            target = os.path.join(root, ".agent", "knowledge", "memories", "patterns", "a.md")
            _write(target, "x")
            resolved = cg.resolve_path(root, ".agent/knowledge/memories/patterns/a.md")
            self.assertEqual(os.path.normpath(resolved), os.path.normpath(target))

    def test_base_dir_relative_style(self):
        with tempfile.TemporaryDirectory() as root:
            target = os.path.join(root, ".agent", "knowledge", "memories", "patterns", "a.md")
            _write(target, "x")
            resolved = cg.resolve_path(root, "memories/patterns/a.md")
            self.assertEqual(os.path.normpath(resolved), os.path.normpath(target))

    def test_unresolvable_path(self):
        with tempfile.TemporaryDirectory() as root:
            self.assertIsNone(cg.resolve_path(root, "memories/patterns/missing.md"))


class BrokenFileLinksTest(unittest.TestCase):
    def test_no_path_field_is_legal(self):
        graph = {"nodes": {"memories": {"mem-001": {"type": "decision", "summary": "no path"}}}}
        with tempfile.TemporaryDirectory() as root:
            self.assertEqual(cg.find_broken_file_links(graph, root), [])

    def test_broken_link_reported(self):
        graph = {"nodes": {"memories": {"mem-001": {"file": ".agent/knowledge/memories/patterns/missing.md"}}}}
        with tempfile.TemporaryDirectory() as root:
            broken = cg.find_broken_file_links(graph, root)
            self.assertEqual(broken, [("mem-001", ".agent/knowledge/memories/patterns/missing.md")])

    def test_resolves_memory_file_and_path_fields(self):
        with tempfile.TemporaryDirectory() as root:
            target = os.path.join(root, ".agent", "knowledge", "memories", "patterns", "a.md")
            _write(target, "x")
            graph = {
                "nodes": {
                    "memories": {
                        "mem-001": {"memory_file": ".agent/knowledge/memories/patterns/a.md"},
                        "mem-002": {"path": "memories/patterns/a.md"},
                    }
                }
            }
            self.assertEqual(cg.find_broken_file_links(graph, root), [])


class UnindexedMemoryFilesTest(unittest.TestCase):
    def test_clean_repo_no_unindexed(self):
        with tempfile.TemporaryDirectory() as root:
            memories_base = os.path.join(root, ".agent", "knowledge", "memories")
            _write(os.path.join(memories_base, "patterns", "a.md"), "x")
            graph = {"nodes": {"memories": {"mem-001": {"file": ".agent/knowledge/memories/patterns/a.md"}}}}
            self.assertEqual(cg.find_unindexed_memory_files(graph, memories_base, root), [])

    def test_unindexed_file_reported(self):
        with tempfile.TemporaryDirectory() as root:
            memories_base = os.path.join(root, ".agent", "knowledge", "memories")
            _write(os.path.join(memories_base, "patterns", "orphan.md"), "x")
            graph = {"nodes": {"memories": {}}}
            unindexed = cg.find_unindexed_memory_files(graph, memories_base, root)
            self.assertEqual(unindexed, [os.path.join(".agent", "knowledge", "memories", "patterns", "orphan.md")])

    def test_readme_excluded(self):
        with tempfile.TemporaryDirectory() as root:
            memories_base = os.path.join(root, ".agent", "knowledge", "memories")
            _write(os.path.join(memories_base, "patterns", "README.md"), "x")
            graph = {"nodes": {"memories": {}}}
            self.assertEqual(cg.find_unindexed_memory_files(graph, memories_base, root), [])

    def test_resolved_dir_excluded(self):
        with tempfile.TemporaryDirectory() as root:
            memories_base = os.path.join(root, ".agent", "knowledge", "memories")
            _write(os.path.join(memories_base, "patterns", "resolved", "archived.md"), "x")
            graph = {"nodes": {"memories": {}}}
            self.assertEqual(cg.find_unindexed_memory_files(graph, memories_base, root), [])

    def test_base_dir_relative_index_recognized(self):
        with tempfile.TemporaryDirectory() as root:
            memories_base = os.path.join(root, ".agent", "knowledge", "memories")
            _write(os.path.join(memories_base, "patterns", "a.md"), "x")
            graph = {"nodes": {"memories": {"mem-001": {"file": "memories/patterns/a.md"}}}}
            self.assertEqual(cg.find_unindexed_memory_files(graph, memories_base, root), [])


class DanglingEdgesTest(unittest.TestCase):
    def test_clean_edges(self):
        graph = {
            "nodes": {"concepts": {"a": {}}, "memories": {"mem-001": {}}},
            "edges": [{"from": "a", "to": "mem-001", "type": "uses"}],
        }
        self.assertEqual(cg.find_dangling_edges(graph), [])

    def test_dangling_edge_reported(self):
        graph = {
            "nodes": {"concepts": {"a": {}}, "memories": {}},
            "edges": [{"from": "a", "to": "ghost", "type": "uses"}],
        }
        dangling = cg.find_dangling_edges(graph)
        self.assertEqual(len(dangling), 1)
        self.assertEqual(dangling[0]["to"], "ghost")


class InvalidConceptRefsTest(unittest.TestCase):
    def test_valid_concepts(self):
        graph = {
            "nodes": {
                "concepts": {"executor": {}},
                "memories": {"mem-001": {"concepts": ["executor"]}},
            }
        }
        self.assertEqual(cg.find_invalid_concept_refs(graph), [])

    def test_invalid_concept_warns_only(self):
        graph = {
            "nodes": {
                "concepts": {"executor": {}},
                "memories": {"mem-001": {"concepts": ["nonexistent"]}},
            }
        }
        self.assertEqual(cg.find_invalid_concept_refs(graph), [("mem-001", "nonexistent")])


class ParseFrontmatterTest(unittest.TestCase):
    def test_parses_flat_frontmatter(self):
        with tempfile.TemporaryDirectory() as root:
            path = os.path.join(root, "a.md")
            _write(path, "---\nname: foo-bar\ndescription: does a thing\ntype: pattern\n---\n\n# Foo\n")
            meta = cg.parse_frontmatter(path)
            self.assertEqual(meta, {"name": "foo-bar", "description": "does a thing", "type": "pattern"})

    def test_no_frontmatter_returns_empty_dict(self):
        with tempfile.TemporaryDirectory() as root:
            path = os.path.join(root, "a.md")
            _write(path, "# Just a heading, no front-matter\n")
            self.assertEqual(cg.parse_frontmatter(path), {})


class FixUnindexedTest(unittest.TestCase):
    def test_adds_stub_node_keyed_by_frontmatter_name(self):
        with tempfile.TemporaryDirectory() as root:
            rel_path = os.path.join(".agent", "knowledge", "memories", "patterns", "orphan.md")
            _write(
                os.path.join(root, rel_path),
                "---\nname: orphan-pattern\ndescription: an orphan\ntype: pattern\n---\n\nbody\n",
            )
            graph = {"nodes": {"memories": {}}}
            added = cg.fix_unindexed(graph, [rel_path], root)

            self.assertEqual(added, [("orphan-pattern", rel_path)])
            node = graph["nodes"]["memories"]["orphan-pattern"]
            today = datetime.date.today().isoformat()
            self.assertEqual(
                node,
                {
                    "type": "pattern",
                    "summary": "an orphan",
                    "file": rel_path,
                    "created": today,
                    "last_validated": today,
                },
            )

    def test_file_with_no_frontmatter_name_is_left_unindexed(self):
        with tempfile.TemporaryDirectory() as root:
            rel_path = os.path.join(".agent", "knowledge", "memories", "patterns", "orphan.md")
            _write(os.path.join(root, rel_path), "no front-matter here\n")
            graph = {"nodes": {"memories": {}}}
            added = cg.fix_unindexed(graph, [rel_path], root)
            self.assertEqual(added, [])
            self.assertEqual(graph["nodes"]["memories"], {})


class MainExitCodeTest(unittest.TestCase):
    def _build_repo(self, root):
        memories_base = os.path.join(root, ".agent", "knowledge", "memories")
        _write(os.path.join(memories_base, "patterns", "a.md"), "x")
        graph = {
            "version": "1.0.0",
            "nodes": {
                "concepts": {"executor": {}},
                "tasks": {},
                "memories": {"mem-001": {"file": ".agent/knowledge/memories/patterns/a.md", "concepts": ["executor"]}},
            },
            "edges": [],
        }
        graph_path = os.path.join(root, ".agent", "knowledge", "graph.json")
        os.makedirs(os.path.dirname(graph_path), exist_ok=True)
        import json
        with open(graph_path, "w", encoding="utf-8") as f:
            json.dump(graph, f)
        return graph_path, memories_base

    def _run_main(self, root, graph_path, memories_base, argv=None):
        cg.REPO_ROOT = root
        cg.GRAPH_PATH = graph_path
        cg.MEMORIES_BASE = memories_base
        old_argv = sys.argv
        sys.argv = argv if argv is not None else ["check-graph.py"]
        try:
            return cg.main()
        finally:
            sys.argv = old_argv

    def test_clean_repo_exits_zero(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            self.assertEqual(self._run_main(root, graph_path, memories_base), 0)

    def test_broken_link_exits_one(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            os.remove(os.path.join(memories_base, "patterns", "a.md"))
            self.assertEqual(self._run_main(root, graph_path, memories_base), 1)

    def test_unindexed_file_exits_one(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            _write(os.path.join(memories_base, "patterns", "orphan.md"), "x")
            self.assertEqual(self._run_main(root, graph_path, memories_base), 1)

    def test_dangling_edge_exits_one(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            import json
            with open(graph_path, "r+", encoding="utf-8") as f:
                graph = json.load(f)
                graph["edges"].append({"from": "ghost", "to": "mem-001", "type": "uses"})
                f.seek(0)
                json.dump(graph, f)
                f.truncate()
            self.assertEqual(self._run_main(root, graph_path, memories_base), 1)

    def test_invalid_concept_alone_exits_zero(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            import json
            with open(graph_path, "r+", encoding="utf-8") as f:
                graph = json.load(f)
                graph["nodes"]["memories"]["mem-001"]["concepts"] = ["nonexistent"]
                f.seek(0)
                json.dump(graph, f)
                f.truncate()
            self.assertEqual(self._run_main(root, graph_path, memories_base), 0)

    def test_deleted_indexed_and_added_unindexed_together_exits_one(self):
        """GH-4496 regression: a single PR that both deletes an indexed
        memory doc (broken link) AND adds a fresh unindexed one (present but
        not referenced) must fail the gate on BOTH counts simultaneously —
        neither direction may mask the other. This is the combined shape the
        drift gate must catch even though strikes 1-2 of the TASK-410 series
        additionally removed the dangling node itself (see
        TestEnforceMemoryDocDeletionGuard in internal/executor/git_test.go
        for that base-branch-aware case, which this single-tree-snapshot
        gate cannot see)."""
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            # Delete the indexed file on disk but leave its graph node
            # pointing at it — a broken link.
            os.remove(os.path.join(memories_base, "patterns", "a.md"))
            # Add a brand-new doc that no node references — an unindexed
            # file.
            _write(os.path.join(memories_base, "patterns", "orphan.md"), "x")

            with open(graph_path, "r", encoding="utf-8") as f:
                import json
                graph = json.load(f)

            broken_links = cg.find_broken_file_links(graph, root)
            unindexed = cg.find_unindexed_memory_files(graph, memories_base, root)
            self.assertEqual(broken_links, [("mem-001", ".agent/knowledge/memories/patterns/a.md")])
            self.assertEqual(unindexed, [os.path.join(".agent", "knowledge", "memories", "patterns", "orphan.md")])

            self.assertEqual(self._run_main(root, graph_path, memories_base), 1)

    def test_fix_indexes_unindexed_file_from_frontmatter(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            orphan_path = os.path.join(memories_base, "patterns", "orphan.md")
            _write(
                orphan_path,
                "---\nname: orphan-thing\ndescription: an orphan thing\ntype: learning\n---\n\nbody\n",
            )

            exit_code = self._run_main(root, graph_path, memories_base, argv=["check-graph.py", "--fix"])
            self.assertEqual(exit_code, 0)

            with open(graph_path, encoding="utf-8") as f:
                graph = json.load(f)
            self.assertIn("orphan-thing", graph["nodes"]["memories"])
            node = graph["nodes"]["memories"]["orphan-thing"]
            self.assertEqual(node["type"], "learning")
            self.assertEqual(node["summary"], "an orphan thing")
            self.assertEqual(node["file"], os.path.relpath(orphan_path, root))

            # Re-running without --fix now passes clean since the node was
            # persisted to graph.json on disk.
            self.assertEqual(self._run_main(root, graph_path, memories_base), 0)

    def test_fix_does_not_touch_broken_links_or_dangling_edges(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            # Class 1: break the indexed file link.
            os.remove(os.path.join(memories_base, "patterns", "a.md"))
            # Class 3: add a dangling edge.
            with open(graph_path, "r+", encoding="utf-8") as f:
                graph = json.load(f)
                graph["edges"].append({"from": "ghost", "to": "mem-001", "type": "uses"})
                f.seek(0)
                json.dump(graph, f)
                f.truncate()

            exit_code = self._run_main(root, graph_path, memories_base, argv=["check-graph.py", "--fix"])
            # Neither class is auto-fixable — --fix must still exit 1.
            self.assertEqual(exit_code, 1)

            with open(graph_path, encoding="utf-8") as f:
                graph = json.load(f)
            self.assertEqual(
                graph["nodes"]["memories"]["mem-001"]["file"],
                ".agent/knowledge/memories/patterns/a.md",
            )
            self.assertIn({"from": "ghost", "to": "mem-001", "type": "uses"}, graph["edges"])

    def test_no_fix_flag_output_is_unchanged(self):
        with tempfile.TemporaryDirectory() as root:
            graph_path, memories_base = self._build_repo(root)
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                exit_code = self._run_main(root, graph_path, memories_base, argv=["check-graph.py"])
            output = buf.getvalue()

            self.assertEqual(exit_code, 0)
            self.assertNotIn("Auto-fixed", output)
            self.assertEqual(
                output,
                "== Broken file links ==\n"
                "  none\n"
                "== Unindexed active memory files ==\n"
                "  none\n"
                "== Dangling edges ==\n"
                "  none\n"
                "== Invalid concept refs (warn only) ==\n"
                "  none\n"
                "\nOK: knowledge graph matches disk state\n",
            )


if __name__ == "__main__":
    unittest.main()
