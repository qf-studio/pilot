#!/usr/bin/env python3
"""Canary tracker reporter (GH-4348).

Maintains ONE mutating status comment (marked with `<!-- canary-status -->`)
on the alert-repo tracker issue for a scenario, instead of appending a new
"Canary re-triggered ... stalled" comment on every failed scheduled run
(evidence: #4265 accumulated 10 duplicate comments in 3 days). Also
auto-closes the tracker after 3 consecutive green scheduled runs while it is
open, and reopens it (rather than filing a new issue) on the next failure --
keeping the pre-existing evergreen-tracker pattern.

Called from the "Report scenario result to tracker issue" step in
.github/workflows/pilot-canary-scenario.yml, once per run (success or
failure). All GitHub I/O goes through GhClient so `report()` -- the decision
logic -- can be unit tested against FakeGhClient without a network call or
the `gh` binary (see canary-report_test.py).
"""
import argparse
import json
import re
import subprocess
import sys

MARKER_START = "<!-- canary-status:"
MARKER_END = " -->"
MAX_HISTORY = 10
GREEN_STREAK_TARGET = 3


def tracker_title(scenario_name):
    return f"[canary:{scenario_name}] pipeline stall"


def parse_state(comment_body):
    """Extract the JSON state blob embedded in a marker comment's body.
    Returns {} for a fresh tracker (no marker comment yet, or a malformed
    one from before this state format existed)."""
    if not comment_body:
        return {}
    m = re.search(re.escape(MARKER_START) + r"(.*?)" + re.escape(MARKER_END), comment_body, re.DOTALL)
    if not m:
        return {}
    try:
        return json.loads(m.group(1))
    except json.JSONDecodeError:
        return {}


def update_state(state, *, result, failed_assertions, canary_issue, run_url, timestamp):
    history = list(state.get("history", []))
    history.append(
        {
            "timestamp": timestamp,
            "result": result,
            "assertions": failed_assertions or "",
            "run_url": run_url or "",
        }
    )
    history = history[-MAX_HISTORY:]

    streak = state.get("streak", 0)
    streak = streak + 1 if result == "success" else 0

    return {"history": history, "streak": streak, "canary_issue": canary_issue or ""}


def should_close(state):
    return state.get("streak", 0) >= GREEN_STREAK_TARGET


def render_comment(state, *, scenario_name):
    history = state.get("history", [])
    latest = history[-1] if history else None
    streak = state.get("streak", 0)

    lines = [f"{MARKER_START}{json.dumps(state, sort_keys=True)}{MARKER_END}"]
    lines.append(f"### Canary status: `{scenario_name}`")
    lines.append("")
    if latest:
        result_word = "PASS" if latest["result"] == "success" else "FAIL"
        lines.append(f"**Latest run ({latest['timestamp']}): {result_word}**")
        if latest["result"] != "success" and latest["assertions"]:
            lines.append(f"- Failed assertion(s): `{latest['assertions']}`")
        if state.get("canary_issue"):
            lines.append(f"- Canary issue: #{state['canary_issue']}")
        if latest.get("run_url"):
            lines.append(f"- Run: {latest['run_url']}")
    lines.append("")
    lines.append(f"Green streak: {streak}/{GREEN_STREAK_TARGET}")
    lines.append("")
    lines.append("| Timestamp | Result | Assertion(s) | Run |")
    lines.append("|---|---|---|---|")
    for entry in reversed(history):
        result_word = "PASS" if entry["result"] == "success" else "FAIL"
        assertion = entry["assertions"] or "-"
        run_link = f"[link]({entry['run_url']})" if entry.get("run_url") else "-"
        lines.append(f"| {entry['timestamp']} | {result_word} | {assertion} | {run_link} |")

    return "\n".join(lines)


def find_marker_comment(client, issue_number):
    for comment in client.list_comments(issue_number):
        if MARKER_START in comment.get("body", ""):
            return comment["id"], comment["body"]
    return None, None


