#!/usr/bin/env python3
"""GitLab CI YAML validity gate for docs/.gitlab-ci.yml.

Fails when any job's `script` (or `before_script`/`after_script`) list
contains a non-string item. A plain-scalar list entry with an unquoted
colon-space (e.g. `- curl --header "PRIVATE-TOKEN: ${TOKEN}" ...`) parses
as a nested YAML mapping instead of a string, and GitLab rejects the whole
pipeline config with:

  jobs:<job>:script config should be a string or a nested array of strings

This exact regression shipped in #5134 (GH-5203): the cleanup-registry
job's two curl lines broke every pilot-docs pipeline from 2026-08-22
19:56Z to 2026-08-24 16:44Z because docs/.gitlab-ci.yml is synced
verbatim to GitLab by sync-docs.yml on every release, and GitHub-side
sync-docs stayed green (validation only happens on the GitLab side).

Fix for a flagged line: wrap it as a `- |` literal block scalar instead
of a plain `- ...` scalar.
"""
import glob
import os
import sys

try:
    import yaml
except ImportError:  # pragma: no cover - exercised only when pyyaml is missing
    print("ERROR: pyyaml is required to run this check (pip install pyyaml)", file=sys.stderr)
    sys.exit(1)

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_PATH = os.path.join(REPO_ROOT, "docs", ".gitlab-ci.yml")
SCRIPT_KEYS = ("script", "before_script", "after_script")


def find_non_string_script_items(config):
    """Return a list of (job_name, key, item) tuples for non-string script entries.

    `config` is a parsed GitLab CI YAML document (top-level mapping of job
    names to job definitions, plus reserved keys like `stages`, `image`,
    hidden `.template` jobs, etc.). Any of those whose value is a dict and
    contains one of SCRIPT_KEYS is inspected.
    """
    violations = []
    if not isinstance(config, dict):
        return violations
    for job_name, job in config.items():
        if not isinstance(job, dict):
            continue
        for key in SCRIPT_KEYS:
            items = job.get(key)
            if not isinstance(items, list):
                continue
            for item in items:
                if not isinstance(item, str):
                    violations.append((job_name, key, item))
    return violations


def check_file(path):
    with open(path, "r", encoding="utf-8") as f:
        config = yaml.safe_load(f)
    return find_non_string_script_items(config)


def main():
    paths = sys.argv[1:] or [DEFAULT_PATH]
    # Support glob patterns for convenience, mirroring how check-graph.py is invoked.
    resolved = []
    for p in paths:
        matches = glob.glob(p)
        resolved.extend(matches if matches else [p])

    all_violations = []
    for path in resolved:
        violations = check_file(path)
        for job_name, key, item in violations:
            all_violations.append((path, job_name, key, item))

    if all_violations:
        print("GitLab CI YAML validity gate FAILED:", file=sys.stderr)
        for path, job_name, key, item in all_violations:
            print(
                f"  {path}: jobs.{job_name}.{key} has a non-string item "
                f"(parsed as {type(item).__name__}): {item!r}",
                file=sys.stderr,
            )
        print(
            "\nLikely cause: an unquoted 'Header: value' colon-space inside a "
            "plain '- ...' script line, which YAML parses as a nested mapping. "
            "Fix by converting that list item to a '- |' literal block scalar.",
            file=sys.stderr,
        )
        return 1

    print(f"OK: all script items are strings in {', '.join(resolved)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
