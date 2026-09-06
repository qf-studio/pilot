# TASK-493: Review-feedback trigger hardening — bot filter on webhook path, stage guard, identity-based exclusion, human-only feedback body

**Status**: ✅ SHIPPED 2026-09-06 in **v2.273.0** — #5314 decomposed into #5326–#5330; code children merged as PR#5331 (predicate) · PR#5332 (stage guard) · PR#5333 (human-only body) · PR#5334 (tests); #5330 (verify+reply) no-op, closed by operator (reply to #5228 already posted). **Post-merge review: PENDING** (interactive rule: review every PR).
**Created**: 2026-09-06
**Assignee**: Pilot

---

## Context

**Problem**:
External report [#5228](https://github.com/qf-studio/pilot/issues/5228) (contributor `stevensommer`, claims verified by nav-research 2026-09-06) asked to make the review-feedback trigger configurable so bot `COMMENTED` reviews could start revision cycles. **Feature DECLINED** by founder 2026-09-06 (security by design — see decision `pilot-never-reads-gh-comments-by-design`). The report also surfaced two genuine defects in the *existing* policy, which this task takes:

1. **Polling and webhook paths disagree.** `hasChangesRequested` (`internal/autopilot/controller.go:4233-4280`, polling) skips reviewers whose login contains `[bot]` or ends `-bot`. `OnReviewRequested` (`controller.go:2692-2735`, webhook) applies **no** reviewer filter — any login, including a bot, flips the PR to `StageReviewRequested`. The bot exclusion is a security property, so the webhook hole is a policy violation, not a feature gap.
2. **A review pulls a parked PR out of `awaiting_approval`.** Webhook path has zero stage guard (`controller.go:2733`); polling gate (`controller.go:8799-8800`) excludes only `StageReviewRequested`/`StageFailed`. A PR waiting for the operator's merge decision is not at rest.

Two adjacent gaps found during verification:

3. **Bot exclusion is a string pattern, not identity.** `getBotLogin` (`controller.go:9229-9247`) already resolves the authenticated login via `GetAuthenticatedUser` (used by the GH-3417 recovery-PR guard at ~9449). The review gate does not use it — a bot whose login matches neither pattern passes.
4. **Revision-issue body ingests every review and every line comment, any author, any state.** `handleReviewRequested` (`controller.go:4059`) fetches `ListPullRequestReviews` + `GetPullRequestComments` unfiltered; `formatReviewFeedback` (`feedback_loop.go:759-796`) includes every non-empty review body and every line comment. Once a human legitimately triggers the loop, untrusted text from any commenter lands in the issue body Pilot executes. Under "comments are for humans and history; Pilot reads only issue bodies", the feedback must narrow to the triggering human's `CHANGES_REQUESTED` review(s) — body + that reviewer's line comments only.

**Goal**:
Make the existing trigger policy consistent and identity-based across both execution modes, keep parked PRs parked, and ensure only the triggering human reviewer's content reaches the revision issue. No new config surface. No dashboard change.

---

## Known Pitfalls & Patterns

- **DECISION** (95%, `pilot-never-reads-gh-comments-by-design`): Pilot never reads GH comments; revision loop keys only on formal human `CHANGES_REQUESTED`. Reflected: no `trigger_states`, no bot allowlist; phase 3 narrows the body to the triggering human.
- **PATTERN** (TASK-486): hold/close sites in `controller.go` never post comments inline — set `prState.Error`/`TerminalLabel`, let `notifyExternalClose` write the audit trail. Stage-guard rejections in phase 2 log only; they do not comment.
- **PITFALL** (GH-5266, `gh5266_test.go`): `hasChangesRequested` is anchored to `ghPR.CreatedAt` cutoff and "COMMENTED cannot clear a standing CHANGES_REQUESTED". The shared matcher in phase 1 must preserve both — extend the table tests, do not rewrite them.
- **PITFALL** (TASK-473 / #4856 → PR#4862): `CreateReviewIssue` claim-before-create; `handleReviewRequested` guards `issueNum<=0`. Phase 3 touches the formatter only, not the claim flow.
- **PATTERN** (GH-3417): `getBotLogin` caches the authenticated identity; reuse it, do not add a second resolver.

---

## Acceptance Criteria

- [ ] Webhook `OnReviewRequested` rejects reviews from Pilot's own login (`getBotLogin`) and from `[bot]`/`-bot` logins, identically to the polling path — one shared predicate, no duplicated logic.
- [ ] Neither path moves a PR out of `StageAwaitApproval` (or any stage other than the eligible pre-review stages) on a review event; the rejection is logged at debug with pr/stage/reviewer.
- [ ] `formatReviewFeedback` output contains only the triggering reviewer's `CHANGES_REQUESTED` review body(ies) and that reviewer's line comments; bot, `COMMENTED`, and other-author content is excluded.
- [ ] Defaults and config are byte-identical: no new YAML fields, `configs/pilot.example.yaml` unchanged except a comment clarifying the trigger contract if needed.
- [ ] No files under `internal/dashboard/` or `internal/web/` change.
- [ ] Existing GH-5266 cutoff/COMMENTED tests still pass unchanged.

---

## Implementation

### Phase 1: Shared reviewer-trust predicate
**Goal**: One function decides "is this reviewer allowed to trigger", used by both paths.

**Tasks**:
- [ ] Extract `isTrustedReviewer(login string) bool` in `controller.go`: false when `login == getBotLogin()`, false on `[bot]` / `-bot` pattern, true otherwise. Case-insensitive on the login.
- [ ] `hasChangesRequested` calls the predicate instead of the inline pattern (line ~4258). Cutoff + COMMENTED-does-not-supersede logic untouched.
- [ ] `OnReviewRequested` calls the predicate before the state check (line ~2712). Reject → return, log debug.

**Files**:
- `internal/autopilot/controller.go` — predicate, two call sites

### Phase 2: Stage guard on both paths
**Goal**: A review event only transitions PRs that are in a pre-review, non-parked stage.

**Tasks**:
- [ ] Define the eligible set once (e.g. `reviewTriggerEligible(stage) bool`): excludes `StageAwaitApproval`, `StageReviewRequested`, `StageFailed`, `StageMerging`, `StageReleasing`, and any terminal stage. Confirm the exact list against the stage enum before coding; document the reasoning in a comment.
- [ ] Webhook: guard before `prState.Stage = StageReviewRequested` (line ~2733).
- [ ] Polling: replace the two-stage exclusion at ~8799-8800 with the shared eligibility check.
- [ ] Keep `handleMerging`'s merge-hold use of `hasChangesRequested` (#5264/#5269) working — that call is a hold check, not a transition, and must not be affected by the eligibility gate.

**Files**:
- `internal/autopilot/controller.go`

### Phase 3: Human-only feedback body
**Goal**: The revision issue carries only the triggering human's review.

**Tasks**:
- [ ] `handleReviewRequested` determines the triggering reviewer login(s): the trusted reviewers whose latest review since the cutoff is `CHANGES_REQUESTED`.
- [ ] Filter `ListPullRequestReviews` results to those reviewers + `CHANGES_REQUESTED` state; filter `GetPullRequestComments` to those reviewers.
- [ ] `formatReviewFeedback` signature unchanged if possible; filtering happens in the caller so the formatter stays a pure grouper.
- [ ] Empty result after filtering (should be impossible once the trigger fired, but guard it): log and skip issue creation rather than filing an empty revision issue.

**Files**:
- `internal/autopilot/controller.go` (`handleReviewRequested` ~4059)
- `internal/autopilot/feedback_loop.go` (`formatReviewFeedback` 759-796) — only if a helper is needed

### Phase 4: Tests
**Tasks**:
- [ ] `gh5228_test.go` (new): table-driven — webhook path rejects `[bot]`, `-bot`, and own-login reviewers; accepts human; parity assertion that polling and webhook return the same verdict for the same review payload.
- [ ] Stage-guard tests: PR in `StageAwaitApproval` receives `changes_requested` via webhook and via poll → stage unchanged; PR in an eligible stage → `StageReviewRequested`.
- [ ] Feedback-body test: reviews from human (CHANGES_REQUESTED), bot (COMMENTED), second human (COMMENTED) + line comments from all three → issue body contains only the first human's content.
- [ ] Extend existing `TestController_HandleReviewRequested_IgnoresSelfReview` to cover identity-based (own-login) exclusion.
- [ ] `gh5266_test.go` passes without modification.

---

## Out of Scope

- `trigger_states` / `trusted_bot_reviewers` config (#5228 feature) — declined by design.
- Any change to when/how iteration counters advance (`max_iterations`) — TASK-486 owns that; unchanged here.
- Dashboard/TUI rendering of stages — the TUI already renders `StageAwaitApproval` and `StageReviewRequested` in the same "approval" slot (`internal/dashboard/tui.go:217`); no change.
- GitHub App identity for PR authorship (P3 backlog) — separate.
- Label- or issue-based revision triggers (future automation direction per the decision) — separate task if ever pursued.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Bot trigger opt-in | config allowlist (#5228) · decline | decline | Security by design: only a human `CHANGES_REQUESTED` is a trusted signal; a knob that disables that is a knob we support forever |
| Reviewer exclusion | string pattern · identity via `getBotLogin` · both | both, in one predicate | Identity catches Pilot's own account regardless of name; pattern catches third-party bots |
| Where to filter feedback | in `formatReviewFeedback` · in caller | caller | Formatter stays a pure grouper; filtering logic lives next to the trigger decision that determines the reviewer |
| Stage eligibility | exclude-list · include-list | include-list of eligible stages | Fails closed when new stages are added |

---

## Verify

```bash
go test ./internal/autopilot/ -run 'ReviewRequested|HasChangesRequested|GH5266|GH5228|ReviewFeedback' -count=1
go build ./...
make lint
git diff --stat -- internal/dashboard internal/web   # must be empty
```

---

## Done

- [ ] Shared predicate + stage eligibility exist and are the only gate logic on both paths
- [ ] New `gh5228_test.go` passes; `gh5266_test.go` unchanged and green
- [ ] Revision issue body contains only the triggering human's review content (test-proven)
- [ ] No dashboard files in the diff
- [ ] #5228 replied: feature declined with rationale, bugs credited, this task's issue linked

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/5314
- [#5228](https://github.com/qf-studio/pilot/issues/5228) — source report (feature declined; bugs 1–2 credited)
- Decision memory: `.agent/knowledge/memories/decisions/pilot-never-reads-gh-comments-by-design.md`
- TASK-486 (iteration-limit mode gate, same contributor's #5227) — `tasks/archive/`
- GH-5266 (`gh5266_test.go`) — cutoff/COMMENTED hardening this task must preserve
- #5264 / #5269 — merge-hold reuse of `hasChangesRequested`

---

**Last Updated**: 2026-09-06
