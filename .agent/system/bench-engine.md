# Bench Engine Architecture

Terminal Bench 2.0 execution engine. Two implementations: Go backend (production, tests Pilot pipeline) and Python engine (standalone, proven 83%).

## Why Custom Engine

Claude Code CLI is a black box — no control over thinking budgets, tool dispatch, context window, or retry behavior. ForgeCode (81.8%) and every top agent uses direct API calls.

**Pilot's differentiator**: model routing, effort classification, quality gates, learning DB. These must be tested, not bypassed.

## Two Implementations

### 1. Go Backend (`backend_anthropic.go`) — PRODUCTION TARGET

Implements Pilot's `Backend` interface. Runs inside the full Go pipeline — router, classifier, gates, learning DB all active. The Go binary calls the Anthropic API directly instead of spawning Claude Code CLI.

```
Harbor → agent.py → pilot binary (Go) → Runner.Execute()
                                           ├── EffortClassifier (Haiku) → complexity
                                           ├── ModelRouter → select model
                                           ├── AnthropicBackend.Execute() → API call
                                           │   ├── SSE streaming
                                           │   ├── Tool execution (bash/read/write/edit)
                                           │   └── Progressive thinking
                                           ├── QualityGates (pytest)
                                           └── PatternContext (learning DB)
```

**Status**: Built, compiles, tests pass. Blocked on API credits (OAuth tokens not supported on raw API).

### 2. Python Engine (`engine.py`) — STANDALONE FALLBACK

Direct API calls from Python. No Go binary, no Pilot pipeline. Just tools + thinking + retry.

```
Harbor → agent.py → engine.py → Anthropic API
                       ├── Tools: bash, read_file, write_file, edit_file
                       ├── Progressive thinking (10K→3K)
                       ├── Loop detection
                       └── Learning DB (SQLite read)
```

**Status**: Proven 83.3% at 12 trials. Killed by API credit exhaustion.

## Model Routing (Go Backend)

Pilot's effort classifier (Haiku, ~$0.001/call) pre-classifies task complexity. Router selects model and effort level. Backend maps effort to thinking budget.

| Complexity | Model | Effort | Thinking | Cost/1M Input |
|-----------|-------|--------|----------|---------------|
| Trivial (~18%) | Haiku | low | OFF | $0.80 |
| Simple (~22%) | Sonnet | medium | OFF | $3.00 |
| Medium (~30%) | Opus | high | 10K→3K progressive | $15.00 |
| Complex (~30%) | Opus | high | 10K→3K progressive | $15.00 |

**Weighted average: ~$7/1M input (53% cheaper than all-Opus).**

Thinking is disabled for Haiku/Sonnet — only Opus uses extended thinking. This avoids compatibility issues and reduces token waste on simple tasks.

## Data Flow (Go Backend)

```
1. Harbor creates Modal sandbox, runs install-pilot-agent.sh.j2
2. agent.py setup():
   a. Upload Go binary (28MB, chunked base64 fallback)
   b. Write /root/.pilot/config.yaml (type: "anthropic-api")
   c. Upload pilot.db (17 patterns)
   d. Env bootstrap → /app/.pilot-env-context.txt
   e. Upload test files to /tests/
3. Harbor calls create_run_agent_commands(instruction)
   → "pilot task 'instruction' --local --project /app"
4. Go binary runs:
   a. EffortClassifier → classify complexity
   b. ModelRouter → select model + effort
   c. PromptBuilder → build phased prompt + patterns
   d. AnthropicBackend.Execute() → API loop
   e. QualityGates → pytest between iterations
5. Writes /logs/agent/pilot-result.json
6. Harbor verifier → reward 0.0 or 1.0
```

## Go Backend Configuration

### Constants (`backend_anthropic.go`)

| Constant | Value | Purpose |
|----------|-------|---------|
| `thinkingHighTurns` | 8 | Turns with high thinking budget |
| `thinkingHighBudget` | 10000 | Planning phase tokens (Opus only) |
| `thinkingLowBudget` | 3000 | Execution phase tokens (Opus only) |
| `maxOutputTokens` | 12000 | Max non-thinking output per turn |
| `apiMaxTurns` | 60 | Max API call iterations |
| `apiBashTimeout` | 120 | Per-command timeout (seconds) |
| `apiMaxRetries` | 5 | API retry attempts |
| `apiOutputCap` | 50000 | Truncate tool output (bytes) |
| `apiContextPruneAt` | 150000 | Estimated tokens before pruning |

