#!/usr/bin/env python3
"""Table-driven tests for scripts/check-gitlab-ci-yaml.py."""
import importlib.util
import os
import tempfile
import unittest

SCRIPT_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "check-gitlab-ci-yaml.py")
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REAL_GITLAB_CI = os.path.join(REPO_ROOT, "docs", ".gitlab-ci.yml")


def _load_module():
    spec = importlib.util.spec_from_file_location("check_gitlab_ci_yaml", SCRIPT_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


cgy = _load_module()


class FindNonStringScriptItemsTest(unittest.TestCase):
    def test_all_string_script_items_pass(self):
        config = {
            "build": {
                "script": [
                    "echo hi",
                    "docker build .",
                ]
            }
        }
        self.assertEqual(cgy.find_non_string_script_items(config), [])

    def test_colon_space_regression_is_flagged(self):
        # Reproduces #5134 / GH-5203: an unquoted 'PRIVATE-TOKEN: ${TOKEN}'
        # inside a plain scalar list item parses as a nested dict.
        config = {
            "cleanup-registry": {
                "script": [
                    {"curl -sS --header \"PRIVATE-TOKEN\"": "${CLEANUP_TOKEN}"},
                ]
            }
        }
        violations = cgy.find_non_string_script_items(config)
        self.assertEqual(len(violations), 1)
        job_name, key, item = violations[0]
        self.assertEqual(job_name, "cleanup-registry")
        self.assertEqual(key, "script")
        self.assertIsInstance(item, dict)

    def test_before_and_after_script_are_checked(self):
        config = {
            "deploy": {
                "before_script": ["echo before"],
                "script": ["echo main"],
                "after_script": [{"bad": "entry"}],
            }
        }
        violations = cgy.find_non_string_script_items(config)
        self.assertEqual([v[1] for v in violations], ["after_script"])

    def test_non_dict_jobs_are_ignored(self):
        config = {"stages": ["build", "deploy"], "image": "docker:latest"}
        self.assertEqual(cgy.find_non_string_script_items(config), [])

    def test_job_without_script_key_is_ignored(self):
        config = {"deploy": {"stage": "deploy-prod"}}
        self.assertEqual(cgy.find_non_string_script_items(config), [])

    def test_literal_block_scalars_pass(self):
        # A YAML '- |' literal block always parses to a single string, even
        # if its content contains colon-space sequences.
        config = {
            "cleanup-registry": {
                "script": [
                    'curl -sS --header "PRIVATE-TOKEN: ${CLEANUP_TOKEN}" "$URL"\n',
                ]
            }
        }
        self.assertEqual(cgy.find_non_string_script_items(config), [])


class CheckFileTest(unittest.TestCase):
    def test_flags_colon_space_plain_scalar(self):
        content = (
            "bad-job:\n"
            "  script:\n"
            "    - curl --header \"PRIVATE-TOKEN: ${TOKEN}\" \"$URL\"\n"
        )
        with tempfile.NamedTemporaryFile("w", suffix=".yml", delete=False) as f:
            f.write(content)
            path = f.name
        try:
            violations = cgy.check_file(path)
            self.assertEqual(len(violations), 1)
        finally:
            os.unlink(path)

    def test_real_docs_gitlab_ci_file_is_clean(self):
        # Regression guard: the actual synced file must never regress to the
        # #5134 / GH-5203 shape again.
        self.assertTrue(os.path.exists(REAL_GITLAB_CI), REAL_GITLAB_CI)
        violations = cgy.check_file(REAL_GITLAB_CI)
        self.assertEqual(violations, [])


class MainTest(unittest.TestCase):
    def test_main_returns_zero_on_real_file(self):
        self.assertEqual(_run_main(), 0)


def _run_main():
    import sys

    argv = sys.argv
    sys.argv = ["check-gitlab-ci-yaml.py", REAL_GITLAB_CI]
    try:
        return cgy.main()
    finally:
        sys.argv = argv


if __name__ == "__main__":
    unittest.main()
