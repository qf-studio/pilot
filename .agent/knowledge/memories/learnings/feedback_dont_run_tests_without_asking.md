---
name: Don't launch bench runs without explicit ask
description: Never launch bench runs or tests autonomously — wait for explicit user instruction
type: feedback
originSessionId: aeaf0f5e-b335-4bda-bd0e-ec1e09d65a61
---
Never launch bench runs, orchestrator, or AWS tests without explicit user instruction. User will say when to run.

**Why:** Each run burns tokens against a daily limit and costs EC2 time. Premature launches waste both. Multiple runs were killed mid-session because of this.

**How to apply:** After committing fixes, stop. Don't auto-relaunch. Say "ready to launch when you say" and wait.
