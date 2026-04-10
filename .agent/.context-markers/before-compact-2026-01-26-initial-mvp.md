# Context Marker: Initial MVP Complete

**Created**: 2026-01-26
**Session**: Pilot project initialization and MVP implementation

---

## Summary

Built complete Pilot MVP - an autonomous AI development pipeline that receives tickets from Linear, implements features using Claude Code, and creates PRs.

## Accomplished

### Core Implementation (Week 1-4)
- **Gateway**: WebSocket control plane + HTTP API on :9090
- **Linear Adapter**: GraphQL client, webhook handler
- **Slack Adapter**: Notifications (start, progress, complete, failed)
- **Executor**: Claude Code spawner, git operations, progress monitoring
- **Memory**: SQLite store, knowledge graph, global patterns
- **Orchestrator**: Python bridge for LLM task planning
- **CLI**: start, stop, status, init, version, test commands
- **TUI Dashboard**: bubbletea-based terminal UI (skeleton)

### Infrastructure
- Homebrew tap: `brew tap qf-studio/pilot && brew install pilot`
- GitHub repo with topics, description configured
- ASCII banner for CLI
- 24 tests passing

## Files Created

```
pilot/
├── cmd/pilot/main.go              # CLI entrypoint
├── internal/
│   ├── gateway/                   # WebSocket + HTTP server
│   ├── adapters/linear/           # Linear integration
│   ├── adapters/slack/            # Slack notifications
│   ├── executor/                  # Claude Code runner
│   ├── memory/                    # SQLite + knowledge graph
│   ├── orchestrator/              # Python bridge
│   ├── pilot/                     # Main application
│   ├── config/                    # YAML config
│   ├── banner/                    # ASCII logo
│   └── dashboard/                 # TUI
├── orchestrator/                  # Python LLM logic
├── .agent/                        # Navigator docs
├── Makefile
└── README.md
```

## Technical Decisions

1. **Go + Python**: Go for daemon (single binary), Python for AI logic
2. **Linear first**: User owns linearinvoices.com, natural synergy
3. **Navigator integration**: Context efficiency for Claude Code execution
4. **SQLite**: Local-first, portable memory storage
5. **Homebrew HEAD install**: Using `--HEAD` for now until proper releases

## Repositories

- **Main**: https://github.com/qf-studio/pilot
- **Homebrew tap**: https://github.com/qf-studio/homebrew-pilot

## Pending / Next Steps

1. **Week 5-6**:
   - End-to-end test with real Linear webhook (needs ngrok)
   - Full Claude Code integration test
   - TUI dashboard wiring

2. **Week 7-8**:
   - Daily brief scheduler
   - Cross-project memory sync
   - GitHub release automation

3. **Future**:
   - Jira/Asana adapters
   - Pilot Cloud (hosted version)

## Key Commands

```bash
# Install
brew tap qf-studio/pilot
brew install --HEAD pilot

# Run
pilot init
pilot start
pilot test -t "Task title" -p /path/to/project

# Dev
cd ~/Projects/startups/pilot
make build
make test
```

## Config Location

`~/.pilot/config.yaml` - needs Linear API key and Slack token for full functionality

---

**To resume**: Start new session in `/Users/aleks.petrov/Projects/startups/pilot` and run `"Start my Navigator session"`
