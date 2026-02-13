# Pilot v1.0 Roadmap

## Context

Pilot has 133 features shipped in ~10 days (v0.25–v0.63). Built fast with reactive roadmaps. Now need to go from "works" to "business-ready" — stable, documented, clean code. 1-month timeline, Pilot self-executes via GitHub issues.

**Current state from audit:**
- Code: 2 god files (runner.go 3.8K, main.go 5.9K), 1 orphaned package, no circular deps
- Tests: 2,232 tests, 165 files, 1 panic blocker (TestQualityGatesHappyPath)
- Docs: 60% complete — 26 pages, ~8 critical pages missing, README stale, changelog empty
- CI: Go version mismatch (1.22 vs 1.24.2), 5 CI fix issues cycling
- Nav port: GH-1026 open with detailed spec, needs execution

**Constraint from TASK-01 lesson:** No sequential issues touching the same file. Each issue must be parallel-safe.

---

## Phase 1: Stabilize (Week 1)

**Goal:** Green CI, no stuck issues, clean queue.

### Issue 1: Fix TestQualityGatesHappyPath panic
- **Files:** `internal/orchestrator/quality_integration_test.go`, `internal/executor/runner.go`
- **What:** Debug panic at runner.go:1684 in `executeWithOptions()`, fix nil pointer or race
- **Blocker:** Prevents `make test-short` and CI

### Issue 2: Update CI Go version 1.22 → 1.24
- **Files:** `.github/workflows/ci.yml`
- **What:** Change `go-version: '1.22'` to `go-version: '1.24'`
- **Parallel-safe:** Yes (CI config only)

### Issue 3: Clean up cycling CI fix issues
- **Manual:** Close GH-1033, 1035, 1037, 1039, 1041 if they're stuck in retry loops
- **Check:** `gh issue list --label autopilot-fix --state open`

### Issue 4: Remove orphaned transcription package
- **Files:** `internal/transcription/` (3 files)
- **What:** Delete package, verify no imports
- **Parallel-safe:** Yes (isolated package)

---

## Phase 2: Nav Port Wiring (Week 1–2)

**Goal:** Complete GH-1026 — wire knowledge, profile, drift, markers into runner.

### Issue: GH-1026 (already exists)
- **Files:** `internal/executor/runner.go`, `cmd/pilot/main.go`, `internal/memory/knowledge.go`
- **What:** 4 integration points in runner.go, component init in main.go
- **Risk:** Touches both god files — must be done BEFORE the split refactoring
- **Acceptance:** BuildPrompt includes Knowledge/Profile sections, markers created post-task

---

## Phase 3: Code Refactoring (Week 2–3)

**Goal:** Split god files into logical units. Same package, no API changes.

### Issue 5: Split runner.go (3,864 → ~800 lines each)
**Single consolidated issue — touches one file.**

Extract from `internal/executor/runner.go` into:

| New File | What Moves | Est. Lines |
|----------|-----------|------------|
| `runner_git.go` | Branch creation, checkout, push, commit, worktree setup | ~600 |
| `runner_prompt.go` | `BuildPrompt()`, knowledge/profile injection, acceptance criteria | ~500 |
| `runner_epic.go` | Epic detection, `PlanEpic()`, `ExecuteSubIssues()` | ~400 |
| `runner_review.go` | Self-review, alignment check, quality gate interaction | ~300 |
| `runner_progress.go` | Phase tracking, signal parsing, progress reporting | ~300 |
| `runner_metrics.go` | Token counting, cost calculation, execution report | ~200 |
| `runner.go` | Core `Execute()`, `executeWithOptions()`, struct definition | ~800 |

**Rules:**
- No new packages — all files in `internal/executor/`
- No interface changes — same public API
- No logic changes — pure file extraction
- Tests stay in existing test files (they test public API)

### Issue 6: Split main.go (5,939 → ~1,500 lines each)
**Single consolidated issue — touches one file.**

Extract from `cmd/pilot/main.go` into:

| New File | What Moves | Est. Lines |
|----------|-----------|------------|
| `adapters.go` | All adapter initialization (GitHub, Telegram, Slack, GitLab, etc.) | ~1,200 |
| `autopilot_setup.go` | Autopilot controller, CI monitor, feedback loop setup | ~800 |
| `poller_setup.go` | GitHub/Linear/Jira poller initialization, multi-repo logic | ~700 |
| `handlers.go` | Issue handler, PR callback, webhook routing | ~600 |
| `main.go` | CLI flags, cobra commands, top-level orchestration | ~1,500 |

**Same rules as runner.go split.**

### Issue 7: Add Config.Validate() method
- **Files:** `internal/config/config.go`
- **What:** Bounds checking (MaxConcurrent, PollInterval), enum validation (Mode), token format warnings
- **Parallel-safe:** Yes (config package only)

