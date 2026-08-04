# Pilot Architecture

**Last Updated:** 2026-08-04 (v2.253.0) — TASK-441 Leg 8 refresh. Previous revision
(2026-05-26/2026-03-04) had drifted: 4 of its 6 documented DB tables were fictional
(`task_queue`, `processed`, `milestones`, `board_state` never existed; see
"Database Schema" below), and 9 packages that now exist on disk went undocumented.
This revision was written against source, not against the prior doc.

## System Overview

Pilot is a Go-based autonomous AI development platform that:
- Receives tickets from GitHub, Linear, Jira, Asana, GitLab, AzureDevOps, Slack, Telegram, Discord, Plane
- Plans and executes implementation using Claude Code, Qwen Code, or OpenCode
- Creates branches, commits, and PRs with optional self-review
- Monitors CI, auto-merges, and deploys via environment-specific pipelines
- Learns patterns from PR reviews and applies them to future tasks
- Provides TUI dashboard and web/desktop dashboards for monitoring

```
                     ┌─────────────────────────────────────────┐
                     │              CLI (cmd/pilot)            │
                     │ start | task | telegram | github | ...  │
                     └─────────────────┬───────────────────────┘
                                       │
         ┌─────────────────────────────┴─────────────────────────────┐
         │                                                           │
         ▼                                                           ▼
┌─────────────────────────┐                          ┌──────────────────────┐
│   Polling Mode          │                          │   Gateway Mode       │
│  (daemon background)    │                          │  (HTTP + WebSocket)  │
│                         │                          │                      │
│  • GitHub poller        │                          │  • Inbound webhooks  │
│  • Linear/Jira/Asana    │                          │  • Web dashboard     │
│  • GitLab/AzureDevOps   │                          │  • Desktop app API   │
│  • Telegram polling     │                          │  • Slack Socket Mode │
│  • Dashboard TUI        │                          │  • Discord WebSocket │
└─────────┬───────────────┘                          └──────────┬───────────┘
          │                                                     │
          └──────────────────────┬──────────────────────────────┘
                                 │
                    ┌────────────▼───────────┐
                    │    Task Dispatcher     │
                    │  (per-project queue)   │
                    └────────────┬───────────┘
                                 │
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
        ▼                        ▼                        ▼
┌─────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│    Executor     │  │     Memory       │  │  Pattern Engine  │
│ • Claude/Qwen   │  │ SQLite + Graph   │  │ Learn from PRs   │
│ • Runner + Git  │  │ Task History     │  │ PR review inject │
│ • Quality Gates │  │ Cross-project    │  │                  │
│ • Autopilot CI  │  │ Patterns         │  │                  │
│ • Self-review   │  └──────────────────┘  └──────────────────┘
└─────────────────┘
```

## Data Flow

### Task Execution Flow (Primary)

```
   Webhook/Polling              GitHub Issue / Telegram / Linear / etc
        │                                    │
        └────────────┬───────────────────────┘
                     │
                     ▼
          ┌──────────────────────┐
          │   Issue Handler      │
          │ (common logic for    │
          │  all adapters)       │
          └────────┬─────────────┘
                   │
                   ▼
          ┌──────────────────────┐
          │   Task Dispatcher    │
          │ (per-project queue + │
          │  parallel execution) │
          └────────┬─────────────┘
                   │
                   ▼
          ┌──────────────────────┐
          │  Runner.Execute()    │
          │ (subprocess + stream │
          │  -json parsing)      │
          └────────┬─────────────┘
                   │
    ┌──────────────┼──────────────┐
    │              │              │
    ▼              ▼              ▼
 Git Ops    Claude Code    Progress/Alerts
 (branch,   (stream-json    (lipgloss,
  commit,    events)         slog)
  PR)
```

### Autopilot CI Pipeline

```
┌───────────────────────────────────────────────────────────────┐
│ PR Created                                                    │
│ ├─ WaitingCI: Poll GitHub for CI checks                      │
│ ├─ CIPassed: All checks passed                               │
│ │  └─ Self-review: Optional code review before merge         │
│ ├─ Merging: Rebase/squash and merge branch                   │
│ ├─ Merged: Branch deleted, monitoring ends                   │
│ ├─ PostMergeCI: Optional CI after merge (tag/deploy)         │
│ └─ (Merge conflict: Auto-rebase via GitHub API)              │
└───────────────────────────────────────────────────────────────┘
```

**Real stage vocabulary** (`PRStage`, `internal/autopilot/types.go:830-854`, persisted
in `autopilot_pr_state` — frozen table, see "Database Schema"): `pr_created`,
`waiting_ci`, `ci_passed`, `ci_failed`, `awaiting_approval`, `merging`, `merged`,
`post_merge_ci`, `releasing`, `review_requested`, `failed` — 11 stages. `AllPRStages()`
(`types.go`) enumerates all of them so the Prometheus exporter can emit zero-values for
absent stages, working around Prometheus's 5-min lookback holding stale non-zero
values. The diagram above is illustrative, not exhaustive — it predates
`awaiting_approval`, `releasing`, and `review_requested`.

