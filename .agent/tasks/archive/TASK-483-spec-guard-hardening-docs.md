# TASK-483: Spec-guard hardening + intake-gate docs (fixes #5150, #5152)

**Status**: 🚀 Dispatched to Pilot
**Last Updated**: 2026-08-24
**Source**: External reports [#5150](https://github.com/qf-studio/pilot/issues/5150), [#5152](https://github.com/qf-studio/pilot/issues/5152) (d3rowy, verified 2026-08-24). Research downgraded #5150: the fail-open is dead at the sole call site — contract fix + test, not a behavior change.

## Context

- `applySpecGuardSDK` (`cmd/pilot/spec_guard_sdk.go:32-111`) documents "returns true when dispatch should be skipped" (:17), but the `ListIssueComments`-error branch (:33-38) warns and `return false` — the only `false` exit in the function. The sole caller (`cmd/pilot/handlers.go:850-853`) discards the return and unconditionally returns `Skipped: true` on any `ValidateSpec` failure, so today the error path already defers (no dispatch, no strike — strikes are purely `AddLabels`/`AddComment` side effects; `MarkProcessed` is not called, next poll retries naturally). The `false` is a latent trap for any future return-honoring caller. Zero test coverage on this branch.
- `internal/ghissue/spec.go:55` `sectionHeaderRe` requires an H2–H6 header from (Acceptance|Implementation|Context|Background|Approach|Design|Refs), English exact-match. The reason string (:110-111) lists the headers but not the H2–H6 scope or that body text under the heading may be any language. `docs/content/features/quality-gates.mdx` covers only post-implementation gates — zero mention of the pre-dispatch spec-guard anywhere in docs except a name-drop at `docs/content/integrations/github.mdx:129`. Bonus defect: `github.mdx:113-120`'s own example issue body uses `## Summary`, which spec-guard would reject.

## Implementation

### Leg A — #5150 contract fix
1. `spec_guard_sdk.go:37`: `return false` → `return true`; reword the warn from "failed to list comments, skipping guard" to deferring language (e.g. "failed to list comments — deferring dispatch to next tick, no strike").
2. New test in `cmd/pilot/spec_guard_sdk_test.go` (use the existing `specGuardFake` httptest server, make the list-comments route return 500): assert return `true`, zero labels added, zero comments posted. Do not break the source-grep test `TestGithubHandlerSDK_SpecGuardWired`.

### Leg B — #5152 docs + message
3. `internal/ghissue/spec.go:110-111`: expand the reason string to state the H2–H6 range explicitly and that only the heading is checked — body content under it may be any language. Reasons flow verbatim into both strike comments (`buildSpecIncompleteComment`/`buildSpecEscalationComment`) and log lines, so this one edit covers all surfaces. Update `spec_test.go` assertions if they match the old string (`TestValidateSpec_NoSectionHeader`, `SectionHeaderVariants`, `H3ToH6SectionHeaders`).
4. `docs/content/features/quality-gates.mdx`: new top-level `## Issue Spec Guard (pre-dispatch)` section (place before `## Configuration`; file has no frontmatter, uses nextra `Callout`): the 7 accepted headers, exact-match, H2–H6 (H1 rejected), any-language body under the heading, the two-strike flow (`pilot-spec-incomplete` → `pilot-blocked`), the skip-label opt-out, and the ≥100-char body rule.
5. `docs/content/integrations/github.mdx`: link the :129 spec-guard name-drop to the new section; fix the :113-120 example body so it passes spec-guard (e.g. `## Summary` → `## Context`).

## Acceptance

- [ ] Error branch returns `true`; new test proves defer-no-strike (no labels, no comments).
- [ ] `make test` green including existing spec-guard wiring/fingerprint tests.
- [ ] Strike comment and log line carry accepted-header list + H2–H6 + any-language nuance (via the spec.go reason string).
- [ ] quality-gates.mdx has the new spec-guard section; github.mdx cross-links it and its example passes the guard.
- [ ] PR body includes `Fixes #5150` and `Fixes #5152`.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5154
- #5150, #5152 (agreed semantics + severity-correction comments posted 2026-08-24)
- `.agent/knowledge/memories/learnings/learning_pilot_issue_spec_guard_headers.md` (two-strike gate history, #4498 fingerprint incident)
