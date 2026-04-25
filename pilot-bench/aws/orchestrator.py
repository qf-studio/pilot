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
import os
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
        resume: bool = False,
        dry_run: bool = False,
        rerun_set: set[tuple[str, str]] | None = None,
    ):
        self.run_id = run_id
        self.model = model
        self.k_trials = k_trials
        self.max_parallel = max_parallel
        self.s3_bucket = s3_bucket
        self.region = region
        self.resume = resume
        self.dry_run = dry_run
        # (task, trial_id) tuples to force-dispatch even if reward.txt exists.
        # Used to re-run specific trials that need redoing (e.g., over-timeout).
        self.rerun_set: set[tuple[str, str]] = rerun_set or set()

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

            # Resume mode: identify already-completed trials from S3 and skip them.
            # A trial is "complete" iff s3://.../{task}/{trial}/reward.txt exists.
            # This filter is PURE ADDITION — without --resume the work queue is identical.
            skip_set: set[tuple[str, str]] = set()
            if self.resume:
                skip_set = self._scan_completed_trials(tasks)
                logger.info(
                    f"[resume] Found {len(skip_set)} completed trials in S3 — will skip"
                )
            # Force-rerun list takes priority over skip_set.
            # Trials in rerun_set are dispatched even if they have existing reward.txt;
            # new artifacts overwrite old ones; S3 versioning preserves history.
            if self.rerun_set:
                before = len(skip_set)
                skip_set -= self.rerun_set
                logger.info(
                    f"[rerun] Forcing re-dispatch of {len(self.rerun_set)} trials "
                    f"(skip_set trimmed from {before} to {len(skip_set)})"
                )

            self._print_banner(bench, total_trials)

            if self.dry_run:
                planned = []
                for task in tasks:
                    for k in range(1, self.k_trials + 1):
                        trial_id = f"trial-{k:03d}"
                        if (task, trial_id) not in skip_set:
                            planned.append(f"{task}/{trial_id}")
                logger.info(f"[dry-run] Would dispatch {len(planned)} trials "
                            f"(skipping {len(skip_set)}). No AWS mutations.")
                for p in planned[:20]:
                    logger.info(f"[dry-run]   {p}")
                if len(planned) > 20:
                    logger.info(f"[dry-run]   ... and {len(planned) - 20} more")
                return bench

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

            # 4. Build work queue (skip trials already complete in resume mode)
            work_queue: deque[tuple[str, str]] = deque()
            skipped = 0
            for task in tasks:
                for k in range(1, self.k_trials + 1):
                    trial_id = f"trial-{k:03d}"
                    if (task, trial_id) in skip_set:
                        skipped += 1
                        continue
                    work_queue.append((task, trial_id))

            if self.resume:
                logger.info(
                    f"Work queue: {len(work_queue)} trials "
                    f"(skipped {skipped} already-complete) across {len(tasks)} tasks"
                )
            else:
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

                    # Parse reward — first try S3 (authoritative), fallback to stdout
                    # SSM stdout truncates at 24KB which often cuts off the "Reward:" line
                    reward = self._read_reward_from_s3(task_name, trial_id)
                    if reward is None:
                        reward = self._parse_reward(result.get("stdout", ""))

                    # Also read trial_status from trial-meta.json — distinguishes
                    # genuine failures from infra issues (rate limit, pilot crash, no-op).
                    trial_status = self._read_trial_status_from_s3(task_name, trial_id)

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

                    # Mark non-real trials with a tag so the dashboard can separate them
                    status_tag = ""
                    if trial_status and trial_status != "real":
                        status_tag = f" [{trial_status}]"

                    logger.info(
                        f"[{completed + failed}/{total_trials}] "
                        f"{task_name}/{trial_id}: {status}{status_tag} "
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

        # Learning DB — Harbor bench compliance (H2): we DELIBERATELY do
        # NOT upload the seeded pilot.db. The run-bench-task.sh no longer
        # downloads it either, and starts each trial with an empty DB. The
        # inject_patterns:false config guarantees that even a (hypothetical)
        # populated DB would not reach the prompt. Belt-and-suspenders.
        # NOTE: do not re-add this upload without revisiting benchmark
        # integrity — the seeded DB contains TB2-targeted patterns.

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
        """Generate pilot config.yaml — mirrors agent.py:_build_config().

        v5: subscription-only (no api_base_url override → Claude Code uses
        api.anthropic.com via CLAUDE_CODE_OAUTH_TOKEN). Model routing ON so
        Pilot's complexity classifier downgrades easy tasks to Sonnet 4.6
        and reserves Opus 4.7 for medium/complex.
        """
        m = self.model
        # Optional explicit base-URL override only if env var is set; otherwise
        # omit the field so Claude Code uses its default endpoint.
        base_url = os.environ.get("ANTHROPIC_BASE_URL", "").strip()
        api_base_url_line = f'  api_base_url: "{base_url}"\n' if base_url else ""
        return f"""version: "1.0"
orchestrator:
  model: "{m}"
executor:
  type: "claude-code"
  default_model: "{m}"
{api_base_url_line}  claude_code:
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
  # v5: gates ON for Build-Verify-Fix retry loop (runner.go:2147+).
  enabled: true
memory:
  path: /root/.pilot/data
  learning:
    enabled: true
    # Harbor bench compliance (TB2 H2): disable prompt injection of learned
    # patterns + knowledge-graph nodes. Recording stays on; only the agent
    # prompt is kept pristine for "harness-only" leaderboard submissions.
    inject_patterns: false
"""

    def _dispatch_batch(self, work_queue: deque[tuple[str, str]]) -> int:
        """Dispatch tasks to idle instances. Returns number dispatched."""
        dispatched = 0
        while work_queue and self.pool.get_idle_count() > 0:
            # Enforce max_parallel at runtime — scale_up only caps at startup
            if self.ssm.active_count >= self.max_parallel:
                break
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

    def _scan_completed_trials(self, tasks: list[str]) -> set[tuple[str, str]]:
        """Return set of (task, trial_id) where reward.txt already exists in S3.

        Uses a single paginated list_objects_v2 per task prefix — cheap and fast.
        Never deletes, never overwrites — read-only scan.
        """
        done: set[tuple[str, str]] = set()
        paginator = self.s3.get_paginator("list_objects_v2")
        for task in tasks:
            prefix = f"{S3_RUNS_PREFIX}/{self.run_id}/{task}/"
            try:
                for page in paginator.paginate(Bucket=self.s3_bucket, Prefix=prefix):
                    for obj in page.get("Contents", []) or []:
                        key = obj["Key"]
                        if key.endswith("/reward.txt"):
                            # key format: .../{task}/{trial_id}/reward.txt
                            parts = key.rstrip("/").split("/")
                            if len(parts) >= 3:
                                trial_id = parts[-2]
                                done.add((task, trial_id))
            except Exception as e:  # pragma: no cover - defensive
                logger.warning(f"[resume] scan failed for {task}: {e}")
        return done

    def _read_reward_from_s3(self, task_name: str, trial_id: str) -> float | None:
        """Read authoritative reward from S3 (reward.txt written by task runner).

        SSM stdout is capped at 24KB and often truncates the final "Reward:" line,
        so we prefer reading the reward file directly from S3.
        Returns None if the file isn't available yet.
        """
        key = f"{S3_RUNS_PREFIX}/{self.run_id}/{task_name}/{trial_id}/reward.txt"
        try:
            resp = self.s3.get_object(Bucket=self.s3_bucket, Key=key)
            content = resp["Body"].read().decode("utf-8").strip()
            return float(content)
        except (self.s3.exceptions.NoSuchKey, ValueError, KeyError):
            return None
        except Exception as e:  # pragma: no cover - defensive
            logger.debug(f"Failed to read reward from S3 for {task_name}/{trial_id}: {e}")
            return None

    def _read_trial_status_from_s3(self, task_name: str, trial_id: str) -> str | None:
        """Read trial_status from trial-meta.json on S3.

        Returns one of: real, pilot_failed, rate_limited, no_op, no_tests — or None
        if the metadata file isn't available.
        """
        key = f"{S3_RUNS_PREFIX}/{self.run_id}/{task_name}/{trial_id}/trial-meta.json"
        try:
            resp = self.s3.get_object(Bucket=self.s3_bucket, Key=key)
            content = resp["Body"].read().decode("utf-8")
            meta = json.loads(content)
            return meta.get("trial_status")
        except (self.s3.exceptions.NoSuchKey, ValueError, KeyError):
            return None
        except Exception as e:  # pragma: no cover - defensive
            logger.debug(f"Failed to read trial_status from S3 for {task_name}/{trial_id}: {e}")
            return None

    def _parse_reward(self, stdout: str) -> float:
        """Extract reward from task runner stdout (fallback when S3 unavailable)."""
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
    parser.add_argument(
        "--resume",
        action="store_true",
        help="Skip trials that already have reward.txt in S3 for this run-id",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print what would be dispatched and exit; no AWS scale/dispatch/upload",
    )
    parser.add_argument(
        "--rerun-list",
        type=str,
        default=None,
        help="Path to file with 'task/trial-NNN' lines — these trials are force-redispatched "
             "even if reward.txt exists; new artifacts overwrite old (S3 versioning preserves history).",
    )
    args = parser.parse_args()

    task_names = None
    if args.tasks != "all":
        task_names = [t.strip() for t in args.tasks.split(",") if t.strip()]

    rerun_set: set[tuple[str, str]] = set()
    if args.rerun_list:
        with open(args.rerun_list) as f:
            for line in f:
                line = line.strip()
                if not line or "/" not in line:
                    continue
                task, trial = line.rsplit("/", 1)
                rerun_set.add((task, trial))
        logger.info(f"[rerun] Loaded {len(rerun_set)} trials from {args.rerun_list}")

    orchestrator = AWSBenchOrchestrator(
        run_id=args.run_id,
        model=args.model,
        k_trials=args.k_trials,
        max_parallel=args.max_parallel,
        s3_bucket=args.s3_bucket,
        region=args.region,
        resume=args.resume,
        dry_run=args.dry_run,
        rerun_set=rerun_set,
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
