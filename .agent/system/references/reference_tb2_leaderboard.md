---
name: TB 2.0 Leaderboard & Competitive Landscape
description: Terminal Bench 2.0 leaderboard scores, top agent techniques, and scaffold impact data as of April 2026.
type: reference
originSessionId: 33a7ad30-b5e9-49dd-b7a5-bf0e6a562c99
---
## Leaderboard (April 2026)

| Agent | Model | Score | Notes |
|-------|-------|-------|-------|
| ForgeCode | GPT-5.4 | 81.8% | Harness engineering only |
| ForgeCode | Opus 4.6 | 81.8% | Same score, different model |
| TongAgents | Gemini 3.1 Pro | 80.2% | |
| SageAgent | GPT-5.3-Codex | 78.4% | |
| Claude Code raw | Opus 4.6 | 58.0% | No scaffold |

## Proven Techniques (with measured impact)

- **Mandatory planning enforcement** (ForgeCode): +28 pts (38%→66%)
- **Build-Verify-Fix loop** (LangChain): +13.7 pts (52.8%→66.5%)
- **Reasoning sandwich** (ForgeCode): high thinking for planning, low for impl, high for verification
- **Non-interactive mode**: eliminate clarification requests
- **Tool naming optimization**: `old_string`/`new_string` naming drops error rates
- **Loop detection**: track edit counts, inject "reconsider" prompts

## Key URLs

- Leaderboard: https://www.tbench.ai/leaderboard/terminal-bench/2.0
- ForgeCode blog: https://forgecode.dev/blog/benchmarks-dont-matter/
- LangChain harness: https://blog.langchain.com/improving-deep-agents-with-harness-engineering/
- Submission repo: https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard
