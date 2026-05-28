---
name: task-314-autopilot-scanner-released-commits-keying
description: P0 — Fix ScanRecentlyMergedPRs releasedCommits keying mismatch that re-adds released PRs to activePRs every scan cycle
metadata:
  type: task
  priority: P0
  area: autopilot
---

# TASK-314: Fix `ScanRecentlyMergedPRs` releasedCommits keying mismatch

**Status**: ✅ Done — shipped `9fe96aa7` (Option B: per-PR `GetTagForSHA`), released v2.159.1 (2026-05-27)
**Created**: 2026-05-27
**Completed**: 2026-05-28
**Priority**: P0 (correctness — dashboard misrepresents state, autopilot loops forever on released PRs)
**Area**: `internal/autopilot/controller.go`

---

## Context

**Problem**:
The merged-PR scanner builds a `releasedCommits` set keyed by `release.TargetCommitish` and looks it up by `pr.MergeCommitSHA`. For releases created by GoReleaser (or any release where `target_commitish` is the branch ref), `TargetCommitish == "main"`, so the lookup never hits. Result: the "release already exists" skip gate (controller.go:2310) is dead code on this repo and the scanner re-adds every recently merged Pilot PR to `activePRs` with `Stage=StageReleasing` on every cycle.

**Observed**: 2026-05-27 dashboard showed PR #3205 stuck at `◐ release` for 5h48m, "+ 3 more PR(s)" (all 4 release PRs of the day: #3205/#3208/#3210/#3212). All 4 had successful releases (v2.156.0–v2.159.0) published on GitHub. Daemon restart cleared activePRs (DB was already clean), confirming the corruption was purely in-memory.

**Evidence (verified via gh API)**:
```bash
$ gh release view v2.156.0 --json targetCommitish
{"targetCommitish":"main"}   # branch ref, not the merge SHA

$ gh api repos/qf-studio/pilot/git/refs/tags/v2.156.0 --jq '.object.sha'
5e9298ca9acd7ef02c4259512ac11920a7305db4   # this IS PR #3205's merge SHA
```

The tag exists on the right SHA; the scanner just can't see it because it's comparing branch names to commit SHAs.

**Goal**:
Make the "release already exists" gate actually skip released PRs. Either resolve each release's tag to its target SHA, or replace the heuristic with the same primitive `handleReleasing` already uses (`GetTagForSHA`).

---

## Acceptance Criteria

- [ ] `ScanRecentlyMergedPRs` correctly skips merged Pilot PRs whose merge SHA is already tagged/released
- [ ] After a release fires, the next scanner cycle does NOT re-add the PR to `activePRs`
- [ ] Unit test reproducing the bug: scanner sees a closed Pilot PR with merge SHA `X` AND a release tag pointing at `X` → scanner does NOT call `trackPR`/insert into `activePRs`
- [ ] Existing tests for orphan-merge recovery (PRs that merged externally with NO release tag yet) still pass — i.e. the scanner still picks up genuinely orphaned merges
- [ ] No new GitHub API requests in the hot path that would noticeably slow scans (resolve once per scan, not once per PR)

---

## Implementation

### Option A (preferred): Resolve release tags to SHAs once per scan

In `ScanRecentlyMergedPRs` (controller.go:2239–2251):
1. After `ListReleases`, resolve each `release.TagName` → tag-object SHA via `git/refs/tags/{tag}` (or a batched primitive on the client).
2. Build `releasedCommits` keyed by the resolved SHA, not `TargetCommitish`.
3. Cache the resolution within the scan to avoid N+1 — N is bounded by 20.

**Pros**: surgical change, scanner stays self-contained, one extra round-trip per release (20 max).
**Cons**: extra API calls; can be cached across scans by SHA.

### Option B: Switch the gate to `GetTagForSHA` per-PR

Replace lines 2238–2251 + 2310 with: for each candidate PR in the loop, call `c.ghClient.GetTagForSHA(ctx, c.owner, c.repo, pr.MergeCommitSHA)`. Skip if non-empty.

**Pros**: same primitive `handleReleasing` already trusts; single source of truth.
**Cons**: O(N) API calls in the scan loop where N = recently-merged Pilot PRs (typically <10 in the 30m window, so cheap in practice).

### Recommended

Go with **Option B** — simpler, fewer moving parts, eliminates the keying-by-string-type bug class entirely, and reuses the existing primitive. The cost (a handful of `GetTagForSHA` calls every `MergedPRScanWindow/2`) is negligible.

### Files

- `internal/adapters/github/client.go` (or wherever `ListReleases` / `GetTagForSHA` live) — no changes expected, just usage
- `internal/autopilot/controller.go:2238–2316` — rewrite the released-commits lookup
- `internal/autopilot/controller_test.go` — add reproduction test

---

## Out of Scope

- The deeper question "why is `target_commitish` empty/branch-name and not the SHA on GoReleaser releases?" — leave as upstream GoReleaser behavior, just don't rely on it
- Reconciliation-loop changes (`reconcileOrphanPRs`) — separate code path, not implicated
- `handleReleasing` fallthrough hardening — see TASK-316
- Persisting a "released-PR" allowlist to disk — overkill, in-memory `activePRs` + a working scanner gate is enough

---

## Technical Decisions

| Decision | Options | Recommended | Reasoning |
|---|---|---|---|
| Keying source | `TargetCommitish` (broken), resolved tag SHA, per-PR `GetTagForSHA` | per-PR `GetTagForSHA` | Reuses primitive `handleReleasing` trusts; eliminates string-type-mismatch bug class |
| When to skip | After merge-metric record, before `trackPR` | Same as today (line 2310) | Preserve idempotent merge metrics; only short-circuit the activePRs add |

---

## Verify

```bash
# Unit test
go test ./internal/autopilot/ -run TestScanRecentlyMergedPRs_SkipsReleased -v

# Live verify after deploy
pilot stop && pilot start --github --env stage --dashboard
# Merge a Pilot PR, wait for release tag, wait for next scan cycle (≤15m)
# Dashboard AUTOPILOT panel should show "idle · no active PR" after release completes
```

---

## Done

- [ ] Reproduction test fails on `main`, passes on the fix branch
- [ ] After landing, `pilot start` + ship a Pilot PR + wait through one full scan cycle → AUTOPILOT panel returns to idle
- [ ] No regression on orphan-merge recovery (PRs merged externally with no tag still get picked up and released)

---

## Refs

- Investigation transcript: this session (2026-05-27)
- Smoking-gun controller code: `internal/autopilot/controller.go:2239`, `:2245–2251`, `:2310`
- Existing primitive to reuse: `GetTagForSHA` (already used at `controller.go:1659`)
- Related (sibling fix): TASK-316 — `handleReleasing` fallthrough on `GetTagForSHA` error

---

**Last Updated**: 2026-05-27