### Effort → Thinking Budget Mapping

```go
switch opts.Effort {
case "low":   thinkingBudget = 1000   // Haiku tasks
case "medium": thinkingBudget = 3000  // Sonnet tasks
case "high":  // progressive: 10K first 8 turns, 3K after (Opus)
case "max":   thinkingBudget = 15000  // Override for hardest tasks
}
// Thinking disabled entirely for non-Opus models
```

### Bench Config (`agent.py _build_config()`)

```yaml
executor:
  type: "anthropic-api"
  model_routing:
    enabled: true
    trivial: "claude-haiku-4-5-20251001"
    simple: "claude-sonnet-4-6"
    medium: "claude-opus-4-6"
    complex: "claude-opus-4-6"
  effort_routing:
    enabled: true
    trivial: low
    simple: medium
    medium: high
    complex: high
  effort_classifier:
    enabled: true
    model: claude-haiku-4-5-20251001
quality:
  enabled: true
  gates:
    - name: test
      command: "uvx pytest /tests/test_outputs.py -rA"
      max_retries: 2
```

## Tools (Both Engines)

| Tool | Description | Limits |
|------|-------------|--------|
| `bash` | Shell command execution | 120s timeout, 50KB output cap |
| `read_file` | Read with line numbers | 500KB max, offset/limit support |
| `write_file` | Create/overwrite files | Creates parent dirs |
| `edit_file` | String replacement | old_string must appear exactly once |

## SSE Streaming (Go Backend)

The Go backend parses Anthropic's Server-Sent Events stream:

```
event: message_start     → capture model, usage
event: content_block_start → new text/tool_use block
event: content_block_delta → accumulate text/input_json
event: content_block_stop  → finalize block
event: message_delta      → stop_reason, final usage
event: message_stop       → done
event: error              → handle/retry
```

Parsed in `parseSSEStream()` with 1MB scanner buffer for large responses.

## API Retry

5 retries with 30/60/90/120/180s backoffs:
- **429** Rate Limit
- **529** API Overloaded
- **5xx** Server errors
- **200 + error body** (overloaded_error in response)
- **Empty content** in response

SDK retries disabled (`max_retries=0` in Python engine) to avoid double-retry stacking.

## Context Window Management

Rough token estimate: `len(text) / 4`.

When estimated context > 150K tokens:
1. Keep first message (initial instruction)
2. Keep last 12 turns (24 messages)
3. Replace middle with: `[Context pruned: N earlier messages removed]`

## Learning DB Integration

17 seed patterns loaded from `/root/.pilot/data/pilot.db`:
- 11 recommended (test-first, brute-force, approach switching, task-specific strategies)
- 6 anti-patterns (analysis paralysis, format mismatch, concurrent processes, retrying)

Go backend: injected via `PatternContext.InjectPatterns()` in `BuildPrompt()`.
Python engine: direct SQLite query in `load_patterns()`.

Managed by `pilot-bench/pilot_agent/scripts/seed-learning-db.py`.

### Pattern Persistence Across Containers

Containers learn during execution. Patterns persist via batch sync:

```
Container finishes task
  → cp /root/.pilot/data/pilot.db /logs/agent/pilot-patterns.db
  → Harbor auto-syncs /logs/agent/ to host (bind mount)
  → populate_context_post_run() merges new patterns into seed DB
  → next container uploads enriched seed DB
```

Merge rules (thread-safe with fcntl file lock for n=3):
- Dedup by title — no duplicate patterns
- New patterns inserted at confidence 0.6 (conservative)
- Existing patterns boosted +0.05 per occurrence (cap 0.95)
- Each wave of containers benefits from previous wave's learnings

With n=3, containers in the same wave can't share (they diverge). But each wave enriches the seed for the next wave. Batch learning, not real-time.

## Auth

**API Key** (`ANTHROPIC_API_KEY`): Works with raw API. Required for bench runs. Costs real money.

**OAuth Token** (`CLAUDE_CODE_OAUTH_TOKEN`): Does NOT work with raw Anthropic API (401 "OAuth not supported" / "invalid x-api-key"). Only works through Claude Code CLI's internal gateway.

