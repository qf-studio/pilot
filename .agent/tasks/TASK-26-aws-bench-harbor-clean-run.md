# TASK-26: AWS Bench Harbor-Compliant Clean Run

**Status**: 🚧 In Progress (clean run `glm-leaderboard-v3` launched 2026-04-22 17:00)
**Created**: 2026-04-22
**Completed**: —

---

## What Was Built

Refactored the AWS bench pipeline (`pilot-bench/aws/`) to produce Harbor
Terminal-Bench 2.0 leaderboard-compliant data end-to-end: resumable
orchestrator, per-task timeout enforcement, broadened oracle-file guard,
credential-leak fix, and WORKDIR compatibility for heterogeneous task
images. Discovered during post-run audit that the initial `glm-leaderboard-v2`
run violated the `timeout_multiplier=1.0` rule for 60/445 trials (13.5%);
attempted a selective rerun, then restarted as a single clean batch
(`glm-leaderboard-v3`) to eliminate batch-mixing concerns.

---

## Implementation

### Phase 1: Resume & dry-run flags
**Completed**: 2026-04-21

Added `--resume` and `--dry-run` to `orchestrator.py`. The orchestrator
originally had no recovery path — a killed run meant re-running everything
or overwriting existing results.

- `--resume`: reads S3 for existing `reward.txt` keys and skips completed
  trials. Pure additive filter; without the flag, behavior is identical.
- `--dry-run`: prints the dispatch plan and exits — no AWS mutations, used
  to confirm counts before committing compute.
- `_scan_completed_trials()`: paginated `list_objects_v2` per task prefix,
  read-only, never deletes or overwrites.

Verified on `glm-leaderboard-v2`: resumed 142 missing trials without
overwriting the 303 existing ones.

### Phase 2: Force-rerun via `--rerun-list`
**Completed**: 2026-04-22

IAM blocks `s3:DeleteObject` on `pilot-s3-agent-data` (good safety net).
Added `--rerun-list FILE` flag: reads `task/trial-NNN` lines, removes them
from the resume skip-set so they get re-dispatched. New artifacts overwrite
old via existing `PutObject` perms; S3 bucket versioning preserves prior
reward values for audit.

### Phase 3: Per-task timeout enforcement (Harbor compliance)
**Completed**: 2026-04-22

Post-run audit found 60 trials exceeded their manifest `agent_timeout_sec`.
Root cause: `run-bench-task.sh` used a global `MAIN_TIMEOUT=5400s` wrapper,
while Pilot's internal `effort_routing` pushed many tasks to `complex: 60m`,
ignoring the per-task cap.

**Fix** (`run-bench-task.sh`):
```bash
# Read agent_timeout_sec from manifest alongside cpus/memory
print(f'TASK_AGENT_TIMEOUT={int(t.get("agent_timeout_sec", 900))}')

# Use per-task timeout as wrapper, not global MAIN_TIMEOUT
timeout "${TASK_AGENT_TIMEOUT:-${MAIN_TIMEOUT}}" pilot task ...
```

Result: every trial now hard-capped at its manifest-declared timeout.

### Phase 4: `/app` symlink fallback for non-standard WORKDIRs
**Completed**: 2026-04-21

Downstream code (oracle removal, git init, pilot exec, verifier) assumes
`/app` as the task directory. The `prove-plus-comm` task image uses
`/workspace` — all 5 trials failed at the env-bootstrap step because
`/app/.pilot-env-context.txt` couldn't be written.

**Fix** (`run-bench-task.sh`, inside container, before agent phase):
```bash
for cand in /workspace /home/user /home/agent /srv; do
    if [ -d "$cand" ]; then
        ln -s "$cand" /app
        break
    fi
done
[ -e /app ] || mkdir -p /app
```

Harbor-compliant: oracle-file removal and canary-grep still operate on
the real workdir contents through the symlink. Verified: prove-plus-comm
went from 0/5 infra failures to 5/5 passes.

### Phase 5: Broadened oracle-file guard (H1 hardening)
**Completed**: 2026-04-22

Pre-rerun hardening. Original guard scanned only `/app/`. Expanded to:
- Base paths: `/app /workspace /tests /home/user /home/agent /srv /root`
- Targets: `test_outputs.py`, `test.sh`, `conftest.py`, `pytest.ini`, `tests/`
- Canary-grep scope: `/app /workspace /tests /home /srv /opt`
  (scoped — no system-wide grep that could delete system files)
