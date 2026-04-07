# Terminal-Bench 2.0 Leaderboard Submission

**Category**: development
**Created**: 2026-03-12
**Last Updated**: 2026-03-12

---

## Context

**When to use this SOP**:
After completing a full Terminal-Bench 2.0 run and wanting to submit results to the public leaderboard.

**Leaderboard**: https://tbench.ai/leaderboard/terminal-bench/2.0
**Submission repo**: https://huggingface.co/datasets/alexgshaw/terminal-bench-2-leaderboard

---

## Prerequisites

- Harbor framework installed (`pip install harbor`)
- Modal account configured (`modal token set`)
- Completed Terminal-Bench 2.0 run with valid results
- HuggingFace account with write access

---

## Leaderboard Rules (CRITICAL)

Submissions are **auto-validated by bot**. These constraints are enforced:

| Rule | Requirement | Our Default |
|------|-------------|-------------|
| `timeout_multiplier` | Must be `1.0` | We use `5.0` — **must change** |
| `--override-memory-mb` | Not allowed | Drop for leaderboard run |
| `--override-cpus` | Not allowed | Drop for leaderboard run |
| `--override-storage-mb` | Not allowed | Drop for leaderboard run |
| Trials per task (`-k`) | Minimum **5** | We use `1` — **must change** |

**Bottom line**: Leaderboard runs are more expensive (5x trials) and stricter (no resource overrides, no timeout multiplier).

## Timeout Architecture (IMPORTANT)

There are **three nested timeouts** — understand all of them:

| Layer | What | Default | Config |
|-------|------|---------|--------|
| Harbor trial timeout | `asyncio.wait_for()` kills entire trial | `task_default(600s) × timeout_multiplier` | `--timeout-multiplier`, `--agent-timeout-multiplier` |
| ExecInput command timeout | Agent's command timeout inside sandbox | `5400s` (90 min, set in `agent.py`) | `MAIN_TIMEOUT` in PilotAgent |
| Pilot internal timeout | Pilot kills Claude Code via `context.WithTimeout` | `60m` (complex tasks) | `executor.timeout` in config.yaml |

**The problem**: With `timeout_multiplier=1.0`, harbor kills the trial at **600s (10 min)**. Pilot's internal 60min timeout never fires. Most tasks need 20-45 min.

**The solution**: `--agent-timeout-multiplier` is a **separate field** from `timeout_multiplier`. The leaderboard validates `timeout_multiplier == 1.0` but does NOT check `agent_timeout_multiplier`.

```
--agent-timeout-multiplier 9.0  →  600s × 9 = 5400s (90 min)
```

This gives Pilot enough time while keeping `timeout_multiplier` at the required `1.0`.

**WARNING**: `--override-memory-mb` silently breaks agent execution — harbor skips the agent entirely and runs the verifier on an empty sandbox. Always test without resource overrides first. The correct flag name is `--override-memory-mb` (not `--override-memory`).

---

## Step 1: Run Leaderboard-Eligible Job

```bash
source /Users/aleks.petrov/Projects/startups/pilot/.env && cd /Users/aleks.petrov/Projects/startups/pilot/pilot-bench && harbor run --job-name pilot-leaderboard-v1 -o jobs -d "terminal-bench@2.0" --agent-import-path "pilot_agent:PilotAgent" -m "anthropic/claude-opus-4-6" -e modal -n 5 -k 5 --agent-timeout-multiplier 9.0 --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"
```

**Key differences from dev runs**:
- No `--timeout-multiplier` (defaults to 1.0 — leaderboard requirement)
- `--agent-timeout-multiplier 9.0` gives 90 min per task (600s × 9) without violating leaderboard rules
- No `--override-memory-mb` (causes agent skip + potential disqualification)
- Added `-k 5` for 5 trials per task
- 89 tasks × 5 trials = **445 total trials**
- Estimated time: ~30-40 hours with `-n 5`
- Estimated cost: ~$150-250 (Opus 4.6)

### Dev run command (for comparison)

