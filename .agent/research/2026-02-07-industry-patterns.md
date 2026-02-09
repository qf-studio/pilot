# Industry Patterns for Background Coding Agents

**Date:** 2026-02-07
**Sources:** 14 URLs across Spotify, Ramp, Block, Coinbase, Shopify, GitHub, Microsoft, Anthropic, Jellyfish

---

## Executive Summary

Analyzed how 8 organizations build and deploy background coding agents. The market is converging on a shared architecture: **ticket → agent → PR → review → merge**, with differentiation in context engineering, verification, and enterprise governance. Pilot already has the core pipeline. The gaps are in verification depth, context optimization, and observability.

---

## Market Data (Jellyfish, 50K+ engineers tracked)

| Metric | Value | Trend |
|--------|-------|-------|
| Code assistant adoption | 49% → 69% (Jan-Oct 2025) | Plateauing ~73% |
| Code review agent adoption | 15% → 51% (Jan-Oct 2025) | **Fastest growing category** |
| PRs merged per engineer (high AI adoption) | +113% vs low adoption | |
| Cycle time reduction | -24% (16.7h → 12.7h) | |
| Bug fix PR ratio increase | 7.5% → 9.5% (+27% relative) | **AI creates more bugs** |
| Cursor agent adoption | 19.3% | Growing fast |
| Claude Code 20-week retention | 81% (vs 89% Copilot/Cursor) | CLI UX friction |

**Key insight:** Code review agents are growing 3x faster than code assistants. Bug-fix ratio increases with AI adoption — validates the need for quality gates.

---

## Architecture Convergence

All organizations converge on the same high-level architecture:

```
Ticket/Spec → Context Assembly → Agent Execution → Verification → PR → Review → Merge → Monitor
```

### Where They Differ

| Stage | Spotify | Ramp | Block/Goose | Coinbase | Microsoft | GitHub |
|-------|---------|------|-------------|----------|-----------|--------|
| **Trigger** | Git-stored prompts | Slack/Web/Chrome | CLI/Desktop | IDE (Cursor) | Spec Kit | @-mention |
| **Context** | Pre-condensed by humans | Repo classifier | .goosehints | Cursor rules | Spec-driven | Issue body |
| **Execution** | Claude Code (sandboxed) | OpenCode (Modal sandbox) | Local Rust agent | Cursor + MCP | Copilot Agent | Multi-agent |
| **Verification** | Deterministic + LLM Judge | Visual (Playwright) | Tool annotations | QA agent | CodeQL + PMD | Code Quality |
| **Scale** | 650+ PRs/month | 30% of merged PRs | 4,000 users | 1,500 engineers | Demo | Platform |

---

## Patterns Pilot Should Implement

### Tier 1: High Impact, Feasible Now

#### 1. LLM Judge (Diff Reviewer)
**Source:** Spotify (25% veto rate, 50% recovery)
**What:** After self-review passes, run a separate LLM pass comparing the diff against the original ticket/prompt. Catches semantic drift where tests pass but changes don't match intent.
**Pilot gap:** Self-review exists but reviews code quality, not intent alignment.
**Implementation:** Add `intentReview` step in runner.go between self-review and PR creation. Use Haiku for cost efficiency — compare `issue.Body` against `git diff`.

#### 2. Progressive Context Loading
**Source:** Anthropic Agent Skills, Spotify, Ramp
**What:** Three-tier loading: metadata at startup (cheap), full instructions on activation, resources on-demand. Spotify found context window overflow was a top failure mode.
**Pilot gap:** BuildPrompt() loads everything upfront. Navigator helps but isn't optimized for token budget.
**Implementation:** Restructure prompt building: Tier 1 (project summary, ~500 tokens) → Tier 2 (relevant .agent/ docs, loaded by intent) → Tier 3 (file contents, loaded by agent).

#### 3. Token Budget Engineering
**Source:** Goose (10-15K tokens burned on tool schemas alone), Spotify (verifier output parsing)
**What:** Tool schemas are expensive. Return plain text not JSON for logs. Parse verifier output to extract only relevant errors. Concise success messages.
**Pilot gap:** No token budget tracking or optimization in executor pipeline.
**Implementation:**
- Track token usage per execution phase (already have cost tracking)
- Optimize BuildPrompt() token count
- Strip verbose tool output in quality gate results

