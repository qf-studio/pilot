# SOP: Daytona Bench Operations

## Prerequisites

| Item | Value |
|------|-------|
| Daytona API Key | `DAYTONA_API_KEY` env var (starts with `dtn_`) |
| Daytona API URL | `DAYTONA_BASE_URL=https://app.daytona.io/api` |
| Claude OAuth Token | `CLAUDE_CODE_OAUTH_TOKEN` (1-year token from `claude setup-token`) |
| Harbor CLI | `harbor` (installed via pip, `pip3 install harbor`) |
| Memory Limit | 10 GiB (Daytona free tier) |
| .env file | `~/Projects/startups/pilot/.env` (gitignored, 3 vars above) |

## Environment Setup

### First time

```bash
# Create .env at project root (gitignored)
cat > .env << 'EOF'
DAYTONA_API_KEY=dtn_...
DAYTONA_BASE_URL=https://app.daytona.io/api
CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
EOF
```

Get keys:
- Daytona API key: https://app.daytona.io/dashboard/keys
- Claude OAuth token: `claude setup-token` (generates 1-year token from Max subscription)

### Every session

```bash
source .env
```

---

## Agent Modes

### Real Pilot Binary (recommended, `feat/pilot-bench-real`)

Runs actual Pilot Go binary → benchmarks production pipeline.

**Before first run** (or after Go code changes):
```bash
make bench-binary  # Cross-compile linux/amd64 static binary
```

**3-task validation**:
```bash
source .env && cd pilot-bench && \
harbor run \
  --job-name pilot-real-val1 \
  -o jobs \
  -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" \
  -e daytona \
  -n 1 \
  --timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN" \
  -t chess-best-move -t break-filter-js-from-html -t gcode-to-text
```

**Full 89-task run** (12-20h, run in tmux):
```bash
source .env && cd pilot-bench && \
harbor run \
  --job-name pilot-real-full \
  -o jobs \
  -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" \
  -e daytona \
  -n 1 \
  --timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"
```

**Specific tasks only**:
```bash
# Add -t flags for each task
-t chess-best-move -t gcode-to-text -t circuit-fibsqrt
```

> Full architecture details: `.agent/sops/development/pilot-bench-real-binary.md`

### Python Agent (legacy, `feat/pilot-bench`)

Same harbor command, different branch. Uses custom 9KB prompt instead of Pilot binary.

---

## Resume a Failed Run

```bash
source .env && cd pilot-bench && \
harbor jobs resume -p jobs/<job-name>
```

## Check Results

```bash
python3 -c "
import json, glob, os
job_dir = 'pilot-bench/jobs/<job-name>'
passed, failed, errors = 0, 0, 0
for rf in sorted(glob.glob(f'{job_dir}/*/result.json')):
    r = json.load(open(rf))
    reward = r.get('reward',{}).get('reward')
    name = os.path.basename(os.path.dirname(rf)).rsplit('__',1)[0]
    if reward == 1.0: passed += 1; print(f'  ✓ {name}')
    elif reward == 0.0: failed += 1; print(f'  ✗ {name}')
    else: errors += 1; print(f'  ? {name} ({reward})')
total = passed + failed + errors
print(f'\nScore: {passed}/{total} = {passed/max(total,1)*100:.1f}%')
"
```

## Daytona Sandbox Management

### List Active Sandboxes

```bash
source .env && \
curl -s -H "Authorization: Bearer $DAYTONA_API_KEY" \
  "$DAYTONA_BASE_URL/sandbox" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(f'Sandboxes: {len(data)}, Mem: {sum(s.get(\"memory\",0) for s in data)}/{10} GiB')
for s in data:
    print(f'  {s[\"id\"]} | {s[\"state\"]} | {s.get(\"memory\",\"?\")}GiB')
"
```

### Delete a Sandbox

```bash
curl -s -X DELETE -H "Authorization: Bearer $DAYTONA_API_KEY" \
  "$DAYTONA_BASE_URL/sandbox/<sandbox-id>"
```

### Delete ALL Sandboxes (nuclear option)

