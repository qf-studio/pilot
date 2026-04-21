#!/usr/bin/env python3
"""Rebuild bench orchestrator log from S3 trial metadata.

Use when the on-disk /tmp/bench-*.log was truncated or lost but S3 still
has the authoritative trial-meta.json + reward.txt for each trial.

Output format matches what bench-status.py expects:
  HH:MM:SS [INFO] [N/M] task_name/trial-XXX: Success reward=R.R duration=Ss [status_tag]

Usage:
  AWS_PROFILE=quantflow python3 rebuild-log-from-s3.py \\
      --run-id glm-leaderboard-v2 \\
      --out /tmp/bench-glm-leaderboard-v2-aggregated.log \\
      [--live-log /tmp/bench-glm-leaderboard-v2.log]

If --live-log is given, its reward= lines are appended after the S3
historical lines so bench-status.py sees one continuous stream.
"""

import argparse
import json
import os
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime

import boto3

BUCKET = "pilot-s3-agent-data"
PREFIX = "bench/runs"
REGION = "eu-central-1"


def list_trials(s3, run_id: str) -> list[tuple[str, str]]:
    """Return [(task, trial_id), ...] for every trial with trial-meta.json."""
    paginator = s3.get_paginator("list_objects_v2")
    trials: list[tuple[str, str]] = []
    for page in paginator.paginate(Bucket=BUCKET, Prefix=f"{PREFIX}/{run_id}/"):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            if not key.endswith("/trial-meta.json"):
                continue
            parts = key.split("/")
            # bench/runs/<run>/<task>/<trial>/trial-meta.json
            if len(parts) < 6:
                continue
            task, trial = parts[-3], parts[-2]
            trials.append((task, trial))
    return trials


def fetch_meta(s3, run_id: str, task: str, trial: str) -> dict | None:
    key = f"{PREFIX}/{run_id}/{task}/{trial}/trial-meta.json"
    try:
        resp = s3.get_object(Bucket=BUCKET, Key=key)
        return json.loads(resp["Body"].read().decode("utf-8"))
    except Exception as e:  # noqa: BLE001
        print(f"[warn] {task}/{trial}: {e}", file=sys.stderr)
        return None


def format_line(idx: int, total: int, meta: dict) -> str:
    task = meta.get("task_name", "unknown")
    trial = meta.get("trial_id", "trial-???")
    reward = meta.get("reward", 0.0)
    duration = int(meta.get("duration_sec", 0))
    completed_at = meta.get("completed_at", "")
    status = "Success" if meta.get("pilot_exit", 1) == 0 else "Failed"
    trial_status = meta.get("trial_status", "real")

    # Parse ISO timestamp → HH:MM:SS
    try:
        dt = datetime.strptime(completed_at, "%Y-%m-%dT%H:%M:%SZ")
        hhmmss = dt.strftime("%H:%M:%S")
    except (ValueError, TypeError):
        hhmmss = "00:00:00"

    tag = f" [{trial_status}]" if trial_status and trial_status != "real" else ""
    return (
        f"{hhmmss} [INFO] [{idx}/{total}] {task}/{trial}: "
        f"{status} reward={reward} duration={duration}s{tag}"
    )


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--live-log", default=None,
                    help="Append reward= lines from this running log after S3 lines")
    ap.add_argument("--workers", type=int, default=32)
    args = ap.parse_args()

    session = boto3.Session(
        profile_name=os.environ.get("AWS_PROFILE", "quantflow"),
        region_name=REGION,
    )
    s3 = session.client("s3")

    print(f"[info] Listing trials for run-id={args.run_id}...", file=sys.stderr)
    trials = list_trials(s3, args.run_id)
    print(f"[info] Found {len(trials)} completed trials in S3", file=sys.stderr)

    # Fetch metas in parallel
    metas: list[dict] = []
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {ex.submit(fetch_meta, s3, args.run_id, t, tr): (t, tr) for t, tr in trials}
        for fut in as_completed(futs):
            m = fut.result()
            if m:
                metas.append(m)

    # Sort by completed_at
    def sort_key(m: dict) -> str:
        return m.get("completed_at", "")
    metas.sort(key=sort_key)

    total = len(metas)
    with open(args.out, "w") as f:
        # Header so bench-status.py recognizes it as an AWS orchestrator log
        f.write(f"# Rebuilt from S3 {datetime.now().isoformat()} — run_id={args.run_id}\n")
        for idx, m in enumerate(metas, 1):
            f.write(format_line(idx, total, m) + "\n")

        # Append fresh reward lines from live log if provided
        if args.live_log and os.path.exists(args.live_log):
            with open(args.live_log) as lf:
                fresh = 0
                live_idx = total
                for line in lf:
                    if "reward=" in line and "/trial-" in line:
                        live_idx += 1
                        f.write(line)
                        fresh += 1
                print(f"[info] Appended {fresh} live-log reward lines", file=sys.stderr)

    print(f"[info] Wrote {total} S3 trials to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
