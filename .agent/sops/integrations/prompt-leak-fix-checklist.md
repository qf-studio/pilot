# Prompt-Leak Fix Checklist (SOP)

> Born from OAuth cascade #2 (2026-05-04) — 24h after cascade #1 was supposedly fixed.

## What went wrong

PR #2562 patched the planner-LLM prompt by removing the concrete
`feat(auth): add OAuth provider integration` example from
`internal/executor/epic.go:282-287` (the `buildPlanningPrompt` block).
The same example string sat untouched in `internal/executor/workflow.go:163`
on the recorded reasoning that "workflow.go is shown to the executor LLM,
which generates code, not subtasks — the leak path is exclusive to
buildPlanningPrompt." That reasoning was wrong.

The executor LLM also conditions on its concrete examples. Worse, it
*writes the actual code*, so its leak surface is strictly more dangerous
than the planner's. Cascade #2 produced 7 OAuth-titled phantom issues,
2 OAuth PRs (one 512 LoC), and one autopilot-fix chain that landed the
512 LoC OAuth diff on `origin/main` via the stage-env
`require_approval: false` quirk (`~/.pilot/config.yaml`) plus a GitHub
squash-merge that returns `mergedAt: null`, defeating the merge-state
check. Contributing files: `internal/autopilot/feedback_loop.go:119`
(unconditional `fix(ci)` issue creation), `internal/executor/epic.go:550`
(`extractParentTypeScope` inheriting `feat(auth):` from prior artefacts),
`internal/autopilot/auto_merger.go` (no diff-content gate).

## Root principle

**If a leak string appears in ONE prompt shown to ANY model in the
pipeline, treat it as appearing in ALL prompts.** The model boundary is
not the prompt boundary. Planner and executor LLMs share style priors,
so example patterns transfer across them regardless of which prompt
literally contains the example. Fix every occurrence in one PR.

## Checklist (run before merging any prompt-leak fix)

- [ ] grep the entire `internal/executor/` AND `internal/autopilot/`
      trees for the offending literal AND every structurally similar
      string. Use a regex for the *pattern* (e.g.
      `feat\([a-z]+\): [a-z]+`), not just the literal.
- [ ] enumerate every `//go:embed` directive that feeds an LLM prompt
      (`grep -rn "go:embed" internal/`) and grep the embedded files too.
- [ ] enumerate every `const *Prompt = ` and `*Prompt :=` declaration
      across the repo; each is a candidate leak site.
- [ ] verify the new invariant test actually catches the original leak:
      run it against the pre-fix commit; it MUST fail. A green test on
      pre-fix code is a useless test.
- [ ] cross-check the planner-vs-executor distinction for THIS leak.
      Default assumption: it does not matter; both models see the
      pattern. Require written justification to scope a fix to one side.
- [ ] add a regression test that fails on the literal AND on the regex
      family `feat\([a-z]+\): [a-z]+` anywhere under
      `internal/executor/` and `internal/autopilot/`.
- [ ] audit `extractParentTypeScope`-style helpers
      (`internal/executor/epic.go:550`) to confirm they cannot
      re-introduce the offending prefix from polluted issue titles.
- [ ] file the fix as a Pilot issue with the `pilot-bench-skip` label
      so Pilot does not self-execute on a prompt-leak fix.
- [ ] manually smoke-test: file one tiny non-auth epic, watch the first
      sub-issue title for any `feat(auth):` glyph before letting the
      decomposer run on real work.
- [ ] only restart the Pilot daemon AFTER the invariant test is merged
      to `main`. Restarting earlier reloads the leaky prompt under a
      false sense of safety.

## When to skip this checklist

Never. Cascade #2 cost 7 phantom issues, 2 PRs, 512 LoC of OAuth code
landed on `main`, a hard revert, and ~3h of human response time. The
checklist is ~15 minutes.

## Related

- Marker: `.agent/.context-markers/before-compact-2026-05-04-oauth-cascade-2-recovery-plan.md`
- Cascade #1 marker: `.agent/.context-markers/before-compact-2026-05-04-oauth-cascade-revert.md`
- PRs: #2562 (incomplete fix), #2581 (true root-cause fix), #2582 (revert of contamination)