#### 4. Deterministic Verifier Abstraction
**Source:** Spotify (auto-detect verifiers from project files), Microsoft (keep CI deterministic)
**What:** Auto-detect available verifiers (pom.xml → Maven, package.json → npm test, go.mod → go test). Expose as opaque "verify" command. Agent doesn't need to know HOW verification works.
**Pilot gap:** Quality gates exist but require manual configuration. Auto-detection is partial (auto build gate).
**Implementation:** Extend auto build gate to auto-detect test runners, linters, formatters based on project files. Return parsed error output, not raw logs.

#### 5. Session Logging & Observability
**Source:** GitHub (complete session logging), Ramp (GCP log uploads + MLflow traces), Coinbase (DORA metrics)
**What:** Full execution logs accessible beyond PR diff. What the agent did, why, what it considered and rejected. DORA metrics (deployment frequency, lead time, failure rate) for ROI measurement.
**Pilot gap:** Execution logs go to stdout/dashboard but aren't persisted per-issue. No DORA-style metrics.
**Implementation:**
- Persist full execution log per issue (already have SQLite)
- Add execution report as PR comment (not just commit message)
- Track: issues/day, PR merge rate, cycle time, retry rate

### Tier 2: High Impact, Medium Effort

#### 6. Sandbox Snapshots / Pre-warming
**Source:** Ramp (30-min image rebuilds, snapshot on completion, restore in seconds)
**What:** First execution for a repo pays full clone+setup cost. Subsequent executions restore from filesystem snapshot. Pre-warm while user types.
**Pilot gap:** Every execution clones fresh and installs dependencies from scratch.
**Implementation:** Cache working directories per project. Snapshot after successful execution. Restore + git pull for subsequent runs. Major speedup for repeated work on same repo.

#### 7. Prompt Queuing
**Source:** Ramp (queue follow-ups, process in order)
**What:** When a new issue arrives while agent is executing, queue it rather than interrupt. Process in FIFO order. User can stack multiple issues.
**Pilot gap:** Sequential mode exists (wait for PR merge before next issue) but no queuing during execution.
**Implementation:** Already partially implemented via task dispatcher. Formalize queue visibility in dashboard.

#### 8. Multi-Client Architecture
**Source:** Ramp (Web + Slack + Chrome), Block (CLI + Desktop + MCP), Coinbase (IDE + Router)
**What:** Server-first agent runtime enables multiple client surfaces against one backend.
**Pilot gap:** Already has Telegram + GitHub + Slack + Dashboard. Gateway exists but isn't fully leveraged.
**Implementation:** Expose gateway API for additional clients (web UI, VS Code extension). The architecture already supports this.

#### 9. Competing Agent / Multi-Model Comparison
**Source:** GitHub (assign multiple agents, compare approaches), Block (multi-model benchmarking)
**What:** Run the same task with different model configurations. Compare PRs. Let human choose best approach.
**Pilot gap:** Single model per execution. Effort routing exists but doesn't compare approaches.
**Implementation:** For complex tasks, spawn 2 executions with different models/prompts. Present both PRs for review. Higher cost but better quality for critical changes.

#### 10. Enterprise Governance
**Source:** GitHub (access controls, audit logs, impact dashboards), Block (read-only vs destructive classification), Anthropic (skill registries)
**What:** Access controls per repo/team, audit trail of all agent actions, impact dashboards for engineering leaders.
**Pilot gap:** Single-user/single-team focus. No audit trail beyond git history.
**Implementation:**
- Audit log table in SQLite (who triggered, what happened, outcome)
- Role-based access (viewer, operator, admin) for multi-user deployments
- Impact dashboard (PRs created, merged, rejected, cycle time)

### Tier 3: Strategic, Longer Term

#### 11. Agent Skills / Recipe System
**Source:** Anthropic (SKILL.md directories), Goose (YAML recipes), Block (shared prompt libraries)
**What:** Portable, version-controlled procedural knowledge. Shareable task patterns. Community marketplace.
**Pilot gap:** Navigator's .agent/ directory is conceptually aligned but not formalized as a standard.
**Implementation:** Adopt Agent Skills spec for .agent/ structure. Add recipe support for common workflows (migration, refactor, test generation). Enable sharing between projects.

