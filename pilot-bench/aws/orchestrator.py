#!/usr/bin/env python3
"""AWS Bench Orchestrator — run Terminal Bench 2.0 on EC2 warm pool instances.

Replaces Harbor/Daytona/Modal with direct AWS execution:
- Scales ASG warm pool instances
- Dispatches tasks via SSM RunCommand
- Collects results from S3
- Produces Harbor-compatible output for analyze-results.py

Usage:
    python3 orchestrator.py --run-id aws-v1 --tasks all --k-trials 1 --max-parallel 5
    python3 orchestrator.py --run-id aws-val --tasks chess-best-move,gcode-to-text
    python3 orchestrator.py --run-id aws-full --tasks all --k-trials 5 --max-parallel 10
"""

import argparse
import json
import logging
import sys
import time
from collections import deque
from dataclasses import dataclass, field
from pathlib import Path

import boto3

from config import (
    AWS_REGION,
    DEFAULT_K_TRIALS,
    DEFAULT_MAX_PARALLEL,
    DEFAULT_MODEL,
    PILOT_BINARY_S3_KEY,
    PILOT_CONFIG_S3_KEY,
    PILOT_DB_S3_KEY,
    S3_BUCKET,
    S3_RUNS_PREFIX,
    TASK_MANIFEST_S3_KEY,
    TASK_RUNNER_S3_KEY,
)
from instance_pool import InstancePool
from ssm_executor import SSMExecutor

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger(__name__)


@dataclass
class TrialResult:
    task_name: str
    trial_id: str
    status: str  # Success | Failed | TimedOut
    reward: float = 0.0
    duration_sec: float = 0.0
    instance_id: str = ""


@dataclass
class BenchRun:
    run_id: str
    model: str
    k_trials: int
    max_parallel: int
    task_names: list[str]
    started_at: float = 0.0
    results: list[TrialResult] = field(default_factory=list)


