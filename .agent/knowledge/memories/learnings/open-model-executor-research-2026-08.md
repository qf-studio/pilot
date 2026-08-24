---
name: open-model-executor-research-2026-08
description: 2026-08-24 deep research — swapping Pilot's executor to open-weight models (DeepSeek/Qwen/Kimi/Nemotron). Verdict — self-hosting is a 50-150x cost INCREASE at current scale; the real lever is supplier-switch via official Anthropic-compatible endpoints; quality gap on agentic failure modes still favors Sonnet 5.
type: learning
---
**Question**: Replace Sonnet 5 with a self-hosted (local/AWS) open model to "cut token cost to zero"?

**Verdict (2026-08-24)**: No at current scale. Three independent findings:

1. **Economics**: Pilot runs on a CC subscription (flat cost, 1-yr OAuth token). Cheapest rig serving a frontier open MoE at agentic latency (8×H200) ≈ $25.6k/mo RunPod / $46k/mo AWS on-demand. Break-even vs open-model APIs ≈ 5B tokens/mo (~$15-25k/mo API spend). Pilot is 2-3 orders of magnitude below. Local Mac fails on prompt-processing latency (5-20 min TTFT at 50-150k agentic contexts); post-hoc INT4 quantization measurably degrades tool-calling (arXiv 2607.27275).
2. **Supplier-switch is the real lever**: DeepSeek (`api.deepseek.com/anthropic`), Moonshot (`api.moonshot.ai/anthropic`), Qwen/DashScope, Z.AI, MiniMax all ship OFFICIAL Anthropic-protocol endpoints documented for Claude Code — `ANTHROPIC_BASE_URL` drop-in, no proxy. DeepSeek V4-Flash $0.22/$0.66 off-peak (~10-30× under Sonnet 5's $2/$10 API). Anthropic's 2026 crackdown targeted subscription-OAuth-in-third-party-harnesses, NOT Claude-Code-to-third-party-backends (unsupported but not blocked). Avoid generic proxies (LiteLLM /v1/messages, y-router) — active breakage with 2026 CC features.
3. **Quality**: no open model beats Sonnet 5 on neutral-harness agentic coding (Aug 2026). Kimi K3 is closest (vendor TB2.1 88.3 vs Sonnet 5 80.4) but Sonnet-priced ($3/$15 + forced reasoning tokens) and admits higher hallucination. Open-model failure modes — false-positive completion claims, tool-call formatting failures, long-task drift, silent serving-tier downgrades — are precisely what an unattended commit-and-PR pipeline can't absorb. Qwen3-Coder-Next (80B-A3B, Apache 2.0) = best self-host per-dollar at ~Sonnet-4.0 class. Nemotron = not an executor candidate (TB 54%).

**Pilot's seam already exists**: `executor.api_base_url`/`api_auth_token`/`default_model` inject ANTHROPIC_* into spawned claude (backend_claudecode.go:610-628); 5 backend types incl. openai-api/opencode. Residual work if ever swapping: model_routing defaults override default_model (backend.go:951, runner.go:1265); 7 aux subprocess sites hardcode claude-haiku-4-5/opus model ids; 3 duplicated pricing tables misprice non-Anthropic models as Sonnet; 4 direct-HTTP clients hardcode api.anthropic.com; epic.go:281 force-sets ANTHROPIC_MODEL.

**Why**: "own the GPUs = free tokens" ignores utilization — an idle $46k/mo box produces the most expensive tokens buyable; and Pilot's marginal token cost is already a flat subscription.

**How to apply**: Revisit only if (a) volume crosses ~5B tok/mo, or (b) an open model beats Sonnet on Terminal-Bench-class evals under a NEUTRAL harness. Cheap experiment if ever wanted: route `model_routing` trivial-tier to DeepSeek V4-Flash via its Anthropic endpoint, gate with pilot-bench + delivery-evidence checks ([[TASK-460]] class). Full report in session 2026-08-24; agent sources dated inline.
