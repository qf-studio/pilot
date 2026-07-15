#!/usr/bin/env python3
"""Table-driven tests for scripts/canary-report.py (GH-4348)."""
import importlib.util
import os
import sys
import unittest

SCRIPT_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "canary-report.py")


def _load_module():
    spec = importlib.util.spec_from_file_location("canary_report", SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


cr = _load_module()


class FakeGhClient:
    """In-memory stand-in for cr.GhClient -- no `gh` binary or network."""

    def __init__(self, repo):
        self.repo = repo
        self.issues = {}  # number -> {"title", "state"}
        self.comments = {}  # issue_number -> [{"id": int, "body": str}]
        self._next_issue = 1
        self._next_comment = 1

    def find_issue_by_title(self, title):
        for number, issue in self.issues.items():
            if issue["title"] == title:
                return {"number": number, "title": title, "state": issue["state"]}
        return None

    def create_issue(self, title, body, labels=None):
        number = self._next_issue
        self._next_issue += 1
        self.issues[number] = {"title": title, "state": "OPEN"}
        self.comments[number] = []
        return number

    def list_comments(self, issue_number):
        return list(self.comments.get(issue_number, []))

    def create_comment(self, issue_number, body):
        comment_id = self._next_comment
        self._next_comment += 1
        self.comments.setdefault(issue_number, []).append({"id": comment_id, "body": body})
        return comment_id

    def update_comment(self, comment_id, body):
        for comments in self.comments.values():
            for comment in comments:
                if comment["id"] == comment_id:
                    comment["body"] = body
                    return
        raise AssertionError(f"update_comment: no such comment id {comment_id}")

    def close_issue(self, issue_number, comment=None):
        if comment:
            self.create_comment(issue_number, comment)
        self.issues[issue_number]["state"] = "CLOSED"

    def reopen_issue(self, issue_number):
        self.issues[issue_number]["state"] = "OPEN"


def _report(client, result, timestamp, failed_assertions="", canary_issue="42", run_url="https://example/run/1"):
    return cr.report(
        client,
        scenario_name="epic-lifecycle",
        sandbox_repo="qf-studio/pilot-canary-sandbox",
        result=result,
        failed_assertions=failed_assertions,
        canary_issue=canary_issue,
        run_url=run_url,
        timestamp=timestamp,
    )


class ParseStateTest(unittest.TestCase):
    def test_missing_marker_returns_empty(self):
        self.assertEqual(cr.parse_state("just a regular comment"), {})

    def test_none_body_returns_empty(self):
        self.assertEqual(cr.parse_state(None), {})

    def test_malformed_json_returns_empty(self):
        body = f"{cr.MARKER_START}not json{cr.MARKER_END}\nrest of body"
        self.assertEqual(cr.parse_state(body), {})

    def test_roundtrip_through_render(self):
        state = cr.update_state({}, result="failure", failed_assertions="cascade", canary_issue="7", run_url="u", timestamp="t1")
        rendered = cr.render_comment(state, scenario_name="epic-lifecycle")
        self.assertEqual(cr.parse_state(rendered), state)


class UpdateStateTest(unittest.TestCase):
    def test_history_caps_at_ten(self):
        state = {}
        for i in range(15):
            state = cr.update_state(state, result="failure", failed_assertions="x", canary_issue="1", run_url="u", timestamp=f"t{i}")
        self.assertEqual(len(state["history"]), cr.MAX_HISTORY)
        self.assertEqual(state["history"][-1]["timestamp"], "t14")
        self.assertEqual(state["history"][0]["timestamp"], "t5")

    def test_streak_increments_on_success_and_resets_on_failure(self):
        state = {}
        state = cr.update_state(state, result="success", failed_assertions="", canary_issue="1", run_url="u", timestamp="t0")
        state = cr.update_state(state, result="success", failed_assertions="", canary_issue="1", run_url="u", timestamp="t1")
        self.assertEqual(state["streak"], 2)
        state = cr.update_state(state, result="failure", failed_assertions="cascade", canary_issue="1", run_url="u", timestamp="t2")
        self.assertEqual(state["streak"], 0)

    def test_should_close_at_target(self):
        state = {"streak": cr.GREEN_STREAK_TARGET}
        self.assertTrue(cr.should_close(state))
        self.assertFalse(cr.should_close({"streak": cr.GREEN_STREAK_TARGET - 1}))


class ConsecutiveFailuresMutateOneCommentTest(unittest.TestCase):
    def test_no_append_per_failure(self):
        client = FakeGhClient("qf-studio/pilot")

        for i in range(5):
            _report(client, "failure", timestamp=f"2026-07-{13 + i} 06:00 UTC", failed_assertions="cascade")

        self.assertEqual(len(client.issues), 1, "must reuse the same tracker issue, not file a new one per failure")
        issue_number = next(iter(client.issues))
        self.assertEqual(
            len(client.comments[issue_number]), 1,
            "must mutate one status comment, not append a new comment per failure (GH-4265 regression)",
        )

        state = cr.parse_state(client.comments[issue_number][0]["body"])
        self.assertEqual(len(state["history"]), 5)
        self.assertEqual(state["streak"], 0)

    def test_marker_comment_is_edited_not_recreated(self):
        client = FakeGhClient("qf-studio/pilot")
        _report(client, "failure", timestamp="t0")
        issue_number = next(iter(client.issues))
        first_comment_id = client.comments[issue_number][0]["id"]

        _report(client, "failure", timestamp="t1")

        self.assertEqual(len(client.comments[issue_number]), 1)
        self.assertEqual(client.comments[issue_number][0]["id"], first_comment_id)


class AutoCloseTest(unittest.TestCase):
    def test_three_green_runs_close_open_tracker(self):
        client = FakeGhClient("qf-studio/pilot")
        _report(client, "failure", timestamp="t0")
        issue_number = next(iter(client.issues))
        self.assertEqual(client.issues[issue_number]["state"], "OPEN")

        for i, ts in enumerate(["t1", "t2"]):
            _report(client, "success", timestamp=ts)
            self.assertEqual(client.issues[issue_number]["state"], "OPEN", f"should stay open before streak hits target (run {i})")

        _report(client, "success", timestamp="t3")
        self.assertEqual(client.issues[issue_number]["state"], "CLOSED")

        # A closing summary comment was posted alongside the mutated marker comment.
        bodies = [c["body"] for c in client.comments[issue_number]]
        self.assertTrue(any("closing tracker" in b for b in bodies))

    def test_success_with_no_prior_tracker_is_noop(self):
        client = FakeGhClient("qf-studio/pilot")
        result = _report(client, "success", timestamp="t0")
        self.assertIsNone(result)
        self.assertEqual(client.issues, {})

    def test_success_while_tracker_already_closed_is_noop(self):
        client = FakeGhClient("qf-studio/pilot")
        _report(client, "failure", timestamp="t0")
        _report(client, "success", timestamp="t1")
        _report(client, "success", timestamp="t2")
        _report(client, "success", timestamp="t3")
        issue_number = next(iter(client.issues))
        self.assertEqual(client.issues[issue_number]["state"], "CLOSED")
        comment_count_before = len(client.comments[issue_number])

        _report(client, "success", timestamp="t4")

        self.assertEqual(client.issues[issue_number]["state"], "CLOSED")
        self.assertEqual(len(client.comments[issue_number]), comment_count_before)


class ReopenOnFailureTest(unittest.TestCase):
    def test_new_failure_after_close_reopens_same_tracker(self):
        client = FakeGhClient("qf-studio/pilot")
        _report(client, "failure", timestamp="t0")
        _report(client, "success", timestamp="t1")
        _report(client, "success", timestamp="t2")
        _report(client, "success", timestamp="t3")
        issue_number = next(iter(client.issues))
        self.assertEqual(client.issues[issue_number]["state"], "CLOSED")

        result = _report(client, "failure", timestamp="t4", failed_assertions="parent-closed")

        self.assertEqual(result, issue_number, "must reopen the existing tracker, not file a new issue")
        self.assertEqual(client.issues[issue_number]["state"], "OPEN")
        self.assertEqual(len(client.issues), 1)

        state = cr.parse_state(cr.find_marker_comment(client, issue_number)[1])
        self.assertEqual(state["streak"], 0)
        self.assertEqual(state["history"][-1]["assertions"], "parent-closed")


class RenderCommentTest(unittest.TestCase):
    def test_contains_marker_scenario_and_history_table(self):
        state = cr.update_state({}, result="failure", failed_assertions="cascade", canary_issue="99", run_url="https://x/run/1", timestamp="2026-07-15 06:00 UTC")
        body = cr.render_comment(state, scenario_name="epic-lifecycle")

        self.assertIn(cr.MARKER_START, body)
        self.assertIn("epic-lifecycle", body)
        self.assertIn("cascade", body)
        self.assertIn("#99", body)
        self.assertIn("| Timestamp | Result | Assertion(s) | Run |", body)


if __name__ == "__main__":
    unittest.main()
