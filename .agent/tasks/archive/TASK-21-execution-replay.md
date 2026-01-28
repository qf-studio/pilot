# TASK-21: Execution Replay & Debugging

**Status**: 📋 Planned
**Created**: 2026-01-26
**Category**: Debugging / DX

---

## Context

**Problem**:
When tasks fail or produce unexpected results, hard to understand what happened.

**Goal**:
Record and replay executions for debugging and improvement.

---

## Features

### Recording
- Full Claude Code stream-json capture
- Tool calls and responses
- File changes (diffs)
- Timing information

### Replay
- Step-by-step playback
- Pause at any point
- Inspect state at each step
- Export for sharing

### Analysis
- Token usage breakdown
- Time spent per phase
- Decision points
- Error root cause

---

## Storage

```
~/.pilot/recordings/
  └── TG-1234567890/
      ├── metadata.json     # Task info, timestamps
      ├── stream.jsonl      # Raw Claude output
      ├── diffs/            # File changes
      └── summary.md        # Human-readable summary
```

---

## CLI

```bash
# List recordings
pilot replay list

# Replay specific task
pilot replay TG-1234567890

# Export for sharing
pilot replay export TG-1234567890 --format html
```

---

## Implementation

### Phase 1: Recording
- Capture stream-json to file
- Store file diffs
- Automatic for all tasks

### Phase 2: CLI Replay
- Terminal-based viewer
- Step forward/backward
- Search within execution

### Phase 3: Web Viewer
- Rich HTML export
- Shareable links
- Team collaboration

---

**Monetization**:
- Free: Last 10 recordings
- Pro: 30 days retention
- Enterprise: Unlimited + export
