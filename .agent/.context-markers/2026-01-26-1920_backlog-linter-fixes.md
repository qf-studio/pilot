# Context Marker: Backlog Expansion & Linter Fixes

**Created**: 2026-01-26 19:20
**Session**: Feature planning + tech debt cleanup

---

## Session Summary

1. **Telegram Progress Updates** - Real-time execution status via EditMessage
2. **Bug Fixes** - MarkdownV2 → Markdown, timeouts for questions, table conversion
3. **Backlog Expansion** - 19 new tasks across 6 categories
4. **Linter Cleanup** - Fixed 21 production code errors

---

## What Was Built

### Telegram Improvements
- Progress updates during task execution (edits single message)
- 90s timeout for questions (prevents runaway exploration)
- Table-to-list conversion (Telegram doesn't support tables)
- SOP: `.agent/sops/telegram-bot-development.md`

### Backlog (TASK-05 to TASK-24)
| Category | Tasks |
|----------|-------|
| Telegram | 05-07 (singleton, image, voice) |
| Adapters | 08-09 (GitHub Issues, Jira) |
| Features | 10-12 (briefs, memory, cloud) |
| Monitoring | 13-15 (metrics, alerts, logging) |
| Monetization | 16-18 (metering, teams, cost controls) |
| Safety | 19-20 (approval, quality gates) |
| DX | 21-23 (replay, webhooks, GitHub App) |
| Tech Debt | 24 (cleanup) |

### Linter Fixes
- 21 errcheck fixes (production code)
- Error strings lowercased (Go convention)
- Removed unused variable

---

## Files Changed This Session

```
internal/adapters/telegram/
├── client.go       - EditMessage, errcheck fixes
├── formatter.go    - FormatProgressUpdate, convertTablesToLists
├── handler.go      - Progress callback, question timeout
└── formatter_test.go

internal/adapters/slack/client.go - errcheck, error strings
internal/adapters/linear/client.go - errcheck
internal/gateway/*.go - errcheck fixes
internal/memory/store.go - errcheck fixes
internal/orchestrator/*.go - errcheck, error strings
internal/pilot/pilot.go - errcheck
internal/executor/progress.go - removed unused var
cmd/pilot/main.go - error strings

.agent/sops/telegram-bot-development.md - NEW
.agent/tasks/TASK-05 through TASK-24 - NEW
```

---

## Current State

- **Version**: v0.2.0+
- **Tests**: All passing
- **Lint**: 14 errors remaining (test files only)
- **Bot**: Working with progress updates

### Priority Next
1. TASK-06: Telegram Image Support (in progress)
2. TASK-13: Execution Metrics (monetization foundation)
3. TASK-18: Cost Controls (prevent bill shock)

---

## Commands

```bash
brew upgrade pilot          # Get latest
pilot telegram -p <project> # Start bot
make test && make lint      # Verify
```

---

## To Restore

```
/nav-start-active
```
