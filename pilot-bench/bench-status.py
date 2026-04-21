#!/usr/bin/env python3
"""Bench status dashboard — pretty terminal output with CIs."""

import json
import glob
import math
import os
import re
import sys
from datetime import datetime
from pathlib import Path

# ── Config ──────────────────────────────────────────────────────────
JOB = os.environ.get("BENCH_JOB", "pilot-leaderboard-clean-v1")
JOBS_DIR = Path(__file__).parent / "jobs" / JOB
TOTAL_TASKS = 89
K = int(os.environ.get("BENCH_K", "5"))
TOTAL_TRIALS = int(os.environ.get("BENCH_TOTAL_TRIALS", str(TOTAL_TASKS * K)))
LEADERBOARD_TOP = 82.9  # Pilot's previous #1 submission — the score to beat
W = 66  # inner width (between ║ borders)

try:
    import unicodedata

    def _charwidth(c: str) -> int:
        eaw = unicodedata.east_asian_width(c)
        return 2 if eaw in ("F", "W") else 1
except ImportError:
    def _charwidth(c: str) -> int:
        return 1

# ── Colors ──────────────────────────────────────────────────────────
DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
BGREEN = "\033[1;32m"
RED = "\033[31m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
WHITE = "\033[97m"
BWHITE = "\033[1;97m"
R = "\033[0m"  # reset


def vlen(s: str) -> int:
    """Visible (terminal) length of string — ANSI-aware + East Asian width."""
    clean = re.sub(r"\033\[[0-9;]*m", "", s)
    return sum(_charwidth(c) for c in clean)


def pad(s: str, width: int) -> str:
    """Pad string to visible width with trailing spaces."""
    return s + " " * max(0, width - vlen(s))


def row(content: str):
    """Print a row padded to W inside box borders."""
    print(f"║ {pad(content, W)}║")


def empty():
    row("")


def hrule(char="═", left="╠", right="╣"):
    print(f"{left}{char * (W + 1)}{right}")


def wilson_ci(passed: int, total: int, z: float = 1.96) -> tuple[float, float]:
    """Wilson score interval for binomial proportion."""
    if total == 0:
        return 0.0, 1.0
    p = passed / total
    d = 1 + z * z / total
    c = (p + z * z / (2 * total)) / d
    e = z * math.sqrt((p * (1 - p) + z * z / (4 * total)) / total) / d
    return max(0.0, c - e), min(1.0, c + e)


def bar(done: int, total: int, width: int = 40) -> str:
    """Unicode progress bar."""
    if total == 0:
        return DIM + "░" * width + R
    filled = int(done / total * width)
    return GREEN + "█" * filled + R + DIM + "░" * (width - filled) + R


