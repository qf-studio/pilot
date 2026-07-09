# SOP: every PR stage entered outside the main dispatch loop needs a `checkExternalMergeOrClose` guard

**Category:** Autopilot / release pipeline
**Implemented:** 2026-07-09
**Source incident:** GH-4124 — `checkExternalMergeOrClose` had a guard for `StagePostMergeCI` (GH-3994) but not for `StageReleasing`, so every `require_ci: true` on_merge release silently wedged: the PR reached `StageReleasing` but was drained on the next poll tick before `handleReleasing` ever ran. No tag was ever cut; pilot's own releases (v2.236.0/.1/.2) had to be cut by hand for weeks before this was traced.

## Problem

`checkExternalMergeOrClose` (`internal/autopilot/controller.go`) runs on **every poll tick, for every tracked PR, before the stage-dispatch switch in `ProcessPR`**. If a PR's current stage was reached by anything other than `checkExternalMergeOrClose` itself immediately falling through to `handleReleasing`/`removePR` in the same tick, the *next* tick will call `checkExternalMergeOrClose` again on that same `ghPR.Merged == true` PR — and without an explicit early-return guard for that stage, it falls through to the GH-411 release-trigger block and then `removePR`, regardless of what stage-specific handler was supposed to own it next.

This only bites `require_ci: true` because that's the one path where a PR transitions into an intermediate stage (`StagePostMergeCI` → `StageReleasing`) across *separate* ticks instead of being resolved same-tick.

## Root cause

The guard list at the top of `checkExternalMergeOrClose` is an allowlist of "stages I must not touch," maintained by hand:
```go
if prState.ScopeKey != "" { return false }        // scope carriers
if prState.Stage == StagePostMergeCI { return false } // GH-3994
if prState.Stage == StageReleasing { return false }   // GH-4124
```
Each guard was added reactively, after the corresponding stage started getting drained in production. There's no compile-time or test-time enforcement that every stage a PR can be routed to *asynchronously* (i.e., where a later tick, not the current call, is expected to advance it) has a matching guard here.

## Fix

Added the `StageReleasing` guard next to the existing `StagePostMergeCI` one (controller.go ~L4327).

## Prevention

When adding a new `PRStage` that a PR can sit in **across multiple poll ticks** while merged on GitHub (`ghPR.Merged == true`), ask: "does `checkExternalMergeOrClose` need to leave this alone?" If the stage is only ever entered and resolved within the same tick (no waiting), no guard is needed — but if a later tick's dispatch (`ProcessPR`'s `switch prState.Stage`) is expected to move it forward, add an explicit early-return guard here, and add a table-driven case to `TestCheckExternalMergeOrClose_StageGuards` (`internal/autopilot/controller_release_cycles_test.go`) asserting it returns `false` and does not call `removePR`.
