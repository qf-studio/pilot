---
name: GoReleaser Release Cancelled
description: Mysterious GoReleaser job cancellation + GITHUB_TOKEN does not fire downstream workflow events
type: project
originSessionId: 86aef822-8124-4724-816f-1f26cf305635
---
Pattern observed v2.75.0 (2026-03-06): GoReleaser job marked `cancelled` (0 steps ran), tag exists but no GitHub Release or binaries created. Subsequent autopilot tags also blocked — unclear if `handleReleasing()` was reached after `checkExternalMergeOrClose()` intercepted merged PRs.

Also: `parseBumpFromMessage()` returns `BumpNone` for `test/docs/chore/ci` commits → no release (by design).

Also: `docs-version-sync.yml` stops triggering because GoReleaser uses `GITHUB_TOKEN` which doesn't fire downstream workflow events (GitHub limitation). PAT required for chained-trigger workflows. Cross-ref: `feedback_gh_actions_pr_create_perm.md`.

**Why:** Multiple silent-failure modes around release + chained-workflow triggering. Root cause of cancellations never confirmed.

**How to apply:** When release pipeline appears stuck, check (1) GoReleaser job status (may be cancelled, not failed), (2) commit type since last release (only feat/fix/refactor bump), (3) chained-workflow auth (need PAT, not GITHUB_TOKEN). Manual recovery: tag at HEAD, GoReleaser will run.
