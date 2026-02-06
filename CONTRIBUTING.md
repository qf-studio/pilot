# Contributing to Pilot

Pilot is source-available under [BSL 1.1](LICENSE). Contributions are welcome and appreciated. This guide covers how to set up a development environment, submit changes, and follow project conventions.

## Development Setup

### Prerequisites

- **Go 1.22+** — [go.dev/dl](https://go.dev/dl/)
- **Claude Code CLI** — Required for running Pilot's executor
- **GitHub CLI** (`gh`) — For testing issue/PR workflows
- **Make** — Build automation

### Clone and Build

```bash
git clone https://github.com/alekspetrov/pilot.git
cd pilot
make build
```

The binary is output to `bin/pilot`.

### Run Tests

```bash
make test
```

### Lint

```bash
make lint
```

### Format

```bash
make fmt
```

## How to Contribute

### Report Bugs

Open a [GitHub Issue](https://github.com/alekspetrov/pilot/issues/new) with:
- Steps to reproduce
- Expected vs actual behavior
- Pilot version (`pilot --version`)
- OS and architecture

### Suggest Features

Open a GitHub Issue with the `enhancement` label. Describe the use case and why it matters. Feature discussions happen in issues before implementation starts.

### Submit Code

1. **Open an issue first** for non-trivial changes. This avoids duplicated effort and ensures alignment on approach.
2. Fork the repository
3. Create a feature branch from `main`
4. Make your changes
5. Run `make test && make lint` — both must pass
6. Submit a pull request

### Improve Documentation

Documentation lives in two places:
- `docs/` — Nextra docs site (MDX pages)
- `README.md` — Project overview

PRs for documentation improvements follow the same process as code changes.

## Code Standards

### Go Conventions

- **Formatting**: `go fmt` (enforced by CI)
- **Linting**: `golangci-lint` (enforced by CI)
- **Testing**: Table-driven tests are the standard pattern
- **Indentation**: Tabs (Go standard)

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
type(scope): description

feat(executor): add timeout configuration
fix(autopilot): refresh HeadSHA before CI check
refactor(dashboard): extract card renderer
test(controller): add merge state transition tests
docs(readme): update installation instructions
chore(ci): upgrade Go version in workflow
```

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`

### Test Tokens

Never use realistic API key patterns in test code. GitHub push protection blocks them.

```go
// Bad — will be blocked by push protection
token := "xoxb-123456789012-1234567890123-abcdefghij"

// Good — use test utilities
import "github.com/alekspetrov/pilot/internal/testutil"
token := testutil.FakeSlackBotToken
```

See `internal/testutil/tokens.go` for all available fake tokens.

## Pull Request Process

1. **CI must pass** — Tests, linting, and formatting are checked automatically
2. **One approval required** — A maintainer reviews all PRs
3. **Keep PRs focused** — One logical change per PR. Split large changes into smaller PRs.
4. **Update tests** — New features need tests. Bug fixes need regression tests.
5. **Update docs** — If your change affects user-facing behavior, update the relevant documentation.

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── adapters/        # GitHub, Telegram, Linear, Slack, Jira
│   ├── alerts/          # Alert engine and channels
│   ├── config/          # Configuration loading
│   ├── dashboard/       # Terminal UI (bubbletea)
│   ├── executor/        # Claude Code process management
│   ├── gateway/         # WebSocket + HTTP server
│   ├── memory/          # SQLite persistence
│   └── testutil/        # Test helpers and fake tokens
├── docs/                # Nextra documentation site
└── configs/             # Example configuration files
```

## License

Pilot is licensed under [BSL 1.1](LICENSE) (Business Source License). By contributing, you agree that your contributions will be licensed under the same terms.

**What BSL 1.1 means for contributors:**
- Your contributions are source-available, not open source (by OSI definition)
- The code converts to Apache 2.0 after the change date specified in the LICENSE
- Free for internal use, self-hosting, evaluation, and development
- The license restricts offering Pilot as a competing hosted service

If you have questions about the license, open an issue or reach out to the maintainers.

## Getting Help

- **GitHub Issues** — Bug reports and feature requests
- **GitHub Discussions** — Questions and community conversations
- **Email** — hello@quantflow.studio

Thank you for contributing.
