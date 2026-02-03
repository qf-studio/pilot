# PR Merge Checklist

**Purpose**: Prevent incomplete merges where changes don't propagate to all relevant commands.

## Pre-Merge Verification

### Code Quality
- [ ] Built locally: `go build ./...`
- [ ] Tests pass: `go test ./...`
- [ ] Linter passes: `make lint`
- [ ] No new warnings introduced

### CLI Flag Changes
If PR adds/modifies CLI flags:
- [ ] Flag exists in **ALL relevant commands** (start, task, github run)
- [ ] `pilot <command> --help` shows new flag for each command
- [ ] Flag actually works when used: `pilot <command> --<flag>`
- [ ] Flag documented in DEVELOPMENT-README.md CLI Flags section

### Integration Verification
- [ ] `make build && ./bin/pilot --help` shows correct version/flags
- [ ] Manual test of the changed functionality
- [ ] Ctrl+C / quit works if TUI involved
- [ ] No panics or error logs during normal operation

### Architecture Changes
If PR modifies core components:
- [ ] `internal/executor/runner.go` still includes Navigator integration
- [ ] BuildPrompt() still adds "Start my Navigator session" when `.agent/` exists
- [ ] Alert interfaces remain compatible

### Documentation
- [ ] CLAUDE.md updated if behavior changes
- [ ] DEVELOPMENT-README.md updated if new features/flags
- [ ] Help text is clear and accurate

## Common Failure Modes

| Failure | Example | Prevention |
|---------|---------|------------|
| Flag only in one command | Missing flag in start vs task | Check all cmd/*.go files |
| Navigator integration removed | BuildPrompt() "simplification" | Never touch runner.go Navigator block |
| Broken TUI exit | Dashboard hangs on Ctrl+C | Always test quit path |
| Missing help text | Flag works but --help blank | Verify cobra flag description |

## Quick Verification Commands

```bash
# Build and basic check
make build && ./bin/pilot --help

# Check flag in all commands
./bin/pilot start --help | grep -i "<flag-name>"
./bin/pilot task --help | grep -i "<flag-name>"
./bin/pilot github run --help | grep -i "<flag-name>"

# Run tests
make test

# Lint
make lint
```

## Before Approving

1. **Read the diff** - Don't just skim
2. **Check scope** - Does change affect multiple commands?
3. **Test locally** - Clone branch, build, run
4. **Verify docs** - Help text matches behavior
