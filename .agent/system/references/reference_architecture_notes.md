---
name: Architecture Notes
description: High-level Pilot architecture reference (autopilot state machine, env config, dashboard, learning loop)
type: reference
originSessionId: 86aef822-8124-4724-816f-1f26cf305635
---
Condensed reference. Detailed feature docs live in the public docs site (Nextra at `pilot.quantflow.studio`); this file holds notes that aren't formally documented yet.

**Autopilot state machine:** PRCreated → WaitingCI → CIPassed → Merging → Merged → PostMergeCI

**Self-review timing:** runs BETWEEN quality gates and PR push (`runner.go` ~lines 934, 1103) — uses `--resume` for session continuity when available; falls back to fresh subprocess on session eviction.

**Environment config (v1.59.0+):** `EnvironmentConfig` struct per env, `ResolvedEnv()` with legacy fallback. No more hardcoded `EnvProd`/`EnvDev` checks — all config-driven. Custom envs supported (qa, canary, etc.) via `environments:` map. Post-merge deployer fires webhook/branch-push/tag actions. Key files: `internal/autopilot/types.go`, `controller.go`, `auto_merger.go`.

**Adapter registry (v2.30.0):** Unified interface, generic ProcessedStore table — Linear/Jira/Asana/GitLab/AzureDevOps/Discord/Plane all conform. GitHub Projects V2 board sync via GraphQL (lazy ID resolution, org-first discovery).

**Pattern Learning (v2.25.0):** `LearnFromReview()` in `memory/feedback.go` — confidence boost / anti-pattern extraction from PR review comments.

**Execution Mode Auto (v2.25.0):** `groupByOverlappingScope()` union-find in poller — scope-overlap guard avoids parallel conflicts.

**Auto-Rebase (v2.25.0):** `handleMergeConflict()` calls `UpdatePullRequestBranch` API before close-and-retry.

**Web Dashboard (v1.53.0–v1.56.0):** React frontend at `/dashboard`, API at `/api/v1/*`, WebSocket log streaming, execution milestones, git graph panel. Desktop app via Wails v2 with macOS GoReleaser artifact. Key files: `internal/gateway/server.go`, `desktop/`.

**Why kept in memory (not docs):** These are internal reference notes for fast recall during debugging — they map to specific filenames/version cuts. Public docs cover higher-level architecture.

**How to apply:** When user asks "where is X", check this file first. If a section grows past 5 bullets, promote to a dedicated reference file.
