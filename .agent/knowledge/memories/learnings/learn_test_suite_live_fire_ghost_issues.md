---
name: test-suite-live-fire-ghost-issues
description: The GH-201 "OAuth loop" ghost issues were created by the test suite itself — tests that rely on ambient gh/claude FAILING go live-fire on dev machines; verify the spawner, don't trust attribution heuristics
type: learning
---

# Test suite live-fire: the real source of the GH-201 "OAuth loop"

**Date:** 2026-06-11 · **Origin:** #3576 investigation during TASK-292 cleanup · **Fix tracked:** [#3579](https://github.com/qf-studio/pilot/issues/3579)

## What happened

Ghost issues titled `feat(auth): add OAuth provider integration` with code-stamped
`<!--autopilot-meta parent: GH-201 inherited-spec: true-->` kept appearing
(#3562–64 on 06-10, #3576 on 06-11). They were attributed to a teammate's
daemon via shared PAT, because the code-stamp "requires real decomposition"
and local DBs had zero GH-201 rows. Local knowledge stores were purged and a
PAT-rotation ask was sent to the team — all unnecessary.

**Actual root cause:** `TestCreateSubIssues_ProceedsWhenNoChildren`
(`internal/executor/subtask_cc_validation_test.go`) drives `CreateSubIssues`
past the dedup guard with fixture parent `GH-201` and `executionPath=""`.
`createSubIssuesViaGitHub` (`epic.go:1170`) has **no dryRun guard** (the
test's `r.dryRun = true` only covers UpdateIssueProgress/CloseIssueWithComment).
With an authed `gh`, the test creates a REAL issue in whatever repo the test
cwd resolves to. Sibling tests likewise spawn the REAL `claude` binary on
machines where it exists ("should fail when binary is not found" — it doesn't fail).
#3576's creation time (12:19:53Z) matched the GH-3573 retry worker's stop-hook
full-suite run to the minute.

## Why the misattribution stuck

- "Code-stamped marker ⇒ real decomposition ⇒ a daemon did it" — FALSE: tests
  stamp the same markers through the same production functions.
- "Zero local DB rows ⇒ not this machine" — FALSE: tests bypass the DB
  entirely, so NO machine would ever have rows. Absence of rows was evidence
  of the test path, not of a remote actor.

## Lessons

1. **Tests must never rely on ambient tools failing.** "gh will fail in
   /nonexistent/path" / "binary not found" assumptions invert on dev machines
   and inside daemon stop-hooks. Stub the exec boundary, always.
2. **dryRun flags must guard every side-effecting path, and tests that set
   them deserve a guard-existence check** — `r.dryRun = true` with a comment
   is not protection if the path never reads it.
3. **When attributing autonomous-system artifacts, verify the spawner, not
   the signature.** Fixture IDs (GH-201) look identical to production output.
   Before blaming an external actor, grep the fixtures:
   `rg "<marker>" --type go` would have solved this in one command on day one.
4. The original knowledge-store-poisoning lesson still holds ([[verify-artifact-not-status]]):
   stores and stamps that feed prompts/attribution are attack surface — but the
   first suspect should be the nearest unmocked test, not the farthest teammate.

(The 06-10 team message about PAT rotation was never sent — no correction needed.)
