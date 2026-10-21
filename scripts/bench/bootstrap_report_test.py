#!/usr/bin/env python3
"""Offline tests for bootstrap_report.py and bootstrap-cost.sh --dry-run (TASK-581).

No test invokes `claude` or `pilot`; the dry-run tests put fake binaries on PATH
that record a marker if they are ever called. Run with `python3 -I`.
"""
import hashlib
import importlib.util
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout

HERE = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(os.path.dirname(HERE))
TESTDATA = os.path.join(HERE, "testdata")
FIXTURE_RUNS = os.path.join(TESTDATA, "bootstrap-runs.jsonl")
FIXTURE_STREAM = os.path.join(TESTDATA, "stream-stage1.jsonl")
FIXTURE_CLONES = os.path.join(TESTDATA, "clones")
PRICES_PATH = os.path.join(HERE, "prices.json")


def _load_module():
    spec = importlib.util.spec_from_file_location("bootstrap_report", os.path.join(HERE, "bootstrap_report.py"))
    module = importlib.util.module_from_spec(spec)
    sys.modules["bootstrap_report"] = module  # dataclasses resolve annotations through sys.modules
    spec.loader.exec_module(module)
    return module


br = _load_module()


def _stream_lines(workdir):
    with open(FIXTURE_STREAM, encoding="utf-8") as fh:
        return [line.replace("{WORKDIR}", workdir) for line in fh]


def _write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


class StreamParsingTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def test_result_event_gives_tokens_cost_turns_and_partial(self):
        got = br.parse_stream(_stream_lines(self.tmp), self.tmp)
        self.assertEqual(got.tokens_input, 1_500_000)
        self.assertEqual(got.tokens_output, 90_000)
        self.assertEqual(got.tokens_cache_write, 250_000)
        self.assertEqual(got.tokens_cache_read, 8_000_000)
        self.assertAlmostEqual(got.usd_reported, 12.3456)
        self.assertEqual(got.turns, 150)
        self.assertEqual(got.subtype, "error_max_turns")
        self.assertEqual(br.stop_reason_for(got.subtype, 0), "turns")

    def test_files_read_and_dirs_touched(self):
        got = br.parse_stream(_stream_lines(self.tmp), self.tmp)
        # sqlx.go is read twice but counted once; /etc/hostname is outside the checkout.
        self.assertEqual(got.files_read, {"sqlx.go", os.path.join("cmd", "main.go")})
        self.assertIn("pkg", got.touched)  # Grep path
        self.assertNotIn("/etc/hostname", got.touched)
        for name in ("cmd", "pkg", "docs"):
            os.makedirs(os.path.join(self.tmp, name))
        os.makedirs(os.path.join(self.tmp, ".git"))
        touched, total, kind = br.coverage(self.tmp, [], got.touched)
        self.assertEqual((touched, total, kind), (2, 3, "dirs"))

    def test_scope_coverage_counts_scope_entries(self):
        touched, total, kind = br.coverage(self.tmp, ["pkg/api", "pkg/services"], {"pkg/api/server.go", "pkg"})
        self.assertEqual((touched, total, kind), (1, 2, "scope"))

    def test_cut_off_stream_sums_assistant_usage_once_per_message(self):
        lines = [ln for ln in _stream_lines(self.tmp) if '"type":"result"' not in ln]
        got = br.parse_stream(lines + ['{"type":"assist'], self.tmp)  # truncated last line
        self.assertEqual(got.usage_source, "assistant_events")
        self.assertIsNone(got.usd_reported)
        self.assertEqual(got.tokens_input, 10 + 12 + 14)  # msg_2 repeats and counts once
        self.assertEqual(got.tokens_output, 5 + 9 + 11)
        self.assertEqual(got.turns, 3)
        self.assertEqual(br.stop_reason_for(got.subtype, 124), "timeout")
        self.assertEqual(br.stop_reason_for(got.subtype, 1), "error")

    def test_stop_reasons(self):
        for subtype, code, want in (
            ("success", 0, "ok"),
            ("error_max_turns", 0, "turns"),
            ("error_max_budget_usd", 0, "budget"),
            ("", 124, "timeout"),
            ("error_during_execution", 1, "error"),
        ):
            self.assertEqual(br.stop_reason_for(subtype, code), want, (subtype, code))

    def test_summarize_writes_a_complete_record(self):
        work = os.path.join(self.tmp, "work")
        for name in ("cmd", "pkg", "docs"):
            os.makedirs(os.path.join(work, name))
        _write(
            os.path.join(work, br.BRIEF_RELPATH),
            "# Brief\n\n### F01: a\nsqlx.go:10 and cmd/main.go:5\n\n### F02: b\npkg/named.go:30\n",
        )
        for n, body in (("01", "x sqlx.go:1 sqlx.go:2 sqlx.go:3"), ("02", "y sqlx.go:9")):
            _write(os.path.join(work, br.TICKETS_RELDIR, f"ticket-{n}.md"), body)
        stream = os.path.join(self.tmp, "stream.out")
        _write(stream, "".join(_stream_lines(work)))
        argv = [
            "summarize", "--stream", stream, "--workdir", work, "--repo", "sqlx", "--class", "S", "--stack", "Go",
            "--loc", "6000", "--files", "40", "--stage", "1", "--model", "claude-opus-5-5", "--effort", "high",
            "--turn-cap", "150", "--timeout-s", "1200", "--wall-s", "321.5", "--exit-code", "0", "--date", "2026-10-21",
        ]
        buf = io.StringIO()
        with redirect_stdout(buf):
            self.assertEqual(br.main(argv), 0)
        rec = json.loads(buf.getvalue())
        self.assertEqual(rec["class"], "S")
        self.assertEqual(rec["tokens_cache_read"], 8_000_000)
        self.assertAlmostEqual(rec["usd_reported"], 12.3456)
        self.assertEqual((rec["turns"], rec["partial"], rec["stop_reason"]), (150, True, "turns"))
        self.assertEqual(rec["files_read"], 2)
        self.assertEqual((rec["coverage_touched"], rec["coverage_total"]), (2, 3))
        self.assertEqual(rec["findings"], 2)
        self.assertEqual(rec["item_refs"], [["sqlx.go:10", "cmd/main.go:5"], ["pkg/named.go:30"]])
        self.assertEqual(rec["wall_s"], 321.5)
        # Every metric the plan lists is a key of the record.
        for key in ("repo", "loc", "files", "stage", "model", "effort", "turn_cap", "timeout_s", "tokens_input",
                    "tokens_output", "tokens_cache_write", "usd_reported", "files_read", "coverage_total",
                    "tickets", "item_refs"):
            self.assertIn(key, rec)
        # Stage 2 counts ticket files and keeps refs per ticket.
        argv[argv.index("--stage") + 1] = "2"
        buf = io.StringIO()
        with redirect_stdout(buf):
            br.main(argv)
        rec2 = json.loads(buf.getvalue())
        self.assertEqual(rec2["tickets"], 2)
        self.assertEqual(rec2["item_refs"], [["sqlx.go:1", "sqlx.go:2", "sqlx.go:3"], ["sqlx.go:9"]])

    def test_pilot_summary(self):
        with open(os.path.join(TESTDATA, "pilot-bootstrap-summary.txt"), encoding="utf-8") as fh:
            got = br.parse_pilot_summary(fh.read())
        self.assertAlmostEqual(got.usd_reported, 2.87)
        self.assertEqual((got.tokens_input, got.tokens_output), (1200, 3_400_000))
        self.assertEqual(br.stop_reason_for(got.subtype, 0), "turns")
        self.assertEqual(got.usage_source, "pilot_summary")

    def test_loc_json(self):
        scc = json.dumps([{"Name": "Go", "Code": 100, "Count": 3}, {"Name": "TS", "Code": 50, "Count": 2}])
        cloc = json.dumps({"header": {}, "SUM": {"code": 77, "nFiles": 4}})
        self.assertEqual(br.parse_loc_json(scc), (150, 5))
        self.assertEqual(br.parse_loc_json(cloc), (77, 4))
        self.assertEqual(br.parse_loc_json("not json"), (None, None))


