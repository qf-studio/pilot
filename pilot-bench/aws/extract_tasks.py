#!/usr/bin/env python3
"""Extract Terminal Bench 2.0 task definitions into a manifest for AWS execution.

Clones the terminal-bench repo, parses each task's metadata (task.toml),
and produces a tasks-manifest.json with everything needed to run tasks
on EC2 instances without Harbor.

Usage:
    python3 extract_tasks.py                    # Clone + extract
    python3 extract_tasks.py --repo-path /tmp/tb  # Use existing clone
    python3 extract_tasks.py --upload           # Extract + upload to S3
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

try:
    import tomllib
except ImportError:
    import tomli as tomllib  # Python < 3.11

from config import (
    S3_BUCKET,
    TASK_MANIFEST_S3_KEY,
    TB_DOCKER_TAG,
    TB_GIT_REF,
    TB_GIT_URL,
)

DEFAULT_CLONE_DIR = "/tmp/terminal-bench"


def clone_repo(dest: str, url: str = TB_GIT_URL, ref: str = TB_GIT_REF) -> Path:
    """Clone or update the terminal-bench repository."""
    dest_path = Path(dest)
    if dest_path.exists() and (dest_path / ".git").exists():
        print(f"Updating existing clone at {dest}...")
        subprocess.run(["git", "-C", dest, "fetch", "origin"], check=True)
        subprocess.run(["git", "-C", dest, "checkout", ref], check=True)
        subprocess.run(["git", "-C", dest, "pull", "--ff-only"], check=False)
    else:
        print(f"Cloning {url} to {dest}...")
        subprocess.run(
            ["git", "clone", "--depth", "1", "--branch", ref, url, dest],
            check=True,
        )
    return dest_path


def parse_task(task_dir: Path, default_tag: str = TB_DOCKER_TAG) -> dict | None:
    """Parse a single task directory into a manifest entry."""
    task_toml = task_dir / "task.toml"
    if not task_toml.exists():
        return None

    with open(task_toml, "rb") as f:
        meta = tomllib.load(f)

    task_name = task_dir.name

    # Read instruction
    instruction_file = task_dir / "instruction.md"
    instruction = ""
    if instruction_file.exists():
        instruction = instruction_file.read_text().strip()

    # Parse resource requirements from task.toml
    # Format varies — handle common structures
    resources = meta.get("resources", {})
    environment = meta.get("environment", {})
    agent = meta.get("agent", {})

    # Docker image: typically alexgshaw/<task-name>:<tag>
    docker_image = environment.get(
        "docker_image",
        meta.get("docker_image", f"alexgshaw/{task_name}:{default_tag}"),
    )

    # CPU/memory
    cpus = resources.get("cpus", environment.get("cpus", 1))
    memory_mb = resources.get("memory_mb", environment.get("memory_mb", 2048))

    # Timeouts
    agent_timeout = agent.get(
        "timeout_sec", meta.get("agent_timeout_sec", meta.get("timeout", 900))
    )
    verifier_timeout = meta.get("verifier_timeout_sec", 900)

    # Test files
    tests_dir = task_dir / "tests"
    test_files = []
    if tests_dir.is_dir():
        test_files = [f.name for f in sorted(tests_dir.iterdir()) if f.is_file()]

    # Check for setup script
    has_setup = (task_dir / "setup.sh").exists()

    # Check for solution directory
    has_solution_dir = (task_dir / "solution").is_dir()

    return {
        "task_name": task_name,
        "docker_image": docker_image,
        "instruction": instruction,
        "cpus": cpus,
        "memory_mb": memory_mb,
        "agent_timeout_sec": agent_timeout,
        "verifier_timeout_sec": verifier_timeout,
        "test_files": test_files,
        "has_setup": has_setup,
        "has_solution_dir": has_solution_dir,
    }


def extract_manifest(repo_path: Path) -> list[dict]:
    """Extract all task definitions from the repo into a manifest."""
    tasks = []

    # Terminal Bench tasks are typically in a tasks/ or benchmarks/ directory
    # Try common locations
    for candidate in ["tasks", "benchmarks", "."]:
        tasks_dir = repo_path / candidate
        if not tasks_dir.is_dir():
            continue

        for entry in sorted(tasks_dir.iterdir()):
            if not entry.is_dir():
                continue
            if entry.name.startswith("."):
                continue

            task = parse_task(entry)
            if task:
                tasks.append(task)

    if not tasks:
        # Fallback: scan all subdirs recursively for task.toml
        print("No tasks found in standard locations, scanning recursively...")
        for task_toml in sorted(repo_path.rglob("task.toml")):
            task = parse_task(task_toml.parent)
            if task:
                tasks.append(task)

    return tasks


def upload_to_s3(manifest_path: Path, bucket: str = S3_BUCKET, key: str = TASK_MANIFEST_S3_KEY) -> None:
    """Upload manifest to S3."""
    print(f"Uploading to s3://{bucket}/{key}...")
    subprocess.run(
        [
            "aws", "s3", "cp",
            str(manifest_path),
            f"s3://{bucket}/{key}",
            "--sse", "aws:kms",
        ],
        check=True,
    )
    print("Upload complete.")


def main():
    parser = argparse.ArgumentParser(description="Extract Terminal Bench task manifest")
    parser.add_argument(
        "--repo-path",
        default=DEFAULT_CLONE_DIR,
        help=f"Path to terminal-bench repo (default: {DEFAULT_CLONE_DIR})",
    )
    parser.add_argument(
        "--no-clone",
        action="store_true",
        help="Skip cloning, use existing repo at --repo-path",
    )
    parser.add_argument(
        "--output",
        default="tasks-manifest.json",
        help="Output manifest file path (default: tasks-manifest.json)",
    )
    parser.add_argument(
        "--upload",
        action="store_true",
        help="Upload manifest to S3 after generation",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        help="Overwrite existing manifest file (default when called from run-aws-bench.sh)",
    )
    args = parser.parse_args()

    output_path = Path(args.output)
    if output_path.exists() and not args.force:
        print(f"Manifest already exists at {output_path}. Use --force to overwrite.")
        return 0

    repo_path = Path(args.repo_path)

    if not args.no_clone:
        repo_path = clone_repo(args.repo_path)

    if not repo_path.exists():
        print(f"ERROR: Repo path {repo_path} does not exist", file=sys.stderr)
        sys.exit(1)

    print(f"Extracting tasks from {repo_path}...")
    tasks = extract_manifest(repo_path)

    if not tasks:
        print("WARNING: No tasks found!", file=sys.stderr)
        sys.exit(1)

    # Summary stats
    mem_buckets = {"<=2GB": 0, "4GB": 0, "8GB+": 0}
    for t in tasks:
        mb = t["memory_mb"]
        if mb <= 2048:
            mem_buckets["<=2GB"] += 1
        elif mb <= 4096:
            mem_buckets["4GB"] += 1
        else:
            mem_buckets["8GB+"] += 1

    manifest = {
        "version": "2.0",
        "source": TB_GIT_URL,
        "ref": TB_GIT_REF,
        "task_count": len(tasks),
        "tasks": tasks,
    }

    output_path = Path(args.output)
    output_path.write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"\nManifest written to {output_path}")
    print(f"  Tasks: {len(tasks)}")
    print(f"  Memory distribution: {mem_buckets}")

    if args.upload:
        upload_to_s3(output_path)

    return 0


if __name__ == "__main__":
    sys.exit(main() or 0)
