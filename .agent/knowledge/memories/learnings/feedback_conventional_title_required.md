---
name: Conventional-commit titles required for Pilot issues
description: All Pilot issues must have conventional-commit titles (type(scope): description) or autopilot will reject PR creation
type: feedback
originSessionId: d180fb61-9631-48b8-bbe2-dc3fc59e81ad
---
When creating a GitHub issue with the `pilot` label, the issue title MUST be a
valid conventional commit (`type(scope): description`). The GH-2325 title gate
rejects PR creation otherwise, and Pilot will burn a full Claude Code run each
retry attempt.

**Why:** Observed on GH-2175 (2026-04-18). An issue titled "Migrate all
alekspetrov/pilot references to qf-studio/pilot" produced 4 consecutive
failures ("PR creation refused: title is not a conventional commit") over
12 minutes before a human renamed it to `chore(repo): replace
alekspetrov/pilot and anthropics/pilot refs with qf-studio/pilot`.

**How to apply:**
- When filing a new `pilot`-labeled issue, start the title with
  `feat|fix|chore|refactor|docs|test(scope):`
- When a community-reported issue has a non-conventional title and you're
  adding the `pilot` label, **rename it first** with
  `gh issue edit N --title "type(scope): ..."`
- Since v2.95.13 (GH-2363), Pilot auto-posts a suggested rewrite + stops
  retrying after the 2nd rejection — but don't rely on that; rename
  proactively to avoid wasted cycles.

**Accepted types:** `feat`, `fix`, `chore`, `refactor`, `docs`, `test`,
`perf`, `ci`, `build`, `style`, `revert`.