class RefsTest(unittest.TestCase):
    def test_extract_refs(self):
        text = "see `sqlx.go:10`, (cmd/main.go:5) and https://example.com:8080/x but also sqlx.go:10 again"
        self.assertEqual(br.extract_refs(text), ["sqlx.go:10", "cmd/main.go:5"])

    def test_ref_valid_checks_path_and_line_range(self):
        root = os.path.join(FIXTURE_CLONES, "sqlx")
        cases = (
            ("sqlx.go:40", True),  # last line
            ("sqlx.go:41", False),  # past the end
            ("sqlx.go:0", False),
            ("pkg/missing.go:7", False),  # no such path
            ("cmd:3", False),  # a directory, not a file
            ("./cmd/main.go:20", True),
            ("../clones/sqlx/sqlx.go:1", False),  # model output must not walk out of the clone
            ("/etc/hostname:1", False),
        )
        for ref, want in cases:
            self.assertEqual(br.ref_valid(ref, root), want, ref)

    def test_line_count(self):
        with tempfile.TemporaryDirectory() as tmp:
            for name, data, want in (("a", b"", 0), ("b", b"x", 1), ("c", b"x\n", 1), ("d", b"x\ny", 2)):
                path = os.path.join(tmp, name)
                with open(path, "wb") as fh:
                    fh.write(data)
                self.assertEqual(br.line_count(path), want, name)


