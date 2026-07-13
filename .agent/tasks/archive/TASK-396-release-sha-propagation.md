# TASK-396: Release SHA propagation — unconditional HeadSHA←PostMergeSHA copy-back in handleReleasing

**Status**: ✅ SHIPPED 2026-07-09 — fix merged as `877e08c3` (HeadSHA←PostMergeSHA resync in `handleReleasing`); daemon runs it since 20:11 UTC. The `releasing → failed → re-adopt` ✗-loop on #4139/#4144/#4145 should clear; verify the next release train cuts cleanly.
**Created**: 2026-07-09
**Assignee**: Pilot

---

## Context

**Problem** (live incident 2026-07-09, stage daemon v2.236.2/.3 — release
pipeline fully wedged, second layer of the mem-093 incident):

Every plain (non-scope) `on_merge` PR that reaches `StageReleasing` fails the
SHA-reachability guard and loops `releasing → failed → re-adopt →
post_merge_ci → releasing → failed` every ~2 minutes, forever. Live evidence:
PR #4139 (external merge, 18 attempts) and PR #4144 (internal autopilot
merge, 6+ attempts) both escalate with
`handleReleasing: escalating to StageFailed ... reason="SHA <head> is not
reachable from main (compare status: \"diverged\")"`. **No organic tag can be
cut** — v2.236.4 blocked; v2.236.2/.3 were manual bootstrap tags.

**Root cause** (researched, exact):

`PRState` carries two SHA fields with different lifecycles:
- `HeadSHA` (types.go:719) — pre-merge branch head, set at `OnPRCreated`
  (controller.go:838-854), reconciler adoption, `handleWaitingCI` refresh.
- `PostMergeSHA` (types.go:780) — the CI-validated post-squash-merge main
  SHA, set correctly by `handlePostMergeCI` (controller.go:2342-2348) and by
  `checkExternalMergeOrClose`'s `RequireCI` branch (controller.go:4413-4414).

The copy-back that syncs them in `handleReleasing` is **gated on scope-release
carriers only** (controller.go:2619-2621, added 2026-07-07 by GH-3990):

```go
isScope := prState.ScopeKey != ""
if isScope {
    prState.HeadSHA = prState.PostMergeSHA
    ...
}
```

For a plain PR, `guardReleaseSHAReachable` (controller.go:3172) then calls
`CompareStatus(base=prState.HeadSHA, head=mainSHA)` (controller.go:3191) with
the **stale branch head**. Pilot squash-merges (`auto_merger.go:80`,
`MergeMethodSquash`); a squash-merged branch head is structurally never an
ancestor of main → compare is always `"diverged"` → `escalateReleasingFailed`
(controller.go:2969) after the retry cap. The re-adoption loop refreshes
`PostMergeSHA` each lap but never `HeadSHA`, so it can never self-heal.

**Regression window**: dormant, not new. Guard added 2026-06-10 (#3559,
commit 09dcb16e); scope-only copy-back 2026-07-07 (GH-3990, b8e13a99); the
path was shielded because the mem-093 drain bug prevented plain merged PRs
from ever reaching `handleReleasing` — **unmasked 2026-07-09 by #4125**
(commit 5c9d2328). `git log -S` confirms none of today's other merges touched
these paths.

**Goal**: plain `on_merge` releases cut tags again — propagate the
CI-validated merge SHA into the field the release guard reads,
unconditionally.

---

## Known Pitfalls & Patterns

- **PITFALL** (95%, mem-093): the sibling defect one layer up — merged PRs
  were drained at `checkExternalMergeOrClose` before `handleReleasing` ran;
  fixed by #4125. Its lesson applies verbatim here: **existing releasing
  tests call `handleReleasing` directly with hand-built, already-consistent
  state and stop at stage transitions** — the new test must drive
  `post_merge_ci → releasing → tag cut` through the real tick path.
- **PATTERN** (research, 2026-07-09): any consumer of the release stage must
  read `PostMergeSHA` when set, not `HeadSHA`, once a PR passed
  `StagePostMergeCI`. `tagCoveringCommit`/`GetTagForSHA` reads at
  controller.go:2650/2678 sit after the fix point and benefit automatically.
- **LEARNING** (95%, mem-020): manual release = push a `v*` tag (release.yml
  GoReleaser); interim operator workaround while this fix lands.

---

## Acceptance Criteria

- [ ] In `handleReleasing`, `HeadSHA` is resynced from `PostMergeSHA`
      **whenever `PostMergeSHA != ""`**, scope or not. The empty-check
      preserves the `SkipPostMergeCI` fast path (controller.go:2090-2110)
      where `PostMergeSHA` is legitimately unset and the branch-head
      `HeadSHA` may still be reachable (non-squash/FF configs).
