# Installation

## Homebrew (Recommended)

```bash
brew tap alekspetrov/pilot
brew install pilot
```

## Go Install

```bash
go install github.com/alekspetrov/pilot/cmd/pilot@latest
```

## From Source

```bash
git clone https://github.com/alekspetrov/pilot
cd pilot
make build
sudo make install-global
```

## Requirements

- **Go 1.22+** (build only)
- **[Claude Code CLI](https://github.com/anthropics/claude-code)** 2.1.17+
- **OpenAI API key** (optional, for voice transcription)

## Verify Installation

```bash
pilot version
```

Expected output:

```
Pilot v1.8.1
```

## Next Steps

After installation, proceed to:

1. [Quick Start](quickstart.md) - Get running in 2 minutes
2. [Configuration](config.md) - Customize Pilot for your workflow