```bash
source .env && \
curl -s -H "Authorization: Bearer $DAYTONA_API_KEY" \
  "$DAYTONA_BASE_URL/sandbox" | \
python3 -c "
import json, sys, subprocess
for s in json.load(sys.stdin):
    print(f'Deleting {s[\"id\"]}...')
    subprocess.run(['curl','-s','-X','DELETE',
      '-H',f'Authorization: Bearer {sys.argv[1]}',
      f'{sys.argv[2]}/sandbox/{s[\"id\"]}'], capture_output=True)
" "$DAYTONA_API_KEY" "$DAYTONA_BASE_URL"
```

## Monitoring

### Quick Check

```bash
# Is harbor running?
pgrep -f harbor

# How many results so far?
find pilot-bench/jobs/<job-name> -maxdepth 2 -name "result.json" | wc -l

# Active sandboxes?
source .env && \
curl -s -H "Authorization: Bearer $DAYTONA_API_KEY" \
  "$DAYTONA_BASE_URL/sandbox" | python3 -c "import json,sys; print(f'{len(json.load(sys.stdin))} sandboxes')"

# Latest trial log?
ls -t pilot-bench/jobs/<job-name>/*/trial.log 2>/dev/null | head -1 | xargs tail -5

# Score so far (while running)?
python3 -c "
import json, glob, os
job = 'pilot-bench/jobs/<job-name>'
p = f = e = 0
for rf in sorted(glob.glob(f'{job}/*/result.json')):
    r = json.load(open(rf)).get('reward',{}).get('reward')
    if r == 1.0: p += 1
    elif r == 0.0: f += 1
    else: e += 1
t = p + f + e
print(f'Done: {t} | Pass: {p} | Fail: {f} | Err: {e} | Score: {p/max(t,1)*100:.1f}%')
"
```

### Watch Mode (auto-refresh every 60s)

```bash
watch -n 60 'find pilot-bench/jobs/<job-name> -maxdepth 2 -name "result.json" | wc -l'
```

## Common Failure Modes

| Error | Cause | Fix |
|-------|-------|-----|
| `API key or JWT token is required` | Missing `DAYTONA_API_KEY` | `source .env` |
| `Total memory limit exceeded` | Stale sandboxes consuming 10GiB | Delete old sandboxes via API |
| `Sandbox not found` | Sandbox expired | Re-run (harbor resume handles this) |
| Auth token expired (44/89 fail) | Claude OAuth expired mid-run | `claude setup-token` for 1-year token |
| fd leak (32/89 fail) | Docker QEMU issue | Use Daytona (`-e daytona`) not Docker |
| Orchestrator dies silently | Process killed, OOM, terminal closed | Run in tmux, monitor with watch |
| `pilot: command not found` | Binary not uploaded or not built | `make bench-binary` then re-run |
| `failed to load config` | config.yaml not written to container | Check setup() in agent.py |
| `not a git repository` | git init failed in /app | Check install template git section |
| Node.js <18 | Container ships old Node | Template has NodeSource upgrade |

## Token Management

### Claude Code OAuth Token (1 year)

```bash
claude setup-token  # Interactive — generates long-lived token
# Output: sk-ant-oat01-...
# Save to .env: CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

### Daytona API Key

Get from: https://app.daytona.io/dashboard/keys

Format: `dtn_<hex>`

## Architecture

```
macOS (local)                          Daytona Cloud (x86 Linux)
┌──────────────┐                       ┌──────────────────────────────┐
│ harbor run   │ ──── create ────────► │ Sandbox (4GiB, x86)         │
│              │ ──── upload agent ──► │  ├── Claude Code (npm)       │
│              │ ──── run commands ──► │  ├── pilot binary (uploaded) │
│              │                       │  ├── config.yaml             │
│              │ ◄─── download logs ── │  └── /app (task workspace)   │
│              │ ──── delete ────────► │                              │
└──────────────┘                       └──────────────────────────────┘
```

- Harbor orchestrator runs **locally** (macOS)
- Each task gets a Daytona cloud sandbox (x86 Linux)
- Sequential mode (`-n 1`): one sandbox at a time, ~4GiB each
- Parallel mode (`-n 4`): needs tier upgrade for >10GiB
- Results: `jobs/<name>/<task>__<hash>/result.json`

---

**Last Updated**: 2026-03-08