#### 12. QA Agent Integration
**Source:** Coinbase (75% accuracy, 300% more bugs found, 86% token cost reduction)
**What:** AI-powered QA that processes test scenarios in natural language, uses visual + textual data. Trades accuracy for coverage.
**Pilot gap:** Quality gates run existing tests but don't generate new test scenarios.
**Implementation:** Post-PR, run a QA agent that generates and executes test scenarios based on the diff. Report findings as PR comments.

#### 13. Outer Feedback Loop (CI → Agent)
**Source:** Spotify (planned), Microsoft (SRE agent monitors post-deploy)
**What:** Agent receives CI/CD results and can iterate. Post-deploy monitoring creates new issues automatically.
**Pilot gap:** Autopilot monitors CI and can create fix issues, but the loop isn't tight (30s poll, new issue creation).
**Implementation:** Tighten the CI feedback loop. When CI fails, feed error output directly back to agent in same session rather than creating a new issue. Already partially implemented in feedback_loop.go.

#### 14. Formal Evaluation Framework
**Source:** Spotify (acknowledged gap), Goose (`goose bench`), Coinbase (DORA)
**What:** Systematic benchmarking of agent performance across different configurations, models, and prompt strategies.
**Pilot gap:** No regression testing for agent quality. No benchmark suite.
**Implementation:** Build a `pilot bench` command that runs a set of known tasks against different configurations and measures success rate, token usage, and time.

---

## What Pilot Already Does Well (Validated by Industry)

| Pilot Feature | Industry Validation |
|---------------|-------------------|
| Ticket → PR pipeline | Every company uses this pattern |
| Self-review before PR | Spotify's LLM Judge, Microsoft's quality agent, Jellyfish bug-fix data |
| Autopilot (CI monitor + auto-merge) | Ramp, Microsoft — async-first with human-in-the-loop |
| Sequential processing | Ramp's prompt queuing validates ordered execution |
| Multi-adapter (Telegram/GitHub/Slack) | Ramp (Web/Slack/Chrome), Block (CLI/Desktop) |
| Effort routing (Haiku/Opus) | Block/Coinbase validate multi-model routing |
| Navigator integration (.agent/) | Block (.goosehints), Anthropic (SKILL.md), Microsoft (Spec Kit) |
| Epic decomposition | Spotify (one change per prompt validates atomic execution) |
| Quality gates | Universal — every company has deterministic verification |

---

---

## Codebase Audit Results (2026-02-07)

### What Pilot Already Has (validated by industry)