def sparkline(values: list[float], width: int = 32) -> str:
    """Unicode sparkline — always exactly `width` visible chars."""
    if not values:
        return DIM + "·" * width + R
    blocks = " ▁▂▃▄▅▆▇█"
    mn, mx = min(values), max(values)
    rng = mx - mn if mx > mn else 1
    bkt = max(1, len(values) // width)
    buckets = []
    for i in range(0, len(values), bkt):
        buckets.append(sum(values[i : i + bkt]) / len(values[i : i + bkt]))
    chars = []
    for v in buckets[:width]:
        idx = int((v - mn) / rng * (len(blocks) - 1))
        chars.append(blocks[idx])
    # Pad to exact width
    while len(chars) < width:
        chars.append(" ")
    return CYAN + "".join(chars) + R


def dots(trials: list[float], k: int = K) -> str:
    """Trial dots: * pass, x fail, - pending (ASCII safe)."""
    out = ""
    for i in range(k):
        if i < len(trials):
            out += BGREEN + "*" + R if trials[i] > 0 else RED + "x" + R
        else:
            out += DIM + "-" + R
    return out


def load_from_jobs(jobs_dir: Path) -> tuple[dict[str, list[float]], list[float]]:
    """Load results from pilot-bench/jobs/<JOB>/*/result.json (Modal format)."""
    results: dict[str, list[float]] = {}
    all_rewards: list[float] = []
    for f in glob.glob(str(jobs_dir / "*" / "result.json")):
        try:
            r = json.load(open(f))
        except (json.JSONDecodeError, OSError):
            continue
        task = r.get("task_name", "unknown")
        vr = r.get("verifier_result")
        reward = 0.0
        if vr and "rewards" in vr:
            reward = float(vr["rewards"].get("reward", 0))
        results.setdefault(task, []).append(reward)
        all_rewards.append(reward)
    return results, all_rewards


def load_from_log(log_path: str) -> tuple[dict[str, list[float]], list[float]]:
    """Load results from AWS orchestrator log file.

    Parses lines like:
      22:00:09 [INFO] [1/445] adaptive-rejection-sampler/trial-003: Success reward=0.0 duration=339s
    """
    results: dict[str, list[float]] = {}
    all_rewards: list[float] = []
    last_seen: dict[str, int] = {}  # task -> line number of last trial (for recency sort)
    pattern = re.compile(r"(\S+)/trial-\d+:.*reward=(\S+)")
    try:
        with open(log_path) as f:
            for lineno, line in enumerate(f):
                m = pattern.search(line)
                if m:
                    task = m.group(1)
                    reward = float(m.group(2))
                    results.setdefault(task, []).append(reward)
                    all_rewards.append(reward)
                    last_seen[task] = lineno
    except OSError:
        pass
    return results, all_rewards, last_seen


def parse_log_timing(log_path: str) -> tuple[str, str, str, str, float]:
    """Extract timing info from orchestrator log. Returns (started, elapsed, vel_str, eta, velocity)."""
    first_ts = last_ts = None
    n_results = 0
    ts_pattern = re.compile(r"^(\d+:\d+:\d+).*reward=")
    try:
        with open(log_path) as f:
            for line in f:
                m = ts_pattern.search(line)
                if m:
                    n_results += 1
                    ts_str = m.group(1)
                    if not first_ts:
                        first_ts = ts_str
                    last_ts = ts_str
    except OSError:
        pass

    if not first_ts:
        return "—", "—", "—", "—", 0.0

    # Anchor start date from file birth time (accurate for multi-day runs)
    try:
        st = os.stat(log_path)
        # macOS: st_birthtime, Linux: fall back to st_ctime
        bt = getattr(st, 'st_birthtime', st.st_ctime)
        file_btime = datetime.fromtimestamp(bt)
        start_date = file_btime.strftime("%Y-%m-%d")
    except OSError:
        start_date = datetime.now().strftime("%Y-%m-%d")
    t0 = datetime.strptime(f"{start_date} {first_ts}", "%Y-%m-%d %H:%M:%S")
    now = datetime.now()
    # If reconstructed t0 is still in the future, shift back one day
    if t0 > now:
        t0 -= __import__("datetime").timedelta(days=1)
    dt = (now - t0).total_seconds()
    h, mi = int(dt // 3600), int((dt % 3600) // 60)
    started = t0.strftime("%H:%M")
    elapsed = f"{h}h {mi:02d}m"
    velocity = n_results / (dt / 3600) if dt > 0 else 0.0
    vel_str = f"{velocity:.0f}/hr" if velocity > 0 else "—"
    left = TOTAL_TRIALS - n_results
    if velocity > 0:
        eta_h = left / velocity
        eta = f"~{eta_h:.0f}h" if eta_h >= 1 else f"~{eta_h * 60:.0f}m"
    else:
        eta = "—"
    return started, elapsed, vel_str, eta, velocity


def main():
    # ── Load ────────────────────────────────────────────────────────
    # Check for AWS orchestrator log (BENCH_LOG env or /tmp/bench-*.log)
    log_path = os.environ.get("BENCH_LOG", "")
    if not log_path:
        # Auto-detect latest bench log in /tmp
        bench_logs = sorted(glob.glob("/tmp/bench-*.log"), key=os.path.getmtime, reverse=True)
        if bench_logs:
            log_path = bench_logs[0]

    if log_path and os.path.exists(log_path):
        # AWS orchestrator mode
        results, all_rewards, last_seen = load_from_log(log_path)
        started, elapsed, vel_str, eta, velocity = parse_log_timing(log_path)
        source = f"aws:{os.path.basename(log_path)}"
    else:
        # Legacy Modal/jobs mode
        results, all_rewards = load_from_jobs(JOBS_DIR)
        last_seen = {}
        source = f"jobs:{JOB}"
        # Timing from file mtimes
        mtimes = []
        for f in glob.glob(str(JOBS_DIR / "*" / "result.json")):
            try:
                mtimes.append(os.path.getmtime(f))
            except OSError:
                pass
        now = datetime.now()
        started = elapsed = vel_str = eta = "—"
        velocity = 0.0
        if mtimes:
            t0 = datetime.fromtimestamp(min(mtimes))
            started = t0.strftime("%H:%M")
            dt = (now - t0).total_seconds()
            h, m = int(dt // 3600), int((dt % 3600) // 60)
            elapsed = f"{h}h {m:02d}m"
            if dt > 0:
                velocity = len(all_rewards) / (dt / 3600)
                vel_str = f"{velocity:.0f}/hr"
                left = TOTAL_TRIALS - len(all_rewards)
                if velocity > 0:
                    eta_h = left / velocity
                    eta = f"~{eta_h:.0f}h" if eta_h >= 1 else f"~{eta_h * 60:.0f}m"

    n_results = len(all_rewards)
    n_tasks = len(results)
    n_passed = sum(1 for rs in results.values() if max(rs) > 0)

    # ── Score & CI ──────────────────────────────────────────────────
    if n_tasks > 0:
        score = n_passed / n_tasks * 100
        ci_lo, ci_hi = wilson_ci(n_passed, n_tasks)
    else:
        score, ci_lo, ci_hi = 0, 0, 1

    # ── Render ──────────────────────────────────────────────────────
    print()
    print(f"╔{'═' * (W + 1)}╗")

    # Header
    row(f"{BWHITE}PILOT BENCH{R}  {DIM}·{R}  {WHITE}{source}{R}")
    row(f"{DIM}Started: {started}  ·  Elapsed: {elapsed}{R}")
    hrule()
    empty()

    # Progress
    pct = n_results / TOTAL_TRIALS * 100 if TOTAL_TRIALS else 0
    pb = bar(n_results, TOTAL_TRIALS, 42)
    row(f"  {pb}  {BWHITE}{n_results}{R}{DIM}/{TOTAL_TRIALS}{R}  ({pct:.1f}%)")
    empty()

    # ── Cards (fixed-width columns) ─────────────────────────────────
    cw = 15  # card width
    labels = ["SCORE", "95% CI", "INFRA", "VELOCITY"]
    values = [
        f"{score:.1f}%",
        f"{ci_lo*100:.1f}–{ci_hi*100:.1f}%",
        "***** 5/5",
        vel_str,
    ]
    subs = [
        f"{n_passed} / {n_tasks}",
        f"n={n_tasks}, k={K}",
        "t3.xlarge",
        f"ETA {eta}",
    ]

    def vcenter(text: str, w: int) -> str:
        """Center text accounting for visible width."""
        v = vlen(text)
        if v >= w:
            return text
        left = (w - v) // 2
        right = w - v - left
        return " " * left + text + " " * right

    # Labels row
    line = "  ".join(f"{DIM}{vcenter(l, cw)}{R}" for l in labels)
    row(line)
    # Values row
    line = "  ".join(f"{BWHITE}{vcenter(v, cw)}{R}" for v in values)
    row(line)
    # Subs row
    line = "  ".join(f"{DIM}{vcenter(s, cw)}{R}" for s in subs)
    row(line)
    empty()

    # ── Task table ──────────────────────────────────────────────────
    row(f"  {BWHITE}{'TASK':<26}{R} {DIM}TRIALS{R} {DIM}PASS{R}  {DIM}{'CI 95%':>14}{R}")
    row(f"  {DIM}{'─' * 56}{R}")

    # Sort: passed first (alphabetical), then failed (alphabetical)
    passed_tasks = sorted(
        [(t, rs) for t, rs in results.items() if max(rs) > 0],
        key=lambda x: x[0],
    )
    failed_tasks = sorted(
        [(t, rs) for t, rs in results.items() if max(rs) == 0],
        key=lambda x: x[0],
    )

    for task, rewards in passed_tasks:
        name = task[:24].ljust(24)
        n_pass = sum(1 for r in rewards if r > 0)
        n_tot = len(rewards)
        tci_lo, tci_hi = wilson_ci(n_pass, n_tot)
        d = dots(rewards, K)
        ci_s = f"[{tci_lo:.2f}, {tci_hi:.2f}]"
        ps = f"{n_pass}/{n_tot}"
        nc = f"{GREEN}{name}{R}"
        row(f"  {nc}  {d}  {ps:>3}   {DIM}{ci_s:>14}{R}")

    if passed_tasks and failed_tasks:
        row(f"  {DIM}{'─' * 56}{R}")

    for task, rewards in failed_tasks:
        name = task[:24].ljust(24)
        n_pass = sum(1 for r in rewards if r > 0)
        n_tot = len(rewards)
        tci_lo, tci_hi = wilson_ci(n_pass, n_tot)
        d = dots(rewards, K)
        ci_s = f"[{tci_lo:.2f}, {tci_hi:.2f}]"
        ps = f"{n_pass}/{n_tot}"
        nc = f"{RED}{name}{R}"
        row(f"  {nc}  {d}  {ps:>3}   {DIM}{ci_s:>14}{R}")

    pending = TOTAL_TASKS - n_tasks
    if pending > 0:
        if failed_tasks:
            row(f"  {DIM}{'─' * 56}{R}")
        row(f"  {DIM}  ○ {pending} tasks pending{R}")
    empty()

    # ── Sparkline ───────────────────────────────────────────────────
    sp_w = 36
    sp = sparkline(sorted(all_rewards), sp_w)
    row(f"  {sp}  {DIM}reward distribution{R}")
    empty()

    # ── Projection ──────────────────────────────────────────────────
    row(f"  {BWHITE}PROJECTION{R}")
    ci_range = f"{ci_lo*100:.1f}%–{ci_hi*100:.1f}%"
    row(f"  {DIM}Now:{R} {WHITE}{score:.1f}%{R}  {DIM}→{R}  {WHITE}{ci_range}{R}  {DIM}(n={n_tasks}/{TOTAL_TASKS}){R}")

    # Leaderboard comparison — bar is exactly lb_w visible chars
    lb_w = 36
    cur_fill = max(1, int(score / 100 * lb_w)) if score > 0 else 0
    top_fill = int(LEADERBOARD_TOP / 100 * lb_w)
    cur_fill = min(cur_fill, lb_w)
    top_fill = min(top_fill, lb_w)
    green_n = min(cur_fill, top_fill)
    over_n = max(0, cur_fill - top_fill)
    dim_n = max(0, top_fill - cur_fill)
    space_n = lb_w - green_n - over_n - dim_n
    lb = (GREEN + "█" * green_n + R +
          BGREEN + "█" * over_n + R +
          DIM + "░" * dim_n + R +
          " " * space_n)
    row(f"  {lb}  {DIM}vs top: {LEADERBOARD_TOP}%{R}")
    empty()

    print(f"╚{'═' * (W + 1)}╝")
    print()


if __name__ == "__main__":
    main()
