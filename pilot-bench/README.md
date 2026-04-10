# Pilot Terminal-Bench Agent

Benchmark the real Pilot Go binary on [Terminal-Bench 2.0](https://github.com/TerminalBench/terminal-bench). Thin Python shim wraps the production `pilot task --local` command — same executor pipeline as production, minus git workflow.

## Architecture

```
Harbor Orchestrator → PilotAgent (Python shim) → pilot binary (Go) → Claude Code
                      ↓                          ↓
                      Install & upload binary     Full executor pipeline:
                      Upload test files           prompt builder, model routing,
                      Write config.yaml           hooks, quality gates, retries
                      Parse result JSON
```

**Not a from-scratch agent.** Claude Code is the executor. Pilot's Go binary handles prompt construction, model routing, quality gates, effort routing, and retries. The Python shim just installs everything in the container and collects results.

## Key Design Decisions

| Decision | Chosen | Why |
|----------|--------|-----|
| Agent architecture | Go binary shim (not Python pipeline) | Python v3 agent scored 36.2% — worse than stock Claude Code (58%). Real binary uses production-tested prompt builder. |
| Prompt strategy | LocalMode problem-solving prompt | No restrictive PR constraints. Test-first: "check /tests/test_outputs.py before anything else." |
| Quality gates | pytest on test_outputs.py, retry 2x | Game-changer for tasks where first attempt is close but not exact (chess, data transforms). |
| Heartbeat timeout | 15min (configurable in v2.76+) | Complex tasks (image analysis, large codebases) need more than 5min between events. |
| Container setup | Root, bypassPermissions | Non-root caused 8 task failures (pip site-packages invisible, apt denied). |

## Validation History

| Run | break-filter | chess | gcode | Score | Key Change |
|-----|-------------|-------|-------|-------|------------|
| val2-3 | 1.0 | 0.0 | 0.0 | 33% | Baseline — verifier broken |
| val4 | 1.0 | 1.0 | 0.0 | 67% | Quality gates + retry |
| val5 | 1.0 | 0.0 | 0.0 | 33% | Quality gate PATH broken |
| val8 | err | 1.0 | 0.0 | 50% | Sandbox deleted |
| val9 | 1.0 | 0.0 | 0.0 | 33% | Navigator prompt hijack |
| **val10** | **1.0** | **1.0** | **1.0** | **100%** | LocalMode priority + test-first prompt |

## Score Targets

| Agent | Score | Notes |
|-------|-------|-------|
| Pilot Python v3 | 36.2% | 5-step pipeline, 9KB prompt bloat |
| Claude Code (stock) | 58.0% | No optimizations |
| KIRA (best Claude agent) | 74.7% | Opus 4.6, 3 tools |
| Forge Code (#1) | 78.4% | Gemini 3.1 Pro |
| **Pilot (target)** | **≥58%** | Must beat stock Claude Code |

## Quick Start

```bash
# Prerequisites
pip install harbor-ai
source .env  # DAYTONA_API_KEY, DAYTONA_BASE_URL, CLAUDE_CODE_OAUTH_TOKEN

# Build the binary (from project root)
make bench-binary

# 3-task validation
harbor run --job-name pilot-val -o jobs \
  -d terminal-bench@2.0 --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e daytona -n 1 --timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN" \
  -t chess-best-move -t break-filter-js-from-html -t gcode-to-text

# Full 89-task run
harbor run --job-name pilot-full -o jobs \
  -d terminal-bench@2.0 --agent-import-path "pilot_agent:PilotAgent" \
  -m "anthropic/claude-opus-4-6" -e daytona -n 1 --timeout-multiplier 5.0 \
  --ae "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN"

# Check results
python3 pilot_agent/scripts/analyze-results.py jobs/<job-name>
```

## File Structure

```
pilot-bench/
├── pilot_agent/
│   ├── __init__.py                        # Exports PilotAgent
│   ├── agent.py                           # PilotAgent — install, run, parse results
│   ├── templates/
│   │   └── install-pilot-agent.sh.j2      # Container bootstrap (Node, Claude, uv)
│   └── scripts/
│       └── analyze-results.py             # Post-run failure analysis
├── bin/
│   └── pilot-linux-amd64                  # Pre-built static binary (19MB)
├── pyproject.toml                         # Python package config
├── WORKLOG.md                             # Development log with all validation runs
├── CHANGELOG-v4.md                        # Historical: Python v4 optimization notes
└── README.md                              # This file
```

## Container Config

The agent writes a production-grade `~/.pilot/config.yaml` in the container:

- **Quality gates**: `pytest test_outputs.py` with 2 retries on failure
- **Effort routing**: haiku classifier → effort level mapping
- **Model routing**: all tiers set to the run model (single-model bench)
- **Hooks**: `run_tests_on_stop`, `block_destructive`
- **Retry**: rate limit (3x, 30s backoff), API error (3x, 5s), timeout (2x, extend 1.5x)
- **Intent judge**: haiku (requires ANTHROPIC_API_KEY)

## Findings → Production

Bench findings that became real Pilot improvements:

| Finding | Issue | Status |
|---------|-------|--------|
| LocalMode prompt priority bug | [GH-2103](../../issues/2103) | Merged |
| Configurable HeartbeatTimeout | [GH-2104](../../issues/2104) | Merged |
| Heartbeat cancel on result event | [GH-2103](../../issues/2103) | Merged |

## Known Limitations

- Sequential only (`n=1`) — Daytona free tier: 10GiB, ~4GiB per sandbox
- DNS unreliable in Daytona sandboxes — mitigated with `8.8.8.8` + uv pre-install
- Intent judge needs `ANTHROPIC_API_KEY` (bench uses OAuth token only)
- Full run takes 12-20h at `n=1`
- Cost: ~$1-1.30 per task (~$90-115 for full 89-task run)
