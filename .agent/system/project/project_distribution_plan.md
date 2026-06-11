---
name: Distribution Plan
description: Channel strategy + June 30 AI Coding Summit funnel; X/Reddit/HN over Threads/LinkedIn; YT low-production only
type: project
---
(Captured 2026-06-10 — interactive session. Owner is time-poor (~2h/week); every item is ranked by leverage-per-hour.)

## Decisions made

- **Drop Threads** entirely; LinkedIn passive cross-post only (re-activate if chasing Harness-style enterprise segment later).
- **Primary channels: X + Reddit + HN** — where the bottom-up indie-dev segment lives (Hermes' 189K-star growth is the proof, see Competitive Landscape).
- **Content engine = dogfooding war stories.** Pilot ships its own tickets; `.agent/tasks/` incidents (GH-3513 decomposition, phantom v2.181.0, 14h20m retry loop) are pre-written posts with receipts. Use `pilot-copywriter` skill to draft.
- **YT: low-production only.** "Pilot ships its own tickets" screencast series — raw dashboard/terminal recording of real runs, time-lapsed 3-6 min, ≤1h/episode hard limit. No produced tutorials. Doubles as Show HN demo material + long-tail search.
- **Podcasts > YT** for reach-per-hour: 60 min, zero production, borrowed audience. Pitch: "my agent ships its own tickets — here's what broke."
- **Benchmark claims scrubbed** (delisted from Terminal-Bench 2.0): docs via issue #3546, repo description fixed 2026-06-10. Never carry falsifiable stale claims into a launch.

## June 30 — AI Coding Summit funnel (deadline-driven, top priority)

1. Live ticket→PR demo, rehearsed; lead with dogfooding *failure* stories (every other talk shows happy paths).
2. One short memorable URL on every slide → repo/landing. Conversion event = star/install.
3. **Show HN the same week** as the talk — demo + README are forced-polished anyway; compound the week. Title shape: "Show HN: Pilot — my AI agent has been shipping its own tickets for N months."
4. Hallway track = podcast/newsletter booking engine.

## One-time backlog

- [ ] JSNation recording (spoke 2026-06): obtain from GitNation, clip 2-3 segments for X, add Talks section to README/docs, seed YT channel.
- [ ] PR Pilot into awesome lists: `awesome-harness-engineering`, `awesome-claude-code`, awesome-ai-agents (~10 min each; "harness engineering" term is hot this quarter).
- [ ] Self-marketing pipeline: weekly Pilot job — merged PRs + incidents → `pilot-copywriter` draft → Telegram approve/post. ("My agent drafts its own changelog posts" is itself a post.)

## Ongoing cadence (~2h/week)

One war-story post on X; cross-post to Reddit when genuinely story-worthy (r/ClaudeAI first, then r/ChatGPTCoding, r/selfhosted, r/SideProject). Format rule: post the story, mention the tool once, answer every comment.

**Why:** Product is ahead, distribution is the bottleneck (Competitive Landscape memo, confirmed by Hermes datapoint). The "agent that ships your tickets, controlled from your messenger" window is open but contested.

**How to apply:** When weighing time allocation, deadline-driven funnel work (June 30) wins; everything else fits the 2h/week cadence or doesn't happen.
