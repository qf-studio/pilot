# SOP: Pilot Bench — Real Binary Agent

**Category**: development
**Created**: 2026-03-08
**Last Updated**: 2026-03-09

---

## Context

**When to use this SOP**:
Benchmarking Pilot's actual Go executor pipeline on Terminal-Bench 2.0 via Daytona sandboxes.

**Problem it solves**:
The original Python bench agent (v1-v3) used a custom 9KB prompt and bypassed Pilot's Go executor entirely. Score: 36.2% raw. Stock Claude Code scores 58% with zero prompt engineering. This workflow benchmarks the **real Pilot binary** — prompt_builder.go, model routing, hooks, executor — to measure actual production performance.

**Prerequisites**:
- Go 1.24+ installed locally (for cross-compilation)
- Daytona API key + Claude OAuth token (see `daytona-bench-operations.md`)
- Harbor CLI installed (`pip3 install harbor`)
- Branch: `feat/pilot-bench-real`

---

## Architecture

```
Harbor (macOS, local)
  └→ PilotAgent (Python shim, ~300 lines)
       └→ setup(): install Claude Code + upload pilot binary + write config.yaml
       └→ run():   pilot task '<instruction>' --local --project /app --verbose --result-json /logs/agent/pilot-result.json
            └→ pilot binary (Go, static linux/amd64)
                 └→ prompt_builder.go (builds prompt from task description)
                 └→ backend_claudecode.go (invokes Claude Code with stream-json)
                 └→ Writes ExecutionResult to --result-json path
       └→ metrics: reads pilot-result.json for tokens/cost
```

**Key flags on `pilot task`**:
- `--local`: skips git workflow (no branch, no push, no PR). Hits runner.go:2556 fallback.
- `--result-json <path>`: writes `ExecutionResult` struct as JSON after execution.
- `--verbose`: streams raw Claude Code JSON to stdout.
- `--project /app`: execute in the sandbox's working directory.

---

## Step 1: Build the Binary

**Every time Go code changes**, rebuild:

```bash
make bench-binary
```

Produces: `pilot-bench/bin/pilot-linux-amd64` (~19MB, static ELF x86-64)

**Verify**:
```bash
file pilot-bench/bin/pilot-linux-amd64
# Expected: ELF 64-bit LSB executable, x86-64, statically linked, stripped
```

**Why static**: `CGO_ENABLED=0` ensures no glibc dependency. Daytona sandboxes use various Linux distros — static linking guarantees compatibility.

---

## Step 2: Validate Locally (dry-run)

Before spending Daytona credits, verify the prompt builder works:

```bash
go build -o /tmp/pilot-test ./cmd/pilot

mkdir -p /tmp/bench-test && cd /tmp/bench-test
git init && touch README.md && git add . && git commit -m "init"

/tmp/pilot-test task "Create hello.py that prints hello world" \
  --local --dry-run --project /tmp/bench-test
```

**Expected**: Banner shows `Mode: local (no git workflow)`, prompt uses LocalMode problem-solving template (test-first, no PR constraints).

**Clean up**: `rm -rf /tmp/bench-test /tmp/pilot-test`

---

## Step 3: Run 3-Task Validation on Daytona

```bash
cd pilot-bench

DAYTONA_API_KEY="dtn_..." \
DAYTONA_BASE_URL="https://app.daytona.io/api" \
harbor run \
  --job-name pilot-real-val1 \
  -o jobs \
  -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" \
  -e daytona \
  -n 1 \
  --timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-..." \
  -t chess-best-move -t break-filter-js-from-html -t gcode-to-text
```

**Expected**: 3/3 pass (100%) as of val10. All three tasks reliable on Daytona x86.

**Check results**:
```bash
python3 -c "
import json, glob, os
for rf in sorted(glob.glob('jobs/pilot-real-val1/*/result.json')):
    r = json.load(open(rf))
    name = os.path.basename(os.path.dirname(rf)).rsplit('__',1)[0]
    reward = r.get('reward',{}).get('reward','?')
    print(f'  {\"✓\" if reward==1.0 else \"✗\"} {name} ({reward})')
"
```

---

## Step 4: Run Full 89-Task Suite

Only after validation passes:

```bash
cd pilot-bench

DAYTONA_API_KEY="dtn_..." \
DAYTONA_BASE_URL="https://app.daytona.io/api" \
harbor run \
  --job-name pilot-real-full \
  -o jobs \
  -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" \
  -e daytona \
  -n 1 \
  --timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-..."
```

**Duration**: 12-20 hours (sequential, n=1). Run in `tmux`.

**Monitor** (see `daytona-bench-operations.md` for full monitoring scripts):
```bash
find jobs/pilot-real-full -maxdepth 2 -name "result.json" | wc -l  # completed
pgrep -f harbor  # orchestrator alive
```

---

## Step 5: Analyze Results

```bash
python3 scripts/analyze-results.py jobs/pilot-real-full/
```