| Industry Pattern | Pilot Implementation | Status | Location |
|---|---|---|---|
| **Ticket → PR pipeline** | Full: GitHub/Linear/Jira/Asana → Claude Code → PR | ✅ Complete | runner.go, main.go |
| **Self-review before PR** | Checks build, undefined methods, unused fields, unwired config | ✅ Complete | runner.go:1763-1837 |
| **Quality gates (deterministic)** | Build/Test/Lint/Coverage/Security/TypeCheck/Custom | ✅ Complete | quality/*.go |
| **Auto-detect build command** | go.mod, package.json, Cargo.toml, pyproject.toml | ✅ Complete | quality/types.go:247-273 |
| **Retry with error feedback** | Feed gate failure output back to Claude, max 2 retries | ✅ Complete | runner.go:1099-1130 |
| **Complexity-based model routing** | Trivial→Haiku, Simple/Medium/Complex→Opus 4.6 | ✅ Complete | complexity.go, model_routing.go |
| **Effort routing** | Maps complexity to Claude API effort: low/medium/high/max | ✅ Complete | backend.go:243-259 |
| **Multi-adapter (8 platforms)** | GitHub, Linear, Slack, Telegram, Jira, GitLab, Azure DevOps, Asana | ✅ Complete | internal/adapters/* |
| **Autopilot CI monitor** | PRCreated→WaitingCI→CIPassed→Merging→Merged→PostMergeCI | ✅ Complete | autopilot/*.go |
| **CI failure → fix loop** | FeedbackLoop creates fix issue with branch metadata | ✅ Complete | feedback_loop.go |
| **Epic decomposition** | Detect epics, plan subtasks, create sub-issues, execute sequentially | ✅ Complete | epic.go, decompose.go |
| **Sequential execution** | One task per project, prevents git conflicts | ✅ Complete | dispatcher.go |
| **Approval workflows** | 3 stages (PreExec/PreMerge/PostFailure), multi-channel | ✅ Complete | approval/*.go |
| **Token tracking** | Real-time per-event accumulation from stream | ✅ Complete | runner.go:1950-1977 |
| **Cost estimation** | Haiku/Sonnet/Opus pricing per 1M tokens | ✅ Complete | runner.go:2461-2498 |
| **Budget enforcement** | Daily/monthly/per-task limits with warn/pause/stop | ✅ Complete | budget/enforcer.go |
| **Execution persistence** | Full output, tokens, cost, files changed in SQLite | ✅ Complete | memory/store.go |
| **PR comments** | Posts success/failure/warning on GitHub issues | ✅ Complete | main.go:1750-1796 |
| **CLI metrics** | summary, daily, projects, export (JSON/CSV) | ✅ Complete | cmd/pilot/metrics.go |
| **Dashboard TUI** | Tokens, cost, queue sparklines, autopilot panel, history | ✅ Complete | dashboard/tui.go |
| **Parallel research** | Up to 3 subagents for Medium/Complex pre-research | ✅ Complete | parallel.go |
| **Pattern context injection** | Learned patterns from memory (min 0.6 confidence, max 5) | ✅ Complete | context.go |
| **Navigator integration** | .agent/ detection, Navigator prompt for non-trivial tasks | ✅ Complete | runner.go:1643-1689 |
| **Gateway API** | WebSocket + REST + webhook endpoints | ✅ Complete | gateway/*.go |
| **Structured logging** | slog with JSON/text, rotation, contextual fields | ✅ Complete | logging/logging.go |
| **Webhook signature validation** | HMAC-SHA256 (GitHub), X-Hook-Secret (Asana) | ✅ Complete | adapters/*/webhook.go |

### What's Missing (gap analysis vs industry)

| Industry Pattern | Source | Gap in Pilot | Impact | Effort |
|---|---|---|---|---|
| **LLM Judge (intent alignment)** | Spotify (25% veto, 50% recovery) | Self-review checks code quality only, NOT whether diff matches original ticket intent | **Critical** — most dangerous failure mode is "CI passes but change is wrong" | Medium — add Haiku call comparing `issue.Body` vs `git diff` |
| **Workspace caching/snapshots** | Ramp (30-min rebuilds, instant restore) | Every execution works on live repo, no snapshot/restore | **High** — major speedup for repeated work on same repo | Medium — cache workdir per project, snapshot after success |
| **Progressive prompt loading** | Anthropic Skills (3-tier), Spotify | BuildPrompt loads Navigator prefix + task in one shot, no tiered loading | **Medium** — context window overflow is Spotify's top failure mode | Low — restructure BuildPrompt into tiers |
| **Token budget in prompt** | Goose (10-15K on schemas), Spotify (parsed verifier output) | No prompt-level token counting or optimization | **Medium** — savings up to 80% on tool outputs | Low — measure BuildPrompt token count, optimize |
| **Execution report on PR** | GitHub (full session logs), Ramp | Issue comment posted but minimal (duration, branch, PR URL only) — no token/cost/files breakdown | **Medium** — observability gap | Low — expand PR comment with metrics from ExecutionResult |
| **DORA metrics** | Coinbase, Jellyfish | Data exists in SQLite but no DORA dashboard (deploy freq, lead time, failure rate, MTTR) | **Medium** — ROI measurement for teams | Low — SQL queries over existing data |
| **MCP support** | Block/Goose (universal extension protocol) | No MCP server consumption or exposure | **Low now, High later** — ecosystem standard | High — significant architecture change |
| **Agent Skills / AGENTS.md** | Anthropic standard, Goose (.goosehints) | Pilot reads CLAUDE.md only, not AGENTS.md or SKILL.md | **Low** — interoperability gap | Low — read AGENTS.md if present |
| **Visual verification** | Ramp (Playwright screenshots) | No visual verification of frontend changes | **Low** — useful for frontend tasks | Medium — integrate Playwright |
| **`pilot bench`** | Goose (goose bench), Spotify (acknowledged gap) | No benchmarking/evaluation framework | **Medium** — can't measure quality regressions | Medium — run known tasks, measure success |
| **Multi-model comparison** | GitHub (competing agents) | Single execution per task | **Low** — nice-to-have for critical tasks | High — doubles cost, complex UX |
| **Read-only vs destructive tool classification** | Block/Goose | No tool annotation or classification | **Low** — Pilot controls what Claude Code can do via sandbox | Low — informational |
| **Multiplayer sessions** | Ramp (multi-user per session) | Single-user execution model | **Low** — enterprise feature | High — architecture change |
| **Prompt queuing during execution** | Ramp (queue follow-ups) | Sequential mode waits for merge, not just for execution | **Low** — existing dispatcher handles this | Low — already works via task queue |

