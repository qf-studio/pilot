# fix(autopilot): reconcile sentinel-bundle truncation with the excerpt assembly budget

**Status**: 🚀 Dispatched 2026-08-11 → [pilot#4844](https://github.com/qf-studio/pilot/issues/4844) (`pilot` + `no-decompose`)
**Created**: 2026-08-11
**Assignee**: Pilot

## Context

PR#4839 (GH-4825) fixed failure-issue bodies with match-anchored excerpt windows. Post-merge review found one in-scope gap plus small riders:

- **Main**: the sentinel-bundle path still blind-cuts `truncateKeepingTail(bundle, 4000)` (internal/autopilot/feedback_loop.go:409) while upstream assembly budgets 12000 chars (internal/autopilot/ci_failure_excerpt.go:23). A multi-check failure whose bundle exceeds 4000 gets cut through the middle — middle checks' match-anchored failing lines can still be dropped, which is the exact failure mode GH-4825 was about. The adjacent "defensive no-op" comment is false whenever the bundle exceeds 4000.
- **Rider 1**: `^FAIL\t` marker (feedback_loop.go:27) is line-anchored but GitHub Actions timestamp-prefixes every log line, so it never fires on the production tiers; `--- FAIL:` covers the real cases. Un-anchor or drop it.
- **Rider 2**: `truncateKeepingTail` (feedback_loop.go:147-151) slices bytes and can split a multi-byte UTF-8 rune at either cut point. Make both cuts rune-safe (byte-capping direction is correct vs GitHub's limit — keep it, just don't split runes).
- **Rider 3**: the old head-truncating `GetFailedCheckLogs` (internal/autopilot/ci_monitor.go:818) survives as production-dead code with test-only callers. Delete it and migrate/delete its tests, making the single-excerpt-path property durable.

## Implementation

1. Align the `:409` bundle cap with the assembly budget — pass/reuse the 12000 assembly constant (or make the truncation window-aware so complete match windows are dropped whole, oldest first, rather than mid-window cuts). Fix the stale comment.
2. Apply riders 1–3.
3. Test: multi-check bundle > 4000 chars where the middle check carries the failing line — assert the failing line survives into the final body; keep the 65536 GitHub body-limit assertion intact.

Out of scope: marker-set changes beyond `^FAIL\t` (the GH-4825 marker work is done); issue-body format.

## Acceptance

- The multi-check middle-failing-line test passes and provably fails against the pre-change `:409` cut.
- No production caller of the deleted `GetFailedCheckLogs` remains (grep-clean).
- Existing GH-4825 tests pass unchanged.

## Refs

- Review verdict: https://github.com/qf-studio/pilot/pull/4839#issuecomment-5253740363
- Prior work: PR#4839 (GH-4825), excerpter GH-4779