class ReportTest(unittest.TestCase):
    def setUp(self):
        self.prices = br.Prices.load(PRICES_PATH)
        self.results = br.load_results(FIXTURE_RUNS)

    def session(self, **match):
        for s in self.results.sessions:
            if all(getattr(s, k) == v for k, v in match.items()):
                return s
        self.fail(f"no session matching {match}")

    def test_prices_file_is_sourced(self):
        self.assertTrue(self.prices.source)
        self.assertTrue(self.prices.as_of)
        self.assertEqual(self.prices.models["claude-opus-5-5"], {"input": 4.0, "output": 20.0, "cache_read": 0.2, "cache_write": 5.0})
        self.assertEqual(self.prices.models["claude-sonnet-5-5"], {"input": 2.0, "output": 10.0, "cache_read": 0.2, "cache_write": 2.5})

    def test_estimate_for_known_opus_row(self):
        # 1.0M in x $4 + 0.1M out x $20 + 0.2M write x $5 + 5.0M read x $0.20
        s = self.session(repo="sqlx", stage=1)
        self.assertAlmostEqual(br.estimate_usd(s, self.prices), 4.0 + 2.0 + 1.0 + 1.0)
        # 0.2M x $2 + 0.05M x $10 + 0.1M x $2.5 + 2.0M x $0.20
        s2 = self.session(repo="sqlx", stage=2)
        self.assertAlmostEqual(br.estimate_usd(s2, self.prices), 0.4 + 0.5 + 0.25 + 0.4)
        self.assertIsNone(br.estimate_usd(br.Session(repo="x", stage=1, model="some-other-model"), self.prices))
        dated = br.Session(repo="x", stage=1, model="claude-opus-5-5-20261001", tokens_input=1_000_000)
        self.assertAlmostEqual(br.estimate_usd(dated, self.prices), 4.0)

    def test_report_prints_both_usd_columns(self):
        text = br.render_report(self.results, self.prices, FIXTURE_CLONES)
        self.assertIn("USD reported", text)
        self.assertIn("USD est.", text)
        row = next(ln for ln in text.splitlines() if ln.startswith("| sqlx | S |") and "| 1 | full |" in ln)
        self.assertIn("$7.50", row)  # reported
        self.assertIn("$8.00", row)  # estimated

    def test_unresolvable_ref_lowers_evidence_validity(self):
        clone = os.path.join(FIXTURE_CLONES, "sqlx")
        full = br.evidence_for(self.session(repo="sqlx", stage=1), clone)
        self.assertEqual((full.valid, full.total), (4, 4))
        self.assertEqual(full.validity, 1.0)
        bad = br.evidence_for(self.session(repo="sqlx", stage=2), clone)
        self.assertEqual((bad.valid, bad.total), (7, 9))
        self.assertLess(bad.validity, 1.0)
        self.assertEqual((bad.items_ok, bad.items), (2, 3))
        self.assertAlmostEqual(bad.density, 3.0)
        text = br.render_report(self.results, self.prices, FIXTURE_CLONES)
        self.assertIn("78%", text)

    def test_without_a_clone_validity_is_not_available(self):
        self.assertIsNone(br.evidence_for(self.session(repo="react-router"), None))
        self.assertIsNone(br.evidence_for(self.session(repo="react-router"), "/nonexistent-clone"))
        text = br.render_report(self.results, self.prices, None)
        rr = next(ln for ln in text.splitlines() if ln.startswith("| react-router |"))
        self.assertIn("n/a", rr)

    def test_partial_runs_are_marked(self):
        text = br.render_report(self.results, self.prices, FIXTURE_CLONES)
        rr = next(ln for ln in text.splitlines() if ln.startswith("| react-router |"))
        self.assertIn("yes (turns)", rr)
        self.assertIn("5/8 dirs", rr)
        killed = next(ln for ln in text.splitlines() if ln.startswith("| cli |"))
        self.assertIn("yes (timeout)", killed)
        self.assertIn("$3.10", killed)  # no reported USD, so only the estimate is filled
        self.assertIn("n/a", killed)

    def test_percentiles_per_group(self):
        self.assertEqual(br.percentile([3.0, 9.0], 50), 3.0)
        self.assertEqual(br.percentile([3.0, 9.0], 90), 9.0)
        self.assertEqual(br.percentile([5.0], 90), 5.0)
        text = br.render_report(self.results, self.prices, None)
        self.assertIn("| sqlx | 1 | full | 1 | $7.50 | $7.50 | $7.50 |", text)

    def test_matrix_guard_stops_and_partial_jsonl_stays_readable(self):
        sessions = self.results.sessions
        # 7.50 + 1.90 + 3.10 (estimated: the killed session reported nothing) + 11.25 crosses 20 at the fourth.
        self.assertEqual(br.matrix_guard_hit(sessions, 20.0, self.prices), 3)
        # With a lower limit the estimated session matters: 7.50 + 1.90 + 3.10 = 12.50 crosses 12 at the third,
        # while without prices the killed session counts 0 and the guard only trips at the fourth.
        self.assertEqual(br.matrix_guard_hit(sessions, 12.0, self.prices), 2)
        self.assertEqual(br.matrix_guard_hit(sessions, 12.0, None), 3)
        self.assertIsNone(br.matrix_guard_hit(sessions, 100.0, self.prices))
        self.assertEqual(self.results.matrix_stop["sessions_run"], len(sessions))
        text = br.render_report(self.results, self.prices, FIXTURE_CLONES)
        self.assertIn("Matrix guard hit", text)
        self.assertIn("4 of 30", text)

        # A runner killed mid-write leaves a truncated last line: still reportable.
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "2026-10-21.jsonl")
            with open(FIXTURE_RUNS, encoding="utf-8") as fh:
                data = fh.read()
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(data + '{"repo": "sqlx", "stage"')
            partial = br.load_results(path)
            self.assertEqual(len(partial.sessions), 4)
            self.assertEqual(partial.bad_lines, 1)
            self.assertIn("unreadable line", br.render_report(partial, self.prices, None))

    def test_usd_subcommand_feeds_the_runner_guard(self):
        line = json.dumps(self.session(repo="sqlx", stage=1).to_dict())
        buf = io.StringIO()
        old_stdin = sys.stdin
        sys.stdin = io.StringIO(line + "\n")
        try:
            with redirect_stdout(buf):
                br.main(["usd", "--prices", PRICES_PATH])
        finally:
            sys.stdin = old_stdin
        self.assertAlmostEqual(float(buf.getvalue()), 7.5)

    def test_latest_results_file_and_empty_dir(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(br.latest_results_file(tmp))
            for name in ("2026-10-20.jsonl", "2026-10-21.jsonl", "notes.txt"):
                _write(os.path.join(tmp, name), "")
            self.assertEqual(os.path.basename(br.latest_results_file(tmp)), "2026-10-21.jsonl")
            buf = io.StringIO()
            with redirect_stdout(buf):
                br.main(["report", "--results-dir", os.path.join(tmp, "empty")])
            self.assertIn("No results yet", buf.getvalue())


def _chat_prompt_const(name):
    """Body of a const in internal/executor/chat_prompt.go, or None if the Go file is absent."""
    path = os.path.join(REPO_ROOT, "internal", "executor", "chat_prompt.go")
    if not os.path.isfile(path):
        return None
    with open(path, encoding="utf-8") as fh:
        m = re.search(r"const %s = (`|\")(.*?)\1\n" % name, fh.read(), re.S)
    return m.group(2) if m else None


class PromptFilesTest(unittest.TestCase):
    def read(self, name):
        with open(os.path.join(HERE, "prompts", name), encoding="utf-8") as fh:
            return fh.read()

    def test_citation_and_dependency_rules_are_present(self):
        stage1, stage2 = self.read("stage1-research.md"), self.read("stage2-draft.md")
        cite = "Cite every claim as a path and line number in the form path:line."
        blocked = '"Blocked by: #N"'
        self.assertIn(cite, stage1)
        self.assertIn(cite, stage2)
        self.assertIn(blocked, stage2)
        self.assertIn("Every acceptance bullet names a test.", stage2)
        self.assertIn("500 changed lines", stage2)
        self.assertIn("minimum 5", stage2)
        self.assertIn("at least 3 valid path:line references", stage2)

    def test_prompts_carry_chat_prompt_text_verbatim(self):
        method = _chat_prompt_const("chatResearchMethod")
        contract = _chat_prompt_const("chatIssueContract")
        if method is None or contract is None:
            self.skipTest("internal/executor/chat_prompt.go is not in this tree")
        persona = _chat_prompt_const("chatPersonaLine")
        stage1, stage2 = self.read("stage1-research.md"), self.read("stage2-draft.md")
        for text in (stage1, stage2):
            self.assertIn(persona, text)
            self.assertIn(method, text)
        self.assertIn(contract, stage2)

    def test_prompts_have_the_budget_placeholders(self):
        stage1, stage2 = self.read("stage1-research.md"), self.read("stage2-draft.md")
        self.assertIn("{{TURN_CAP}}", stage1)
        self.assertIn("{{SCOPE_LINE}}", stage1)
        self.assertIn("{{TURN_CAP}}", stage2)
        self.assertIn("80 percent", stage1)
        self.assertIn(".agent/system/bootstrap-research.md", stage1)
        self.assertIn("### F01", stage1)


def _snapshot(root):
    snap = {}
    for dirpath, dirnames, filenames in os.walk(root):
        for name in dirnames:
            snap[os.path.relpath(os.path.join(dirpath, name), root)] = "dir"
        for name in filenames:
            path = os.path.join(dirpath, name)
            with open(path, "rb") as fh:
                snap[os.path.relpath(path, root)] = hashlib.sha256(fh.read()).hexdigest()
    return snap


class DryRunTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.tree = os.path.join(self.tmp, "tree")
        shutil.copytree(HERE, os.path.join(self.tree, "scripts", "bench"), ignore=shutil.ignore_patterns("__pycache__", "results"))
        _write(os.path.join(self.tree, ".agent", "DEVELOPMENT-README.md"), "index\n")
        self.script = os.path.join(self.tree, "scripts", "bench", "bootstrap-cost.sh")
        # Fake binaries that leave a marker if anything ever calls them.
        self.fakebin = os.path.join(self.tmp, "fakebin")
        self.markers = os.path.join(self.tmp, "markers")
        os.makedirs(self.markers)
        for name in ("claude", "pilot", "git", "scc", "cloc"):
            path = os.path.join(self.fakebin, name)
            _write(path, f'#!/bin/sh\necho "$0 $*" >> "{self.markers}/{name}"\nexit 1\n')
            os.chmod(path, 0o755)

    def run_script(self, *args):
        env = dict(os.environ)
        env["PATH"] = self.fakebin + os.pathsep + env.get("PATH", "")
        env["TMPDIR"] = self.tmp
        env.pop("BOOTSTRAP_BENCH_SCRATCH", None)
        return subprocess.run(["bash", self.script, *args], cwd=self.tree, env=env,
                              capture_output=True, text=True, timeout=60, check=False)

    def commands(self, out):
        return [ln for ln in out.splitlines() if ln.startswith("  $ ")]

    def test_script_parses(self):
        done = subprocess.run(["bash", "-n", self.script], capture_output=True, text=True, check=False)
        self.assertEqual(done.returncode, 0, done.stderr)

    def test_full_matrix_prints_30_commands_and_runs_nothing(self):
        before = _snapshot(self.tmp)
        done = self.run_script("--dry-run")
        self.assertEqual(done.returncode, 0, done.stderr)
        cmds = self.commands(done.stdout)
        self.assertEqual(len(cmds), 30)
        self.assertEqual(sum(1 for ln in done.stdout.splitlines() if ln.startswith("SESSION ")), 30)
        # 4 repos x 3 stages x 2 runs, 4 fallback runs, 2 scoped runs.
        self.assertEqual(sum(1 for c in cmds if "pilot navigator bootstrap" in c), 8)
        self.assertEqual(sum(1 for c in cmds if "stage1" in c or "bootstrap-research.md" in c), 8 + 4 + 2)
        self.assertEqual(sum(1 for c in cmds if "claude -p" in c), 22)
        self.assertEqual(sum(1 for ln in done.stdout.splitlines() if "variant=fallback" in ln), 4)
        self.assertEqual(sum(1 for ln in done.stdout.splitlines() if "variant=scoped" in ln), 2)
        self.assertEqual(os.listdir(self.markers), [], "dry-run invoked an external binary")
        self.assertEqual(_snapshot(self.tmp), before, "dry-run changed the tree or created files")

    def test_dry_run_leaves_agent_dir_and_results_untouched(self):
        before = _snapshot(os.path.join(self.tree, ".agent"))
        done = self.run_script("--dry-run", "--repos", "sqlx", "--runs", "1")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(_snapshot(os.path.join(self.tree, ".agent")), before)
        self.assertFalse(os.path.exists(os.path.join(self.tree, "scripts", "bench", "results")))
        self.assertEqual(len(self.commands(done.stdout)), 4)  # stages 0,1,2 + the Sonnet fallback
        self.assertEqual(os.listdir(self.markers), [])

    def test_explicit_scope_runs_only_scoped_sessions(self):
        done = self.run_script("--dry-run", "--repos", "grafana", "--stages", "1", "--runs", "2",
                               "--scope", "pkg/api,pkg/services,public/app/features")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(len(self.commands(done.stdout)), 2)
        self.assertNotIn("variant=full", done.stdout)
        self.assertNotIn("variant=fallback", done.stdout)
        self.assertIn("scope=pkg/api,pkg/services,public/app/features", done.stdout)

    def test_flags_shape_the_commands(self):
        done = self.run_script("--dry-run", "--repos", "sqlx", "--runs", "1", "--stages", "1,2",
                               "--model-research", "model-r", "--model-draft", "model-d",
                               "--max-usd", "12", "--no-fallback")
        self.assertEqual(done.returncode, 0, done.stderr)
        cmds = self.commands(done.stdout)
        self.assertEqual(len(cmds), 2)
        self.assertIn("--model model-r", cmds[0])
        self.assertIn("--model model-d", cmds[1])
        self.assertIn("--max-turns 150", cmds[0])
        self.assertIn("--max-turns 40", cmds[1])
        self.assertIn("--max-budget-usd 12", cmds[0])
        self.assertIn("--output-format stream-json", cmds[0])
        self.assertIn("Write", cmds[0])
        for forbidden in ("Edit", "Bash\\(rm", "git\\ push"):
            self.assertNotIn(forbidden, cmds[0])

    def test_bad_flags_fail_without_side_effects(self):
        for args in (["--bogus"], ["--runs", "0"], ["--stages", "3"], ["--repos", "nosuchrepo"], ["--max-usd", "abc"]):
            done = self.run_script("--dry-run", *args)
            self.assertEqual(done.returncode, 2, args)
        self.assertEqual(os.listdir(self.markers), [])


if __name__ == "__main__":
    unittest.main()
