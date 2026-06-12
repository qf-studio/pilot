# TASK-361: Autopilot decomposition integrity — GH-3513 incident fix

**Status**: 🟢 fix SHIPPED in **v2.183.0** (2026-06-10) — PR [#3527](https://github.com/qf-studio/pilot/pull/3527) squash-merged to main (`8af5cc18`), tag cut manually (daemon doesn't release `fix/*` branches), goreleaser green, fix artifact-verified at the tag. **MANUAL** fix (TASK-320 B2 rationale).
**Live verification (wave 2)**: 🟢 **FULL EPIC EXERCISED 2026-06-11/12 on GH-3582 — wave-2 integrity HELD.** The v2.186.3 daemon decomposed #3582 into 4 children (#3584–87, single-package — the split itself was wrong, filed as [#3597](https://github.com/qf-studio/pilot/issues/3597): prose never machine-checked, context-cited paths defeat `isSinglePackageScope`). Checklist: ✅ no premature parent close — #3582 closed only after every child resolved; ✅ no false supersession — all content verifiably on main before closes (sanitizer def + wiring + tests via #3590/#3591); ✅ conflict self-heal — #3588/#3589 conflict-closed with explicit comments, #3584 re-executed from updated main → #3595, merged after Telegram approval; ✅ child PR bases correct. **Bookkeeping noise (non-blocking, watch):** children whose content merged via sibling PRs show `no_op` rows (redispatch no-ops — correct, but confusing audit trail); GH-3582's second row credits #3595 (GH-3584's re-execution); GH-3584's row SHA `ae860e7f` ≠ closed PR #3588 head `23c95750` (likely the re-execution branch commit). Archive after #3597 lands.

**Live verification (wave 1, superseded)**: 🔴 **EXERCISED 2026-06-10 on GH-3535 (TASK-285) — MIXED, 2 of 4 checks FAILED.** The v2.183.0 daemon decomposed #3535 → #3536 (memory/CLI, shipped via PR #3539 ✅ base=main, in-scope) + #3537 (TUI wiring) + #3538 (hallucinated OAuth child, recurring junk). Checklist results:
- ✅ Scope fence present in children bodies; #3536's PR stayed in-slice
- ✅ Child PR `baseRefName` == `main` (#3539)
- ❌ **Premature parent close again**: #3535 closed `pilot-done` 10:33:55Z (claiming child's PR #3539 as its own, "Duration 0s", wrong branch name in comment) while #3537 was still open and unshipped
- ❌ **False supersession again, new variant**: #3537 auto-closed 10:51:59Z "parent already shipped this work" — parent had NOT shipped the TUI part. The #3527 open-PR veto can't fire when the child has **no PR yet** — unguarded case. Bonus bug: #3538's supersession comment names parent epic **#201** (wrong-parent match).
- ✅ `ErrParentDone` re-decomposition guard fired (11:07:18Z, `pilot-failed` stacked on `pilot-done` — known guard-collision cosmetics, chain link 3)

Net: TASK-285 partially shipped (TUI wiring missing on main, `tui.go:2446` unscoped; recovery → standalone issue #3552). Residual holes (premature-close path bypassing `openSubIssueCount`, supersession of PR-less children, hallucinated child generation, model-stamped `Parent:` refs) → **TASK-364** (MANUAL, P1).

**Wave 2**: 🟢 **SHIPPED in v2.186.0** (2026-06-10) — **MANUAL** PR [#3565](https://github.com/qf-studio/pilot/pull/3565) squash-merged to main (`b02ee1c1`), tag cut manually at the merge commit, artifacts verified at main HEAD (`shouldDeferIssueClose` present, TASK-358 statuses terminal at `dispatcher.go:507`). +737/−3, 13 new tests. Root cause pinned: `WaitForExecution` non-terminal on TASK-358 statuses → hung handler woken by `selfHealForPR` stamping the child's PR on the parent's row → false "✅ completed" → parent closed via `handleMerging`. Fixes: (1a) classified statuses terminal; (1b) `shouldDeferIssueClose` gate in `handleMerging`; (1c) parent self-heal only when last child merged (fail-closed); (1d) `issueHasOpenChildren` gate on the TASK-321 close path; (2) supersession requires child evidence (merged `pilot/GH-N` PR or completed execution row); (3) empty-description subtask filter + foreign-parent plan rejection; (4) `ErrParentDone` = benign skip, no label stacking. Invariant: decomposed parents close ONLY via the count-verified path. Covers all 4 residual holes listed for TASK-364 except model-stamped `Parent:` refs in LLM output. **First live evidence (2026-06-10 evening, v2.186.0 daemon, #3558/#3559 — partial, leaf path)**: ✅ #3558 closed `pilot-done` with a CORRECT completion comment (own PR #3559, own branch `pilot/GH-3558`, real duration 26m59s — no "Duration 0s", no sibling adoption); ✅ closed parent #3557's execution row NOT falsely promoted (no `completed` row stamped with the child's PR — the GH-3513 failure mode did not reproduce); ✅ no `pilot-failed` stacking. **Full epic checklist still pending the next real decomposed epic** (parent stays open till ALL children's PRs merge; no PR-less supersession).
**Priority**: P1
**Origin**: GH-3513 incident, 2026-06-09 (TASK-284 handoff)

## Incident (full chain, all confirmed)

TASK-284 → issue #3513 (`pilot` label). Autopilot decomposed it into #3514–#3517 (`inherited-spec: true`):

1. **Full-spec inheritance** — children #3515/#3517 each implemented the ENTIRE feature (two ~12-file, byte-near-identical PRs #3519/#3523); #3516 did a 13-line fragment (#3520).
2. **Premature `pilot-done`** — #3520 merged → `maybeCloseParentIssue` saw native open-count 0 (partial `LinkSubIssue` coverage) → closed parent #3513 `pilot-done`. No PR-merge verification anywhere.
3. **Guard collision** — re-decomposition hit `ErrParentDone` → `pilot-failed` stacked on `pilot-done`.
4. **False supersession** — poller auto-closed #3514/#3515/#3517 as "parent already shipped this work" off the label alone → PRs #3519/#3523 orphaned (CI-green, MERGEABLE, never merged).
5. **Phantom release** — #3520's base was sibling branch `pilot/GH-3515`, NOT main; its merge commit `aa3b1e82` was tagged `v2.181.0` → release diverged from main. **main ended with 0% of TASK-284.**

Memories: `mem-030` (premature close), `mem-031` (label-trust supersession), `mem-032` (full-spec inheritance), `mem-033` (verify artifact not status).

## Shipped in PR #3527 (branch `fix/autopilot-premature-parent-close`, commit `daffb38f`)

| Chain link | Fix | Where |
|---|---|---|
| 2 | `openSubIssueCount()` — native 0 must be confirmed by text search; max of tiers; shared by `maybeCloseParentIssue` + `recoverStaleParentIssues` | `internal/autopilot/controller.go` |
| 4 | open `pilot/GH-N` PR vetoes supersession; fail-open on lookup error | `internal/adapters/github/poller.go` `skipSupersededByParent` |
| 1 | scope fence appended to decomposer sub-issue bodies (`subIssueBody()` helper, both GitHub + adapter paths) | `internal/executor/epic.go` |

Tests: 4 new `TestRecoverStaleParentIssues` cases (incl. GH-3513 regression), `TestPoller_CheckForNewIssues_DoesNotSupersedeWithOpenPR`, `TestSubIssueBody_ScopeFence`. 3 packages `ok`, lint 0 issues.

## NOT fixed (follow-ups)

- ~~**Base-branch routing**~~ ✅ **FIXED 2026-06-10** — GH-3540 (child of epic GH-3532) shipped "pin decomposed-child PR base to repo default branch" via PR #3548 (v2.185.x). Live-verified: child PR #3555 (created post-fix) based on `main`. The phantom-release vector is closed.
- **PR-merge verification in `closeParentNow`** — fix is defense-in-depth on counting; closing still doesn't verify merged PRs per child. Option 2 from research (track `PRState.IssueNumber` → merged) if counting proves insufficient.
- **Scope fence efficacy** — prompt-level only; verify on the next decomposed epic that children stay in-slice.
- **`handleReleasing` no-timeout loop** + `GetTagForSHA` 20-tag cap (separate stuck-release diagnosis, self-cleared this time).
- **Hallucinated/empty children keep coming**: GH-3553 (closed 2026-06-10) — another wrong-parent child (`parent: GH-201`, like #3538) with an EMPTY slice description (scope fence wrapping nothing). Decomposer should reject children with empty descriptions before creation; the wrong-parent match needs root-causing. ⚠️ #3553's author was `mvanhorn`, not alekspetrov — a second Pilot instance appears to operate on qf-studio/pilot.

## Resolved 2026-06-10 (user approved 1+2)

1. ✅ **PR #3523 squash-merged** (daemon down → no reconciler interference). Artifact-verified on main: store sigs 3/3 `projectPath`, `runDashboardMode(projectPath)`. TASK-284 shipped.
2. ✅ **PR #3519 closed** as superseded, with pointer to #3523 + #3527.
3. ✅ **Phantom `v2.181.0`: keep the tag, supersede.** Deleting it would wedge `pilot upgrade` (installed binary reports v2.181.0; latest would drop below current). Next daemon-cut release (≥v2.182.0) from main carries the real feature. ⚠️ Until then the v2.181.0 binary contains feature-branch code — fine for the user's own machine, but the next release should land before any wider rollout.
