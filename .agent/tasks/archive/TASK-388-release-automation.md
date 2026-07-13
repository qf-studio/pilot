# feat(autopilot): release automation — publish modes, verification, human merges (TASK-388)

**Status**: ✅ SHIPPED (2026-07-07). A #3926 ✅ v2.223.0 · B #3927 ✅ via #3952/#3953/#3954 (v2.226–v2.228; parent manually closed — ghost-close veto loop, breaker filed #4006 ✅) · C #3928 ✅. Config rollout applied to all 8 repos 2026-07-07 (backup: config.yaml.bak-20260707-release-rollout). Ready to archive.
**Created**: 2026-07-06
**Assignee**: Pilot

---

## Context

**Problem**:
Autopilot tags all 8 wired repos on merge, but only **pilot** has a tag-triggered
release workflow (GoReleaser). Found live 2026-07-06: autopilot tagged
studio-sdk v0.29.0, logged `"tag created (GoReleaser will create release)"`
(`controller.go:2284`), and no GitHub Release ever appeared — the chain broke
silently (studio-sdk has CI only; its past releases were manual). Separately,
human-authored `feat/*` merges never reach the release pipeline
(`ScanRecentlyMergedPRs` filters `pilot/*`, `controller.go:2974`) — v2.215.0
needed a manual tag for PRs #3890/#3894–96.

**Goal**: three independently mergeable changes —
- **A**: per-project `release.publish: workflow|api|tag_only`; in `api` mode
  autopilot POSTs the GitHub Release itself (changelog body). Accurate logs.
- **B**: in `workflow` mode, verify the release publishes within a window;
  `release_missing` alert when it doesn't (today's incident, made loud).
- **C**: opt-in `release.tag_human_merges` so human merges to the default
  branch enter the existing release pipeline.

---

## Known Pitfalls & Patterns

<!-- From knowledge graph — these MUST shape the implementation -->

- **DECISION** (decision_release_pipeline_tag_only): **Never pre-create a GitHub
  Release on a repo with a GoReleaser workflow** — GoReleaser 422-collides on
  assets and silently skips the Homebrew formula publish (bit v2.149.4 and
  v2.166.6). Consequence: `api` mode is ONLY for repos with no tag-triggered
  release workflow; pilot itself stays `workflow`. Document as a config footgun.
- **PATTERN** (pattern_autopilot_pr_state_ephemeral): `autopilot_pr_state` rows
  are deleted on lifecycle completion (`removePR`). Post-tag verification cannot
  persist state there — hence fire-and-forget goroutine + scanner backstop.
- **PATTERN** (pattern_squash_merge_mergedat_null): squash-merged PRs can show
  `mergedAt: null` while the commit is on main — merged-PR detection must not
  rely on `mergedAt` alone (existing scanner checks apply to human PRs too).
- **PITFALL** (pitfall_sdk_ports_go_stale_vs_intree): autopilot uses the
  studio-sdk github client (v0.28.1+) — all release APIs (`CreateRelease`,
  `GetReleaseByTag`, `UpdateRelease`) already exist there; no SDK release needed,
  and no new in-tree HTTP calls.

---

## Issue ledger

| Issue | Title | Deps | Status |
|---|---|---|---|
| [#3926](https://github.com/qf-studio/pilot/issues/3926) | per-project release publish mode + API-published Releases | — | dispatched |
| [#3927](https://github.com/qf-studio/pilot/issues/3927) | post-tag release verification + `release_missing` alert | Blocked by: #3926 | dispatched (gated) |
| [#3928](https://github.com/qf-studio/pilot/issues/3928) | opt-in release tagging for human merges | Blocked by: #3926 | dispatched (gated) |

---

## Design decisions

| Decision | Chosen | Reasoning |
|---|---|---|
| Publish mechanism | autopilot API, per-project knob | Centralized in the hub; no workflow files replicated across 8 repos; SDK client already has `CreateRelease` |
| Per-project config semantics | overlay (project > env > global), pointer fields | Env blocks are full-replacement; an overlay avoids `publish:`-only blocks silently disabling releases (`Enabled` zero-value) |
| Default publish mode | `workflow` (empty value) | Byte-identical behavior for every existing config; pilot self-upgrade depends on GoReleaser assets |
| Verification persistence | none — goroutine + `ScanRecentlyMergedPRs` backstop | Hot upgrades restart the daemon several times/day; PR-state rows are ephemeral by design; backstop covers ≤ `merged_pr_scan_window` (30m) |
| Human-merge tagging | opt-in flag, scanner-only, default-branch-only | `DetectBumpType` already gates docs/chore; Pilot-only side effects (metrics/self-heal/board) stay gated on `pilot/*` branches |
| `generate_summary` wiring | excluded | Never wired in prod (`SetReleaseSummaryGenerator` uncalled in main.go); separate follow-up if wanted |

---

## Config rollout (ops, after all three ship + daemon self-upgrade)

⚠️ After B ships, every tagged repo without a release workflow will alert
unless its publish mode is set. Edit `~/.pilot/config.yaml`:

| repo | `publish` | `tag_human_merges` |
|---|---|---|
| pilot | `workflow` (default — GoReleaser owns publishing, see decision_release_pipeline_tag_only) | `true` |
| studio-sdk | `api` | `true` |
| other 6 | `api` (or `tag_only` per repo, decide at edit time) | `false` (default) |

Optional cleanup: `gh release create v0.29.0` on studio-sdk manually (the
backstop will not retro-create old tags).

---

## Verify

```bash
go test ./internal/autopilot/... ./internal/alerts/... ./internal/config/...
make lint && make build
```

End-to-end after rollout: next merged PR on studio-sdk ⇒ tag **and** GitHub
Release with changelog; daemon log line "resolved release policy" shows
`publish=api source=project:studio-sdk`. Next pilot release ⇒ GoReleaser +
self-upgrade chain unchanged.

---

## Refs

- Plan: `~/.claude/plans/i-d-like-to-work-keen-bonbon.md` (approved 2026-07-06)
- Incident: studio-sdk v0.29.0 tag without release (2026-07-06, this session);
  v2.215.0 manual tag (context marker 2026-07-06_m7-trial-verified-incidents-fixed)
- Key code: `internal/autopilot/controller.go` (`handleReleasing` :2152, log :2284,
  scanner :2942), `releaser.go`, `types.go:351` (ReleaseConfig),
  `internal/config/config.go:232` (ProjectConfig), `internal/alerts/engine.go`
- Issues: #3926 (publish modes) · #3927 (verification, gated) · #3928 (human merges, gated)

---

**Last Updated**: 2026-07-06