class AWSBenchOrchestrator:
    """Orchestrate Terminal Bench runs on AWS warm pool infrastructure."""

    def __init__(
        self,
        run_id: str,
        model: str = DEFAULT_MODEL,
        k_trials: int = DEFAULT_K_TRIALS,
        max_parallel: int = DEFAULT_MAX_PARALLEL,
        s3_bucket: str = S3_BUCKET,
        region: str = AWS_REGION,
    ):
        self.run_id = run_id
        self.model = model
        self.k_trials = k_trials
        self.max_parallel = max_parallel
        self.s3_bucket = s3_bucket
        self.region = region

        self.pool = InstancePool(region=region)
        self.ssm = SSMExecutor(region=region)
        self.s3 = boto3.client("s3", region_name=region)

        self.results_dir = Path(f"results/{run_id}")
        self.results_dir.mkdir(parents=True, exist_ok=True)

    def run(self, task_names: list[str] | None = None) -> BenchRun:
        """Execute a full benchmark run.

        Args:
            task_names: Specific tasks to run, or None for all tasks.

        Returns:
            BenchRun with all trial results.
        """
        bench = BenchRun(
            run_id=self.run_id,
            model=self.model,
            k_trials=self.k_trials,
            max_parallel=self.max_parallel,
            task_names=task_names or [],
            started_at=time.time(),
        )

        try:
            # 1. Load task manifest
            manifest = self._load_manifest()
            all_tasks = [t["task_name"] for t in manifest["tasks"]]

            if task_names:
                tasks = [t for t in task_names if t in all_tasks]
                missing = set(task_names) - set(all_tasks)
                if missing:
                    logger.warning(f"Tasks not in manifest (skipping): {missing}")
            else:
                tasks = all_tasks

            bench.task_names = tasks
            total_trials = len(tasks) * self.k_trials

            self._print_banner(bench, total_trials)

            # 2. Upload assets to S3
            self._upload_assets()

            # 3. Scale up instances
            n_instances = min(self.max_parallel, total_trials)
            self.pool.scale_up(n_instances)
            instances = self.pool.wait_for_instances(n_instances)
            ssm_ready = self.pool.wait_for_ssm(instances)

            if not ssm_ready:
                raise RuntimeError("No instances with SSM available")

            logger.info(f"{len(ssm_ready)} instances ready for execution")

            # 4. Build work queue
            work_queue: deque[tuple[str, str]] = deque()
            for task in tasks:
                for k in range(1, self.k_trials + 1):
                    trial_id = f"trial-{k:03d}"
                    work_queue.append((task, trial_id))

            logger.info(f"Work queue: {len(work_queue)} trials across {len(tasks)} tasks")

            # 5. Dispatch and poll
            completed = 0
            failed = 0

            # Initial dispatch — fill all idle instances
            self._dispatch_batch(work_queue)

            # Poll loop
            while self.ssm.active_count > 0 or work_queue:
                time.sleep(10)

                # Check for completed commands
                done = self.ssm.poll_all_active()
                for result in done:
                    instance_id = result.get("instance_id", "")
                    task_name = result.get("task_name", "")
                    trial_id = result.get("trial_id", "")
                    status = result.get("status", "Failed")
                    duration = result.get("duration_sec", 0)

                    # Parse reward from SSM output
                    reward = self._parse_reward(result.get("stdout", ""))

                    trial = TrialResult(
                        task_name=task_name,
                        trial_id=trial_id,
                        status=status,
                        reward=reward,
                        duration_sec=duration,
                        instance_id=instance_id,
                    )
                    bench.results.append(trial)

                    if status == "Success":
                        completed += 1
                    else:
                        failed += 1

                    self.pool.release_instance(instance_id)

                    logger.info(
                        f"[{completed + failed}/{total_trials}] "
                        f"{task_name}/{trial_id}: {status} "
                        f"reward={reward} duration={int(duration)}s"
                    )

                # Dispatch more work to freed instances
                self._dispatch_batch(work_queue)

            # 6. Collect results from S3
            self._collect_results()

            # 7. Print summary
            self._print_summary(bench, total_trials)

        except KeyboardInterrupt:
            logger.warning("Interrupted! Scaling down...")
        except Exception as e:
            logger.error(f"Run failed: {e}")
            raise
        finally:
            # Always scale down
            try:
                self.pool.scale_down()
            except Exception as e:
                logger.error(f"Scale-down failed: {e}")

        return bench

    def _load_manifest(self) -> dict:
        """Download and parse task manifest from S3."""
        local_path = self.results_dir / "tasks-manifest.json"

        # Try local first
        if local_path.exists():
            return json.loads(local_path.read_text())

        # Download from S3
        logger.info("Downloading task manifest from S3...")
        self.s3.download_file(
            self.s3_bucket,
            TASK_MANIFEST_S3_KEY,
            str(local_path),
        )
        return json.loads(local_path.read_text())

    def _upload_assets(self) -> None:
        """Upload pilot binary, config, learning DB, and task runner to S3."""
        logger.info("Uploading assets to S3...")

        assets = {
            TASK_RUNNER_S3_KEY: self._find_asset("run-bench-task.sh"),
        }

        # Pilot binary
        bench_dir = Path(__file__).parent.parent
        binary_gz = bench_dir / "bin" / "pilot-linux-amd64.gz"
        binary_raw = bench_dir / "bin" / "pilot-linux-amd64"
        if binary_gz.exists():
            assets[PILOT_BINARY_S3_KEY] = str(binary_gz)
        elif binary_raw.exists():
            # Compress on the fly
            import gzip
            import shutil
            gz_tmp = binary_raw.with_suffix(".gz")
            with open(binary_raw, "rb") as f_in, gzip.open(gz_tmp, "wb") as f_out:
                shutil.copyfileobj(f_in, f_out)
            assets[PILOT_BINARY_S3_KEY] = str(gz_tmp)
        else:
            logger.warning(
                "Pilot binary not found. Run 'make bench-binary' first. "
                "Skipping binary upload — instance must have pilot pre-installed."
            )

        # Generate pilot config for this model and upload
        config_path = self.results_dir / "pilot-config.yaml"
        config_path.write_text(self._generate_pilot_config())
        assets[PILOT_CONFIG_S3_KEY] = str(config_path)

        # Learning DB
        db_path = bench_dir / "pilot_agent" / "data" / "pilot.db"
        if db_path.exists():
            assets[PILOT_DB_S3_KEY] = str(db_path)

        # Task manifest
        manifest_local = self.results_dir / "tasks-manifest.json"
        if manifest_local.exists():
            assets[TASK_MANIFEST_S3_KEY] = str(manifest_local)

        for s3_key, local_path in assets.items():
            if local_path and Path(local_path).exists():
                logger.info(f"  Uploading {Path(local_path).name} -> s3://{self.s3_bucket}/{s3_key}")
                self.s3.upload_file(
                    str(local_path),
                    self.s3_bucket,
                    s3_key,
                    ExtraArgs={"ServerSideEncryption": "aws:kms"},
                )

    def _find_asset(self, name: str) -> str | None:
        """Find an asset file in the aws/ directory."""
        path = Path(__file__).parent / name
        if path.exists():
            return str(path)
        return None

    def _generate_pilot_config(self) -> str:
        """Generate pilot config.yaml — mirrors agent.py:_build_config()."""
        m = self.model
        return f"""version: "1.0"
orchestrator:
  model: "{m}"
executor:
  type: "claude-code"
  claude_code:
    command: claude
    use_structured_output: true
    use_session_resume: true
    use_from_pr: false
  hooks:
    enabled: true
    run_tests_on_stop: true
    block_destructive: true
    lint_on_save: false
  heartbeat_timeout: 15m
  model_routing:
    enabled: true
    trivial: "claude-haiku-4-5-20251001"
    simple: "claude-sonnet-4-6"
    medium: "{m}"
    complex: "{m}"
  timeout:
    default: 30m
    trivial: 15m
    simple: 25m
    medium: 30m
    complex: 60m
  effort_routing:
    enabled: true
    trivial: low
    simple: medium
    medium: high
    complex: high
  effort_classifier:
    enabled: true
    model: claude-haiku-4-5-20251001
    timeout: 30s
  intent_judge:
    enabled: false
  retry:
    enabled: true
    rate_limit:
      max_attempts: 3
      initial_backoff: 30s
      backoff_multiplier: 2
    api_error:
      max_attempts: 3
      initial_backoff: 5s
      backoff_multiplier: 2
    timeout:
      max_attempts: 2
      initial_backoff: 0s
      backoff_multiplier: 0
      extend_timeout: true
      timeout_multiplier: 1.5
quality:
  enabled: true
  gates:
    - name: test
      type: test
      command: "export PATH=/root/.local/bin:/usr/local/bin:$PATH; if [ -f /tests/test_outputs.py ]; then cd /app && uvx -p 3.13 -w pytest==8.4.1 pytest /tests/test_outputs.py -rA 2>&1; fi"
      required: true
      timeout: 5m
      max_retries: 2
      retry_delay: 5s
      failure_hint: "Tests failed. Read /tests/test_outputs.py to understand what is expected, then fix your implementation."
memory:
  path: /root/.pilot/data
  learning:
    enabled: true
"""

    def _dispatch_batch(self, work_queue: deque[tuple[str, str]]) -> int:
        """Dispatch tasks to idle instances. Returns number dispatched."""
        dispatched = 0
        while work_queue and self.pool.get_idle_count() > 0:
            instance_id = self.pool.acquire_instance()
            if not instance_id:
                break

            task_name, trial_id = work_queue.popleft()
            self.ssm.send_task(
                instance_id=instance_id,
                task_name=task_name,
                trial_id=trial_id,
                run_id=self.run_id,
                model=self.model,
                s3_bucket=self.s3_bucket,
            )
            dispatched += 1

        return dispatched

    def _parse_reward(self, stdout: str) -> float:
        """Extract reward from task runner stdout."""
        for line in reversed(stdout.splitlines()):
            line = line.strip()
            if line.startswith("Reward:"):
                try:
                    return float(line.split(":", 1)[1].strip())
                except ValueError:
                    pass
        return 0.0

    def _collect_results(self) -> None:
        """Download all results from S3 to local results directory."""
        logger.info("Collecting results from S3...")
        s3_prefix = f"{S3_RUNS_PREFIX}/{self.run_id}/"

        paginator = self.s3.get_paginator("list_objects_v2")
        for page in paginator.paginate(Bucket=self.s3_bucket, Prefix=s3_prefix):
            for obj in page.get("Contents", []):
                key = obj["Key"]
                # Strip prefix to get relative path
                rel_path = key[len(s3_prefix):]
                local_path = self.results_dir / rel_path
                local_path.parent.mkdir(parents=True, exist_ok=True)

                self.s3.download_file(self.s3_bucket, key, str(local_path))

        logger.info(f"Results downloaded to {self.results_dir}")

    def _print_banner(self, bench: BenchRun, total_trials: int) -> None:
        print()
        print("=" * 60)
        print("  AWS BENCH RUNNER — Terminal Bench 2.0")
        print("=" * 60)
        print(f"  Run ID:      {bench.run_id}")
        print(f"  Model:       {bench.model}")
        print(f"  Tasks:       {len(bench.task_names)}")
        print(f"  Trials/task: {bench.k_trials}")
        print(f"  Total:       {total_trials} trials")
        print(f"  Parallel:    {bench.max_parallel} instances")
        print(f"  Region:      {self.region}")
        print(f"  S3 bucket:   {self.s3_bucket}")
        print("=" * 60)
        print()

    def _print_summary(self, bench: BenchRun, total_trials: int) -> None:
        elapsed = time.time() - bench.started_at
        passed = sum(1 for r in bench.results if r.reward >= 1.0)
        failed = sum(1 for r in bench.results if r.reward < 1.0)
        score = passed / len(bench.results) * 100 if bench.results else 0

        print()
        print("=" * 60)
        print("  RESULTS SUMMARY")
        print("=" * 60)
        print(f"  Run ID:     {bench.run_id}")
        print(f"  Completed:  {len(bench.results)}/{total_trials} trials")
        print(f"  Passed:     {passed}")
        print(f"  Failed:     {failed}")
        print(f"  Score:      {score:.1f}%")
        print(f"  Duration:   {elapsed / 60:.1f} minutes")
        print(f"  Results:    {self.results_dir}")
        print("=" * 60)
        print()

        # Write summary JSON
        summary = {
            "run_id": bench.run_id,
            "model": bench.model,
            "k_trials": bench.k_trials,
            "task_count": len(bench.task_names),
            "total_trials": total_trials,
            "completed": len(bench.results),
            "passed": passed,
            "failed": failed,
            "score_pct": round(score, 1),
            "duration_sec": round(elapsed),
            "results": [
                {
                    "task": r.task_name,
                    "trial": r.trial_id,
                    "status": r.status,
                    "reward": r.reward,
                    "duration_sec": round(r.duration_sec),
                }
                for r in bench.results
            ],
        }
        summary_path = self.results_dir / "summary.json"
        summary_path.write_text(json.dumps(summary, indent=2) + "\n")
        logger.info(f"Summary written to {summary_path}")