- Audit log: every removal prefixed `[oracle-guard]` so reviewers see the
  containment actions in `pilot-stdout.log`.

Tests are still only copied IN via `docker cp` AFTER agent exits
(line 567, unchanged). Isolation preserved.

### Phase 6: Credential leak fix
**Completed**: 2026-04-22

`run-bench-task.sh` used `tee /tmp/ssm-auth-debug.log` when loading the
Z.AI `ANTHROPIC_AUTH_TOKEN` from SSM — wrote the decrypted token to disk
on the warm-pool host where it persisted across trials.

**Fix**: removed the `tee`, added `rm -f /tmp/ssm-auth-debug.log` at run
start to scrub any stale file. Audit of 445 existing traces confirmed
zero real token values present (151 log-line matches were all variable-
name references like `auth_source=ANTHROPIC_AUTH_TOKEN`, no secret content).

### Phase 7: Clean run launch
**Completed**: 2026-04-22 17:00 (in progress, ~11-18h ETA)

Rather than continue the selective rerun (60 of 445 trials with disclosure),
chose a fresh `glm-leaderboard-v3` run at parallel=3, all patches applied.
Eliminates batch-mixing grey area for Harbor reviewers.

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Resume strategy | Code flag vs. S3 surgery | `--resume` flag with S3 scan | Pure additive code; no mutations; read-only scan; easy rollback |
| Force-rerun mechanism | Delete S3 → re-resume / add `--rerun-list` flag | `--rerun-list` | IAM blocked Delete; overwrite-in-place via PutObject + versioning history is cleaner |
| WORKDIR compatibility | Per-task WORKDIR detection / `/app` symlink | Symlink to first-matching common dir | Minimal diff; preserves all downstream `/app/...` code paths unchanged |
| Oracle guard scope | `/app` only / scoped expansion / system-wide | Scoped expansion (7 task dirs) | Catches heterogeneous images without risk of deleting system files |
| Token handling | Keep tee debug / remove entirely / rotate each trial | Remove entirely + scrub stale file at start | Debug log was net-harmful; token still available via env var |
| Submission strategy | Rerun 60 + disclose / clean run v3 / submit 62.2% floor | Clean run v3 at parallel=3 | Zero grey areas; simplest Harbor reviewer story; patched runner correct-by-construction |
| Rerun S3 writes | Delete markers / overwrite in place | Overwrite in place | IAM blocked delete; versioning preserves history either way |

---

## Files Modified

- `pilot-bench/aws/orchestrator.py` — +79 lines (`--resume`, `--dry-run`, `--rerun-list` flags, `_scan_completed_trials()`, force-rerun set logic). Commit `13decf41`.
- `pilot-bench/aws/run-bench-task.sh` — +18 lines committed (`/app` symlink block). Subsequent uncommitted: per-task timeout, broadened oracle guard, token-leak removal. Total ~50 lines across phases.
- `pilot-bench/submissions/glm-leaderboard-v2-notes.md` — created: submission disclosure (H1/T2/etc. audit summary, first-action pattern disclosure, containment evidence).
- `pilot-bench/bench-status.py` — temporarily added RERUN MODE dashboard block; reverted on user request (clean cumulative view preferred during v3 run).
- `.agent/sops/benchmark-integrity-audit.md` — already created pre-session, referenced throughout audit.

---

## Challenges & Solutions

**Challenge**: Config drift near-miss during resume
- **Problem**: Launched first resume attempt without setting `ANTHROPIC_BASE_URL` inline. `_generate_pilot_config()` read the shell's `ANTHROPIC_BASE_URL=https://api.anthropic.com` and uploaded a pilot-config.yaml pointing to Anthropic (not Z.AI/GLM). Would have silently switched providers mid-run, contaminating the submission.
- **Solution**: Killed before any SSM dispatch, restored 1165-byte config from S3 versioning, relaunched with explicit inline env var. Verified byte-identical match via diff.
- **Memory note**: `feedback_bench_env_vars.md` created.

**Challenge**: `prove-plus-comm` 0/5 infra failures
- **Problem**: Image uses `/workspace` WORKDIR, not `/app`. All trials died at the env-bootstrap step with "No such file or directory".
- **Solution**: `/app → $WORKDIR` symlink fallback (Phase 4). Verified via `docker inspect` on a live warm-pool instance before patching.

**Challenge**: 60 trials violated `timeout_multiplier=1.0`
- **Problem**: Global `MAIN_TIMEOUT=5400s` wrapper + Pilot's `complex: 60m` effort routing gave many tasks 2-4× their manifest-declared `agent_timeout_sec`.
- **Solution**: Read `agent_timeout_sec` from manifest per task, use as wrapper. Phase 3 fix.

**Challenge**: Dashboard confusion during selective rerun
- **Problem**: Force-rerun overwrote S3 in-place. Cumulative card showed blended old+new state with two progress bars (cumulative 445/445 + rerun 17/60). User found it unreadable.
- **Solution**: Abandoned selective rerun approach entirely; reverted dashboard to clean non-rerun state; launched `glm-leaderboard-v3` fresh.

**Challenge**: Near-silent IAM block on `s3:DeleteObject`
- **Problem**: Initial delete loop appeared to succeed (no error in silent output) but actually returned 0 deletions due to IAM deny.
- **Solution**: Diagnostic single-object call surfaced AccessDenied. Pivoted to `--rerun-list` overwrite-in-place approach.

---

## Verify

Commands executed to validate each phase:

```bash
# Phase 1: dry-run resume on v2 (before launching v3)
ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic AWS_PROFILE=quantflow \
  python3 orchestrator.py --run-id glm-leaderboard-v2 --tasks all --k-trials 5 \
  --max-parallel 5 --resume --dry-run
# Expected: "Found 303 completed trials in S3 — will skip"
# "Would dispatch 142 trials (skipping 303)"

# Phase 3: audit over-timeout trials in v2
python3 <<'PY'
import json, glob
manifest = json.load(open('pilot-bench/aws/tasks-manifest.json'))
limits = {t['task_name']: int(t.get('agent_timeout_sec', 900)) for t in manifest['tasks']}
over = [m for p in glob.glob('results/glm-leaderboard-v2/*/trial-*/trial-meta.json')
        for m in [json.load(open(p))] if m['duration_sec'] > limits[m['task_name']]]
print(f'Over-timeout: {len(over)} / 445')
PY
# Expected: 60

# Phase 4: verify symlink works on prove-plus-comm
# Launched targeted rerun for prove-plus-comm only — saw 5/5 passes at ~180-215s.

# Phase 7: clean run health check
tail -f /tmp/bench-glm-leaderboard-v3.log
# Watch for: "[N/445] <task>/trial-NNN: Success reward=X.X duration=Ys"
```

**Watch command** (running now):
```bash
watch -c -n 60 '
  AWS_PROFILE=quantflow python3 ~/Projects/startups/pilot/pilot-bench/aws/rebuild-log-from-s3.py \
    --run-id glm-leaderboard-v3 \
    --out /tmp/bench-glm-leaderboard-v3-aggregated.log 2>/dev/null;
  BENCH_LOG=/tmp/bench-glm-leaderboard-v3-aggregated.log \
    python3 ~/Projects/startups/pilot/pilot-bench/bench-status.py
'
```

---

## Done

- [x] `--resume` / `--dry-run` / `--rerun-list` flags land in orchestrator
- [x] Per-task `agent_timeout_sec` enforced in task runner
- [x] Oracle guard covers `/app /workspace /tests /home/user /home/agent /srv /root`
- [x] Token leak (`tee /tmp/ssm-auth-debug.log`) removed
- [x] `/app` symlink fallback for non-standard WORKDIRs
- [x] Audit confirmed zero real credentials in existing 445 traces
- [x] Clean run `glm-leaderboard-v3` launched with all patches (PID 21183)
- [ ] Clean run completes (~11-18h at parallel=3)
- [ ] Submission translator written (~4-6h): AWS artifacts → TB2 schema (`<task>__<suffix>/config.json + result.json + agent/command-N/ + verifier/`)
- [ ] `metadata.yaml` + task_checksum computation
- [ ] HF PR opened against `harborframework/terminal-bench-2-leaderboard`

---

## Related

**SOPs**:
- `.agent/sops/benchmark-integrity-audit.md` — Harbor H1-H4, T1-T7, S1-S4 checklist (used for this run's audit)

**Memory notes added**:
- `feedback_bench_env_vars.md` — inline `ANTHROPIC_BASE_URL=z.ai` discipline for GLM runs

**Prior submission reference**:
- v35 / PR #108 at 82.0% — had `agent_timeout_multiplier: 9.0` in config.json. Also leaked OAuth token in public dataset (flagged to user for rotation).

---

**Last Updated**: 2026-04-22