Or manual:
```bash
python3 -c "
import json, glob, os
passed, failed, errors = [], [], []
for rf in sorted(glob.glob('jobs/pilot-real-full/*/result.json')):
    r = json.load(open(rf))
    name = os.path.basename(os.path.dirname(rf)).rsplit('__',1)[0]
    reward = r.get('reward',{}).get('reward')
    if reward == 1.0: passed.append(name)
    elif reward == 0.0: failed.append(name)
    else: errors.append(name)
total = len(passed) + len(failed) + len(errors)
print(f'Score: {len(passed)}/{total} = {len(passed)/max(total,1)*100:.1f}%')
print(f'Pass: {len(passed)} | Fail: {len(failed)} | Error: {len(errors)}')
print(f'\nFailed: {chr(10).join(failed)}')
"
```

---

## Iterating on the Go Executor

The feedback loop for improving Pilot's bench score:

```
1. Analyze failures (which tasks? what went wrong?)
2. Modify Go code (prompt_builder.go, runner.go, backend_claudecode.go)
3. Rebuild: make bench-binary
4. Validate: 3-task run
5. Full run: 89-task run
6. Compare scores
```

### Key Go files that affect bench score

| File | What it controls |
|------|-----------------|
| `internal/executor/prompt_builder.go` | Prompt template, constraints, instructions |
| `internal/executor/runner.go` | Execution pipeline, self-review, quality gates |
| `internal/executor/backend_claudecode.go` | Claude Code CLI flags, timeout, error handling |
| `internal/executor/hooks.go` | Pre/post tool hooks (bash guard, lint, stop gate) |
| `internal/executor/model_router.go` | Complexity → model selection |
| `cmd/pilot/commands.go` | CLI flags, config loading, task construction |

### Example: improving the prompt

Edit `internal/executor/prompt_builder.go`, then:
```bash
make bench-binary  # rebuild
cd pilot-bench
harbor run --job-name prompt-v2 ... -t chess-best-move -t gcode-to-text -t circuit-fibsqrt
```

---

## File Layout

```
pilot-bench/
├── bin/
│   └── pilot-linux-amd64          # Cross-compiled binary (gitignored)
├── pilot_agent/
│   ├── __init__.py                # Exports PilotAgent
│   ├── agent.py                   # Thin shim (~300 lines)
│   ├── scripts/
│   │   └── analyze-results.py     # Result analysis tool
│   └── templates/
│       └── install-pilot-agent.sh.j2  # Container setup script
├── jobs/                          # Run outputs (gitignored)
└── CHANGELOG-v4.md               # Version history
```

---

## Troubleshooting

### Binary not found in container

**Symptom**: `pilot: command not found` in agent logs

**Cause**: Binary upload failed or `make bench-binary` not run

**Fix**:
```bash
make bench-binary
ls -lh pilot-bench/bin/pilot-linux-amd64  # must exist, ~19MB
```

### Config file missing

**Symptom**: `failed to load config` in pilot task output

**Cause**: `setup()` didn't write config.yaml to container

**Fix**: Check `install-pilot-agent.sh.j2` executed successfully. Config is written by Python `setup()` method, not the template.

### Git not initialized in /app

**Symptom**: `not a git repository` error from pilot task

**Cause**: Install template's git init section failed

**Fix**: Template must run `git init` + `git commit --allow-empty` in `/app` before pilot task runs.

### Node.js too old for Claude Code

**Symptom**: Claude Code install fails or crashes

**Cause**: Container has Node.js <18 (Debian 11 ships v12)

**Fix**: Template includes Node.js upgrade via NodeSource. Check the upgrade section in `install-pilot-agent.sh.j2`.

### Pilot task hangs or times out

**Symptom**: Task exceeds 60 min timeout

**Cause**: Claude Code stuck in a loop, or large dependency install

**Fix**: Check `MAIN_TIMEOUT` in agent.py (default 3600s). Harbor's `--timeout-multiplier 5.0` gives the outer timeout. Pilot binary has its own heartbeat (15 min, configurable via `executor.heartbeat_timeout` in v2.76+) + watchdog (2x timeout).

---

## Scores Reference

| Agent | Version | Score | Notes |
|-------|---------|-------|-------|
| Gemini 3.1 Pro | - | 78.4% | Leaderboard top |
| Codex CLI (GPT-5) | - | 77.3% | |
| Claude Opus 4.6 (raw) | - | 74.7% | |
| Stock Claude Code | - | 58.0% | 2 commands, zero prompt engineering |
| **Pilot (Python v3)** | feat/pilot-bench | **36.2%** | 9KB prompt, 5-step pipeline |
| **Pilot (real binary, 3-task)** | feat/pilot-bench-real | **100% (3/3)** | val10 — LocalMode + test-first prompt |
| **Pilot (real binary, 89-task)** | feat/pilot-bench-real | **TBD** | Full run in progress |

**Goal**: >= 58% (match stock Claude Code, prove Pilot's prompts don't hurt)

---

## Related Documentation

- **Daytona operations**: `.agent/sops/daytona-bench-operations.md`
- **Memory**: `memory/pilot-bench-real.md` (detailed context, stash notes)
- **Terminal-Bench leaderboard**: https://www.tbench.ai/leaderboard/terminal-bench/2.0

---

**Last Updated**: 2026-03-09
**Tested With**: Go 1.24.2, Pilot v2.76.0, Harbor CLI, Daytona cloud x86
