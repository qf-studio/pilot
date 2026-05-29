# Pilot: AI That Ships Your Tickets

**Navigator plans. Pilot executes.**

## Who is reading this file?

This project ships an autonomous executor (Pilot) that runs Claude Code
against this very repo to implement tickets. That means this `CLAUDE.md`
is read by two very different kinds of sessions:

1. **Pilot-executor sessions** — spawned by `pilot start` to implement a
   specific GitHub issue. The prompt describes a concrete task and expects
   code changes, a commit, and a PR. **In these sessions, YOU ARE Pilot.
   Implement the task directly. The "Navigator + Pilot pipeline" rules in
   the next section DO NOT apply — you are the execution leg of that
   pipeline.** Signals you're in this mode:
   - Prompt begins with `GitHub Issue #NNN:` or `Task:`
   - No interactive user is following up
   - CWD is inside a pilot worktree or a branch named `pilot/GH-*`
2. **Interactive dev sessions** — a human developer is planning or
   reviewing work on the Pilot project itself. In these, follow the
   Navigator + Pilot pipeline below.

When in doubt, look at the incoming prompt: if it hands you a specific
task with file paths and expected outputs, implement it. If it's a human
asking open-ended questions about the project, plan via Navigator.

## ⚠️ Git & Worktree Discipline (ALL sessions)

Multiple sessions (interactive terminals, Claude Code sessions, and the
Pilot daemon) operate on this repo **concurrently**. Running `git checkout`
in the shared repo root rips the branch out from under every other session
— this has repeatedly left the root on a stranger's PR branch with orphaned
uncommitted changes and a graveyard of stashes.

**Rules:**

- ❌ **NEVER `git checkout <branch>` / `git switch` in the repo root**
  (`/Users/.../startups/pilot`). Keep the root pinned to `main`; treat it as
  reference + build-from-main only.
- ✅ **Do all branch work in your own worktree.** Interactive Claude sessions:
  use the worktree flow (sessions land in `.claude/worktrees/<name>`). The
  Pilot daemon already isolates via `pilot-worktree-GH-*` — leave those alone.
- ✅ Base worktrees on `origin/main` (fresh), not on whatever the root
  happens to be pointing at.
- ✅ Commit in your worktree branch; push it; open a PR. Never pile unrelated
  work onto someone else's branch/PR.
- ❌ Do not `git stash pop` blindly in the root — a pathspec-limited stash or
  a worktree is safer when other sessions may have uncommitted work there.
- If you find the root on a non-`main` branch with uncommitted changes you
  did not make, **STOP** — that's another session's work. Don't checkout,
  don't reset, don't commit it. Flag it.

## ⚠️ WORKFLOW: Navigator + Pilot Pipeline (interactive sessions only)

**If this is an interactive dev session**, use Navigator to plan and Pilot
to execute:

| Phase | Tool | Action |
|-------|------|--------|
| 1. Plan | `/nav-task` | Design solution, create implementation plan |
| 2. Execute | GitHub Issue | Create issue with `pilot` label |
| 3. Review | PR Review | Check Pilot's PR, request changes if needed |
| 4. Ship | Merge | Merge PR when approved |

### Quick Commands

```bash
# Plan a feature (Navigator)
/nav-task "Add rate limiting to API endpoints"

# Hand off to Pilot
gh issue create --title "Add rate limiting" --label pilot --body "..."

# Check Pilot's queue
gh issue list --label pilot --state open

# Review and merge
gh pr view <number> && gh pr merge <number>
```

### Rules (interactive sessions)

- ✅ Use `/nav-task` for planning and design
- ✅ Create GitHub issues with `pilot` label for execution
- ✅ Review every PR before merging
- ❌ In *interactive* sessions, do not write code directly — defer to
  Pilot so the knowledge graph and quality gates run
- ❌ Do not make commits manually from an interactive planning session
- ❌ Do not create PRs manually from an interactive planning session

Pilot-executor sessions are the exception: they MUST write code, commit,
and push — that's their entire job.

**Pilot runs in a separate terminal** (`pilot start --telegram --github`) and auto-picks issues labeled `pilot`.

---

## Memory: Navigator only (auto-memory disabled for this project)

**This project uses Navigator's memory system as the single source of truth for persistent knowledge.** The Claude Code auto-memory system at `~/.claude/projects/-Users-aleks-petrov-Projects-startups-pilot/memory/` is **deprecated for new writes** in this project.

**Rules:**

- ❌ **Do not write to** `~/.claude/projects/.../memory/MEMORY.md` or any file under that directory. Treat the auto-memory `MEMORY.md` index as read-only legacy context.
- ❌ Do not invoke the auto-memory "save a memory" flow described in the user's global instructions (the `user`/`feedback`/`project`/`reference` taxonomy under `~/.claude/projects/...`).
- ✅ **Write all new memory to Navigator:**
  - **Experiential knowledge** (patterns, pitfalls, decisions, learnings) → `.agent/knowledge/memories/{type}s/{slug}.md`, with the entry indexed in `.agent/knowledge/graph.json`. The four types are `pattern`, `pitfall`, `decision`, `learning` (see `nav-graph` skill / `memory_writer.py` for the file template).
  - **Architecture / long-lived system docs** → `.agent/system/{topic}.md`
  - **Operational procedures** → `.agent/sops/{category}/{slug}.md`
  - **Active task plans** → `.agent/tasks/TASK-{N}-{slug}.md`; archive to `.agent/tasks/archive/` when done
  - **Session checkpoints** → `.agent/.context-markers/{date}_{slug}.md`
