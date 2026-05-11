---
name: strategy_pilot_positioning
description: Pilot positioning — not a CC replacement, an orchestration layer that makes CC 25 points better. Infrastructure play for Anthropic ecosystem.
type: project
---

## Pilot Strategy: Enrich, Don't Replace

**Core thesis:** Pilot is infrastructure that makes Claude Code 25 points better on benchmarks. Don't compete with CC — orchestrate it.

### What Anthropic Builds
- The AI brain (Claude models)
- The CLI (Claude Code)

### What Pilot Builds (The Nervous System)
- Tickets come in → Pilot routes to the right model
- CC executes → Pilot runs quality gates
- PR created → Pilot monitors CI, auto-merges
- Task fails → Pilot learns, retries with different strategy
- Patterns accumulate → every task makes the next one better

### The Proof
- Claude Code alone: 58% on Terminal Bench 2.0
- Claude Code + Pilot: 85% on Terminal Bench 2.0
- Same model, same API — the delta is Pilot's orchestration layer

### Business Models
1. **Anthropic acquisition/partnership** — Pilot becomes CC's enterprise orchestration layer
2. **Independent infrastructure** — like Vercel to Next.js, Pilot to Claude Code
3. **Enterprise licenses** — companies pay for Pilot to orchestrate their CC usage
4. **Managed service** — Pilot-as-a-service, hosted orchestration

### Positioning
- Source-available (not MIT) — free for individuals, paid for companies
- Distribution: `npm install -g pilot` or `brew install pilot`
- Not "another coding agent" — the orchestration layer that makes any coding agent better
- Multi-backend: CC today, but architecture supports any LLM backend

### What Pilot Has Over CC
- Multi-source orchestration (Linear/Jira/GitHub/Telegram/Discord/Slack)
- Autopilot (CI monitor, auto-merge, release pipeline)
- Model routing across complexity levels
- Self-improvement (learning DB, pattern extraction, knowledge graph)
- Epic decomposition into subtasks
- Quality gates with retry
- 85% Terminal Bench vs CC's 58%

### Inspiration
- Boris Cherny built CC inside Anthropic
- Pilot enriches CC from the outside — proving the ecosystem value
- The benchmark score opens the door, the product keeps it open