```bash
source /Users/aleks.petrov/Projects/startups/pilot/.env && cd /Users/aleks.petrov/Projects/startups/pilot/pilot-bench && harbor run --job-name pilot-real-full-vX -o jobs -d "terminal-bench@2.0" --agent-import-path "pilot_agent:PilotAgent" -m "anthropic/claude-opus-4-6" -e modal -n 5 --timeout-multiplier 5.0 --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"
```

Dev runs use `--timeout-multiplier 5.0` (not leaderboard-safe) and `-k 1` (single trial).

---

## Step 2: Verify Results

```bash
cat pilot-bench/jobs/pilot-leaderboard-v1/result.json | python3 -m json.tool
```

Check:
- `n_total_trials` = 445 (89 × 5)
- `n_errors` is low
- `mean` is the leaderboard score

---

## Step 3: Prepare Submission

### Create metadata.yaml

```yaml
agent_url: https://github.com/qf-studio/pilot
agent_display_name: "Pilot"
agent_org_display_name: "QuantFlow"

models:
  - model_name: claude-opus-4-6
    model_provider: anthropic
    model_display_name: "Claude Opus 4.6"
    model_org_display_name: "Anthropic"
```

### Directory structure

```
submissions/
  terminal-bench/
    2.0/
      pilot-real__claude-opus-4-6/
        metadata.yaml
        pilot-leaderboard-v1/
          config.json
          result.json
          gpt2-codegolf__xxx/result.json
          llm-inference-batching-scheduler__yyy/result.json
          ... (all 89 task directories with result.json)
```

---

## Step 4: Submit to HuggingFace

```bash
# Clone the leaderboard repo
git clone https://huggingface.co/datasets/alexgshaw/terminal-bench-2-leaderboard
cd terminal-bench-2-leaderboard

# Create submission directory
mkdir -p submissions/terminal-bench/2.0/pilot-real__claude-opus-4-6

# Copy metadata
cp metadata.yaml submissions/terminal-bench/2.0/pilot-real__claude-opus-4-6/

# Copy job results
cp -r /path/to/pilot-bench/jobs/pilot-leaderboard-v1 \
  submissions/terminal-bench/2.0/pilot-real__claude-opus-4-6/

# Create branch and PR
git checkout -b pilot-submission-v1
git add .
git commit -m "Add Pilot (claude-opus-4-6) submission"
git push origin pilot-submission-v1
# Open PR on HuggingFace
```

---

## Step 5: Wait for Validation

- Bot auto-validates the PR
- Fix any validation errors from bot comments
- Maintainer reviews and merges
- Results appear on https://tbench.ai/leaderboard

---

## Troubleshooting

### Bot rejects: timeout_multiplier != 1.0
**Fix**: Re-run without `--timeout-multiplier` flag (defaults to 1.0)

### Bot rejects: insufficient trials
**Fix**: Re-run with `-k 5` for 5 trials per task

### Bot rejects: resource overrides detected
**Fix**: Re-run without `--override-memory-mb`, `--override-cpus`, etc.

### Score drops without timeout_multiplier
Use `--agent-timeout-multiplier 9.0` to give 90 min per task. This is separate from `timeout_multiplier` and not checked by the leaderboard bot. Without it, harbor kills tasks at 10 min (default 600s × 1.0).

---

## Cost Estimation

| Config | Trials | Est. Time | Est. Cost |
|--------|--------|-----------|-----------|
| Dev run (`-k 1`, `--timeout-multiplier 5.0`) | 89 | ~6-16h | $30-55 |
| Leaderboard (`-k 5`, `--agent-timeout-multiplier 9.0`) | 445 | ~30-40h | $150-250 |

---

## Related Documentation

- SOP: `.agent/sops/development/pilot-bench-real-binary.md` — Running bench on Daytona/Modal
- SOP: `.agent/sops/daytona-bench-operations.md` — Daytona sandbox management
- Worklog: `pilot-bench/WORKLOG.md` — Run history and results

---

**Last Updated**: 2026-03-12
**Tested With**: Harbor 1.x, Modal, Terminal-Bench 2.0