### Environment Config System (v1.59.0+)

```
┌─────────────────────────────────────────┐
│  Autopilot Environment Config           │
├─────────────────────────────────────────┤
│ EnvironmentConfig per environment:      │
│  • dev:    Skip CI, no approval         │
│  • stage:  Wait for CI, auto-merge      │
│  • prod:   Wait for CI, require approval│
│  • custom: User-defined via YAML        │
├─────────────────────────────────────────┤
│ Post-merge actions:                     │
│  • Webhook trigger                      │
│  • Branch push deployment               │
│  • Tag-based release                    │
└─────────────────────────────────────────┘
```

## Multi-Executor Backend System (v1.9.0+)

Pilot supports multiple AI execution backends:

| Backend | Status | Model Family | Use Case | Notes |
|---------|--------|--------------|----------|-------|
| Claude Code | Primary | Claude 3.x | Default, best for complex tasks | v1.0+ |
| Qwen Code | Supported | Qwen 2.5 | Cost-sensitive, simpler tasks | v1.9.0+ |
| OpenCode | Future | Alibaba CodeStudio | On-device inference | Placeholder |

**Backend Selection:**
- Config-driven: `executor.backend: claude|qwen|opencode`
- Model selection: Route Haiku (trivial) → Sonnet 4.6 (medium) → Opus 4.6 (complex)
- Preflight validation: Backend CLI check (v1.39.0)

## Package Architecture

### Core Packages (Wired in main.go)

