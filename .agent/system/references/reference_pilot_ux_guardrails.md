---
name: Pilot UX guardrails shipped in v2.95.13
description: Three behavior changes in v2.95.13 that affect debugging and issue-creation expectations
type: reference
originSessionId: d180fb61-9631-48b8-bbe2-dc3fc59e81ad
---
Shipped 2026-04-18 in v2.95.13. Future sessions should expect these
behaviors:

### 1. `pilot doctor` detects adapter-gap (GH-2361)
If `projects:` are configured but **no** issue-source adapter
(`adapters.github|gitlab|linear|jira|asana|azuredevops|plane`) is enabled,
doctor now emits a warning. Previously reported "All systems operational!"
even when the poller had nothing to do.

Also: `pilot start --github` (and sibling flags) now fail loudly if the
corresponding adapter isn't enabled in config.

**Debugging implication:** when a user reports "dashboard up but no issues
picked up," ask them to run `pilot doctor` first — if the adapter block is
missing, doctor will now name the problem directly instead of hiding it.

### 2. Title-rejection auto-suggest (GH-2363)
After the **2nd** consecutive conventional-commit rejection on the same
issue title, Pilot:
- Posts a single comment with the rejected title + suggested rewrite +
  copy-pasteable `gh issue edit` command
- Adds `pilot-failed`, stops retrying until the title hash changes

Key file: `internal/executor/title_rejection_test.go`

**Debugging implication:** if you see a 4-in-a-row "PR creation refused:
title is not a conventional commit" pattern on an issue, that's pre-v2.95.13
behavior — the daemon is outdated.

### 3. Repo rename fully landed (GH-2175)
All functional `alekspetrov/pilot` and `anthropics/pilot` refs on `origin/main`
replaced with `qf-studio/pilot`. Residuals remaining are intentional:
- `.agent/tasks/archive/TASK-43/44/45*.md` — historical nav docs
- `.agent/tasks/gh-2175.md` — the migration task doc itself (meta)
- `internal/executor/title_rejection_test.go` — fixtures for the GH-2363
  autofix feature (assertions about rewriting old-org titles)
- `pilot-bench/WORKLOG.md` — historical changelog

**Debugging implication:** if a user hits `ghcr.io/anthropics/pilot:latest`
404, they're on outdated docs; pilot.quantflow.studio now serves correct
`ghcr.io/qf-studio/pilot`.