---

## Phase 4: Docs Refresh (Week 2–4)

**Goal:** 100% feature coverage, up-to-date references, complete CLI docs.

All docs issues are **parallel-safe** — each creates/updates a separate .mdx file.

### Issue 8: Update README.md and version references
- **Files:** `README.md`, `docs/pages/getting-started/quickstart.mdx`
- **What:** Fix v0.23.3 → v1.0, update install examples, feature count

### Issue 9: Complete CLI Reference page
- **Files:** `docs/pages/cli-reference/commands.mdx`
- **What:** Add 12+ missing commands: `pilot brief`, `pilot patterns`, `pilot replay`, `pilot budget`, `pilot team`, `pilot setup`, `pilot completion`, `pilot version`, `pilot github run`, `pilot metrics`

### Issue 10: Add Quality Gates documentation
- **Files:** `docs/pages/features/quality-gates.mdx`
- **What:** Test/lint/build gates, retry behavior, config examples

### Issue 11: Add Alerts & Monitoring documentation
- **Files:** `docs/pages/features/alerts.mdx`
- **What:** Alert engine, 5 channels (Slack, Telegram, Email, Webhook, PagerDuty), rules, cooldowns

### Issue 12: Add Dashboard TUI guide
- **Files:** `docs/pages/features/dashboard.mdx`
- **What:** Metrics cards, queue panel (5 states), keyboard shortcuts, autopilot panel

### Issue 13: Add Budget & Cost Controls documentation
- **Files:** `docs/pages/features/budget.mdx`
- **What:** Daily/monthly limits, per-task limits, budget alerts, `pilot budget` command

### Issue 14: Add Teams & Permissions documentation
- **Files:** `docs/pages/features/teams.mdx`
- **What:** Team CRUD, RBAC, project mapping, `--team` flag

### Issue 15: Add Infrastructure & Deployment guide
- **Files:** `docs/pages/guides/deployment.mdx`
- **What:** K8s health probes, Prometheus metrics, JSON logging, Cloudflare tunnel, self-hosted setup

### Issue 16: Complete Configuration Reference
- **Files:** `docs/pages/getting-started/configuration.mdx`
- **What:** Add missing sections: Slack, Linear, Jira, Azure DevOps, quality gates, alerts, budget, teams, worktree

### Issue 17: Add Memory & Learning documentation
- **Files:** `docs/pages/features/memory.mdx`
- **What:** Knowledge store, patterns, profile manager, execution history

### Issue 18: Add Replay & Debug documentation
- **Files:** `docs/pages/features/replay.mdx`
- **What:** Recording, list/show/play commands, analysis, export formats

### Issue 19: Generate CHANGELOG.md
- **Manual or script:** Pull from GitHub releases (`gh release list --limit 100`)
- **Files:** `CHANGELOG.md`

---

## Phase 5: Final Review & Release (Week 4)

**Goal:** Integration test, version bump, tag v1.0.0.

### Issue 20: Pre-release integration test
- **What:** Run full test suite, verify all docs build, test `pilot start` with each adapter
- **Manual:** Smoke test Telegram, GitHub polling, Slack, autopilot modes

### Issue 21: Version bump to v1.0.0
- **Files:** Version references across codebase
- **What:** Update version strings, CLAUDE.md, DEVELOPMENT-README.md
- **Then:** `git tag v1.0.0 && git push origin v1.0.0` → GoReleaser creates release

---

## Issue Dependency Graph

```
Phase 1 (parallel):
  [1] Fix test panic
  [2] CI Go version
  [3] Clean stuck issues (manual)
  [4] Remove transcription
      │
Phase 2 (after Phase 1):
  [GH-1026] Nav port wiring ←── must complete before runner.go split
      │
Phase 3 (sequential for 5→6, parallel for 7):
  [5] Split runner.go ←── after GH-1026
  [6] Split main.go ←── after GH-1026 (can parallel with 5 if careful)
  [7] Config.Validate() (parallel with 5,6)
      │
Phase 4 (all parallel, can start Week 2):
  [8-19] All docs issues (parallel-safe, each touches different file)
      │
Phase 5 (after all above):
  [20] Integration test
  [21] Version bump + release
```

## Issue Creation Rules

Per TASK-01 lesson — prevent serial conflict cascade:
1. **runner.go split** = 1 issue (not 6 separate ones)
2. **main.go split** = 1 issue (not 4 separate ones)
3. **Docs** = parallel-safe (each is a new .mdx file)
4. **GH-1026 before splits** — it adds code to runner.go/main.go, split comes after
5. Each issue gets `pilot` label
6. Phase gates enforced manually — don't create Phase 3 issues until Phase 2 merges

## Total: 21 issues + GH-1026 = 22 work items over 4 weeks → v1.0.0