| Package | Purpose | Key Files | Version Added |
|---------|---------|-----------|----------------|
| `pilot` | Top-level orchestration | `pilot.go` | v0.1 |
| `executor` | Claude/Qwen process management | `runner.go`, `git.go`, `backends.go` | v0.1 |
| `config` | YAML configuration loading | `config.go`, `schema.go` | v0.1 |
| `memory` | SQLite + knowledge graph | `store.go`, `graph.go`, `patterns.go` | v0.1 |
| `logging` | Structured slog logging | `logger.go` | v0.1 |
| `alerts` | Event-based alerting + dead-man liveness tracking | `engine.go`, `dispatcher.go`, `deadman.go` | v0.1 (`DeadManTracker` added TASK-441 L2, PR#4712) |
| `quality` | Quality gates (test/lint) | `executor.go`, `gates.go` | v0.1 |
| `dashboard` | Bubbletea TUI | `tui.go` | v0.1 |
| `gateway` | HTTP + WebSocket server | `server.go`, `router.go` | v0.1 |
| `autopilot` | CI monitor, auto-merge, deploy | `controller.go`, `auto_merger.go` | v0.3 |
| `briefs` | Daily/weekly summaries | `generator.go` | v0.1 |
| `replay` | Execution recording viewer | `player.go` | v0.1 |
| `upgrade` | Self-update mechanism | `upgrader.go` | v0.1 |

### Adapter Packages (v2.30.0+: Common Registry)

| Package | Purpose | Status | Added |
|---------|---------|--------|-------|
| `adapters/github` | GitHub Issues + PR ops | Polling + webhook | v0.1 |
| `adapters/linear` | Linear workspace | Webhook + ProcessedStore | v1.11.0 |
| `adapters/jira` | Jira instance | Webhook + ProcessedStore | v1.12.0 |
| `adapters/asana` | Asana workspace | Webhook + ProcessedStore | v1.12.0 |
| `adapters/gitlab` | GitLab instance | REST + webhook | v1.12.0 |
| `adapters/azuredevops` | Azure DevOps | REST + webhook | v1.12.0 |
| `adapters/slack` | Slack bot | Socket Mode + notifications | v0.1 |
| `adapters/telegram` | Telegram bot | Long-polling + voice | v0.1 |
| `adapters/discord` | Discord bot | Gateway WebSocket | v2.25.0 |
| `adapters/plane` | Plane.so | REST + webhooks | v2.25.0 |

**Common Adapter Registry (v2.30.0):**
- Unified `Adapter` interface (ProcessedStore, state transitions)
- Generic `handleIssueGeneric()` consolidates 5 adapter flows
- State transitions: `UpdateIssueState()`, `TransitionIssueTo()`, `CompleteTask()`

### Supporting Packages

| Package | Purpose | Status |
|---------|---------|--------|
| `approval` | Human-in-the-loop gates | Implemented, optional |
| `budget` | Cost controls + rate limiting | Implemented, CLI command |
| `teams` | RBAC + rule-based approvals | Implemented |
| `tunnel` | Cloudflare tunnel integration | Implemented |
| `webhooks` | Outbound webhook triggers | Implemented |
| `health` | K8s health probes | Implemented |
| `testutil` | Safe test token constants | Test-only |

**9 packages undocumented as of the prior (2026-03-04) audit, added here (2026-08-04):**

| Package | Purpose | Key File |
|---------|---------|----------|
| `adapterhealth` | Panic recovery + bounded restart-with-backoff for adapter goroutines (GH-4314) | `adapterhealth.go:52` (`Registry`) |
| `comms` | Shared communication handler/contract logic used across adapter implementations | `handler.go`, `types.go` |
| `ghbudget` | Tracks the shared GitHub primary rate-limit budget; gates low-priority background GitHub API consumers under low headroom (GH-4391) | `ghbudget.go` |
| `ghissue` | GitHub issue-creation policy (conventional-commit title validation + repo allowlist) atop the studio-sdk github client; ported from `adapters/github/issue_create.go` (M7 4d.1) | `create.go` |
| `intent` | Anthropic-API-backed conversational intent classifier with a `ConversationStore` (per-chat message history, TTL-bounded); classifies free-text chat messages (e.g. Telegram) into structured intents | `classifier.go`, `conversation.go`, `intent.go` |
| `llm` | Generic LLM HTTP client | `client.go` |
| `singleton` | OS-level advisory flock single-instance guard for the daemon, adapter-agnostic (GH-4311) | `lock.go` |
| `text` | Leaf text-primitive utilities with zero internal deps, importable by any adapter without import cycles | `sanitize.go` |
| `wiring` | Test harnesses mirroring `cmd/pilot/main.go`'s two init paths (polling/gateway mode), validating `Runner` wiring consistency | `harness.go` |

**Naming trap:** `internal/intent` (above) classifies chat messages, and is unrelated to
`IntentJudge` (`internal/executor/intent_judge.go:31`, config `IntentJudgeConfig` at
`internal/executor/backend.go:659`), which judges whether an *execution's* final output
actually satisfies the dispatched task's intent. Same word, two different seams — do
not conflate them when searching the codebase or writing tasks that touch either.

**gh-guard** (GH-4671/PR#4704, TASK-441 L7) is not a package, it's a CLI-subcommand +
policy pair: `cmd/pilot/ghguard.go` (hidden `pilot gh-guard` subcommand, byte-exact argv
passthrough — the exec target a per-execution PATH shim re-execs `gh` calls into) and
`internal/executor/ghguard/policy.go` (pure allow/deny policy core, `Classify`
function). Spawn-side wiring in `internal/executor/ghguard_spawn.go` (creates the shim
dir, sets `PILOT_TASK_ISSUE`/`PILOT_TASK_REPO`/`PILOT_TASK_BRANCH`/`PILOT_GH_REAL`) and
`ghguard_audit.go`. Intercepts executor-issued `gh` CLI calls at the Bash-tool boundary
— the durable/preventive half of the GH-4649 containment pair (GH-4670 is the detective
half, a post-run audit). Mirrors the precedent of `RepoAllowlist`/`ValidateTargetRepo`
in `internal/executor/repo_guardrail.go` (GH-3027/TASK-286).

## studio-sdk Boundary

Pilot depends on `github.com/qf-studio/studio-sdk` (`go.mod:11`, no `replace`
directive — no local module override), which owns the tracker-integration code that
used to live in `internal/adapters/*`. A local checkout is sometimes referenced in docs
at `/var/lib/pilot/repos/startups/studio-sdk` (see `.agent/system/
notify-started-adapter-audit.md:8`), but this repo consumes it as a pinned module, not
a path dependency.

**What lives in `sdk/core` (studio-sdk, frozen surface):**
- The adapter-agnostic poller registry and dispatch machinery.
- `sdkcore.IssueHandlerFunc` — the callback shape every adapter's poller closure
  implements.
- Per-integration packages (`sdk/integrations/{linear,jira,asana,gitlab,azuredevops,
  plane,github,discord}`) each own their tracker's HTTP client, poller loop
  (`processIssueAsync`/`processWorkItemAsync`), and a `Notifier` type
  (`NotifyTaskStarted`, etc.).
- **`api.golden`** (`sdk/core/api.golden`, inside studio-sdk itself — not vendored into
  this repo) is a golden-file snapshot locking `sdk/core`'s exported declaration
  surface only. Integration packages and `sdk/util` are explicitly *not* frozen by it.
  Referenced from this repo's planning docs at `.agent/tasks/TASK-441-
  contract-hardening-tune-up.md:70` and the `saas-*` design docs — treat any PR that
  would change `sdk/core`'s public API as requiring explicit sign-off (see "External
  Contract Freeze" below).

**What lives pilot-side (`cmd/pilot/`, this repo):**
- `PollerDeps` (`cmd/pilot/poller_registry.go:21-48`) — the shared-infra struct every
  adapter poller registration closes over: `*config.Config`, `ProjectPath`,
  `*executor.Dispatcher`, `*executor.Runner`, `*executor.Monitor`, `*tea.Program`
  (dashboard), `*alerts.Engine`, `*budget.Enforcer`, `*memory.Store`,
  `*autopilot.Controller`, per-repo `AutopilotControllers`, `GitHubPollers`,
  `AdapterHealth`.
- The registry (`cmd/pilot/poller_registry.go:61-71`) that constructs and starts each
  adapter's poller: `poller_linear.go`, `poller_jira.go`, `poller_asana.go`,
  `poller_azuredevops.go`, `poller_plane.go`, `poller_discord.go`, `poller_gitlab.go`,
  `poller_github.go` (8 registered SDK pollers; Telegram and Slack use a separate
  long-polling/Socket-Mode mechanism, not this registry).
- Per-adapter `*PollerRegistration().CreateAndStart` closures — where the notify-started
  wiring for TASK-441 L3 landed (Linear #4717, Jira #4718, Asana #4719, GitLab #4720,
  AzureDevOps #4721/PR#4729; see `.agent/system/notify-started-adapter-audit.md`).

**The structural fact that mattered for GH-4692 and TASK-441 L3:** the six non-GitHub
SDK pollers (`linear`, `jira`, `asana`, `plane`, `gitlab`, `azuredevops`) each apply
their own in-progress label/tag **internally**, inside `processIssueAsync`/
`processWorkItemAsync`, unconditionally, before invoking the pilot-supplied handler
callback — a structural guarantee GitHub's poller never had (it performs zero label
operations internally, which is why the pilot-side handler had to do it, and why that
handler's original omission was the actual GH-4692 bug). This means the six siblings
were never at dispatch-dedup/orphan-recovery risk; what was missing on 5 of the 6 was
purely the human-facing "Pilot started" comment/note, not a correctness gap — see the
audit doc for the full per-adapter breakdown.

## Dashboard Systems

### TUI Dashboard (Bubbletea, v0.1+)

Real-time monitoring with sparkline cards, git graph visualization, and state-aware queue:

| Panel | Features | Updates |
|-------|----------|---------|
| **Queue** | Task lifecycle visualization, 5 states (done/running/queued/pending/failed) | Per-event |
| **History** | Epic-aware task history, execution metrics | Per-task |
| **Logs** | Real-time Claude Code output streaming | Per-event |
| **Autopilot** | PR status, CI checks, merge progress | Per-check |
| **Git Graph** | Live branch visualization with 4 size modes | Per-branch |
| **Metrics** | Token usage, cost tracking, uptime | Per-interval |

**States (v2.13.0+):**
- `done` ✓ (sage green)
- `running` ● (steel blue, pulses)
- `queued` ◌ (mid gray, shimmer)
- `pending` · (slate)
- `failed` ✗ (dusty rose)

### Web Dashboard (React, v1.56.0+)

Full-featured monitoring at `http://localhost:9090/dashboard`:

| Feature | Tech | Version |
|---------|------|---------|
| **Tasks** | React hooks + SSE | v1.55.0 |
| **Autopilot** | Real-time CI status | v1.55.0 |
| **History** | Pagination + filtering | v1.62.0 |
| **WebSocket** | Log streaming | v1.56.0 |
| **API** | REST endpoints `/api/v1/*` | v1.55.0 |

### Desktop App (Wails v2, v1.53.0+)

Native macOS/Windows app with React frontend:

| Feature | Version |
|---------|---------|
| Git graph panel | v1.53.0 |
| WebSocket log streaming | v1.56.0 |
| HTTP data provider | v1.53.1 |
| Native titlebar | v1.62.0 |
| Responsive layout | v2.38.0 |

## Worktree Isolation + Epic Interaction

**Worktree Isolation (v0.53-v2.56)**: Execute tasks in isolated git worktrees, preventing conflicts with user's uncommitted changes.

| Version | Feature | Issue |
|---------|---------|-------|
| v0.53.2 | Initial worktree isolation | GH-936 |
| v0.56.0 | Epic + worktree integration | GH-945 |
| v0.57.3 | Crash recovery, orphan cleanup | GH-962 |
| v1.0.11 | Serial conflict cascade prevention | GH-1265 |
| v2.53.0 | Merged PR guard in poller | GH-1855 |

**Epic Decomposition Guard (v1.0.11):**
```go
// Prevent serial conflict cascade
isSinglePackageScope() {
    // Detects when all subtasks target same directory
    // Consolidates into single task instead of creating separate issues
}
```

**Key files:** `internal/executor/worktree.go`, `epic.go`, `runner.go`

### Epic Execution Flow

```
┌──────────────────────────────────────────────────┐
│  Epic Detected (>5 phases, structural signals)   │
├──────────────────────────────────────────────────┤
│  1. Check `no-decompose` label (v1.57.0)         │
│  2. Check for single-package scope (v1.0.11)     │
│  3. Create worktree with unique path              │
│  4. Copy .agent/ (Navigator preservation)         │
│  5. Plan decomposition in worktree                │
│  6. Create sub-issues via GitHub API              │
│  7. Execute sub-issues SEQUENTIALLY               │
│     └─ allowWorktree=false (no nesting)           │
│  8. Cleanup worktree (deferred)                   │
└──────────────────────────────────────────────────┘
```

## Pattern Learning System (v2.25.0+)

Learn from PR reviews and inject patterns into future prompts:

```
┌────────────────────────────────────────┐
│  PR Review Analysis                    │
├────────────────────────────────────────┤
│  1. Extract comments from review       │
│  2. Classify as pattern or anti-pattern│
│  3. Calculate confidence score         │
│  4. Store in memory.cross_patterns     │
│  5. Inject top patterns into prompts   │
│     for similar future tasks           │
└────────────────────────────────────────┘
```

**Files:** `internal/memory/feedback.go`, `LearnFromReview()` integration in autopilot

### CI Error Pattern Learning (v2.49.0+)

Extract and learn from CI failures with categorized error patterns:

```
┌──────────────────────────────────────┐
│  CI Failure Detection                │
├──────────────────────────────────────┤
│  1. Capture CI check logs            │
│  2. Extract error patterns by type:  │
│     • Compilation errors             │
│     • Test failures                  │
│     • Linter violations              │
│     • Build failures                 │
│  3. Tag with source:ci + category    │
│  4. Store as anti-patterns (0.5 conf)│
│  5. Boost confidence on recurrence   │
│  6. Inject into retry prompts        │
└──────────────────────────────────────┘
```

**Features:**
- Pattern categorization: compilation, test, lint, build
- Automatic confidence boosting on pattern recurrence
- Context-aware: tracks check names and CI framework
- Integration with retry system: injects CI patterns into follow-up prompts

**Files:** `internal/memory/extractor.go` (pattern extraction), `internal/memory/feedback.go` (learning loop)

## Self-Review System (v0.33.14+)

Optional automated code review before PR merge:

| Phase | Action | Version |
|-------|--------|---------|
| Execution | Create PR without merge | v0.8 |
| Self-Review | Analyze code + comment | v0.8 |
| Alignment Check | Verify modified files vs issue title | v0.33.14 |
| AC Verification | Extract + verify acceptance criteria | v2.49.0 |
| Auto-Approval | Approve if quality gates pass | v0.61 |

## Auto-Rebase on Conflict (v2.25.0+)

Automatically resolve merge conflicts:

```
┌──────────────────────────────────────┐
│  Merge Conflict Detected              │
├──────────────────────────────────────┤
│  1. GitHub UpdateBranch API           │
│  2. Rebase branch against main        │
│  3. Retry merge                       │
│  4. Create CI fix issue if still fails│
│     (Depends on: #N annotation)       │
└──────────────────────────────────────┘
```

## GitHub Projects V2 Board Sync (v2.30.0+)

Automatic GraphQL board sync with 3-column layout:

```
┌─────────────┬─────────────┬──────────────┐
│ Backlog     │ Review      │ Done         │
│ (open)      │ (in PR)     │ (merged)     │
│             │ (in progress)              │
└─────────────┴─────────────┴──────────────┘
```

**Features:**
- Lazy ID resolution (org-first discovery)
- Concurrent issue moves
- Custom field updates
- Key files: `internal/adapters/github/project_board.go`

## Key Integration Points

### Claude Code Integration

```go
// internal/executor/runner.go - Stream-JSON parsing
cmd := exec.Command("claude",
    "-p", prompt,
    "--output-format", "stream-json",
    "--dangerously-skip-permissions",
)
// Parses: system, assistant, tool_use, tool_result, result events
```

### Navigator Integration

Pilot activates `/nav-loop` mode when `.agent/` exists (v0.33.15+):

```go
if useNavigator {
    sb.WriteString("Use /nav-loop mode for this task.\n\n")
}
```

**Navigator context bridge (v1.18.0):**
- Load key files, components, structure into prompt
- Post-execution docs update: feature matrix, knowledge capture

### Hooks System (v1.3.0+)

Claude Code inline quality gates via JSON hooks (v1.50.0 format):

```json
{
  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [{"type": "command", "command": "..."}]
    }
  ]
}
```

**Key files:** `internal/executor/hooks.go` (generation + merging)

### Alerts Integration

Event-based multi-channel dispatch:

```go
// internal/executor/alerts.go
type AlertEventProcessor interface {
    ProcessEvent(event alerts.Event)
}

// Emits: TaskStarted, TaskProgress, TaskCompleted, TaskFailed
```

## Configuration Structure

```yaml
# ~/.pilot/config.yaml
gateway:
  host: "127.0.0.1"
  port: 9090

executor:
  backend: "claude"  # or qwen
  use_worktree: true
  navigator:
    auto_init: true

adapters:
  github:
    enabled: true
    polling:
      interval: 30s
      label: "pilot"
  linear:
    enabled: false
    api_key: "..."
  slack:
    enabled: false
    app_token: "..."

autopilot:
  enabled: true

environments:
  dev:
    ci_required: false
    approval_required: false
  stage:
    ci_required: true
    approval_required: false
  prod:
    ci_required: true
    approval_required: true
    post_merge:
      action: "tag"

memory:
  path: "~/.pilot/memory.db"

alerts:
  enabled: true
  channels: ["slack", "telegram"]
```

## Database Schema (SQLite)

**Schema-of-record: `internal/memory/store.go`** (`migrate()`, `store.go:98`). Do not
hand-copy `CREATE TABLE` bodies into this doc — they drift (see the note above: 4 of
the 6 tables previously listed here — `task_queue`, `processed`, `milestones`,
`board_state` — never existed). Read the source for exact columns.

Three files own the daemon's SQLite schema. Direct enumeration of every
`CREATE TABLE IF NOT EXISTS` (2026-08-04 audit) found **30 live tables**, not the "34"
this leg's ticket assumed — see the note at the end of this section.

**`internal/memory/store.go` (19 tables):** `executions` (:100, main execution ledger —
**frozen external contract**, see below), `patterns` (:113), `projects` (:123),
`cross_patterns` (:131, cross-project pattern learning), `pattern_projects` (:145,
pattern↔project join), `pattern_feedback` (:155), `usage_events` (:208), `sessions`
(:226), `autopilot_metrics` (:239), `brief_history` (:261), `execution_logs` (:271),
`model_outcomes` (:280), `pattern_performance` (:292), `eval_tasks` (:306),
`eval_results` (:324), `approval_pending` (:341), `execution_events` (:374, per-execution
stage timeline, FK → `executions` — **frozen**), `execution_claims` (:402, TASK-407/
GH-4349 atomic dispatch-admission claim — **frozen**), `repick_backoff` (:423, GH-4394
persisted repick cooldown).

**`internal/memory/knowledge.go` (1 table):** `memories` (:58) — knowledge-graph entries.

**`internal/teams/store.go` (4 tables):** `teams` (:30), `team_members` (:37),
`team_audit_log` (:49), `project_access` (:61).

**`internal/autopilot/state_store.go` (6 live tables):** `autopilot_pr_state` (:54, PR
lifecycle/stage state — **frozen**, see "Autopilot Stage Vocabulary" below),
`autopilot_metadata` (:75), `autopilot_pr_failures` (:80), `adapter_processed` (:90,
GH-1838 generic multi-adapter dispatch guard, replaced 7 per-adapter tables),
`autopilot_scope_release` (:122, GH-3990 epic/label scope-release carrier tracking —
**frozen**), `autopilot_spawned_fixes` (:159, GH-4307 dedup claim for autogenerated fix
issues). Three additional tables in this file (`adapter_processed_gh3819` :292,
`autopilot_pr_state_gh3903` :426, `autopilot_pr_failures_gh3903` :499) are transient
rename targets used mid-migration during primary-key rebuilds — not steady-state
schema, excluded from the count above.

**Not in this schema:** `instance_events` (named in this leg's ticket alongside the
tables above) does not exist anywhere in `internal/memory` or `internal/autopilot`.
It belongs to the separate SaaS/cloud console design (`.agent/system/
saas-fleet-design.md`, `saas-roadmap.md`) — a different codebase area with its own
PostgreSQL schema (`cloud/migrations/001_initial_schema.sql`: `users`, `organizations`,
`memberships`, `projects`, `invitations`, `audit_logs`, `integrations`, `executions`,
`usage_records`, `subscriptions`, `api_keys`, `sessions`) — not part of the daemon's
SQLite store this section documents. Recorded here rather than silently included,
since fabricating a table is exactly the kind of drift this refresh exists to remove.

**Load-bearing / frozen tables** (external contract freeze list applies — see below):
`executions`, `execution_claims`, `execution_events`, `autopilot_pr_state`,
`autopilot_scope_release`.

## External Contract Freeze

A running list of surfaces that other systems (console, dashboards, self-upgrade,
tenant configs, cross-repo protocol) depend on by shape or name. Changing any of these
without explicit operator sign-off breaks a consumer outside this repo. Reproduced from
`.agent/tasks/TASK-441-contract-hardening-tune-up.md` (constraint on every leg of that
task) so it's discoverable from the architecture doc, not just a task file:

- studio-sdk `sdk/core` public API (`api.golden`; console C3/C4 consume `SyncCapable`)
- `pilot-*` label vocabulary (`internal/adapters/github/types.go:99-122` — cross-repo
  protocol, console board sync depends on it)
- Prometheus metric names (`internal/gateway/prometheus.go` ↔ grafterm dashboards)
- Release artifact naming `pilot-{os}-{arch}.tar.gz`
  (`internal/upgrade/upgrade.go:369` — self-upgrade fetches by name)
- `~/.pilot/config.yaml` schema (tenant configrender)
- `executions` / `execution_claims` / `execution_events` DB schema (pilot-board-remote,
  TUI, trace) — see "Database Schema" above for the load-bearing table list, which
  additionally includes `autopilot_pr_state` and `autopilot_scope_release`
- gateway REST + `/ws/dashboard` + webhook paths (`internal/gateway/server.go:219-253`)
- Telegram command surface

## TASK-441 Seam Infrastructure (2026-08-04)

A week of production incidents in 2026-08 shared one shape: wiring bugs wearing a
green test suite (intent judge dead 17 days behind a fail-open path; `pilot-in-progress`
never applied since the 07-16 SDK cutover because the tested `Notifier` was wired to
nothing; `runSelfReview` executing in the repo root for the 3rd time via a
mock that discarded `ExecuteOptions`). TASK-441 generalized the point-fixes so the next
seam break is loud within an hour instead of silent for weeks:

- **`make check-mocks`** (Leg 1, PR#4711) — `Makefile:133`, runs `scripts/
  check-mocks.sh`. CI-fails on argument-discarding `Backend.Execute` mocks in
  `*_test.go` (the `Execute(_ context.Context, _ ExecuteOptions)` pattern that hid the
  self-review root-directory bug). Recommends the recording-mock pattern in
  `internal/executor/backend_execute_guard_test.go`'s `guardRecordingBackend`.
- **`alerts.DeadManTracker`** (Leg 2, PR#4712) — `internal/alerts/deadman.go:70`. A
  reusable liveness primitive: any seam registers via `RegisterDeadManTracker` with a
  threshold; the tracker counts attempts *and* successes separately (not just absence
  of errors — "removed" logs fire on never-applied, not just failed-then-removed) and
  fires its `AlertType` once when the consecutive-failure streak reaches threshold.
- **Notify-started adapter audit** (Leg 3, PR#4713) — `.agent/system/
  notify-started-adapter-audit.md`. See "studio-sdk Boundary" above for the finding.
- **`LivenessPolicy`** (Leg 4, PR#4722) — `internal/executor/liveness_policy.go:56`,
  resolved via `ResolveLivenessPolicy` (line 79). Single source for the stdout-silence
  thresholds (`StallTimeout`, `StallWatchdogInterval`, `HeartbeatFloor`) that
  `heartbeat_monitor.go` (hard SIGKILL) and `watchdog.go` (soft-stall context-cancel)
  both read — GH-4695 had to hand-resync these after they drifted independently, and
  GH-4691 flagged the recurrence risk. Merges the *policy*, not the *enforcement*: the
  two detectors keep their distinct kill semantics.
- **`ExecutionLifecycle` post-terminal tripwire sweep** (Leg 5, PR#4724) —
  `internal/executor/lifecycle.go:349`, `runFinishTripwireSweep(l.store,
  l.alertProcessor, execID)`, called from `Persist` (`lifecycle.go:297`), which both
  `Finish` and any direct Classify-then-Persist caller route through — so it covers
  every production terminal-write call site, not just `Finish`. Checks: root-clean (no
  staged/unstaged diff in the task's project path), label lifecycle completed,
  decomposed children all terminal, worktree pruned with no commits-without-PR.
  Log-and-alert only, panics recovered — never blocks or changes the write it's
  piggybacking on.
- **gh-guard shim** (Leg 7, PR#4704, GH-4671) — see "Package Architecture" above.

Legs 6 (narrow autopilot GitHub client interface) and 8 (this doc) round out the task;
see `.agent/tasks/TASK-441-contract-hardening-tune-up.md` for full status.

## Test Coverage

| Package | Test Files | Status |
|---------|-----------|--------|
| adapters/github | 5 | ✅ |
| adapters/slack | 2 | ✅ |
| adapters/telegram | 7 | ✅ |
| adapters/jira | 3 | ✅ |
| adapters/linear | 3 | ✅ |
| adapters/asana | 2 | ✅ |
| adapters/gitlab | 1 | ✅ |
| adapters/azuredevops | 1 | ✅ |
| adapters/discord | 2 | ✅ |
| adapters/plane | 2 | ✅ |
| alerts | 4 | ✅ |
| approval | 2 | ✅ |
| autopilot | 8 | ✅ |
| briefs | 4 | ✅ |
| budget | 2 | ✅ |
| config | 1 | ✅ |
| executor | 20 | ✅ |
| gateway | 4 | ✅ |
| logging | 2 | ✅ |
| memory | 8 | ✅ |
| quality | 3 | ✅ |
| replay | 4 | ✅ |
| teams | 1 | ✅ |
| tunnel | 6 | ✅ |
| upgrade | 1 | ✅ |
| webhooks | 1 | ✅ |

**Packages without tests:** banner, dashboard, health, pilot, testutil, transcription

## Build & Deploy

```bash
# Build
make build    # → ./bin/pilot

# Test
make test     # go test ./...

# Lint
make lint     # golangci-lint

# Development
make dev      # Build + run with hot reload

# Release (tag-only)
git tag v2.X.Y && git push origin v2.X.Y  # GoReleaser CI handles rest
```

**Binary versioning:** `v2.X.Y` (semantic)

## Security Considerations

1. **Tokens in tests**: Use `internal/testutil/tokens.go` for fake tokens
2. **API keys**: Environment variables or config file (`~/.pilot/config.yaml`)
3. **Sandbox mode**: Claude Code runs with `--dangerously-skip-permissions` (trusted context)
4. **Webhook secrets**: HMAC validation for incoming webhooks (SHA256)
5. **Database**: SQLite with WAL mode, connection pooling (`SetMaxOpenConns(1)`)

## Key Execution Modes

| Mode | Trigger | Behavior |
|------|---------|----------|
| Sequential | Default or many file changes | One issue at a time |
| Parallel | Few file changes, different scopes | Multiple issues concurrently |
| Epic | >5 phases detected | Decompose into sub-issues |
| Worktree | `use_worktree: true` | Isolated execution environment |

**Execution mode selection (v2.25.0):** Scope-based auto-switching via union-find algorithm

## Version History

| Version | Key Milestone | Date |
|---------|---------------|------|
| v0.1 | Initial release | 2025-12 |
| v0.53.2 | Worktree isolation | 2026-01 |
| v0.57.5 | Previous arch doc | 2026-02-13 |
| v1.0.0 | v1.0 stabilization | 2026-02-14 |
| v1.59.0 | Environment config | 2026-02-19 |
| v1.62.0 | Gateway + desktop app | 2026-02-20 |
| v2.25.0 | Pattern learning, auto-rebase, Discord | 2026-02-25 |
| v2.30.0 | Common adapter registry, board sync | 2026-02-26 |
| v2.53.0 | Merged PR guard, CI error patterns | 2026-02-28 |
| v2.56.0 | Previous arch doc revision | 2026-03-04 |
| v2.253.0 | TASK-441 seam hardening (dead-man tracking, LivenessPolicy, tripwire sweep, gh-guard); this doc refreshed against source | 2026-08-04 |

## Appendix: Full Package Audit

**Last Audit:** 2026-08-04 (previous audit 2026-03-04 predated the 9 rows marked
"added 2026-08-04" below — they existed on disk and were undocumented, not newly
created by this refresh).

| Package | Exists | Imported | Wired | Tests | Status |
|---------|--------|----------|-------|-------|--------|
| adapters/github | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/jira | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/linear | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/slack | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/telegram | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/asana | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/gitlab | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/azuredevops | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/discord | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/plane | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapterhealth *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| alerts | ✅ | ✅ | ✅ | ✅ | ✅ |
| approval | ✅ | ✅ | ✅ | ✅ | ✅ |
| autopilot | ✅ | ✅ | ✅ | ✅ | ✅ |
| banner | ✅ | ✅ | ✅ | ❌ | ✅ |
| briefs | ✅ | ✅ | ✅ | ✅ | ✅ |
| budget | ✅ | ✅ | ✅ | ✅ | ✅ |
| comms *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| config | ✅ | ✅ | ✅ | ✅ | ✅ |
| dashboard | ✅ | ✅ | ✅ | ❌ | ✅ |
| executor | ✅ | ✅ | ✅ | ✅ | ✅ |
| gateway | ✅ | ✅ | ✅ | ✅ | ✅ |
| ghbudget *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| ghissue *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| health | ✅ | ✅ | ✅ | ❌ | ✅ |
| intent *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| llm *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| logging | ✅ | ✅ | ✅ | ✅ | ✅ |
| memory | ✅ | ✅ | ✅ | ✅ | ✅ |
| orchestrator | ✅ | ✅ | ✅ | ✅ | ✅ |
| pilot | ✅ | ✅ | ✅ | ❌ | ✅ |
| quality | ✅ | ✅ | ✅ | ✅ | ✅ |
| replay | ✅ | ✅ | ✅ | ✅ | ✅ |
| singleton *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| teams | ✅ | ✅ | ✅ | ✅ | ✅ |
| testutil | ✅ | ✅ | ❌ | ❌ | ✅ |
| text *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |
| transcription | ✅ | ✅ | ❌ | ❌ | ✅ |
| tunnel | ✅ | ✅ | ✅ | ✅ | ✅ |
| upgrade | ✅ | ✅ | ✅ | ✅ | ✅ |
| webhooks | ✅ | ✅ | ✅ | ✅ | ✅ |
| wiring *(added 2026-08-04)* | ✅ | ✅ | ✅ | ✅ | ✅ |

**Summary:**
- 43 packages total (34 from the 2026-03-04 audit + 9 undocumented-but-existing
  packages surfaced by this refresh)
- 100% exist and are imported
- 100% wired in main.go (`wiring` package's own harness tests validate this)
- ~86% have test files (6 without: `banner`, `dashboard`, `health`, `pilot`, `testutil`,
  `transcription` — `testutil` is also unwired by design, it's fake-token constants
  consumed only from `*_test.go` files, not a runtime-wired subsystem)
- 100% of tested packages pass

---

## Critical Integration Constraints

### 1. Navigator Integration (DO NOT REMOVE)

`BuildPrompt()` in `internal/executor/runner.go` MUST invoke `/nav-loop` mode when `.agent/` exists. This is Pilot's core value proposition.

**Incident 2026-01-26**: Accidental removal during refactor. Pilot without Navigator = just another Claude Code wrapper.

### 2. Git Worktree Isolation

Worktree isolation prevents conflicts when user has uncommitted changes. **DO NOT remove `use_worktree` config option.**

### 3. Serial Conflict Cascade Prevention (v1.0.11)

`isSinglePackageScope()` in `epic.go` detects when all planned subtasks target the same directory. When detected, epic is consolidated into a single task instead of creating separate issues.

**Why?** Each sub-issue branches from stale `main`, creates conflicts when they all modify the same files.

---

**For questions, refer to DEVELOPMENT-README.md completed log and task files in `.agent/tasks/`**
