# Getting Started with Pilot

This directory contains examples, templates, and quick setup resources to help you get started with Pilot.

## Contents

- `example-config.yaml` - Example configuration file with common settings
- `sample-tasks/` - Sample task definitions and examples
- `setup.sh` - Quick setup script for development environment

## Quick Setup

1. **Initialize Pilot:**
   ```bash
   pilot init
   ```

2. **Copy example config:**
   ```bash
   cp getting-started/example-config.yaml ~/.pilot/config.yaml
   ```

3. **Start Pilot:**
   ```bash
   pilot start --github --telegram
   ```

## Next Steps

For detailed documentation, see:
- [Installation Guide](../docs/getting-started/install.md)
- [Quick Start Guide](../docs/getting-started/quickstart.md)
- [Configuration Reference](../docs/getting-started/config.md)