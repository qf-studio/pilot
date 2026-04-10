#!/usr/bin/env python3
"""
Analyze Terminal-Bench run results to identify failure patterns.

Reads Harbor output directory, identifies failed tasks, extracts error
signatures, and suggests new patterns for the pattern library.

Usage:
    python3 analyze-results.py <results-dir>
    python3 analyze-results.py <results-dir> --suggest-patterns
    python3 analyze-results.py <results-dir> --compare <other-results-dir>
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import Counter
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class TaskResult:
    """Result of a single Terminal-Bench task."""
    task_id: str
    passed: bool
    test_output: str = ""
    claude_output: str = ""
    retries: int = 0
    error_category: str = ""
    error_signature: str = ""


@dataclass
class RunSummary:
    """Summary of a full benchmark run."""
    total: int = 0
    passed: int = 0
    failed: int = 0
    score: float = 0.0
    results: list[TaskResult] = field(default_factory=list)
    error_categories: Counter = field(default_factory=Counter)
    retry_conversions: int = 0  # tasks that failed initially but passed after retry


# ── Error classification ────────────────────────────────────────────────────

ERROR_SIGNATURES = [
    ("missing_executable", re.compile(r"command not found|No such file or directory.*bin/|not found in PATH", re.I)),
    ("permission_denied", re.compile(r"Permission denied|Operation not permitted|EACCES", re.I)),
    ("service_not_running", re.compile(r"Connection refused|could not connect|Failed to connect|ECONNREFUSED", re.I)),
    ("config_syntax", re.compile(r"syntax error|parse error|invalid.*config|YAML.*error|JSON.*error|unexpected token", re.I)),
    ("missing_file", re.compile(r"No such file or directory|ENOENT|File not found", re.I)),
    ("wrong_output", re.compile(r"expected.*got|does not match|assertion.*failed|mismatch", re.I)),
    ("timeout", re.compile(r"timed? ?out|deadline exceeded|took too long", re.I)),
    ("build_failure", re.compile(r"undefined reference|cannot find -l|make.*Error|compilation failed|linking failed", re.I)),
    ("import_error", re.compile(r"ModuleNotFoundError|ImportError|No module named", re.I)),
    ("oom", re.compile(r"out of memory|OOM|Cannot allocate|MemoryError|SIGKILL", re.I)),
    ("port_in_use", re.compile(r"Address already in use|EADDRINUSE|bind.*failed", re.I)),
    ("wrong_path", re.compile(r"expected at .* but found at|wrong directory|incorrect path", re.I)),
]


def classify_error(output: str) -> tuple[str, str]:
    """Classify error output into category and signature."""
    for category, pattern in ERROR_SIGNATURES:
        match = pattern.search(output)
        if match:
            # Extract the matching line for the signature
            for line in output.split("\n"):
                if pattern.search(line):
                    return category, line.strip()[:200]
            return category, match.group(0)[:200]
    return "unknown", output.strip()[:200] if output.strip() else "no output"


def parse_results_dir(results_dir: Path) -> RunSummary:
    """Parse a Harbor results directory into a RunSummary."""
    summary = RunSummary()

    # Harbor stores results in per-task directories
    # Look for common patterns: results.json, test output, agent output
    results_file = results_dir / "results.json"
    if results_file.exists():
        return _parse_results_json(results_file)

    # Fallback: scan for individual task directories
    task_dirs = sorted(d for d in results_dir.iterdir() if d.is_dir())
    for task_dir in task_dirs:
        result = _parse_task_dir(task_dir)
        if result:
            summary.results.append(result)
            summary.total += 1
            if result.passed:
                summary.passed += 1
            else:
                summary.failed += 1
                summary.error_categories[result.error_category] += 1

    if summary.total > 0:
        summary.score = summary.passed / summary.total * 100

    return summary


def _parse_results_json(results_file: Path) -> RunSummary:
    """Parse Harbor's results.json format."""
    summary = RunSummary()

    with open(results_file) as f:
        data = json.load(f)

    # Handle both list and dict formats
    tasks = data if isinstance(data, list) else data.get("results", data.get("tasks", []))

    for task_data in tasks:
        task_id = task_data.get("task_id", task_data.get("id", "unknown"))
        passed = task_data.get("passed", task_data.get("success", False))
        test_output = task_data.get("test_output", task_data.get("output", ""))

        error_cat, error_sig = ("", "")
        if not passed:
            error_cat, error_sig = classify_error(test_output)

        result = TaskResult(
            task_id=task_id,
            passed=passed,
            test_output=test_output,
            error_category=error_cat,
            error_signature=error_sig,
        )
        summary.results.append(result)
        summary.total += 1
        if passed:
            summary.passed += 1
        else:
            summary.failed += 1
            summary.error_categories[error_cat] += 1

    if summary.total > 0:
        summary.score = summary.passed / summary.total * 100

    return summary


def _parse_task_dir(task_dir: Path) -> TaskResult | None:
    """Parse a single task directory."""
    task_id = task_dir.name

    # Check for test output
    test_output = ""
    for test_file in ["test-output.txt", "test-output-0.txt", "test_output.txt"]:
        path = task_dir / test_file
        if path.exists():
            test_output = path.read_text()
            break

    # Check for pass/fail
    passed = False
    rc_file = task_dir / "return-code.txt"
    if rc_file.exists():
        try:
            passed = int(rc_file.read_text().strip()) == 0
        except ValueError:
            pass

    # Check for retry conversions
    retries = 0
    for i in range(1, 4):
        if (task_dir / f"test-output-{i}.txt").exists():
            retries = i

    error_cat, error_sig = ("", "")
    if not passed and test_output:
        error_cat, error_sig = classify_error(test_output)

    return TaskResult(
        task_id=task_id,
        passed=passed,
        test_output=test_output,
        retries=retries,
        error_category=error_cat,
        error_signature=error_sig,
    )