**Key resolution** (Go backend, matches effort_classifier.go):
```
ANTHROPIC_API_KEY → PILOT_ENGINE_API_KEY → ANTHROPIC_AUTH_TOKEN → CLAUDE_CODE_OAUTH_TOKEN
```
All `sk-ant-*` tokens sent as `x-api-key` header.

## Deployment

### Binary Upload to Modal

The 28MB Go binary upload via Harbor's `upload_file()` can take 5-15 minutes. Fallback: chunked base64 via `environment.exec()` heredocs (50KB chunks).

Install script (`install-pilot-agent.sh.j2`) handles: apt deps, uv, numpy, git init. No Node.js or Claude Code needed.

### Running

```bash
# 3-task validation
source .env && cd pilot-bench && harbor run \
  --job-name pilot-api-vN -o jobs -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e modal -n 2 -k 1 \
  --agent-timeout-multiplier 9.0 --agent-setup-timeout-multiplier 5.0 \
  --ae "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY" \
  --task-name "break-filter-js-from-html" \
  --task-name "chess-best-move" \
  --task-name "gcode-to-text"

# Full k=5 (leaderboard submission)
source .env && cd pilot-bench && harbor run \
  --job-name pilot-api-full-k5 -o jobs -d terminal-bench@2.0 \
  --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e modal -n 2 -k 5 \
  --agent-timeout-multiplier 9.0 --agent-setup-timeout-multiplier 5.0 \
  --ae "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY"
```

### Debugging

```bash
# Engine logs
cat pilot-bench/jobs/<job>/<task>/agent/command-0/stderr.txt

# Verifier output
cat pilot-bench/jobs/<job>/<task>/verifier/command-0/stdout.txt

# Setup logs
cat pilot-bench/jobs/<job>/<task>/agent/setup/stdout.txt

# Check results
python3 -c "
import json
r = json.load(open('pilot-bench/jobs/<job>/result.json'))
s = r['stats']['evals']['pilot-real__claude-opus-4-6__terminal-bench']
print(f\"Score: {s['metrics'][0]['mean']:.1%}\")
"
```

## Files

| File | Purpose |
|------|---------|
| `internal/executor/backend_anthropic.go` | Go API backend (Backend interface) |
| `internal/executor/backend_factory.go` | Factory — `anthropic-api` case |
| `pilot-bench/pilot_agent/engine.py` | Python standalone engine (fallback) |
| `pilot-bench/pilot_agent/agent.py` | Harbor agent shim |
| `pilot-bench/pilot_agent/templates/install-pilot-agent.sh.j2` | Container bootstrap |
| `pilot-bench/pilot_agent/data/pilot.db` | Learning DB (17 patterns) |
| `pilot-bench/pilot_agent/scripts/seed-learning-db.py` | Pattern seeder |
| `pilot-bench/.analysis/` | Research scripts, trajectory CSVs |

## Version History

| Version | Date | Engine | Score | Notes |
|---------|------|--------|-------|-------|
| v1 (CC) | 2026-03-08 | Claude Code | 55.6% @18 | First bench run |
| v24 (CC) | 2026-03-22 | Claude Code | 65.9% k=1 | Best CC, Haiku classifier |
| v32 (CC) | 2026-03-25 | Claude Code | 49.2% @58 k=5 | CC k=5, all improvements |
| engine-v9 | 2026-03-25 | Python engine | 83.3% @12 | All-Opus, credits exhausted ($57) |
| api-v13 | 2026-03-25 | Go backend | 0% | OAuth rejected, needs API credits |
| **v35** | **2026-03-26** | **CC optimized** | **82.0% final (445 trials, validated)** | **Stripped binary, n=3, routing, resume. SUBMITTED to leaderboard.** |

## Known Issues

1. **OAuth tokens don't work** on raw Anthropic API — need `ANTHROPIC_API_KEY` with credits
2. **Binary upload slow** — 28MB takes 5-15 min on Modal, use base64 fallback if hanging
3. **Rate limit** — Opus at 30K tokens/min, use `-n 2` concurrency max
4. **Cost** — all-Opus with thinking: ~$4.75/trial. With routing: ~$0.16/trial (projected)