- ✅ **Read both** during sessions. The legacy auto-memory `MEMORY.md` index is still loaded into context automatically and contains months of accumulated nuance — use it as you would Navigator memory, but route any *new* entry to Navigator.
- ✅ When you would have updated a legacy auto-memory file, instead:
  1. Find the equivalent Navigator location (or create one),
  2. Write the new entry there,
  3. Optionally leave a `→ moved to .agent/knowledge/memories/...` pointer in the old file so future searches don't miss the update.
- ✅ Use `nav-graph` skill for graph queries (`/nav-graph "what do we know about X?"`) rather than grepping the auto-memory directory.

**Migration status:** legacy auto-memory contents are being ported into Navigator. Until that migration finishes, treat the auto-memory directory as a frozen archive — readable, not writable.

---

## Project Overview

Pilot is an autonomous AI development pipeline that:
- Receives tickets from Linear/Jira/Asana
- Plans and executes implementation using Claude Code
- Creates PRs and notifies via Slack
- Learns patterns across projects

## Quick Start

```bash
# Build
make build

# Run
./bin/pilot start

# Or development mode
make dev
```

## Architecture

```
Gateway (Go)      → WebSocket control plane + HTTP webhooks
Adapters          → Telegram, GitHub, GitLab, Azure DevOps, Linear, Jira, Slack
Executor          → Claude Code process management + Navigator integration
Autopilot         → CI monitoring, auto-merge, feedback loop, release pipeline
Memory            → SQLite + knowledge graph
Dashboard         → Terminal UI (bubbletea)
```

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── gateway/         # WebSocket + HTTP server
│   ├── adapters/        # Telegram, GitHub, GitLab, AzureDevOps, Linear, Jira, Slack
│   ├── executor/        # Claude Code runner + intent judge
│   ├── autopilot/       # CI monitor, auto-merge, release pipeline
│   ├── alerts/          # Alert engine + multi-channel dispatch
│   ├── memory/          # SQLite + knowledge graph
│   ├── config/          # YAML config
│   ├── dashboard/       # TUI (bubbletea)
│   └── testutil/        # Safe test token constants
├── docs/                # Nextra v4 documentation site
└── .agent/              # Navigator docs
```

## Code Standards

- **Go**: Follow standard Go conventions, `go fmt`, `golangci-lint`
- **Python**: PEP 8, type hints, dataclasses
- **Architecture**: KISS, DRY, SOLID
- **Testing**: Table-driven tests for Go

## Test Token Guidelines

When writing tests that need API tokens or secrets:

- ❌ **DON'T** use realistic patterns that trigger GitHub push protection:
  - `xoxb-123456789012-1234567890123-abcdefghij` (Slack)
  - `sk-abcdefghijklmnopqrstuvwxyz123456` (OpenAI)
  - `ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx` (GitHub PAT)
  - `AKIAIOSFODNN7EXAMPLE` (AWS)

- ✅ **DO** use obviously fake tokens:
  - `test-slack-bot-token`
  - `fake-api-key`
  - `test-github-token`

- ✅ **DO** use constants from `internal/testutil/tokens.go`:
  ```go
  import "github.com/qf-studio/pilot/internal/testutil"

  token := testutil.FakeSlackBotToken
  ```

**Why?** GitHub's push protection blocks realistic-looking secrets even in test files. 9 branches were blocked for hours due to this.

## Key Commands

```bash
make build          # Build binary
make dev            # Run in dev mode
make test           # Run tests
make lint           # Run linter
make fmt            # Format code
make install-hooks  # Install git pre-commit hooks
make check-secrets  # Check for secret patterns in tests
```

## Configuration

Config file: `~/.pilot/config.yaml`

Key per-adapter env vars (only what each adapter needs):
- `GITHUB_TOKEN` — GitHub polling + PR creation
- `LINEAR_API_KEY` — Linear webhook adapter
- `SLACK_BOT_TOKEN` — Slack Socket Mode adapter
- `TELEGRAM_BOT_TOKEN` — Telegram adapter

Full reference: `configs/pilot.example.yaml`

## Commit Guidelines

- Format: `type(scope): description`
- Types: feat, fix, refactor, test, docs, chore
- Reference tasks: `feat(gateway): add webhook handler TASK-01`

## Navigator Integration

This project uses Navigator for planning, Pilot for execution:

```bash
/nav-start              # Start session, load context
/nav-task "feature"     # Plan implementation
gh issue create ...     # Hand off to Pilot
```

Documentation in `.agent/`:
- `DEVELOPMENT-README.md` - Navigator index
- `tasks/` - Implementation plans
- `system/` - Architecture docs

## Forbidden Actions

- ❌ No secrets in code
- ❌ No package.json modifications without approval
- ❌ No bulk doc loading (use Navigator lazy loading)
- ❌ No Claude Code mentions in commits
- ❌ No `git checkout`/`git switch` in the repo root — work in a worktree (see "Git & Worktree Discipline")

## Development Workflow

1. Start Navigator: `/nav-start`
2. Plan feature: `/nav-task "description"`
3. Create issue: `gh issue create --title "..." --label pilot --body "..."`
4. Wait for Pilot to execute and create PR
5. Review PR: `gh pr view <n>`
6. Merge when ready: `gh pr merge <n>`

## Current Status

See `docs/lib/version.ts` for the current release and `.agent/DEVELOPMENT-README.md` § "Current State" for recent feature history.

## Documentation Maintenance

Keep `.agent/*.md` files lean. Alternative locations for long-lived content:

- **Changelog / release notes** → `git log` or GitHub Releases
- **Architectural decisions** → `.agent/system/` (one file per decision)
- **Completed task history** → `.agent/tasks/archive/`
- **Active task plans** → `.agent/tasks/` (remove when merged)

Rules:

- Do **not** append to `## Recent` blocks — replace the block content instead.
- Do **not** let any `.agent/*.md` section grow append-only; prune or archive instead.

<!-- GitHub integration verified -->