def print_summary(summary: RunSummary) -> None:
    """Print a formatted summary of results."""
    print(f"\n{'='*60}")
    print(f"TERMINAL-BENCH RESULTS ANALYSIS")
    print(f"{'='*60}")
    print(f"\nScore: {summary.score:.1f}% ({summary.passed}/{summary.total})")
    print(f"Passed: {summary.passed}")
    print(f"Failed: {summary.failed}")

    if summary.error_categories:
        print(f"\n--- Error Categories ---")
        for cat, count in summary.error_categories.most_common():
            pct = count / summary.failed * 100 if summary.failed else 0
            print(f"  {cat:<25} {count:>3} ({pct:.0f}%)")

    # Show failed tasks
    failed = [r for r in summary.results if not r.passed]
    if failed:
        print(f"\n--- Failed Tasks ({len(failed)}) ---")
        for r in sorted(failed, key=lambda x: x.error_category):
            retry_info = f" [retried {r.retries}x]" if r.retries > 0 else ""
            print(f"  {r.task_id:<40} {r.error_category:<20}{retry_info}")
            if r.error_signature:
                print(f"    → {r.error_signature[:80]}")


def suggest_patterns(summary: RunSummary) -> None:
    """Suggest new patterns based on failure analysis."""
    print(f"\n{'='*60}")
    print(f"SUGGESTED PATTERNS")
    print(f"{'='*60}\n")

    # Group by error category
    for cat, count in summary.error_categories.most_common():
        if count < 2:
            continue  # Only suggest for recurring failures

        failures = [r for r in summary.results if not r.passed and r.error_category == cat]
        signatures = [r.error_signature for r in failures if r.error_signature]

        print(f"Category: {cat} ({count} failures)")
        print(f"  Tasks: {', '.join(r.task_id for r in failures[:5])}")
        if signatures:
            print(f"  Common signature: {signatures[0][:100]}")
        print(f"  Suggested pattern:")
        print(f'    {{')
        print(f'        "id": "{cat}-fix",')
        print(f'        "category": "{cat.replace("_", " ").title()}",')
        print(f'        "pattern": "TODO: Write pattern based on {count} failures in this category",')
        print(f'        "severity": "{"critical" if count >= 5 else "high" if count >= 3 else "medium"}",')
        print(f'    }}')
        print()


def compare_runs(summary1: RunSummary, summary2: RunSummary) -> None:
    """Compare two benchmark runs to show improvement."""
    print(f"\n{'='*60}")
    print(f"RUN COMPARISON")
    print(f"{'='*60}\n")

    print(f"Run 1: {summary1.score:.1f}% ({summary1.passed}/{summary1.total})")
    print(f"Run 2: {summary2.score:.1f}% ({summary2.passed}/{summary2.total})")
    diff = summary2.score - summary1.score
    print(f"Delta: {diff:+.1f}pp")

    # Find newly passed and newly failed
    results1 = {r.task_id: r.passed for r in summary1.results}
    results2 = {r.task_id: r.passed for r in summary2.results}

    newly_passed = [tid for tid in results2 if results2[tid] and not results1.get(tid, False)]
    newly_failed = [tid for tid in results2 if not results2[tid] and results1.get(tid, True)]

    if newly_passed:
        print(f"\nNewly PASSED ({len(newly_passed)}):")
        for tid in newly_passed:
            print(f"  + {tid}")

    if newly_failed:
        print(f"\nNewly FAILED ({len(newly_failed)}):")
        for tid in newly_failed:
            print(f"  - {tid}")


def main() -> None:
    parser = argparse.ArgumentParser(description="Analyze Terminal-Bench results")
    parser.add_argument("results_dir", type=Path, help="Harbor results directory")
    parser.add_argument("--suggest-patterns", action="store_true", help="Suggest new patterns from failures")
    parser.add_argument("--compare", type=Path, help="Compare with another results directory")
    parser.add_argument("--json", action="store_true", help="Output as JSON")
    args = parser.parse_args()

    if not args.results_dir.exists():
        print(f"Error: {args.results_dir} does not exist", file=sys.stderr)
        sys.exit(1)

    summary = parse_results_dir(args.results_dir)

    if args.json:
        output = {
            "score": summary.score,
            "total": summary.total,
            "passed": summary.passed,
            "failed": summary.failed,
            "error_categories": dict(summary.error_categories),
            "results": [
                {
                    "task_id": r.task_id,
                    "passed": r.passed,
                    "retries": r.retries,
                    "error_category": r.error_category,
                    "error_signature": r.error_signature,
                }
                for r in summary.results
            ],
        }
        print(json.dumps(output, indent=2))
        return

    print_summary(summary)

    if args.suggest_patterns:
        suggest_patterns(summary)

    if args.compare:
        summary2 = parse_results_dir(args.compare)
        compare_runs(summary, summary2)


if __name__ == "__main__":
    main()