def report(client, *, scenario_name, sandbox_repo, result, failed_assertions, canary_issue, run_url, timestamp):
    """Decide what to do with the tracker issue for one scenario run and
    apply it via `client`. Returns the tracker issue number touched, or None
    if there was nothing to report (scenario has never failed and this run
    passed, or the tracker is already closed and this run passed)."""
    title = tracker_title(scenario_name)
    issue = client.find_issue_by_title(title)

    if issue is None:
        if result != "failure":
            return None
        body = (
            f"Synthetic end-to-end pipeline canary stall tracker (scenario `{scenario_name}`).\n\n"
            "Status is maintained in a single mutating comment below (marked "
            f"`{MARKER_START.strip()}`) rather than appended per failure -- GH-4348. "
            "Investigate daemon health (poll -> spec -> execute -> PR -> merge). See TASK-403."
        )
        issue_number = client.create_issue(title, body, labels=["bug"])
        state = update_state(
            {},
            result=result,
            failed_assertions=failed_assertions,
            canary_issue=canary_issue,
            run_url=run_url,
            timestamp=timestamp,
        )
        client.create_comment(issue_number, render_comment(state, scenario_name=scenario_name))
        return issue_number

    issue_number = issue["number"]

    if result != "failure" and issue["state"] == "CLOSED":
        return None

    if result == "failure" and issue["state"] == "CLOSED":
        client.reopen_issue(issue_number)

    comment_id, comment_body = find_marker_comment(client, issue_number)
    state = parse_state(comment_body)
    state = update_state(
        state,
        result=result,
        failed_assertions=failed_assertions,
        canary_issue=canary_issue,
        run_url=run_url,
        timestamp=timestamp,
    )
    rendered = render_comment(state, scenario_name=scenario_name)
    if comment_id is None:
        client.create_comment(issue_number, rendered)
    else:
        client.update_comment(comment_id, rendered)

    if result == "success" and should_close(state):
        summary = (
            f"Scenario `{scenario_name}` passed {GREEN_STREAK_TARGET} consecutive scheduled "
            f"runs -- closing tracker (canary issue: {sandbox_repo}#{canary_issue})."
        )
        client.close_issue(issue_number, comment=summary)

    return issue_number


def _gh(*args):
    result = subprocess.run(["gh", *args], capture_output=True, text=True, check=True)
    return result.stdout


class GhClient:
    """Thin `gh` CLI wrapper -- the only place in this module that performs
    real GitHub I/O. Swap for FakeGhClient in tests."""

    def __init__(self, repo):
        self.repo = repo

    def find_issue_by_title(self, title):
        out = _gh(
            "issue", "list",
            "--repo", self.repo,
            "--state", "all",
            "--search", f"{title} in:title",
            "--json", "number,title,state",
        )
        for item in json.loads(out):
            if item["title"] == title:
                return item
        return None

    def create_issue(self, title, body, labels=None):
        args = ["issue", "create", "--repo", self.repo, "--title", title, "--body", body]
        for label in labels or []:
            args += ["--label", label]
        url = _gh(*args).strip()
        return int(url.rsplit("/", 1)[-1])

    def list_comments(self, issue_number):
        out = _gh("api", f"repos/{self.repo}/issues/{issue_number}/comments", "--paginate")
        return json.loads(out)

    def create_comment(self, issue_number, body):
        _gh("issue", "comment", str(issue_number), "--repo", self.repo, "--body", body)

    def update_comment(self, comment_id, body):
        _gh("api", f"repos/{self.repo}/issues/comments/{comment_id}", "-X", "PATCH", "-f", f"body={body}")

    def close_issue(self, issue_number, comment=None):
        args = ["issue", "close", str(issue_number), "--repo", self.repo]
        if comment:
            args += ["--comment", comment]
        _gh(*args)

    def reopen_issue(self, issue_number):
        _gh("issue", "reopen", str(issue_number), "--repo", self.repo)


def parse_args(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--alert-repo", required=True, help="owner/name of the repo the tracker issue lives on")
    parser.add_argument("--sandbox-repo", required=True, help="owner/name of the sandbox repo running the scenario")
    parser.add_argument("--scenario-name", required=True)
    parser.add_argument("--result", required=True, choices=("success", "failure"))
    parser.add_argument("--failed-assertions", default="")
    parser.add_argument("--canary-issue", default="")
    parser.add_argument("--run-url", default="")
    parser.add_argument("--timestamp", required=True)
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv if argv is not None else sys.argv[1:])
    client = GhClient(args.alert_repo)
    issue_number = report(
        client,
        scenario_name=args.scenario_name,
        sandbox_repo=args.sandbox_repo,
        result=args.result,
        failed_assertions=args.failed_assertions,
        canary_issue=args.canary_issue,
        run_url=args.run_url,
        timestamp=args.timestamp,
    )
    if issue_number is not None:
        print(f"Reported {args.result} to {args.alert_repo}#{issue_number}")
    else:
        print("Nothing to report (tracker healthy and closed, or never failed).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