- [ ] A plain (non-scope) `PRState` with stale diverged `HeadSHA` and valid
      `PostMergeSHA` **releases successfully** — tag cut against
      `PostMergeSHA`, no escalation. (New case in
      `TestHandleReleasing_DivergedSHARefused`, controller_releasing_test.go:732 —
      current fixture starts already-consistent and never exercises the
      propagation gap.)
- [ ] `TestCheckExternalMergeOrClose_RequireCITrue_RoutesToPostMergeCI`
      (controller_release_cycles_test.go:555) and
      `TestScanRecentlyMergedPRs_RequireCI_RoutesToPostMergeCI` (:628) are
      extended to drive **one more tick into `handleReleasing`** and assert a
      tag is created against the merge SHA instead of an escalation.
- [ ] Scope-carrier behavior unchanged (scope_release_test.go:145-157 still
      green).
- [ ] Full suite + lint pass.

---

## Implementation

### Phase 1: Unconditional copy-back + tick-through regression tests
**Goal**: one small diff in `handleReleasing`, three test extensions.

**Tasks**:
- [ ] controller.go:2619-2621 — replace the scope-gated copy-back:
      ```go
      // PostMergeSHA is authoritative whenever handlePostMergeCI (or the
      // require_ci external-merge branch) ran — resync scope or not.
      if prState.PostMergeSHA != "" {
          prState.HeadSHA = prState.PostMergeSHA
      }
      if isScope && len(prState.ScopeMemberPRs) == 0 {
          c.hydrateScopeMembers(prState)
      }
      ```
      (Preserve the existing scope-member hydration exactly; only the
      copy-back gate widens.)
- [ ] Extend the three tests listed in Acceptance Criteria — the two
      route-to-post-merge-CI tests must not stop at the
      `StagePostMergeCI → StageReleasing` transition (the mem-093 blind spot).
- [ ] Do NOT change `guardReleaseSHAReachable` semantics, retry caps, or
      `escalateReleasingFailed` — the guard is correct once fed the right SHA.

**Files**:
- `internal/autopilot/controller.go` — `handleReleasing` (~2619)
- `internal/autopilot/controller_releasing_test.go`
- `internal/autopilot/controller_release_cycles_test.go`

---

## Out of Scope

- Capturing the merge-commit SHA at `handleMerging` success (re-fetch PR
  after `MergePR`; `MergePullRequest` returns only `error`) — belt-and-
  suspenders hardening, separate follow-up if wanted.
- Recovery of the currently-looping PRs #4139/#4144/#4145 — they resolve on
  the next daemon upgrade or age out; operator cuts v2.236.4 manually
  (mem-020) in the interim.
- The `SkipPostMergeCI` path behavior — unchanged by design.
- Daemon self-upgrade adoption (mem-044: hot upgrade needs a TUI keypress) —
  operational, not code.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where to fix SHA propagation | (a) widen copy-back in `handleReleasing`; (b) capture merge SHA at `handleMerging`; (c) make guard read `PostMergeSHA` directly | (a) | Smallest diff at the single choke point every release path funnels through (internal, external-require_ci, scan-recovery); (b) misses external merges; (c) touches guard call sites and the later `tagCoveringCommit` reads that also need the synced value. |
| Copy-back condition | (a) unconditional; (b) `PostMergeSHA != ""` | (b) | `SkipPostMergeCI` fast path never sets `PostMergeSHA`; unconditional would blank `HeadSHA` there. |

---

## Verify

```bash
make test
make lint
go test ./internal/autopilot/ -run 'Releasing|ReleaseCycles|ScopeRelease' -v
```

Live validation (post-merge): merge any PR → daemon log shows
`handleReleasing` cutting the tag (no `escalating to StageFailed` with a
head SHA); `git ls-remote --tags origin` shows the new `v*` tag; dashboard
autopilot panel shows `released`, not the failed/release loop.

---

## Done

- [ ] Copy-back widened at controller.go:2619 with `PostMergeSHA != ""` guard
- [ ] Diverged-HeadSHA + valid-PostMergeSHA case releases in unit tests
- [ ] Both route-to-post-merge-CI tests tick through `handleReleasing`
- [ ] Scope-carrier tests unchanged and green; full suite + lint green

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4146
- Research: navigator-research agent a8924ab7144423881, 2026-07-09 (SHA
  lifecycle table, regression-window verdict)
- Unmasking fix: #4124 → PR #4125 (mem-093, StageReleasing drain guard)
- Guard origin: GH-3558 → PR #3559 (2026-06-10); scope copy-back: GH-3990
  (2026-07-07, b8e13a99)
- Live evidence: daemon.log 2026-07-09 20:30–21:19 CEST, PRs #4139/#4144;
  head 0b9d4bf vs squash 3ee1d7e (PR #4139)
- Sibling task: TASK-394 / #4140 (epic sub-issue execution ledger)

---

**Last Updated**: 2026-07-09
