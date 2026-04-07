# TASK-09: Docs Website Update v2.56.0

**Status**: 🚧 In Progress
**Created**: 2026-03-04

---

## Context

**Problem**:
Docs site header shows v2.38.11, content is stale across multiple pages. Major features from v2.39.0–v2.56.0 are undocumented: self-improvement system (v3 roadmap), outcome-based model routing, expanded pattern extractors, knowledge graph, GitHub Projects V2 board sync, merged PR guard.

**Goal**:
5 GitHub issues for Pilot to bring docs to v2.56.0 parity.

**Related**:
- TASK-08: Docs coverage gaps (v2.38.11 → v2.53.0) — partially overlapping
- #2026: Version sync auto-merge (CI fix, already filed)
- ROADMAP-V3-SELF-IMPROVEMENT.md: Phase 1-3 shipped, Phase 4 queued

---

## Implementation Plan

### Issue 1: Version string sweep
**Goal**: Update all stale version references across 63 docs pages

**Scope**:
- `docs/app/layout.tsx` — navbar badge v2.38.11 → v2.56.0
- `docs/content/getting-started/quickstart.mdx` — install/version refs
- `docs/content/getting-started/installation.mdx` — version refs
- `docs/content/getting-started/configuration.mdx` — example versions
- Any other page with hardcoded version strings
- Feature count: 240+ → 260+

### Issue 2: New page — `features/self-improvement.mdx`
**Goal**: Document the v3 self-improvement system (Pilot's differentiator)

**Content**:
- Overview: 20 learning mechanisms, self-evolving pipeline
- Anti-pattern injection: how patterns are injected into execution prompts
- Self-review pattern checks: learned patterns validated during code review
- CI failure learning: error pattern extraction from CI logs
- Acceptance criteria verification: structured AC checklist in self-review
- Pattern extractors: 11 categories (API, concurrency, config, test, performance, security, etc.)
- Configuration: learning-related config options

**Add to `docs/content/features/_meta.js`** as new nav entry.

### Issue 3: Update `concepts/model-routing.mdx`
**Goal**: Add Sonnet 4.6 defaults + outcome-based routing

**Content to add**:
- Default model routing: Haiku (trivial), Sonnet 4.6 (simple/medium), Opus 4.6 (complex)
- Outcome-based escalation: `model_outcomes` SQLite table, failure rate tracking
- Auto-escalation: Haiku → Sonnet → Opus when failure rate > 30%
- Model ID updates: `claude-sonnet-4-6`, `claude-opus-4-6`
- Cost implications: Sonnet 4.6 is 40% cheaper than Opus, near-Opus quality

### Issue 4: Update `features/memory.mdx`
**Goal**: Add pattern learning, CI failure extraction, knowledge graph sections

**Content to add**:
- Pattern learning from PR reviews: `LearnFromReview()`, confidence boosting
- CI failure pattern extraction: error categorization, auto-learning from logs
- Self-review pattern extraction: findings feed back to learning system
- Knowledge graph: `graph.AddLearning()` / `graph.GetRelated()` (Phase 4 — note as upcoming)
- Temporal pattern relevance: per-project confidence, recency decay (Phase 4 — note as upcoming)
- Database indexes for pattern queries

### Issue 5: Update `integrations/github.mdx`
**Goal**: Add Projects V2 board sync + merged PR guard

**Content to add**:
- GitHub Projects V2 board sync: GraphQL mutations, lazy ID resolution, org-first discovery
- Board columns: Review, Done, Failed — auto-moved by autopilot
- Merged PR guard: poller checks for existing merged PRs before dispatch
- PR filtering: poller skips pull requests from issues API (upcoming fix)
- Auto-delete branches: remote branch cleanup after PR merge/close

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| New page vs section | Add to memory.mdx vs new page | New `self-improvement.mdx` | Self-improvement is a distinct concept, not just memory |
| Knowledge graph docs | Document now vs wait | Document as "upcoming" | Phase 4 queued but not shipped yet |
| Version sweep approach | Manual vs scripted | Pilot task | Consistent with workflow — Pilot handles execution |

---

## Dependencies

**Requires**:
- [x] Phase 1-3 of v3 roadmap shipped (provides content to document)

**Blocks**:
- [ ] Threads/social content (needs docs links to reference)

---

## Done

- [ ] 5 GitHub issues filed with `pilot` label
- [ ] Pilot executes all 5
- [ ] PRs reviewed and merged
- [ ] Docs site reflects v2.56.0 content

---

**Last Updated**: 2026-03-04
