---
name: base-presence-gate-false-positives-hold-then-silent-needs-human
description: The dispatch base-presence gate treats ANY backticked `x/y.ext`-shaped token in an issue body as a repo file path that must exist on main (hasFileExtension, dependency_detector.go:213) — branch names like `release/1.0`, home paths, Go symbols, other-repo paths, files the task should CREATE all trigger 5-min holds; at 20 holds it applies pilot-needs-human WITH NO COMMENT and finalizes skipped. Box-log census 2026-09-03: 165 holds / 10 tasks / 0 true positives. Recovery: remove the label (+ edit the body — held ticks re-read the live body per GH-5193). Authoring rule: never backtick a path-shaped example unless it exists on main.
type: pitfall
---

# Pitfall: base-presence gate — path-shaped tokens hold the task, then escalate silently

**Incident (GH-257, pilot-console, 2026-09-02→03):** issue body contained `` `release/1.0` `` as a branch-name EXAMPLE in a test sentence. `ExtractReferencedPaths` (`internal/executor/dependency_detector.go:169-208`) keeps backticked spans with a `/` and a dot-extension; `hasFileExtension` (`:213-219`) is a pure `LastIndex(".")` check, so `.0` qualifies. `checkBasePresence` (`base_presence.go:388`) then asked the GitHub Contents API for that path on the default branch → miss → `Task held: prerequisite not on main`, every `StaleRecoveryInterval` (5 min). At `hold_count >= 20` (`dispatcher.go:146,175`), `escalateBasePresenceHold` (`dispatcher.go:3672-3713`) applied `pilot-needs-human`, fired an internal alert, and the execution finalized `skipped` — **no issue comment** (every sibling escalation path posts one: `escalateRefusal` 2365, `escalateStalledTask` 2503, autopilot `escalateAndHold`). The label blocked admission for 27h; each poll tick called `repickBackoff.recordClaimLostDrop` (`cmd/pilot/handlers.go:743-761`) so `claim_lost_drops` climbed to 94 with NO real collision — misleading enough that the operator deleted a harmless claim row. The re-pick after label removal re-held on the SAME token (11 holds) until the body was edited.

**Census (box daemon.log, all time):** 165 holds across 10 tasks; referenced "paths": `~/.pilot/config.yaml` (45, GH-5246) · `release/1.0` (23) · `.agent/tasks/q-<epoch>.md` (template) · `admin/token_service.go` (file the task creates) · `internal/autopilot.Config` (Go symbol) · `navigator/9.0.0/templates/...` (plugin path) · `sdk/core/chat.go` (other repo) · `system/FEATURE-MATRIX.md` (relative to .agent) · `cmd/pilot/*.go` (glob). Zero true positives found. GH-5145 self-wedged the same way on 08-23.

**What is NOT the problem:** claim release (`skipped` is non-terminal for generation bumps — `nextRetryGeneration`, `dispatcher.go:1736`), and the #5274 reaper (rides the 5-min ticker; correctly ignores row-present claims). Worktrees base on fetched `origin/main`.

**Recovery:** `gh issue edit N --remove-label pilot-needs-human` (+ remove/re-add `pilot`); edit the body to remove the token — the held tick re-fetches the live body (`presenceCheckBody = state.Body`, `dispatcher.go:3325`, GH-5193). No claim surgery, no cancel.

**Authoring rule (until the gate is fixed):** in pilot-labeled issues, never backtick a slash+dot token unless that exact path exists on the target repo's default branch. Write examples in words ("a release branch with a version segment").

**Open decision (founder, 2026-09-03):** narrow the gate to explicit `Depends on: #N` refs vs harden the heuristic (semver/branch exclusion + created-file awareness). Defects to file: heuristic · silent escalation · misleading counter (no corrective log line, unlike `handler_common.go:216-225`) · `finish_tripwire_root_clean` has no baseline (381 violations). pilot#5301 was mis-filed for this incident (label pulled).