def main():
    parser = argparse.ArgumentParser(
        description="AWS Bench Orchestrator — Terminal Bench 2.0 on EC2 warm pool"
    )
    parser.add_argument(
        "--run-id",
        required=True,
        help="Unique run identifier (e.g., aws-v1-20260331)",
    )
    parser.add_argument(
        "--tasks",
        default="all",
        help="Comma-separated task names, or 'all' (default: all)",
    )
    parser.add_argument(
        "--k-trials",
        type=int,
        default=DEFAULT_K_TRIALS,
        help=f"Number of trials per task (default: {DEFAULT_K_TRIALS})",
    )
    parser.add_argument(
        "--max-parallel",
        type=int,
        default=DEFAULT_MAX_PARALLEL,
        help=f"Max parallel instances (default: {DEFAULT_MAX_PARALLEL})",
    )
    parser.add_argument(
        "--model",
        default=DEFAULT_MODEL,
        help=f"Model to use (default: {DEFAULT_MODEL})",
    )
    parser.add_argument(
        "--s3-bucket",
        default=S3_BUCKET,
        help=f"S3 bucket (default: {S3_BUCKET})",
    )
    parser.add_argument(
        "--region",
        default=AWS_REGION,
        help=f"AWS region (default: {AWS_REGION})",
    )
    args = parser.parse_args()

    task_names = None
    if args.tasks != "all":
        task_names = [t.strip() for t in args.tasks.split(",") if t.strip()]

    orchestrator = AWSBenchOrchestrator(
        run_id=args.run_id,
        model=args.model,
        k_trials=args.k_trials,
        max_parallel=args.max_parallel,
        s3_bucket=args.s3_bucket,
        region=args.region,
    )

    bench = orchestrator.run(task_names=task_names)

    # Exit with non-zero if score < 50%
    if bench.results:
        score = sum(1 for r in bench.results if r.reward >= 1.0) / len(bench.results)
        sys.exit(0 if score >= 0.5 else 1)
    else:
        sys.exit(1)


if __name__ == "__main__":
    main()