---

## Revised Priority Roadmap

### Phase 1: Verification & Observability (v0.24.x)
1. **LLM Judge** — Haiku call comparing issue body vs git diff after self-review
   - Add `runIntentReview()` in runner.go between self-review and PR creation
   - Veto if diff doesn't match intent, feed feedback back for retry
   - Target: ~25% veto rate (Spotify benchmark)
2. **Rich PR comment** — Expand issue comment with token/cost/files/model breakdown
   - Data already in ExecutionResult, just format and post
3. **Read AGENTS.md** — If present in target repo, include in prompt
   - Simple file check + append to BuildPrompt

### Phase 2: Performance (v0.25.x)
4. **Workspace caching** — Snapshot workdir after successful execution, restore on next run
   - `git stash` + tarball approach, or rsync-based
   - Major speedup for repos with heavy dependencies
5. **Token budget tracking** — Measure BuildPrompt token count, log per-execution
   - tiktoken estimation or Anthropic token counting
   - Identify optimization opportunities
6. **Auto-detect test runners** — Extend auto build gate to also detect test/lint commands
   - go.mod→`go test ./...`, package.json→`npm test`, Cargo.toml→`cargo test`

### Phase 3: Metrics & Enterprise (v0.26.x)
7. **DORA dashboard** — Deploy frequency, lead time, change failure rate in TUI + CLI
   - SQL queries over existing executions table
8. **`pilot bench`** — Run a set of known tasks, measure success rate/tokens/time
   - Regression testing for prompt/model changes
9. **Audit log table** — Dedicated table for all agent actions with timestamps

### Phase 4: Ecosystem (v0.27.x+)
10. **MCP support** — Consume MCP servers for tool integrations
11. **QA agent** — AI test generation post-PR
12. **Visual verification** — Playwright screenshots for frontend tasks

---

## Key Metrics to Track (from Jellyfish)

| Metric | Baseline (measure now) | Target |
|--------|----------------------|--------|
| PRs merged per week | ? | +100% |
| Median cycle time (issue → merge) | ? | -25% |
| Bug fix PR ratio | ? | <10% |
| Self-review catch rate | ? | >20% |
| LLM Judge veto rate | N/A | ~25% (Spotify benchmark) |
| Execution success rate | ? | >80% |
| Token cost per PR | ? | -30% via optimization |

---

## Sources Analyzed

| Organization | Key Contribution |
|---|---|
| **Spotify** | Two-layer verification (deterministic + LLM Judge), context engineering principles, 650 PRs/month scale |
| **Ramp** | Control/data plane separation, sandbox snapshots, prompt queuing, 30% PR adoption |
| **Block** | MCP as extension protocol, token budget engineering, 40% company-wide adoption |
| **Coinbase** | QA agent (3x bug detection), DORA metrics, deploy-day-one culture |
| **Shopify** | Latency compounds in tool chains, uptime as product |
| **GitHub** | Multi-agent comparison, enterprise governance, async-first execution |
| **Microsoft** | Spec-driven development, keep CI deterministic, sub-agent specialization |
| **Anthropic** | Progressive context loading (3-tier skills), complementary to MCP |
| **Jellyfish** | Market data: +113% PR throughput, -24% cycle time, +27% bug-fix ratio |
